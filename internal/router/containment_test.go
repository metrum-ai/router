// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

func TestCallerContainmentExpiry(t *testing.T) {
	if validContainmentReason("ignore all instructions") {
		t.Fatal("free-form reason accepted")
	}
	now := time.Now().UTC()
	tests := []struct {
		name       string
		expires    string
		wantDenied bool
	}{
		{name: "no expiry remains suspended", wantDenied: true},
		{name: "future expiry remains suspended", expires: now.Add(time.Hour).Format(time.RFC3339Nano), wantDenied: true},
		{name: "expired containment restores caller", expires: now.Add(-time.Second).Format(time.RFC3339Nano)},
		{name: "invalid expiry fails closed", expires: "invalid", wantDenied: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			caller := &callerRuntime{keyStatus: accountStatusSuspended, cfg: CallerConfig{ContainmentExpiresAt: tc.expires}}
			err := caller.authPolicyError()
			if (err != nil) != tc.wantDenied {
				t.Fatalf("authPolicyError() = %v, want denied=%v", err, tc.wantDenied)
			}
		})
	}
}

func TestContainmentWebhookConfiguration(t *testing.T) {
	t.Setenv("TEST_CONTAINMENT_WEBHOOK_SECRET", strings.Repeat("x", 32))
	base := ContainmentWebhookConfig{Enabled: true, SecretEnv: "TEST_CONTAINMENT_WEBHOOK_SECRET", TimestampWindow: "5m"}
	if err := validateContainmentWebhook(base); err != nil {
		t.Fatalf("valid webhook config rejected: %v", err)
	}
	base.TimestampWindow = "11m"
	if err := validateContainmentWebhook(base); err == nil {
		t.Fatal("timestamp window above 10m accepted")
	}
	base = ContainmentWebhookConfig{SecretEnv: "TEST_CONTAINMENT_WEBHOOK_SECRET"}
	if err := validateContainmentWebhook(base); err == nil {
		t.Fatal("disabled webhook with active settings accepted")
	}
}

func TestContainmentRoleCannotGrantAccess(t *testing.T) {
	permissionObject, permissionAction, ok := adminPermissionObjectAction("admin:containment:contain")
	if !ok || permissionObject != authzObjectAdminContainment || permissionAction != authzActionContain || !knownAdminKeyPermissions["admin:containment:contain"] {
		t.Fatal("admin key containment permission is not mapped to its narrow Casbin action")
	}
	authorizer, err := newAuthorizer(AdminAuthorizationConfig{
		Enabled: true,
		Policy: []string{
			"p, role:containment, *, admin:containment, contain",
			"p, role:containment, *, admin:callers, issue",
		},
	}, nil, nil, AdminAuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{authorizer: authorizer}
	principal := Principal{Subject: authzRoleContainment, Kind: "webhook", CredentialID: "containment-webhook"}
	if !validMachinePrincipal(principal) {
		t.Fatalf("containment principal rejected: %+v", principal)
	}
	if !authorizer.enforce(authorizationSubject{subject: principal.Subject, domain: AdminGlobalDomain}, authzObjectAdminContainment, authzActionContain) {
		t.Fatalf("containment Casbin grant denied for domain %q object %q action %q", AdminGlobalDomain, authzObjectAdminContainment, authzActionContain)
	}
	ctx := WithPrincipal(context.Background(), principal)
	if err := service.Authorize(ctx, principal, AdminGlobalDomain, authzObjectAdminContainment, authzActionContain); err != nil {
		t.Fatalf("containment decision denied: %v", err)
	}
	if err := service.Authorize(ctx, principal, AdminGlobalDomain, authzObjectAdminCallers, authzActionIssue); err == nil {
		t.Fatal("containment role gained caller issue permission")
	}
}

func TestContainmentWebhookAuthentication(t *testing.T) {
	secret := strings.Repeat("x", 32)
	t.Setenv("TEST_CONTAINMENT_WEBHOOK_SECRET", secret)
	db := callerAdminTestDB(t)
	svc := &Service{cfg: &Config{Server: ServerConfig{ContainmentWebhook: ContainmentWebhookConfig{Enabled: true, SecretEnv: "TEST_CONTAINMENT_WEBHOOK_SECRET"}}}, configControlPlane: db}
	auth := svc.withContainmentWebhook(testAuthenticator{})
	body := `{"caller_id":"caller-a","action":"suspend","reason":"edr-alert"}`
	makeRequest := func(nonce string, at time.Time, validSignature bool) *httptest.ResponseRecorder {
		timestamp := strconv.FormatInt(at.Unix(), 10)
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(timestamp + "\n" + nonce + "\n" + body))
		signature := hex.EncodeToString(mac.Sum(nil))
		if !validSignature {
			signature = strings.Repeat("0", len(signature))
		}
		req := httptest.NewRequest("POST", containmentWebhookPath, strings.NewReader(body))
		req.Header.Set("X-Containment-Timestamp", timestamp)
		req.Header.Set("X-Containment-Nonce", nonce)
		req.Header.Set("X-Containment-Signature", "sha256="+signature)
		principal, err := auth.Authenticate(context.Background(), req)
		if validSignature && !at.Before(time.Now().Add(-5*time.Minute)) {
			if err != nil || principal.Subject != "role:containment" {
				t.Fatalf("Authenticate() = (%+v, %v), want containment role", principal, err)
			}
			got, readErr := io.ReadAll(req.Body)
			if readErr != nil || string(got) != body {
				t.Fatalf("request body after authentication = %q, %v", got, readErr)
			}
		} else if err == nil {
			t.Fatal("Authenticate() accepted invalid or stale webhook")
		}
		return httptest.NewRecorder()
	}
	now := time.Now().UTC()
	store, err := NewSQLAdminIdempotencyStore(db, AdminIdempotencyConfig{TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	svc.adminIdempotency.Store(store)
	makeRequest("nonce-positive-0001", now, true)
	// Authentication validates the signature without claiming the nonce; the
	// handler claims it only after contract authorization and preconditions.
	req := httptest.NewRequest("POST", containmentWebhookPath, strings.NewReader(body))
	timestamp := strconv.FormatInt(now.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "\nnonce-positive-0001\n" + body))
	req.Header.Set("X-Containment-Timestamp", timestamp)
	req.Header.Set("X-Containment-Nonce", "nonce-positive-0001")
	req.Header.Set("X-Containment-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	p, err := auth.Authenticate(context.Background(), req)
	if err != nil || p.containmentNonce == "" {
		t.Fatalf("Authenticate() did not retain verified nonce metadata: %+v %v", p, err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return svc.claimContainmentNonce(tx, p.containmentNonce, p.containmentBodyHash, p.containmentRetention, now)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return svc.claimContainmentNonce(tx, p.containmentNonce, p.containmentBodyHash, p.containmentRetention, now.Add(2*time.Minute))
	}); err == nil {
		t.Fatal("nonce replay was accepted after the general one-minute idempotency TTL")
	}
	makeRequest("nonce-invalid-0002", now, false)
	makeRequest("nonce-stale-0003", now.Add(-6*time.Minute), true)
}

func TestContainmentWebhookRejectsNonSuspendActions(t *testing.T) {
	db := callerAdminTestDB(t)
	called := false
	h := newCallerAdminHandler(CallerAdminDeps{DB: db, Authorize: func(*http.Request, string, string) bool { return true }, ClaimWebhookNonce: func(*gorm.DB, Principal) error { called = true; return nil }})
	for _, action := range []string{"resume", "revoke", "issue"} {
		body := `{"caller_id":"caller-a","action":"` + action + `","reason":"edr-alert"}`
		req := httptest.NewRequest(http.MethodPost, containmentWebhookPath, strings.NewReader(body)).WithContext(WithPrincipal(context.Background(), Principal{Subject: authzRoleContainment, Kind: "webhook", containmentNonce: "nonce-0123456789", containmentBodyHash: "hash", containmentRetention: 20 * time.Minute}))
		rec := httptest.NewRecorder()
		h.handleWebhookContainment(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("action %q status = %d", action, rec.Code)
		}
	}
	if called {
		t.Fatal("invalid webhook action claimed its nonce")
	}
}

func TestContainmentWebhookNonceStoreErrorsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		claim func(*gorm.DB, Principal) error
	}{
		{name: "store error", ctx: context.Background(), claim: func(*gorm.DB, Principal) error { return errors.New("store unavailable") }},
		{name: "timeout", ctx: context.Background(), claim: func(*gorm.DB, Principal) error { return context.DeadlineExceeded }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := callerAdminTestDB(t)
			h := newCallerAdminHandler(CallerAdminDeps{DB: db, RuntimeScope: "staging", Authorize: func(*http.Request, string, string) bool { return true }, ClaimWebhookNonce: tc.claim})
			issue := httptest.NewRecorder()
			h.handleIssue(issue, httptest.NewRequest(http.MethodPost, "/callers/issue", strings.NewReader(`{"owner_user":"alice","project":"search","allow":["default"]}`)))
			if issue.Code != http.StatusCreated {
				t.Fatalf("issue fixture status = %d: %s", issue.Code, issue.Body.String())
			}
			var issued callerAdminIssueResponse
			if err := json.Unmarshal(issue.Body.Bytes(), &issued); err != nil {
				t.Fatal(err)
			}
			body := `{"caller_id":"` + issued.Caller.ID + `","action":"suspend","reason":"edr-alert"}`
			p := Principal{Subject: authzRoleContainment, Kind: "webhook", containmentNonce: "nonce-0123456789", containmentBodyHash: "hash", containmentRetention: 20 * time.Minute}
			req := httptest.NewRequest(http.MethodPost, containmentWebhookPath, strings.NewReader(body)).WithContext(WithPrincipal(tc.ctx, p))
			rec := httptest.NewRecorder()
			h.handleWebhookContainment(rec, req)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("nonce-store status = %d", rec.Code)
			}
		})
	}
}

func TestAdminIdempotencyJanitorPrunesOnTicker(t *testing.T) {
	db := callerAdminTestDB(t)
	store, err := NewSQLAdminIdempotencyStore(db, AdminIdempotencyConfig{})
	if err != nil {
		t.Fatal(err)
	}
	scope := adminIdempotencyScope{PrincipalKind: "test", PrincipalSubject: "janitor", Domain: AdminGlobalDomain, Operation: "test.prune", Key: "expired", RequestHash: "hash"}
	if _, _, err := store.Begin(scope, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`UPDATE router_admin_idempotency SET expires_at = ? WHERE idempotency_key = ?`, adminIdempotencyTime(time.Now().Add(-time.Second)), scope.Key).Error; err != nil {
		t.Fatal(err)
	}
	counting := &countingPruneStore{AdminIdempotencyKeyStore: store}
	stop := make(chan struct{})
	done := StartAdminIdempotencyJanitor(counting, 10*time.Millisecond, stop)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var count int64
		if err := db.Table(adminIdempotencyTable).Where("idempotency_key = ?", scope.Key).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count == 0 && counting.calls.Load() >= 3 {
			close(stop)
			<-done
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(stop)
	<-done
	t.Fatal("expired idempotency row was not pruned by the janitor")
}

type countingPruneStore struct {
	AdminIdempotencyKeyStore
	calls atomic.Int64
}

func (s *countingPruneStore) PruneExpired(now time.Time) (int64, error) {
	s.calls.Add(1)
	return s.AdminIdempotencyKeyStore.PruneExpired(now)
}

type testAuthenticator struct{}

func (testAuthenticator) Authenticate(context.Context, *http.Request) (Principal, error) {
	return Principal{}, ErrInvalidCredential
}

func TestCallerContainmentHTTPFlow(t *testing.T) {
	legacyMux := http.NewServeMux()
	RegisterCallerDirectoryAdmin(legacyMux, CallerAdminDeps{})
	if _, pattern := legacyMux.Handler(httptest.NewRequest(http.MethodPost, "/admin/containment", nil)); pattern != "" {
		t.Fatalf("legacy serving port unexpectedly mounted containment at %q", pattern)
	}
	db := callerAdminTestDB(t)
	allow := true
	fakeNow := time.Now().UTC().Add(24 * time.Hour)
	h := newCallerAdminHandler(CallerAdminDeps{
		DB: db, RuntimeScope: "staging",
		Authorize: func(_ *http.Request, _, _ string) bool { return allow },
		CallerWriteAuthorized: func(r *http.Request, _ string) bool {
			p, ok := PrincipalFrom(r.Context())
			return !ok || p.Subject != authzRoleContainment
		},
		Now: func() time.Time { return fakeNow },
	})
	issueBody, _ := json.Marshal(map[string]any{"owner_user": "Alice", "project": "Search", "allow": []string{"default"}})
	issueRec := httptest.NewRecorder()
	h.handleIssue(issueRec, httptest.NewRequest(http.MethodPost, "/admin/callers/issue", strings.NewReader(string(issueBody))))
	if issueRec.Code != http.StatusCreated {
		t.Fatalf("issue status = %d", issueRec.Code)
	}
	var issued callerAdminIssueResponse
	if err := json.Unmarshal(issueRec.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	contain := func(action, ttl string, ctx context.Context) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(callerContainmentRequest{CallerID: issued.Caller.ID, Action: action, Reason: "edr-alert", TTL: ttl})
		req := httptest.NewRequest(http.MethodPost, "/admin/containment", strings.NewReader(string(body))).WithContext(ctx)
		rec := httptest.NewRecorder()
		h.handleContainment(rec, req)
		return rec
	}
	allow = false
	if got := contain("suspend", "5m", context.Background()).Code; got != http.StatusForbidden {
		t.Fatalf("unauthorized containment status = %d", got)
	}
	allow = true
	if got := contain("suspend", "not-a-duration", context.Background()).Code; got != http.StatusBadRequest {
		t.Fatalf("invalid TTL status = %d", got)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if got := contain("suspend", "5m", canceled).Code; got != http.StatusInternalServerError {
		t.Fatalf("canceled containment status = %d", got)
	}
	expired, expire := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expire()
	if got := contain("suspend", "5m", expired).Code; got != http.StatusInternalServerError {
		t.Fatalf("expired containment request status = %d", got)
	}
	var status string
	if err := db.Raw(`SELECT status FROM router_config_callers WHERE config_set_id = ? AND caller_id = ?`, "set-1", issued.Caller.ID).Row().Scan(&status); err != nil || status != accountStatusActive {
		t.Fatalf("caller status after canceled request = %q, %v", status, err)
	}
	if got := contain("suspend", "5m", context.Background()).Code; got != http.StatusOK {
		t.Fatalf("suspend status = %d", got)
	}
	var raw string
	if err := db.Raw(`SELECT canonical_yaml FROM router_config_documents WHERE config_set_id = ?`, "set-1").Row().Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	var caller CallerConfig
	for _, candidate := range cfg.Callers {
		if candidate.ID == issued.Caller.ID {
			caller = candidate
		}
	}
	if caller.Status != accountStatusSuspended || caller.ContainmentExpiresAt == "" {
		t.Fatalf("canonical caller containment = status %q expiry %q", caller.Status, caller.ContainmentExpiresAt)
	}
	containmentCtx := WithPrincipal(context.Background(), Principal{Subject: authzRoleContainment, Kind: "adminkey"})
	if got := contain("resume", "", containmentCtx).Code; got != http.StatusConflict {
		t.Fatalf("containment role resumed an administrator suspension: %d", got)
	}
	if got := contain("resume", "", context.Background()).Code; got != http.StatusOK {
		t.Fatalf("administrator resume status = %d", got)
	}
	if got := contain("suspend", "5m", containmentCtx).Code; got != http.StatusOK {
		t.Fatalf("containment suspend status = %d", got)
	}
	if got := contain("resume", "", containmentCtx).Code; got != http.StatusOK {
		t.Fatalf("containment resume status = %d", got)
	}
	auth := newCallerAuthState()
	runtimeCaller := &callerRuntime{cfg: caller, keyStatus: normalizeStatusDefault(caller.Status)}
	auth.rebuild([]*callerRuntime{runtimeCaller})
	svc := &Service{callerAuth: auth}
	if _, _, err := svc.authenticate("Bearer "+issued.Token, ""); err == nil {
		t.Fatal("suspended caller authenticated")
	}
	caller.Status = accountStatusActive
	caller.ContainmentExpiresAt = ""
	runtimeCaller = &callerRuntime{cfg: caller, keyStatus: accountStatusActive}
	auth.rebuild([]*callerRuntime{runtimeCaller})
	if _, _, err := svc.authenticate("Bearer "+issued.Token, ""); err != nil {
		t.Fatalf("resumed caller failed authentication: %v", err)
	}
}

func TestContainmentSuspensionOwnershipUsesAuthorization(t *testing.T) {
	db := callerAdminTestDB(t)
	cfg := &Config{Server: ServerConfig{AdminAuth: AdminAuthConfig{APIKeys: []AdminAPIKeyConfig{
		{KeyID: "secops", Subject: "adminkey:secops", Domain: "*", Status: "active"},
		{KeyID: "full", Subject: "adminkey:full", Domain: "*", Status: "active"},
	}}}}
	authz, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: []string{
		"g, adminkey:secops, role:containment, *",
		"p, role:containment, *, admin:containment, contain",
		"g, adminkey:full, role:full-admin, *",
		"p, role:full-admin, *, admin:containment, contain",
		"p, role:full-admin, *, admin:callers, issue",
		"p, role:full-admin, *, admin:callers, rotate",
		"p, role:full-admin, *, admin:callers, revoke",
	}}, nil, nil, cfg.Server.AdminAuth)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{cfg: cfg}
	svc.authorizerSnapshot.Store(authz)
	principalOf := func(r *http.Request) (Principal, string, bool) {
		p, ok := PrincipalFrom(r.Context())
		return p, AdminGlobalDomain, ok
	}
	decision := func(ctx context.Context, p Principal, domain, object, action string) error {
		return svc.Authorize(ctx, p, domain, object, action)
	}
	h := newCallerAdminHandler(AdminV1CallerAdminDeps(CallerAdminDeps{DB: db, RuntimeScope: "staging"}, principalOf, decision))
	secops := Principal{Subject: "adminkey:secops", Kind: "adminkey", CredentialID: "secops"}
	full := Principal{Subject: "adminkey:full", Kind: "adminkey", CredentialID: "full"}
	contain := func(p Principal, callerID, action, reason string) *httptest.ResponseRecorder {
		t.Helper()
		body := `{"caller_id":"` + callerID + `","action":"` + action + `","reason":"` + reason + `"}`
		req := httptest.NewRequest(http.MethodPost, "/admin/containment", strings.NewReader(body)).WithContext(WithPrincipal(context.Background(), p))
		rec := httptest.NewRecorder()
		h.handleContainment(rec, req)
		return rec
	}
	issue := func(owner string) string {
		t.Helper()
		body := `{"owner_user":"` + owner + `","project":"search","allow":["default"]}`
		req := httptest.NewRequest(http.MethodPost, "/admin/callers/issue", strings.NewReader(body)).WithContext(WithPrincipal(context.Background(), full))
		rec := httptest.NewRecorder()
		h.handleIssue(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("full admin issue status = %d: %s", rec.Code, rec.Body.String())
		}
		var issued callerAdminIssueResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
			t.Fatal(err)
		}
		return issued.Caller.ID
	}
	adminSuspended, containmentSuspended := issue("alice"), issue("bob")
	if rec := contain(full, adminSuspended, "suspend", "admin-review"); rec.Code != http.StatusOK {
		t.Fatalf("full admin suspend status = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := contain(secops, adminSuspended, "resume", "edr-cleared"); rec.Code != http.StatusConflict {
		t.Fatalf("Casbin-mapped containment key resumed admin suspension: %d", rec.Code)
	}
	if rec := contain(secops, containmentSuspended, "suspend", "edr-alert"); rec.Code != http.StatusOK {
		t.Fatalf("Casbin-mapped containment suspend status = %d: %s", rec.Code, rec.Body.String())
	}
	if got := readContainmentSource(t, db, containmentSuspended); got != "containment" {
		t.Fatalf("Casbin-mapped suspension source = %q", got)
	}
	if rec := contain(secops, containmentSuspended, "resume", "edr-cleared"); rec.Code != http.StatusOK {
		t.Fatalf("Casbin-mapped containment resume status = %d: %s", rec.Code, rec.Body.String())
	}
	if got := readContainmentSource(t, db, adminSuspended); got != "admin" {
		t.Fatalf("full admin suspension source = %q", got)
	}
	if rec := contain(full, adminSuspended, "resume", "admin-cleared"); rec.Code != http.StatusOK {
		t.Fatalf("full admin resume status = %d: %s", rec.Code, rec.Body.String())
	}
}

func readContainmentSource(t *testing.T, db *gorm.DB, callerID string) string {
	t.Helper()
	var raw string
	if err := db.Raw(`SELECT canonical_yaml FROM router_config_documents WHERE config_set_id = ?`, "set-1").Row().Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	for _, caller := range cfg.Callers {
		if caller.ID == callerID {
			return caller.ContainmentSource
		}
	}
	t.Fatalf("caller %q missing from canonical config", callerID)
	return ""
}

func TestCallerContainmentAdminV1EndToEnd(t *testing.T) {
	harness := newAdminAuditHarness(t)
	harness.authorize.denyAll = true
	reason := strings.Join([]string{"sk", "fakecredential12345"}, "-")
	body := callerContainmentRequest{CallerID: "alice", Action: "suspend", Reason: reason, TTL: "10m"}
	denied := harness.do(t, http.MethodPost, "/admin/v1/containment", body, map[string]string{
		"Idempotency-Key": "contain-denied", "If-Match": harness.currentETag(t),
	})
	if denied.Code != http.StatusForbidden {
		t.Fatalf("denied containment status = %d", denied.Code)
	}
	harness.authorize.denyAll = false
	changed := harness.do(t, http.MethodPost, "/admin/v1/containment", body, map[string]string{
		"Idempotency-Key": "contain-suspend", "If-Match": harness.currentETag(t),
	})
	if changed.Code != http.StatusOK {
		t.Fatalf("suspend status = %d", changed.Code)
	}
	var status string
	if err := harness.db.Raw(`SELECT status FROM router_config_callers WHERE caller_id = ?`, "alice").Row().Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != accountStatusSuspended {
		t.Fatalf("stored caller status = %q", status)
	}
	events := harness.changeEvents(t)
	foundContainmentAudit := false
	for _, event := range events {
		if event.Action == "caller_containment_suspend" {
			foundContainmentAudit = true
			if strings.Contains(event.Ticket, body.Reason) {
				t.Fatal("secret-like reason code persisted in the audit event")
			}
		}
	}
	if !foundContainmentAudit {
		t.Fatal("containment mutation did not emit an audit event")
	}
	replayed := harness.do(t, http.MethodPost, "/admin/v1/containment", body, map[string]string{
		"Idempotency-Key": "contain-suspend", "If-Match": `"stale"`,
	})
	if replayed.Code != http.StatusOK {
		t.Fatalf("idempotent retry status = %d", replayed.Code)
	}
	body.Action, body.TTL = "resume", ""
	released := harness.do(t, http.MethodPost, "/admin/v1/containment", body, map[string]string{
		"Idempotency-Key": "contain-resume", "If-Match": harness.currentETag(t),
	})
	if released.Code != http.StatusOK {
		t.Fatalf("resume status = %d", released.Code)
	}
	if err := harness.db.Raw(`SELECT status FROM router_config_callers WHERE caller_id = ?`, "alice").Row().Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != accountStatusActive {
		t.Fatalf("stored caller status after resume = %q", status)
	}
}

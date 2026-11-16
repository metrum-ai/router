// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
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
	makeRequest("nonce-positive-0001", now, true)
	// Replays are rejected by the persistent SQL idempotency claim.
	req := httptest.NewRequest("POST", containmentWebhookPath, strings.NewReader(body))
	timestamp := strconv.FormatInt(now.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "\nnonce-positive-0001\n" + body))
	req.Header.Set("X-Containment-Timestamp", timestamp)
	req.Header.Set("X-Containment-Nonce", "nonce-positive-0001")
	req.Header.Set("X-Containment-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	if _, err := auth.Authenticate(context.Background(), req); err == nil {
		t.Fatal("Authenticate() accepted replayed nonce")
	}
	makeRequest("nonce-invalid-0002", now, false)
	makeRequest("nonce-stale-0003", now.Add(-6*time.Minute), true)
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
		Now:       func() time.Time { return fakeNow },
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
	auth := newCallerAuthState()
	runtimeCaller := &callerRuntime{cfg: caller, keyStatus: normalizeStatusDefault(caller.Status)}
	auth.rebuild([]*callerRuntime{runtimeCaller})
	svc := &Service{callerAuth: auth}
	if _, _, err := svc.authenticate("Bearer "+issued.Token, ""); err == nil {
		t.Fatal("suspended caller authenticated")
	}
	if got := contain("resume", "", context.Background()).Code; got != http.StatusOK {
		t.Fatalf("resume status = %d", got)
	}
	caller.Status = accountStatusActive
	caller.ContainmentExpiresAt = ""
	runtimeCaller = &callerRuntime{cfg: caller, keyStatus: accountStatusActive}
	auth.rebuild([]*callerRuntime{runtimeCaller})
	if _, _, err := svc.authenticate("Bearer "+issued.Token, ""); err != nil {
		t.Fatalf("resumed caller failed authentication: %v", err)
	}
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
	for _, event := range events {
		if event.Action == "caller_containment_suspend" && strings.Contains(event.Ticket, body.Reason) {
			t.Fatal("secret-like reason code persisted in the audit event")
		}
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

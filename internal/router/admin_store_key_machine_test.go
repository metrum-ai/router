// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// API-issued admin keys are accepted by the same immutable machine generation as
// every other credential: the generation carries a snapshot of the
// authoritative rows, and each presented key is still rechecked against the
// store. These are the negative regressions for that contract.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqliteDriver "github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type storeKeyFixture struct {
	svc    *Service
	store  *adminKeyStore
	now    time.Time
	events *gorm.DB
}

// newStoreKeyFixture builds a Service with the authoritative key store, the
// durable credential-event table and the production machine runtime factory.
func newStoreKeyFixture(t *testing.T, grant func(subject string) []string) (*storeKeyFixture, *authorizer) {
	t.Helper()
	db, err := gorm.Open(sqliteDriver.Open(filepath.Join(t.TempDir(), "keys.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyAdminKeySchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(adminCredentialEventsTestDDL).Error; err != nil {
		t.Fatal(err)
	}
	if err := applyAdminEmergencyActivationSchema(db); err != nil {
		t.Fatal(err)
	}
	fixture := &storeKeyFixture{now: time.Now().UTC(), events: db}
	cfg := &Config{}
	cfg.Admin.Enabled = true
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true}
	snap := NewConfigSnapshot()
	snap.Store(cfg)
	svc := &Service{
		cfg:                cfg,
		configSnapshot:     snap,
		policyFreshness:    newPolicyFreshnessState(30 * time.Second),
		quota:              &quotaStore{callers: map[string]*callerRuntime{}},
		usage:              &usageStore{db: db},
		callerAuth:         &callerAuthState{},
		configControlPlane: db,
	}
	// Production init: the key store (with the #869 lifecycle recorder) and the
	// machine runtime factory.
	if err := svc.initAdminKeys(); err != nil {
		t.Fatal(err)
	}
	fixture.store = svc.adminKeys
	fixture.store.now = func() time.Time { return fixture.now }
	if err := svc.initAdminMachineRuntime(); err != nil {
		t.Fatal(err)
	}
	fixture.svc = svc
	return fixture, nil
}

// publishWithGrant publishes a complete generation whose authorizer carries the
// given policy lines, exactly as a configuration/policy publication would.
func (f *storeKeyFixture) publishWithGrant(t *testing.T, lines []string) {
	t.Helper()
	// The published configuration carries the same policy, so a guard built from
	// the configuration agrees with the authorizer that is published.
	f.svc.cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: lines}
	authz, err := newAuthorizer(f.svc.cfg.Server.AdminAuth.Authorization, f.svc.quota.callers, f.svc.usage, f.svc.cfg.Server.AdminAuth)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.publishMachinePlane(func(machinePlaneSnapshot) (*machinePlanePublication, error) {
		generation, err := f.svc.prepareAdminMachineConfigGeneration(f.svc.cfg, authz)
		if err != nil {
			return nil, err
		}
		return &machinePlanePublication{Generation: generation, Authorizer: authz}, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAdminIssuedKeyAuthenticatesThroughPublishedGeneration(t *testing.T) {
	fixture, _ := newStoreKeyFixture(t, nil)
	issued, err := fixture.store.issue(context.Background(), adminKeyIssueRequest{ExpiresAt: fixture.now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	fixture.publishWithGrant(t, []string{"p, " + issued.Subject + ", *, admin:config, read"})
	if code := machineAuthorizeRequest(fixture.svc, issued.Secret); code != http.StatusNoContent {
		t.Fatalf("issued key through the machine generation = %d, want 204", code)
	}
	// The production recorder wrote the issue event at issue time and the
	// authentication event at request time, both for this credential.
	var authenticated int
	for _, row := range adminKeyEventsIn(t, fixture.events) {
		if row.Kind == "adminkey" && row.Outcome == "success" && row.CredentialID == issued.ID && row.Lifecycle == "authenticate" {
			authenticated += row.Count
		}
	}
	if authenticated != 1 {
		t.Fatalf("issued key authentication events = %d, want 1 (%+v)", authenticated, adminKeyEventsIn(t, fixture.events))
	}
	// An issued key is its own subject: it does not inherit configured-key
	// compatibility grants, so an operation with no policy binding is denied at
	// the sole authorizer even though the identity authenticated.
	if code := machineAuthorizeRequest(fixture.svc, issued.Secret); code != http.StatusNoContent {
		t.Fatalf("repeat request = %d", code)
	}
	unbound := machineAuthorizeRequestObject(fixture.svc, issued.Secret, "admin:keys", "issue")
	if unbound != http.StatusForbidden {
		t.Fatalf("unbound issued key on a privileged operation = %d, want 403", unbound)
	}
}

func TestAdminRevokedAndExpiredKeysStopAuthenticating(t *testing.T) {
	fixture, _ := newStoreKeyFixture(t, nil)
	issued, err := fixture.store.issue(context.Background(), adminKeyIssueRequest{ExpiresAt: fixture.now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	fixture.publishWithGrant(t, []string{"p, " + issued.Subject + ", *, admin:config, read"})
	if code := machineAuthorizeRequest(fixture.svc, issued.Secret); code != http.StatusNoContent {
		t.Fatalf("issued key = %d, want 204", code)
	}
	if err := fixture.store.revoke(context.Background(), issued.ID); err != nil {
		t.Fatal(err)
	}
	// Revocation is authoritative on the next request even before any
	// republication excludes the key from the snapshot.
	if code := machineAuthorizeRequest(fixture.svc, issued.Secret); code != http.StatusUnauthorized {
		t.Fatalf("revoked key = %d, want 401", code)
	}
	if err := fixture.svc.reloadMachineCredentials(); err != nil {
		t.Fatal(err)
	}
	if code := machineAuthorizeRequest(fixture.svc, issued.Secret); code != http.StatusUnauthorized {
		t.Fatalf("revoked key after republication = %d, want 401", code)
	}

	// Absolute expiry, evaluated against the store clock.
	second, err := fixture.store.issue(context.Background(), adminKeyIssueRequest{ExpiresAt: fixture.now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	fixture.publishWithGrant(t, []string{"p, " + second.Subject + ", *, admin:config, read"})
	if err := fixture.svc.reloadMachineCredentials(); err != nil {
		t.Fatal(err)
	}
	if code := machineAuthorizeRequest(fixture.svc, second.Secret); code != http.StatusNoContent {
		t.Fatalf("second key = %d, want 204", code)
	}
	fixture.now = fixture.now.Add(2 * time.Hour)
	if code := machineAuthorizeRequest(fixture.svc, second.Secret); code != http.StatusUnauthorized {
		t.Fatalf("expired key = %d, want 401", code)
	}
}

func TestAdminRotatedKeyOverlapWindows(t *testing.T) {
	fixture, _ := newStoreKeyFixture(t, nil)
	issued, err := fixture.store.issue(context.Background(), adminKeyIssueRequest{ExpiresAt: fixture.now.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	fixture.publishWithGrant(t, []string{"p, " + issued.Subject + ", *, admin:config, read"})
	rotated, err := fixture.store.rotate(context.Background(), issued.ID, adminKeyRotateRequest{Overlap: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.svc.reloadMachineCredentials(); err != nil {
		t.Fatal(err)
	}
	for name, secret := range map[string]string{"previous generation": issued.Secret, "current generation": rotated.Secret} {
		if code := machineAuthorizeRequest(fixture.svc, secret); code != http.StatusNoContent {
			t.Fatalf("%s during overlap = %d, want 204", name, code)
		}
	}
	fixture.now = fixture.now.Add(11 * time.Minute)
	if code := machineAuthorizeRequest(fixture.svc, issued.Secret); code != http.StatusUnauthorized {
		t.Fatalf("previous generation after overlap = %d, want 401", code)
	}
	if code := machineAuthorizeRequest(fixture.svc, rotated.Secret); code != http.StatusNoContent {
		t.Fatalf("current generation after overlap = %d, want 204", code)
	}
	// The store's authoritative verdict alone refuses the old generation, even
	// against a generation snapshot that still contains it.
	if err := fixture.svc.reloadMachineCredentials(); err != nil {
		t.Fatal(err)
	}
	if code := machineAuthorizeRequest(fixture.svc, issued.Secret); code != http.StatusUnauthorized {
		t.Fatalf("previous generation after republication = %d, want 401", code)
	}
}

// A file-mode service (no control-plane database) never advertises or accepts
// API-issued keys: the store, the routes and the snapshot are all absent.
func TestAdminFileModeHasNoIssuedKeys(t *testing.T) {
	svc := &Service{adminKeys: nil}
	registry, err := svc.adminKeyRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range registry.Operations() {
		if op.Object == adminKeyObject {
			t.Fatalf("file-mode service advertised %s", op.ID)
		}
	}
}

// Revoking the last enabled administrator must be refused BEFORE the store row
// changes: the guard evaluates the candidate inventory (the published generation
// minus the credential being revoked), not the published one that still contains
// it.
func TestAdminRevokeLastAdministratorIsRefusedBeforeMutation(t *testing.T) {
	fixture, _ := newStoreKeyFixture(t, nil)
	issued, err := fixture.store.issue(context.Background(), adminKeyIssueRequest{ExpiresAt: fixture.now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	// The issued key is the only machine administrator able to administer policy.
	fixture.publishWithGrant(t, []string{
		"p, " + issued.Subject + ", *, admin:policy, write|activate",
		"g, " + issued.Subject + ", " + securityAdministratorRole + ", *",
	})
	if code := machineAuthorizeRequestObject(fixture.svc, issued.Secret, "admin:policy", "write"); code != http.StatusNoContent {
		t.Fatalf("issued key cannot administer policy before the revocation = %d", code)
	}
	mux := newAdminMux()
	if err := RegisterAdminV1(mux, AdminV1Deps{
		Registry: func() *AdminV1Registry {
			registry, err := NewAdminV1Registry(adminKeyOperations())
			if err != nil {
				t.Fatal(err)
			}
			return registry
		}(),
		Authenticate: AuthenticatorFunc(func(context.Context, *http.Request) (Principal, error) {
			return Principal{Subject: "adminkey:root", Kind: "adminkey", CredentialID: "root"}, nil
		}),
		Authorize: func(context.Context, Principal, string, string, string) error { return nil },
		Handlers:  fixture.svc.adminKeyHandlers(),
		CurrentETag: func(*http.Request) (string, int64, error) {
			return AdminETagForRevision(1), 1, nil
		},
		Idempotency:      newAdminV1DBHarnessIdempotency(t, fixture.events),
		IdempotencyOn:    true,
		Denials:          adminDenialSinkFunc(func(context.Context, AdminDenialRecord) error { return nil }),
		CredentialEvents: adminCredentialEventSinkFunc(func(context.Context, AdminCredentialEvent) error { return nil }),
	}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/admin/v1/admin-keys/"+issued.ID+"/revoke", nil)
	request.Header.Set("If-Match", AdminETagForRevision(1))
	request.Header.Set("Idempotency-Key", "revoke-1")
	recorder := httptest.NewRecorder()
	mux.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("revoking the last administrator = %d body=%s, want 409", recorder.Code, recorder.Body.String())
	}
	if fixture.svc.adminMetrics().policyGuardRefusals != 1 {
		t.Fatal("refusal was not counted")
	}
	// Nothing was revoked: the key still authenticates and still authorizes.
	if code := machineAuthorizeRequestObject(fixture.svc, issued.Secret, "admin:policy", "write"); code != http.StatusNoContent {
		t.Fatalf("refused revocation still disabled the key = %d", code)
	}
	// Positive control: a second administrator makes the revocation legal.
	second, err := fixture.store.issue(context.Background(), adminKeyIssueRequest{ExpiresAt: fixture.now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	fixture.publishWithGrant(t, []string{
		"p, " + issued.Subject + ", *, admin:policy, write|activate",
		"p, " + second.Subject + ", *, admin:policy, write|activate",
		"g, " + issued.Subject + ", " + securityAdministratorRole + ", *",
		"g, " + second.Subject + ", " + securityAdministratorRole + ", *",
	})
	if err := fixture.svc.reloadMachineCredentials(); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest("POST", "/admin/v1/admin-keys/"+issued.ID+"/revoke", nil)
	request.Header.Set("If-Match", AdminETagForRevision(1))
	request.Header.Set("Idempotency-Key", "revoke-2")
	recorder = httptest.NewRecorder()
	mux.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("revoking with another administrator left = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
}

func adminKeyEventsIn(t *testing.T, db *gorm.DB) []machineCredentialEventRowCount {
	t.Helper()
	var rows []machineCredentialEventRowCount
	if err := db.Raw(`SELECT kind, credential_id, outcome, lifecycle, count FROM router_admin_credential_events ORDER BY at`).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

type failingKeyRecorder struct{ err error }

func (f failingKeyRecorder) record(*gorm.DB, adminKeyLifecycleEvent) error { return f.err }

// #869: every lifecycle mutation writes its credential event inside the key
// transaction, and a failed event rolls the mutation back.
func TestAdminKeyLifecycleAuditInProductionInit(t *testing.T) {
	fixture, _ := newStoreKeyFixture(t, nil)
	if fixture.store.recorder == nil {
		t.Fatal("production init did not install the credential-event recorder")
	}
	issued, err := fixture.store.issue(context.Background(), adminKeyIssueRequest{ExpiresAt: fixture.now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.rotate(context.Background(), issued.ID, adminKeyRotateRequest{Overlap: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.revoke(context.Background(), issued.ID); err != nil {
		t.Fatal(err)
	}
	rows := adminKeyEventsIn(t, fixture.events)
	lifecycles := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Kind != "adminkey" || row.Outcome != "success" || row.CredentialID != issued.ID {
			t.Fatalf("unexpected credential event %+v", row)
		}
		lifecycles = append(lifecycles, row.Lifecycle)
	}
	if len(lifecycles) != 3 || lifecycles[0] != "issue" || lifecycles[1] != "rotate" || lifecycles[2] != "revoke" {
		t.Fatalf("lifecycle events = %v, want issue, rotate, revoke", lifecycles)
	}

	// A recorder failure rolls the mutation back: no key and no event.
	failing, _ := newStoreKeyFixture(t, nil)
	failing.store.recorder = failingKeyRecorder{err: errors.New("audit unavailable")}.record
	if _, err := failing.store.issue(context.Background(), adminKeyIssueRequest{ExpiresAt: failing.now.Add(time.Hour)}); err == nil {
		t.Fatal("issue succeeded without its audit event")
	}
	if list, err := failing.store.list(context.Background(), adminKeyListRequest{}); err != nil || len(list.Keys) != 0 {
		t.Fatalf("failed audit left a key row: %+v err=%v", list.Keys, err)
	}
	if rows := adminKeyEventsIn(t, failing.events); len(rows) != 0 {
		t.Fatalf("failed audit wrote events: %+v", rows)
	}
}

// The revoke guard must fail CLOSED: an unevaluable guard answers 503 and leaves
// the credential untouched instead of allowing the revocation.
func TestAdminRevokeGuardFailsClosed(t *testing.T) {
	fixture, _ := newStoreKeyFixture(t, nil)
	issued, err := fixture.store.issue(context.Background(), adminKeyIssueRequest{ExpiresAt: fixture.now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	fixture.publishWithGrant(t, []string{"p, " + issued.Subject + ", *, admin:keys, list"})
	// Break the candidate build: an invalid policy is a configuration error, so the
	// guard cannot be evaluated.
	broken := *fixture.svc.cfg
	broken.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, broken"}}
	fixture.svc.configSnapshot.Store(&broken)
	mux := newAdminMux()
	registry, err := NewAdminV1Registry(adminKeyOperations())
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterAdminV1(mux, AdminV1Deps{
		Registry: registry,
		Authenticate: AuthenticatorFunc(func(context.Context, *http.Request) (Principal, error) {
			return Principal{Subject: "adminkey:root", Kind: "adminkey"}, nil
		}),
		Authorize:        func(context.Context, Principal, string, string, string) error { return nil },
		Handlers:         fixture.svc.adminKeyHandlers(),
		CurrentETag:      func(*http.Request) (string, int64, error) { return AdminETagForRevision(1), 1, nil },
		Idempotency:      newAdminV1DBHarnessIdempotency(t, fixture.events),
		IdempotencyOn:    true,
		Denials:          adminDenialSinkFunc(func(context.Context, AdminDenialRecord) error { return nil }),
		CredentialEvents: adminCredentialEventSinkFunc(func(context.Context, AdminCredentialEvent) error { return nil }),
	}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/admin/v1/admin-keys/"+issued.ID+"/revoke", nil)
	request.Header.Set("If-Match", AdminETagForRevision(1))
	request.Header.Set("Idempotency-Key", "revoke-guard-1")
	recorder := httptest.NewRecorder()
	mux.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unevaluable guard = %d body=%s, want 503", recorder.Code, recorder.Body.String())
	}
	key, err := fixture.store.get(context.Background(), issued.ID)
	if err != nil || key.Status != "active" || key.RevokedAt != nil {
		t.Fatalf("guard failure mutated the credential: %+v err=%v", key, err)
	}
}

// An unclassified store failure is an operational failure, never a client error.
func TestAdminKeyStoreProblemMapping(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"validation", fmt.Errorf("%w: expiry", errAdminKeyInvalidRequest), http.StatusBadRequest},
		{"absent", errAdminKeyAbsent, http.StatusNotFound},
		{"revoked", errAdminKeyRevoked, http.StatusConflict},
		{"overlap", errAdminKeyOverlapActive, http.StatusConflict},
		{"secret", errAdminKeySecretUnavailable, http.StatusConflict},
		{"idempotency", errAdminKeyIdempotencyConflict, http.StatusConflict},
		{"database failure", errors.New("database is locked"), http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			adminKeyStoreProblem(recorder, httptest.NewRequest("POST", "/admin/v1/admin-keys", nil), tc.err)
			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.status)
			}
		})
	}
}

// A publication failure AFTER the commit must not destroy the one-time secret:
// the key is returned with published=false, logged, and the secret stays usable
// once a later publication carries it.
func TestAdminKeyIssueAndRotateKeepSecretWhenPublicationFails(t *testing.T) {
	fixture, _ := newStoreKeyFixture(t, nil)
	mux := newAdminMux()
	registry, err := NewAdminV1Registry(adminKeyOperations())
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterAdminV1(mux, AdminV1Deps{
		Registry: registry,
		Authenticate: AuthenticatorFunc(func(context.Context, *http.Request) (Principal, error) {
			return Principal{Subject: "adminkey:root", Kind: "adminkey"}, nil
		}),
		Authorize:        func(context.Context, Principal, string, string, string) error { return nil },
		Handlers:         fixture.svc.adminKeyHandlers(),
		CurrentETag:      func(*http.Request) (string, int64, error) { return AdminETagForRevision(1), 1, nil },
		Idempotency:      newAdminV1DBHarnessIdempotency(t, fixture.events),
		IdempotencyOn:    true,
		Denials:          adminDenialSinkFunc(func(context.Context, AdminDenialRecord) error { return nil }),
		CredentialEvents: adminCredentialEventSinkFunc(func(context.Context, AdminCredentialEvent) error { return nil }),
	}); err != nil {
		t.Fatal(err)
	}
	// Break the factory so the post-commit publication fails.
	fixture.svc.machineGenerationBuilder = func(*Config, *authorizer) (*adminMachineGeneration, error) {
		return nil, errors.New("publication unavailable")
	}
	do := func(method, path, body, idempotency string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("If-Match", AdminETagForRevision(1))
		request.Header.Set("Idempotency-Key", idempotency)
		recorder := httptest.NewRecorder()
		mux.mux.ServeHTTP(recorder, request)
		return recorder
	}
	expires := fixture.now.Add(time.Hour).Format(time.RFC3339)
	issued := do("POST", "/admin/v1/admin-keys", `{"expires_at":"`+expires+`"}`, "pub-fail-issue")
	if issued.Code != http.StatusCreated {
		t.Fatalf("issue with failed publication = %d body=%s, want 201", issued.Code, issued.Body.String())
	}
	var created struct {
		ID        string `json:"id"`
		Secret    string `json:"secret"`
		Published *bool  `json:"published"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Secret == "" || created.Published == nil || *created.Published {
		t.Fatalf("secret dropped or wrongly marked published: %+v", created)
	}
	rotated := do("POST", "/admin/v1/admin-keys/"+created.ID+"/rotate", `{"overlap":"5m"}`, "pub-fail-rotate")
	if rotated.Code != http.StatusCreated {
		t.Fatalf("rotate with failed publication = %d body=%s, want 201", rotated.Code, rotated.Body.String())
	}
	var next struct {
		Secret    string `json:"secret"`
		Published *bool  `json:"published"`
	}
	if err := json.Unmarshal(rotated.Body.Bytes(), &next); err != nil {
		t.Fatal(err)
	}
	if next.Secret == "" || next.Secret == created.Secret || next.Published == nil || *next.Published {
		t.Fatalf("rotated secret dropped or wrongly marked published: %+v", next)
	}
	// The committed credentials exist: a later successful publication carries them.
	fixture.svc.machineGenerationBuilder = nil
	if err := fixture.svc.initAdminMachineRuntime(); err != nil {
		t.Fatal(err)
	}
	fixture.publishWithGrant(t, []string{"p, " + "adminkey:" + created.ID + ", *, admin:keys, list"})
	if err := fixture.svc.reloadMachineCredentials(); err != nil {
		t.Fatal(err)
	}
	if code := machineAuthorizeRequestObject(fixture.svc, next.Secret, "admin:keys", "list"); code != http.StatusNoContent {
		t.Fatalf("rotated secret not accepted after a later publication = %d", code)
	}
}

// Without the control-plane key migration the lifecycle surface stays unmounted:
// production init must not advertise routes backed by tables that do not exist.
func TestAdminKeyRoutesStayUnmountedWithoutTheMigration(t *testing.T) {
	db, err := gorm.Open(sqliteDriver.Open(filepath.Join(t.TempDir(), "unmigrated.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{}
	cfg.Admin.Enabled = true
	svc := &Service{cfg: cfg, configControlPlane: db, usage: &usageStore{db: db}}
	svc.configSnapshot = NewConfigSnapshot()
	svc.configSnapshot.Store(cfg)
	if err := svc.initAdminKeys(); err != nil {
		t.Fatal(err)
	}
	if svc.adminKeys != nil {
		t.Fatal("key store installed without its tables")
	}
	registry, err := svc.adminV1ProductionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range registry.Operations() {
		if op.Object == adminKeyObject {
			t.Fatalf("unmigrated service advertised %s", op.ID)
		}
	}
}

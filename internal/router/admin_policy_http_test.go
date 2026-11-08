// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"context"
	"encoding/json"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Exercises the real shared mount + Casbin + guarded backend. This is not an
// admin-listener, production migration, #853 adapter or replica acceptance test.
func TestPolicyAdminMountedCreateReadAndPreconditions(t *testing.T) {
	s, deps, p, lines := policyMutationFixture(t)
	lines[0] = strings.Replace(lines[0], "read|write|activate", "read|write|activate|rollback", 1)
	cfg := deps.PublishedConfig()
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Source: "inline", Policy: lines}
	oldCurrent := deps.Current
	deps.Current = func(tx *gorm.DB, cfg *Config) (*Service, error) {
		view, err := oldCurrent(tx, cfg)
		if err == nil {
			view.configSnapshot = NewConfigSnapshot()
			view.configSnapshot.Store(cfg)
		}
		return view, err
	}
	deps.Build = func(_ *gorm.DB, _ *Config, lines []string) (*Service, error) {
		a, e := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: lines}, nil, nil, AdminAuthConfig{})
		return &Service{authorizer: a}, e
	}
	registry, err := NewAdminV1Registry(policyOperationSpecs())
	if err != nil {
		t.Fatal(err)
	}
	current, err := deps.Current(s.db, deps.PublishedConfig())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	err = RegisterAdminV1(mux, AdminV1Deps{Registry: registry, Authenticate: &fakeAuthenticator{principal: p}, Authorize: current.Authorize,
		PolicyCurrentETag: func(*http.Request) (string, int64, error) {
			var r policyMutationRevision
			e := s.db.First(&r, 1).Error
			return AdminETagForRevision(r.Revision), r.Revision, e
		},
		IdempotencyOn: true, Idempotency: NewMemoryAdminIdempotencyStore(AdminIdempotencyConfig{}, 32),
		Denials: &recordingDenialSink{}, CredentialEvents: &recordingCredentialSink{},
		CursorCodec: NewAdminCursorCodec([]byte("policy-pagination-fixture-key-012345")),
		Handlers: newPolicyAdminHandlers(s, deps, func(tx *gorm.DB, rev int64, result policyResourceResult) error {
			return insertAuthzPolicyAudit(tx, result.SetID, p.Subject, "http-change", "request", "", time.Now())
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"id":"draft","name":"main","version":1,"rules":[{"subject_or_role":"root","domain":"*","object":"admin:policy","action":"read|write|activate"},{"subject_or_role":"root","domain":"*","object":"admin:reports","action":"read"}],"role_memberships":[{"subject":"root","role":"security-admin","domain":"*"}]}`
	request := func(method, path, tag, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(context.Background())
		r.Header.Set("Content-Type", "application/json")
		if tag != "" {
			r.Header.Set("If-Match", tag)
		}
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := request("POST", "/admin/v1/policy-sets", "", "create-draft"); w.Code != 428 {
		t.Fatalf("missing If-Match=%d %s", w.Code, w.Body.String())
	}
	created := request("POST", "/admin/v1/policy-sets", `"rev-0"`, "create-draft")
	if created.Code != 201 {
		t.Fatalf("create=%d %s", created.Code, created.Body.String())
	}
	if w := request("GET", "/admin/v1/policy-sets/draft", "", ""); w.Code != 200 || w.Header().Get("ETag") != `"rev-1"` {
		t.Fatalf("get=%d tag=%s body=%s", w.Code, w.Header().Get("ETag"), w.Body.String())
	}
	if w := request("PUT", "/admin/v1/policy-sets/draft", `"rev-0"`, "replace-draft"); w.Code != 412 {
		t.Fatalf("stale If-Match=%d %s", w.Code, w.Body.String())
	}
	body = strings.Replace(body, `"id":"draft"`, `"id":"second"`, 1)
	body = strings.Replace(body, `"name":"main"`, `"name":"second-main"`, 1)
	if w := request("POST", "/admin/v1/policy-sets", `"rev-1"`, "second-draft"); w.Code != 201 {
		t.Fatalf("second create=%d %s", w.Code, w.Body.String())
	}
	first := request("GET", "/admin/v1/policy-sets?limit=1", "", "")
	if first.Code != 200 {
		t.Fatalf("list=%d %s", first.Code, first.Body.String())
	}
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.Items[0].ID != "draft" {
		t.Fatalf("first page=%s err=%v", first.Body.String(), err)
	}
	link := first.Header().Get("Link")
	next, _, ok := strings.Cut(strings.TrimPrefix(link, "<"), ">")
	if !ok {
		t.Fatalf("missing shared signed cursor: %s", link)
	}
	last := request("GET", next, "", "")
	if last.Code != 200 {
		t.Fatalf("next page=%d %s", last.Code, last.Body.String())
	}
	if err := json.Unmarshal(last.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.Items[0].ID != "second" {
		t.Fatalf("next page=%s err=%v", last.Body.String(), err)
	}
	body = "{}"
	if w := request("POST", "/admin/v1/policy-sets/draft/activate", `"rev-2"`, "activate-first"); w.Code != 200 {
		t.Fatalf("activate first=%d %s", w.Code, w.Body.String())
	}
	if w := request("POST", "/admin/v1/policy-sets/second/activate", `"rev-3"`, "activate-second"); w.Code != 200 {
		t.Fatalf("activate second=%d %s", w.Code, w.Body.String())
	}
	rolled := request("POST", "/admin/v1/policy-sets/draft/rollback", `"rev-4"`, "rollback-first")
	var result policyMutationDocument
	if rolled.Code != 200 {
		t.Fatalf("rollback=%d %s", rolled.Code, rolled.Body.String())
	}
	if err := json.Unmarshal(rolled.Body.Bytes(), &result); err != nil || result.ID == "draft" || result.ID == "second" || result.ID == "" || result.Revision != 5 {
		t.Fatalf("rollback result=%s err=%v", rolled.Body.String(), err)
	}
}

// TestPolicyAdminMountedMutationWritesTheRealAuditRow mounts the guarded policy
// handlers with the PRODUCTION audit binding (#869) and proves through HTTP that
// one authenticated actor's mutation writes exactly one usage audit row in the
// change's transaction, carrying the mount-recorded audit context; when the audit
// insert is impossible, the request fails and the policy state does not change.
func TestPolicyAdminMountedMutationWritesTheRealAuditRow(t *testing.T) {
	s, deps, p, lines := policyMutationFixture(t)
	if err := applyUsageAdminAuditMigration(s.db); err != nil {
		t.Fatal(err)
	}
	cfg := deps.PublishedConfig()
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Source: "inline", Policy: lines}
	oldCurrent := deps.Current
	deps.Current = func(tx *gorm.DB, cfg *Config) (*Service, error) {
		view, err := oldCurrent(tx, cfg)
		if err == nil {
			view.configSnapshot = NewConfigSnapshot()
			view.configSnapshot.Store(cfg)
		}
		return view, err
	}
	deps.Build = func(_ *gorm.DB, _ *Config, lines []string) (*Service, error) {
		a, e := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: lines}, nil, nil, AdminAuthConfig{})
		return &Service{authorizer: a}, e
	}
	registry, err := NewAdminV1Registry(policyOperationSpecs())
	if err != nil {
		t.Fatal(err)
	}
	current, err := deps.Current(s.db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	err = RegisterAdminV1(mux, AdminV1Deps{
		Registry: registry, Authenticate: &fakeAuthenticator{principal: p}, Authorize: current.Authorize,
		PolicyCurrentETag: func(*http.Request) (string, int64, error) {
			var r policyMutationRevision
			e := s.db.First(&r, 1).Error
			return AdminETagForRevision(r.Revision), r.Revision, e
		},
		IdempotencyOn: true, Idempotency: NewMemoryAdminIdempotencyStore(AdminIdempotencyConfig{}, 32),
		Denials: &recordingDenialSink{}, CredentialEvents: &recordingCredentialSink{},
		CursorCodec: NewAdminCursorCodec([]byte("policy-audit-fixture-key-01234567")),
		// The real binding, not a fixture: the mount's authenticated audit
		// context becomes the same-transaction audit row.
		Handlers: newPolicyAdminHandlersWithAudit(s, deps, policyMutationAuditWriter),
	})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"id":"audited","name":"main","version":1,"rules":[{"subject_or_role":"root","domain":"*","object":"admin:policy","action":"read|write|activate"},{"subject_or_role":"root","domain":"*","object":"admin:reports","action":"read"}],"role_memberships":[{"subject":"root","role":"security-admin","domain":"*"}]}`
	request := func(method, path, tag, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("If-Match", tag)
		r.Header.Set("Idempotency-Key", key)
		r.Header.Set("X-Admin-Ticket", "INC-42")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := request("POST", "/admin/v1/policy-sets", `"rev-0"`, "audited-create"); w.Code != 201 {
		t.Fatalf("create=%d %s", w.Code, w.Body.String())
	}
	var rows []adminPolicyAuditRow
	if err := s.db.Table(usageAdminAuditTable).Order("at ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want exactly one for one mutation", len(rows))
	}
	row := rows[0]
	if row.PrincipalSubject != p.Subject || row.PrincipalKind != p.Kind || row.CredentialID != p.CredentialID {
		t.Fatalf("audit actor = %+v, want the authenticated principal %+v", row, p)
	}
	if row.OperationID != "createPolicySet" || row.Action != "write" || row.Object != "admin:policy" {
		t.Fatalf("audit operation = %s %s %s", row.OperationID, row.Action, row.Object)
	}
	if row.ConfigSetID != "audited" || row.ConfigRevision != 1 || row.AfterHash == "" || row.RequestID == "" {
		t.Fatalf("audit change evidence = %+v", row)
	}
	if row.Ticket != "INC-42" {
		t.Fatalf("ticket = %q, want the mount-recorded ticket", row.Ticket)
	}

	// With the audit store gone the change must not commit: the response fails
	// and the policy state (sets, revision) is unchanged.
	if err := s.db.Migrator().DropTable(usageAdminAuditTable); err != nil {
		t.Fatal(err)
	}
	body = strings.Replace(body, `"id":"audited"`, `"id":"unaudited"`, 1)
	body = strings.Replace(body, `"name":"main"`, `"name":"second"`, 1)
	before := request("GET", "/admin/v1/policy-sets?limit=5", "", "")
	if w := request("POST", "/admin/v1/policy-sets", `"rev-1"`, "unaudited-create"); w.Code == 201 {
		t.Fatalf("mutation committed without its audit row: %s", w.Body.String())
	}
	after := request("GET", "/admin/v1/policy-sets?limit=5", "", "")
	if before.Body.String() != after.Body.String() {
		t.Fatalf("policy listing changed after a refused audit:\nbefore=%s\nafter=%s", before.Body.String(), after.Body.String())
	}
	var revision policyMutationRevision
	if err := s.db.First(&revision, 1).Error; err != nil {
		t.Fatal(err)
	}
	if revision.Revision != 1 {
		t.Fatalf("revision = %d after refused audit, want 1", revision.Revision)
	}
}

// newRealAuditPolicyMount mounts the guarded policy operations with the
// production audit writer, a distinct config ETag source and an explicit policy
// ETag source, so tests can prove the sources do not leak into each other.
func newRealAuditPolicyMount(t *testing.T, configETag func(*http.Request) (string, int64, error)) (*http.ServeMux, *usageStore, Principal) {
	t.Helper()
	s, deps, p, lines := policyMutationFixture(t)
	if err := applyUsageAdminAuditMigration(s.db); err != nil {
		t.Fatal(err)
	}
	cfg := deps.PublishedConfig()
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Source: "inline", Policy: lines}
	oldCurrent := deps.Current
	deps.Current = func(tx *gorm.DB, cfg *Config) (*Service, error) {
		view, err := oldCurrent(tx, cfg)
		if err == nil {
			view.configSnapshot = NewConfigSnapshot()
			view.configSnapshot.Store(cfg)
		}
		return view, err
	}
	deps.Build = func(_ *gorm.DB, _ *Config, lines []string) (*Service, error) {
		a, e := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: lines}, nil, nil, AdminAuthConfig{})
		return &Service{authorizer: a}, e
	}
	registry, err := NewAdminV1Registry(policyOperationSpecs())
	if err != nil {
		t.Fatal(err)
	}
	current, err := deps.Current(s.db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	policyETag := func(*http.Request) (string, int64, error) {
		var row policyMutationRevision
		if err := s.db.First(&row, 1).Error; err != nil {
			return "", 0, err
		}
		return AdminETagForRevision(row.Revision), row.Revision, nil
	}
	mux := http.NewServeMux()
	if err := RegisterAdminV1(mux, AdminV1Deps{
		Registry: registry, Authenticate: &fakeAuthenticator{principal: p}, Authorize: current.Authorize,
		CurrentETag: configETag, PolicyCurrentETag: policyETag,
		Denials: &recordingDenialSink{}, CredentialEvents: &recordingCredentialSink{},
		IdempotencyOn: true, Idempotency: NewMemoryAdminIdempotencyStore(AdminIdempotencyConfig{}, 32),
		CursorCodec: NewAdminCursorCodec([]byte("policy-audit-fixture-key-01234567")),
		Handlers:    newPolicyAdminHandlersWithAudit(s, deps, policyMutationAuditWriter),
	}); err != nil {
		t.Fatal(err)
	}
	return mux, s, p
}

// TestPolicyMountUsesThePolicyETagSourceInAComposedMount mounts configuration and
// policy operations behind ONE mount whose configuration validator would reject
// every policy write, and proves a policy write is gated on the POLICY revision.
func TestPolicyMountUsesThePolicyETagSourceInAComposedMount(t *testing.T) {
	// A configuration validator that would 412 a policy write if it were used.
	configETag := func(*http.Request) (string, int64, error) {
		return AdminETagForRevision(999), 999, nil
	}
	mux, _, _ := newRealAuditPolicyMount(t, configETag)
	body := `{"id":"etag-set","name":"main","version":1,"rules":[{"subject_or_role":"root","domain":"*","object":"admin:policy","action":"read|write|activate"},{"subject_or_role":"root","domain":"*","object":"admin:reports","action":"read"}],"role_memberships":[{"subject":"root","role":"security-admin","domain":"*"}]}`
	request := func(tag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/admin/v1/policy-sets", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("If-Match", tag)
		r.Header.Set("Idempotency-Key", "etag-"+tag)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	// The correct POLICY tag must not be refused because the config tag differs.
	if w := request(`"rev-0"`); w.Code != http.StatusCreated {
		t.Fatalf("policy write with the correct policy tag = %d: %s", w.Code, w.Body.String())
	}
	body = strings.Replace(body, `"id":"etag-set"`, `"id":"etag-set-2"`, 1)
	body = strings.Replace(body, `"name":"main"`, `"name":"second"`, 1)
	// A tag from the CONFIG validator (999) is not a policy tag and must fail.
	if w := request(`"rev-999"`); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("policy write with a config-revision tag = %d, want 412", w.Code)
	}
}

// TestPolicyMutationIdempotentRetryDoesNotChangeOrAuditTwice proves a replayed
// Idempotency-Key returns the recorded response without a second policy change
// and without a second audit row.
func TestPolicyMutationIdempotentRetryDoesNotChangeOrAuditTwice(t *testing.T) {
	mux, s, _ := newRealAuditPolicyMount(t, func(*http.Request) (string, int64, error) {
		return AdminETagForRevision(0), 0, nil
	})
	body := `{"id":"retry-set","name":"main","version":1,"rules":[{"subject_or_role":"root","domain":"*","object":"admin:policy","action":"read|write|activate"},{"subject_or_role":"root","domain":"*","object":"admin:reports","action":"read"}],"role_memberships":[{"subject":"root","role":"security-admin","domain":"*"}]}`
	do := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/admin/v1/policy-sets", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("If-Match", `"rev-0"`)
		r.Header.Set("Idempotency-Key", "retry-same-key")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	first := do()
	if first.Code != http.StatusCreated {
		t.Fatalf("first = %d: %s", first.Code, first.Body.String())
	}
	retry := do()
	if retry.Code != http.StatusCreated && retry.Code != http.StatusOK {
		t.Fatalf("replay = %d: %s", retry.Code, retry.Body.String())
	}
	if retry.Body.String() != first.Body.String() {
		t.Fatalf("replay body differs:\nfirst=%s\nretry=%s", first.Body.String(), retry.Body.String())
	}
	var revision policyMutationRevision
	if err := s.db.First(&revision, 1).Error; err != nil {
		t.Fatal(err)
	}
	if revision.Revision != 1 {
		t.Fatalf("replayed mutation advanced the revision to %d, want 1", revision.Revision)
	}
	var rows int64
	if err := s.db.Table(usageAdminAuditTable).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("audit rows = %d after a replay, want exactly 1", rows)
	}
}

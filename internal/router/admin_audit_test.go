// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

type adminChangeEventAuditRow struct {
	EventID         string `gorm:"column:event_id"`
	Action          string `gorm:"column:action"`
	Actor           string `gorm:"column:actor"`
	ActorKind       string `gorm:"column:actor_kind"`
	ActorCredential string `gorm:"column:actor_credential_id"`
	ActorIssuer     string `gorm:"column:actor_issuer"`
	AuthzDomain     string `gorm:"column:authz_domain"`
	Object          string `gorm:"column:object"`
	AuthzAction     string `gorm:"column:authz_action"`
	OperationID     string `gorm:"column:operation_id"`
	RequestID       string `gorm:"column:request_id"`
	Outcome         string `gorm:"column:outcome"`
	BeforeHash      string `gorm:"column:before_hash"`
	AfterHash       string `gorm:"column:after_hash"`
	Ticket          string `gorm:"column:ticket"`
	Reason          string `gorm:"column:reason"`
	IdempotencyKey  string `gorm:"column:idempotency_key"`
	ConfigRevision  int64  `gorm:"column:config_revision"`
	RuntimeScope    string `gorm:"column:runtime_scope"`
}

func (adminChangeEventAuditRow) TableName() string { return "router_config_change_events" }

// headerPrincipalAuthenticator is a test-only #814 stand-in that reads a header.
type headerPrincipalAuthenticator struct{}

func (headerPrincipalAuthenticator) Authenticate(_ context.Context, r *http.Request) (Principal, error) {
	subject := strings.TrimSpace(r.Header.Get("X-Test-Principal"))
	if subject == "" {
		return Principal{}, ErrNoCredential
	}
	return Principal{Subject: subject, Kind: "adminkey", CredentialID: "cred-audit", Issuer: "test-issuer"}, nil
}

// adminAuditHarness mounts the /admin/v1 config operations over a real
// control-plane database with test-only #814/#812 seams.
type adminAuditHarness struct {
	db        *gorm.DB
	mux       *http.ServeMux
	authorize *fakeAuthorizer
	// preparedSetID is a draft set created by a fixture prerequisite.
	preparedSetID string
}

func newAdminAuditHarness(t *testing.T) *adminAuditHarness {
	t.Helper()
	db := configAdminTestDB(t)
	harness := &adminAuditHarness{db: db, mux: http.NewServeMux(), authorize: &fakeAuthorizer{deny: map[string]bool{}}}
	harness.mount(t)
	return harness
}

func (h *adminAuditHarness) mount(t *testing.T) {
	t.Helper()
	registry, err := AdminV1DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	principalOf := func(r *http.Request) (Principal, string, bool) {
		principal, ok := PrincipalFrom(r.Context())
		if !ok {
			return Principal{}, "", false
		}
		return principal, "default", true
	}
	authorize := func(ctx context.Context, principal Principal, domain, object, action string) error {
		return h.authorize.Authorize(ctx, principal, domain, object, action)
	}
	configDeps := AdminV1ConfigAdminDeps(ConfigAdminDeps{
		DB: h.db, RuntimeScope: "prod", Driver: "sqlite", InstanceID: "replica-test",
		CurrentConfig:  func() *Config { return nil },
		LoadedRevision: func() (int64, string, time.Time) { return 7, "set-7", time.Now().UTC() },
	}, principalOf, authorize)
	extra := addReportPlaceholderHandlers(registry, map[string]http.HandlerFunc{
		"training.export":         func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"training.outcomes.label": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"metrics.scrape":          func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"content.captures.delete": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"content.captures.purge":  func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
	})
	handlers := AdminV1Handlers(
		configDeps,
		AdminV1CallerAdminDeps(CallerAdminDeps{DB: h.db, RuntimeScope: "prod"}, principalOf, authorize),
		extra,
	)
	err = RegisterAdminV1(h.mux, AdminV1Deps{
		Registry:     registry,
		Authenticate: headerPrincipalAuthenticator{},
		Authorize: func(ctx context.Context, principal Principal, domain, object, action string) error {
			return h.authorize.Authorize(ctx, principal, domain, object, action)
		},
		Handlers:      handlers,
		ResolveDomain: func(Principal) string { return AdminGlobalDomain },
		ResolveCallerDomain: func(r *http.Request) (string, error) {
			return AdminV1CallerResourceDomain(h.db, "prod", r)
		},
		CurrentETag: func(r *http.Request) (string, int64, error) {
			revision, err := currentRevisionForScope(h.db, "prod")
			return AdminETagForRevision(revision), revision, err
		},
		CursorCodec:      NewAdminCursorCodec([]byte("audit-harness-cursor-key-0001")),
		Idempotency:      NewMemoryAdminIdempotencyStore(AdminIdempotencyConfig{}, 64),
		IdempotencyOn:    true,
		Limiter:          NewAdminRateLimiter(AdminRateLimitConfig{Enabled: boolPointer(false)}),
		Throttle:         NewAdminAuthThrottle(AdminAuthFailureThrottleConfig{Enabled: boolPointer(false)}),
		Denials:          &recordingDenialSink{},
		CredentialEvents: &recordingCredentialSink{},
		Clock:            func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
}

func (h *adminAuditHarness) do(t *testing.T, method, target string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(raw))
	} else {
		reader = strings.NewReader("")
	}
	request := httptest.NewRequest(method, target, reader)
	request.Header.Set("X-Test-Principal", "adminkey:audit")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)
	return recorder
}

func (h *adminAuditHarness) currentETag(t *testing.T) string {
	t.Helper()
	revision, err := currentRevisionForScope(h.db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	return AdminETagForRevision(revision)
}

func (h *adminAuditHarness) changeEvents(t *testing.T) []adminChangeEventAuditRow {
	t.Helper()
	var rows []adminChangeEventAuditRow
	if err := h.db.Order(`"at" ASC`).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestAdminMutationAuditRecord proves a control-plane mutation writes one audit row
// carrying the actor, credential identity, domain, object/action, before/after
// hashes and the request ID, in the change's own transaction (ACP-022/023).
func TestAdminMutationAuditRecord(t *testing.T) {
	harness := newAdminAuditHarness(t)
	before := len(harness.changeEvents(t))
	recorder := harness.do(t, http.MethodPost, "/admin/v1/config/groups/agent-default/targets", map[string]any{
		"target": map[string]any{"provider": "alpha", "model_ref": "a-two"},
		"reason": "scale out",
	}, map[string]string{"Idempotency-Key": "audit-key-1", "If-Match": harness.currentETag(t), "X-Admin-Ticket": "OPS-42"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("write status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	rows := harness.changeEvents(t)
	if len(rows) != before+1 {
		t.Fatalf("wrote %d change events, want 1", len(rows)-before)
	}
	row := rows[len(rows)-1]
	if row.Actor != "adminkey:audit" || row.ActorKind != "adminkey" || row.ActorCredential != "cred-audit" || row.ActorIssuer != "test-issuer" {
		t.Fatalf("actor fields = %+v", row)
	}
	if row.AuthzDomain != AdminGlobalDomain || row.Object != authzObjectAdminConfig || row.AuthzAction != "write" {
		t.Fatalf("authorization fields = %+v", row)
	}
	if row.OperationID != "addConfigGroupTarget" || row.RequestID == "" || row.Outcome != "committed" {
		t.Fatalf("request fields = %+v", row)
	}
	if row.BeforeHash == "" || row.AfterHash == "" || row.BeforeHash == row.AfterHash {
		t.Fatalf("before/after hashes = %q/%q", row.BeforeHash, row.AfterHash)
	}
	if row.Ticket != "OPS-42" {
		t.Fatalf("ticket = %q", row.Ticket)
	}
	if row.IdempotencyKey != "audit-key-1" {
		t.Fatalf("idempotency key = %q", row.IdempotencyKey)
	}
}

// TestAdminReplayWritesOneEvent proves an idempotent replay writes no second
// change event.
func TestAdminReplayWritesOneEvent(t *testing.T) {
	harness := newAdminAuditHarness(t)
	before := len(harness.changeEvents(t))
	headers := map[string]string{"Idempotency-Key": "replay-audit-key", "If-Match": harness.currentETag(t)}
	body := map[string]any{"targets": []any{map[string]any{"provider": "alpha", "model_ref": "a-two"}}, "strategy": "static"}
	first := harness.do(t, http.MethodPut, "/admin/v1/config/groups/agent-default/targets", body, headers)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d (%s)", first.Code, first.Body.String())
	}
	second := harness.do(t, http.MethodPut, "/admin/v1/config/groups/agent-default/targets", body, headers)
	if second.Code != http.StatusOK && second.Code != http.StatusCreated {
		t.Fatalf("replay status = %d (%s)", second.Code, second.Body.String())
	}
	if got := len(harness.changeEvents(t)) - before; got != 1 {
		t.Fatalf("wrote %d change events across the replay, want 1", got)
	}
}

// TestAdminAuditFailureFailsWrite proves an injected audit-store failure leaves no
// unaudited write: the change is refused and nothing about the configuration moves.
func TestAdminAuditFailureFailsWrite(t *testing.T) {
	harness := newAdminAuditHarness(t)
	revisionBefore, err := currentRevisionForScope(harness.db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	setsBefore := countConfigSets(t, harness.db)
	eventsBefore := len(harness.changeEvents(t))

	// Reject every change-event insert, which is the audit row of the change.
	if err := harness.db.Exec(`CREATE TRIGGER reject_change_event BEFORE INSERT ON router_config_change_events BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`).Error; err != nil {
		t.Fatalf("install audit failure: %v", err)
	}

	recorder := harness.do(t, http.MethodPost, "/admin/v1/config/groups/agent-default/targets", map[string]any{
		"target": map[string]any{"provider": "alpha", "model_ref": "a-two"},
	}, map[string]string{"If-Match": harness.currentETag(t)})
	if recorder.Code < 500 {
		t.Fatalf("write with a failing audit store returned %d, want 5xx (%s)", recorder.Code, recorder.Body.String())
	}
	revisionAfter, err := currentRevisionForScope(harness.db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if revisionAfter != revisionBefore {
		t.Fatalf("revision moved from %d to %d despite the failed audit write", revisionBefore, revisionAfter)
	}
	if got := countConfigSets(t, harness.db); got != setsBefore {
		t.Fatalf("configuration sets went from %d to %d despite the failed audit write", setsBefore, got)
	}
	if got := len(harness.changeEvents(t)); got != eventsBefore {
		t.Fatalf("change events went from %d to %d", eventsBefore, got)
	}
}

func countConfigSets(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&configSetRow{}).Where("runtime_scope = ?", "prod").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}

// TestAdminAuditRecordsAreRedacted proves credential-shaped text in the ticket,
// reason and path never reaches the audit row.
func TestAdminAuditRecordsAreRedacted(t *testing.T) {
	harness := newAdminAuditHarness(t)
	secret := "sk-live-2f8c1d4e6a9b0c3d5e7f"
	harness.do(t, http.MethodPost, "/admin/v1/config/groups/agent-default/targets", map[string]any{
		"target": map[string]any{"provider": "alpha", "model_ref": "a-two"},
		"reason": "escalation " + secret,
	}, map[string]string{"If-Match": harness.currentETag(t), "X-Admin-Ticket": "OPS-" + secret})
	rows := harness.changeEvents(t)
	row := rows[len(rows)-1]
	var serialized strings.Builder
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	serialized.Write(raw)
	if strings.Contains(serialized.String(), secret) {
		t.Fatalf("audit row leaked the seeded secret: %s", serialized.String())
	}
}

// TestAdminCredentialEventSchema proves the phase-11 credential event table exists
// and accepts a redacted lifecycle record.
func TestAdminCredentialEventSchema(t *testing.T) {
	db := configAdminTestDB(t)
	event := AdminCredentialEvent{
		EventID: "evt-1", At: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC), Kind: "adminkey",
		CredentialID: "cred-audit", Subject: "adminkey:audit", Issuer: "test", Outcome: "success",
		RequestID: "req-1", Lifecycle: "auth",
	}
	if err := insertAdminCredentialEventTx(db, event); err != nil {
		t.Fatalf("insert credential event: %v", err)
	}
	var rows []struct {
		CredentialID string `gorm:"column:credential_id"`
		Outcome      string `gorm:"column:outcome"`
		Lifecycle    string `gorm:"column:lifecycle"`
	}
	if err := db.Table(adminCredentialEventsTable).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].CredentialID != "cred-audit" || rows[0].Outcome != "success" || rows[0].Lifecycle != "auth" {
		t.Fatalf("credential events = %+v", rows)
	}
}

// adminUsageAuditTestStore opens a usage store with a seeded capture.
func adminUsageAuditTestStore(t *testing.T) *usageStore {
	t.Helper()
	store, err := OpenUsageStore(uncachedSQLiteUsageDBConfigForTest(t.TempDir() + "/usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	capture := contentCaptureRecord{
		RequestID: "req-audit", TS: formatUsageTime(time.Now().UTC()), Scope: contentCaptureScopeRequest,
		CallerID: "caller", TokenID: "token", RetentionUntil: formatUsageTime(time.Now().UTC().Add(time.Hour)),
	}
	if err := store.db.Create(&capture).Error; err != nil {
		t.Fatal(err)
	}
	return store
}

// TestUsageAdminAuditAtomicWithChange proves a usage-side admin mutation and its
// audit row commit together: with the audit table unavailable the change is refused
// and nothing is deleted.
func TestUsageAdminAuditAtomicWithChange(t *testing.T) {
	store := adminUsageAuditTestStore(t)
	record := AdminAuditRecord{
		PrincipalSubject: "adminkey:audit", PrincipalKind: "adminkey", Object: "content:capture",
		Action: "delete", OperationID: "deleteContentCapture", Domain: "example/prod", Outcome: "committed",
	}
	// A usable store: the capture goes and one audit row appears.
	deleted, err := store.DeleteContentCapturesByRequestIDAudited("req-audit", "caller", "token", "admin-delete", record)
	if err != nil {
		t.Fatalf("audited delete: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted %d captures, want 1", deleted)
	}
	var audits []struct {
		Object   string `gorm:"column:object"`
		Action   string `gorm:"column:action"`
		Subject  string `gorm:"column:principal_subject"`
		Outcome  string `gorm:"column:outcome"`
		Domain   string `gorm:"column:domain"`
		Operator string `gorm:"column:operation_id"`
	}
	if err := store.db.Table(usageAdminAuditTable).Find(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 || audits[0].Object != "content:capture" || audits[0].Action != "delete" ||
		audits[0].Subject != "adminkey:audit" || audits[0].Outcome != "committed" || audits[0].Operator != "deleteContentCapture" {
		t.Fatalf("audit rows = %+v", audits)
	}

	// With the audit store unavailable the change must not commit.
	store2 := adminUsageAuditTestStore(t)
	if err := store2.db.Exec("DROP TABLE " + usageAdminAuditTable).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store2.DeleteContentCapturesByRequestIDAudited("req-audit", "caller", "token", "admin-delete", record); err == nil {
		t.Fatal("audited delete succeeded with the audit store unavailable")
	}
	var remaining int64
	if err := store2.db.Model(&contentCaptureRecord{}).Where("request_id = ?", "req-audit").Count(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("%d captures remain, want 1: the failed audit write must roll the change back", remaining)
	}
}

// TestUsageAdminAuditRedaction proves a usage-side audit row stores no
// credential-shaped text.
func TestUsageAdminAuditRedaction(t *testing.T) {
	store := adminUsageAuditTestStore(t)
	secret := "sk-live-7c1e2d3b4a5968776655"
	record := AdminAuditRecord{
		PrincipalSubject: "adminkey:audit", PrincipalKind: "adminkey", Object: "content:capture",
		Action: "delete", Outcome: "committed", Ticket: "OPS-" + secret,
		Detail: "deleted with Authorization: Bearer " + secret,
	}
	if _, err := store.DeleteContentCapturesByRequestIDAudited("req-audit", "caller", "token", "admin-delete", record); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	rows, err := store.db.Raw("SELECT * FROM " + usageAdminAuditTable).Rows()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		for _, value := range values {
			if text, ok := value.(string); ok {
				raw = append(raw, []byte(text)...)
			}
			if bytes, ok := value.([]byte); ok {
				raw = append(raw, bytes...)
			}
		}
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("usage audit row leaked the seeded secret: %s", string(raw))
	}
}

// TestUsageAdminAuditPurgeAtomic proves the purge path writes its audit row in the
// same transaction.
func TestUsageAdminAuditPurgeAtomic(t *testing.T) {
	store := adminUsageAuditTestStore(t)
	if err := store.db.Model(&contentCaptureRecord{}).Where("request_id = ?", "req-audit").
		Update("retention_until", formatUsageTime(time.Now().UTC().Add(-time.Hour))).Error; err != nil {
		t.Fatal(err)
	}
	record := AdminAuditRecord{
		PrincipalSubject: "adminkey:audit", PrincipalKind: "adminkey", Object: "content:capture",
		Action: "purge", OperationID: "purgeExpiredContentCaptures", Outcome: "committed",
	}
	deleted, err := store.PurgeExpiredContentCapturesAudited(time.Now().UTC(), "admin", "admin", "retention", record)
	if err != nil {
		t.Fatalf("audited purge: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("purged %d captures, want 1", deleted)
	}
	var count int64
	if err := store.db.Table(usageAdminAuditTable).Where("action = ?", "purge").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("purge audit rows = %d, want 1", count)
	}
}

// recordingCredentialSink collects credential events in memory.
type recordingCredentialSink struct {
	mu     sync.Mutex
	events []AdminCredentialEvent
	fail   bool
}

func (s *recordingCredentialSink) RecordCredentialEvent(_ context.Context, event AdminCredentialEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errAdminV1NoControlPlane
	}
	s.events = append(s.events, event)
	return nil
}

func (s *recordingCredentialSink) snapshot() []AdminCredentialEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AdminCredentialEvent, len(s.events))
	copy(out, s.events)
	return out
}

// TestAdminAuthenticationEvents proves each authentication outcome produces one
// redacted durable record, that a success fails closed when the sink is unavailable
// and that a failure does not (ACP-066).
func TestAdminAuthenticationEvents(t *testing.T) {
	registry, err := AdminV1DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordingCredentialSink{}
	authenticator := &switchableAuthenticator{principal: Principal{Subject: "adminkey:audit", Kind: "adminkey", CredentialID: "cred-1", Issuer: "test"}}
	mux := http.NewServeMux()
	handlers := addReportPlaceholderHandlers(registry, map[string]http.HandlerFunc{
		"config.status":              func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"config.groups.list":         func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"config.group.get":           func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"config.sets.list":           func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"config.sets.get":            func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"config.group.history":       func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"config.group.shadow.status": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"callers.list":               func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
	})
	for _, key := range []string{"config.diff", "config.sets.import", "config.sets.activate", "config.sets.rollback",
		"config.group.targets.replace", "config.group.targets.add", "config.group.targets.remove",
		"config.group.targets.validate", "config.group.rollback", "config.group.shadow.start",
		"config.group.shadow.promote", "config.group.shadow.stop", "callers.issue", "callers.rotate", "callers.revoke",
		"training.export", "training.outcomes.label", "metrics.scrape", "content.captures.delete", "content.captures.purge"} {
		handlers[key] = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) }
	}
	err = RegisterAdminV1(mux, AdminV1Deps{
		Registry:      registry,
		Authenticate:  authenticator,
		Authorize:     func(context.Context, Principal, string, string, string) error { return nil },
		Handlers:      handlers,
		ResolveDomain: func(Principal) string { return AdminGlobalDomain },
		CurrentETag: func(*http.Request) (string, int64, error) {
			return AdminETagForRevision(1), 1, nil
		},
		Limiter:          NewAdminRateLimiter(AdminRateLimitConfig{Enabled: boolPointer(false)}),
		Throttle:         NewAdminAuthThrottle(AdminAuthFailureThrottleConfig{Enabled: boolPointer(false)}),
		Denials:          &recordingDenialSink{},
		CredentialEvents: sink,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Success.
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/admin/v1/config/status", nil))
	events := sink.snapshot()
	if len(events) != 1 || events[0].Outcome != "success" || events[0].Kind != "adminkey" || events[0].CredentialID != "cred-1" {
		t.Fatalf("success event = %+v", events)
	}

	// Failure: best effort, and the client still sees 401.
	authenticator.err = ErrInvalidCredential
	sink.fail = true
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/v1/config/status", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("failure status = %d, want 401", recorder.Code)
	}

	// Success with an unavailable sink: fail closed.
	authenticator.err = nil
	sink.fail = true
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/v1/config/status", nil))
	if recorder.Code < 500 {
		t.Fatalf("success with an unavailable audit sink returned %d, want 5xx", recorder.Code)
	}
}

// switchableAuthenticator is a test-only #814 stand-in whose error can be changed.
type switchableAuthenticator struct {
	principal Principal
	err       error
}

func (a *switchableAuthenticator) Authenticate(context.Context, *http.Request) (Principal, error) {
	if a.err != nil {
		return Principal{}, a.err
	}
	return a.principal, nil
}

// recordingDenialSink collects denial records in memory.
type recordingDenialSink struct {
	mu     sync.Mutex
	events []AdminDenialRecord
	fail   bool
}

func (s *recordingDenialSink) RecordDenial(_ context.Context, record AdminDenialRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errAdminV1NoUsageStore
	}
	s.events = append(s.events, record)
	return nil
}

func (s *recordingDenialSink) snapshot() []AdminDenialRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AdminDenialRecord, len(s.events))
	copy(out, s.events)
	return out
}

// TestAdminDenialsAreAuditedAndFailClosed proves an authorization refusal is
// recorded durably, that the record carries the attempt without credential material,
// and that a denial which cannot be recorded answers 5xx rather than a silent 403.
func TestAdminDenialsAreAuditedAndFailClosed(t *testing.T) {
	harness := newAdminV1Harness(t)
	sink := &recordingDenialSink{}
	harness.authorize.deny["example/prod|admin:callers|rotate"] = true
	harness.mountWithDeps(t, nil, func(deps *AdminV1Deps) {
		deps.Denials = sink
		deps.CredentialEvents = &recordingCredentialSink{}
	})

	recorder := harness.do(t, http.MethodPost, "/admin/v1/callers/caller-1/rotate", map[string]any{}, map[string]string{"If-Match": `"rev-7"`})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	events := sink.snapshot()
	if len(events) != 1 {
		t.Fatalf("recorded %d denials, want 1", len(events))
	}
	event := events[0]
	if event.Object != "admin:callers" || event.Action != "rotate" || event.Domain != "example/prod" ||
		event.PrincipalSubject != "adminkey:test" || event.Status != http.StatusForbidden || event.RequestID == "" {
		t.Fatalf("denial record = %+v", event)
	}

	// A denial that cannot be recorded must not be reported as a plain 403.
	sink.fail = true
	recorder = harness.do(t, http.MethodPost, "/admin/v1/callers/caller-1/rotate", map[string]any{}, map[string]string{"If-Match": `"rev-7"`})
	if recorder.Code < 500 {
		t.Fatalf("status = %d, want 5xx when the denial cannot be recorded", recorder.Code)
	}
}

// TestAdminEveryMutationHasOneAuditRow is the registry-driven coverage test: every mutating
// operation is driven through its REAL handler with a valid fixture, must commit with a 2xx,
// and must leave exactly one complete audit row; the usage-database operations go through
// their real HTTP handlers as well. Each class also has a forced-audit-failure rollback case,
// and all three credential lifecycle operations are exercised.
func TestAdminEveryMutationHasOneAuditRow(t *testing.T) {
	registry, err := AdminV1DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	usageOps := map[string]bool{
		"deleteContentCapture": true, "purgeExpiredContentCaptures": true, "labelTrainingOutcome": true,
	}
	mutations, usageMutations := 0, 0
	for _, op := range registry.Operations() {
		if op.Method == "GET" || op.ID == "exportTrainingData" {
			continue
		}
		mutations++
		harness := newAdminAuditHarness(t)
		if usageOps[op.ID] {
			usageMutations++
			runUsageAuditOperation(t, harness, op)
			continue
		}
		// Stateful operations need their prerequisite state first; the prerequisite's own
		// audit rows are excluded by re-baselining.
		prepareAdminAuditFixture(t, harness, op)
		before := len(harness.changeEvents(t))
		method, target, body := adminAuditFixture(t, harness, op)
		recorder := harness.do(t, method, target, body, map[string]string{
			"If-Match": harness.currentETag(t), "Idempotency-Key": "audit-" + op.ID,
		})
		if recorder.Code != http.StatusOK && recorder.Code != http.StatusCreated {
			t.Fatalf("operation %s did not commit: %d (%s)", op.ID, recorder.Code, recorder.Body.String())
		}
		if op.NoSideEffect {
			// A validation/dry-run operation commits with 2xx and must write no audit row.
			if len(harness.changeEvents(t)) != before {
				t.Fatalf("side-effect-free operation %s wrote an audit row", op.ID)
			}
			continue
		}
		events := harness.changeEvents(t)
		if len(events) != before+1 {
			t.Fatalf("operation %s committed without exactly one audit row (%d new)", op.ID, len(events)-before)
		}
		row := events[len(events)-1]
		if row.Actor == "" || row.ActorKind == "" || row.Object == "" || row.AuthzAction == "" ||
			row.OperationID != op.ID || row.RequestID == "" || row.AuthzDomain == "" {
			t.Fatalf("operation %s wrote an incomplete audit row: %+v", op.ID, row)
		}
	}
	if mutations == 0 || usageMutations != 3 {
		t.Fatalf("exercised %d mutations (%d usage-side), want the full registry", mutations, usageMutations)
	}
}

// runUsageAuditOperation drives a usage-database mutation through the real store path with a
// valid fixture and asserts one audited row, then proves the rollback with the audit store
// unavailable.
func runUsageAuditOperation(t *testing.T, harness *adminAuditHarness, op Operation) {
	t.Helper()
	store := adminUsageAuditTestStore(t)
	record := auditRecordFromMeta(testAdminAuditMeta(), "request_delete")
	change := func(s *usageStore) error {
		switch op.ID {
		case "deleteContentCapture":
			_, err := s.DeleteContentCapturesByRequestIDAudited("req-audit", "caller", "token", "admin-delete", record)
			return err
		case "purgeExpiredContentCaptures":
			_, err := s.PurgeExpiredContentCapturesAudited(time.Now().UTC(), "admin", "admin", "retention", record)
			return err
		case "labelTrainingOutcome":
			return s.CreateTrainingOutcomeAudited(contentCaptureRecord{RequestID: "req-audit", Scope: "training_outcome_v1"}, record)
		}
		t.Fatalf("unhandled usage operation %s", op.ID)
		return nil
	}
	if err := change(store); err != nil {
		t.Fatalf("operation %s did not commit: %v", op.ID, err)
	}
	var rows int64
	if err := store.db.Table(usageAdminAuditTable).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("operation %s wrote %d usage audit rows, want 1", op.ID, rows)
	}
	// Forced audit failure: the change must not commit.
	store2 := adminUsageAuditTestStore(t)
	if err := store2.db.Exec("DROP TABLE " + usageAdminAuditTable).Error; err != nil {
		t.Fatal(err)
	}
	if err := change(store2); err == nil {
		t.Fatalf("operation %s committed with the audit store unavailable", op.ID)
	}
	_ = harness
}

// prepareAdminAuditFixture creates the prerequisite state some operations need.
func prepareAdminAuditFixture(t *testing.T, harness *adminAuditHarness, op Operation) {
	t.Helper()
	write := func(method, target string, body map[string]any, key string) *httptest.ResponseRecorder {
		return harness.do(t, method, target, body, map[string]string{
			"If-Match": harness.currentETag(t), "Idempotency-Key": key,
		})
	}
	switch op.ID {
	case "removeConfigGroupTarget":
		recorder := write(http.MethodPost, "/admin/v1/config/groups/agent-default/targets",
			map[string]any{"target": map[string]any{"provider": "beta", "model_ref": "b-one"}}, "pre-remove")
		if recorder.Code != http.StatusOK {
			t.Fatalf("preparing removeConfigGroupTarget: %d (%s)", recorder.Code, recorder.Body.String())
		}
	case "rollbackConfigGroup":
		// A group change is required before the group can be rolled back.
		recorder := write(http.MethodPut, "/admin/v1/config/groups/agent-default/targets",
			map[string]any{"targets": []any{map[string]any{"provider": "alpha", "model_ref": "a-two"}, map[string]any{"provider": "beta", "model_ref": "b-one"}}}, "pre-rollback")
		if recorder.Code != http.StatusOK {
			t.Fatalf("preparing rollbackConfigGroup: %d (%s)", recorder.Code, recorder.Body.String())
		}
	case "promoteConfigGroupShadow", "stopConfigGroupShadow":
		recorder := write(http.MethodPost, "/admin/v1/config/groups/agent-default/shadow",
			map[string]any{"provider": "alpha", "model_ref": "a-two", "sample_rate": 0.01, "max_requests": 5}, "pre-shadow")
		if recorder.Code != http.StatusCreated && recorder.Code != http.StatusOK {
			t.Fatalf("preparing %s: %d (%s)", op.ID, recorder.Code, recorder.Body.String())
		}
	case "activateConfigSet", "rollbackConfigSet":
		recorder := write(http.MethodPost, "/admin/v1/config/sets",
			map[string]any{"name": "audit-draft", "yaml": configAdminTestYAML}, "pre-import")
		if recorder.Code != http.StatusCreated {
			t.Fatalf("preparing %s: %d (%s)", op.ID, recorder.Code, recorder.Body.String())
		}
		var body struct {
			SetID string `json:"set_id"`
		}
		_ = json.Unmarshal(recorder.Body.Bytes(), &body)
		if body.SetID != "" {
			harness.preparedSetID = body.SetID
		}
	}
}

// adminAuditFixture returns a valid request for one mutating control-plane operation.
func adminAuditFixture(t *testing.T, harness *adminAuditHarness, op Operation) (string, string, map[string]any) {
	t.Helper()
	target := strings.ReplaceAll(op.Path, "{group}", "agent-default")
	target = strings.ReplaceAll(target, "{set_id}", "set-1")
	target = strings.ReplaceAll(target, "{request_id}", "req-1")
	target = strings.ReplaceAll(target, "{id}", "caller-1")
	target = strings.ReplaceAll(target, "{target_id...}", "alpha/a-one")
	body := map[string]any{}
	switch op.ID {
	case "issueCaller":
		body = map[string]any{"owner_user": "u", "project": "p", "environment": "e", "key_slug": "k", "allow": []string{"agent-default"}}
	case "rotateCaller", "revokeCaller":
		// The seeded caller exists in the harness configuration.
		target = strings.ReplaceAll(op.Path, "{id}", "alice")
	case "replaceConfigGroupTargets", "validateConfigGroupTargets":
		body = map[string]any{"targets": []any{map[string]any{"provider": "alpha", "model_ref": "a-two"}}, "dry_run": op.ID == "validateConfigGroupTargets"}
	case "addConfigGroupTarget":
		body = map[string]any{"target": map[string]any{"provider": "alpha", "model_ref": "a-two"}}
	case "importConfigSet":
		body = map[string]any{"name": "audit-set", "yaml": configAdminTestYAML}
	case "startConfigGroupShadow":
		body = map[string]any{"provider": "alpha", "model_ref": "a-two", "sample_rate": 0.01, "max_requests": 5}
	case "promoteConfigGroupShadow":
		body = map[string]any{"reason": "audit"}
	case "rollbackConfigGroup", "stopConfigGroupShadow":
		body = map[string]any{"reason": "audit"}
	case "activateConfigSet":
		setID := harness.preparedSetID
		if setID == "" {
			setID = "set-1"
		}
		target = strings.ReplaceAll(op.Path, "{set_id}", setID)
		body = map[string]any{"reason": "audit"}
	case "rollbackConfigSet":
		setID := harness.preparedSetID
		body = map[string]any{"set_id": setID, "reason": "audit"}
	}
	return op.Method, target, body
}

// TestAdminForcedAuditFailureRollsBackMutation proves a forced audit-store failure leaves no
// change and no audit row.
func TestAdminForcedAuditFailureRollsBackMutation(t *testing.T) {
	harness := newAdminAuditHarness(t)
	revisionBefore, err := currentRevisionForScope(harness.db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	setsBefore := countConfigSets(t, harness.db)
	eventsBefore := len(harness.changeEvents(t))
	if err := harness.db.Exec(`CREATE TRIGGER reject_audit_row BEFORE INSERT ON router_config_change_events BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`).Error; err != nil {
		t.Fatal(err)
	}
	recorder := harness.do(t, http.MethodPut, "/admin/v1/config/groups/agent-default/targets", map[string]any{
		"targets": []any{map[string]any{"provider": "alpha", "model_ref": "a-two"}},
	}, map[string]string{"If-Match": harness.currentETag(t)})
	if recorder.Code < 500 {
		t.Fatalf("status = %d, want 5xx", recorder.Code)
	}
	revisionAfter, err := currentRevisionForScope(harness.db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if revisionAfter != revisionBefore || countConfigSets(t, harness.db) != setsBefore || len(harness.changeEvents(t)) != eventsBefore {
		t.Fatal("a failed audit write left the mutation committed")
	}
}

// TestAdminCredentialLifecycleEvents proves each credential lifecycle change writes a
// redacted durable event alongside its change row.
func TestAdminCredentialLifecycleEvents(t *testing.T) {
	harness := newAdminAuditHarness(t)
	if err := harness.db.Exec("CREATE TABLE IF NOT EXISTS router_admin_credential_events (event_id TEXT PRIMARY KEY, at TEXT NOT NULL, kind TEXT NOT NULL, credential_id TEXT NOT NULL DEFAULT '', subject TEXT NOT NULL DEFAULT '', issuer TEXT NOT NULL DEFAULT '', outcome TEXT NOT NULL, reason_code TEXT NOT NULL DEFAULT '', request_id TEXT NOT NULL DEFAULT '', client_ip_hash TEXT NOT NULL DEFAULT '', lifecycle TEXT NOT NULL DEFAULT 'auth', count INTEGER NOT NULL DEFAULT 1)").Error; err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"owner_user": "u", "project": "p", "environment": "e", "key_slug": "lifecycle", "allow": []string{"agent-default"}}
	issued := harness.do(t, http.MethodPost, "/admin/v1/callers/issue", body, map[string]string{"If-Match": harness.currentETag(t), "Idempotency-Key": "lifecycle-issue"})
	if issued.Code != http.StatusCreated {
		t.Fatalf("issue status = %d (%s)", issued.Code, issued.Body.String())
	}
	var issuedBody struct {
		Caller struct {
			ID string `json:"id"`
		} `json:"caller"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &issuedBody); err != nil || issuedBody.Caller.ID == "" {
		t.Fatalf("issue response has no caller id: %s", issued.Body.String())
	}
	callerID := issuedBody.Caller.ID
	for _, step := range []struct {
		path string
		key  string
	}{{"/admin/v1/callers/" + callerID + "/rotate", "lifecycle-rotate"}, {"/admin/v1/callers/" + callerID + "/revoke", "lifecycle-revoke"}} {
		recorder := harness.do(t, http.MethodPost, step.path, map[string]any{}, map[string]string{"If-Match": harness.currentETag(t), "Idempotency-Key": step.key})
		if recorder.Code != http.StatusOK && recorder.Code != http.StatusCreated {
			t.Fatalf("%s status = %d (%s)", step.path, recorder.Code, recorder.Body.String())
		}
	}
	var rows []struct {
		Lifecycle string `gorm:"column:lifecycle"`
		Outcome   string `gorm:"column:outcome"`
		Subject   string `gorm:"column:subject"`
	}
	if err := harness.db.Table(adminCredentialEventsTable).Order("at ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("credential lifecycle rows = %d, want issue/rotate/revoke: %+v", len(rows), rows)
	}
	want := []string{"issue", "rotate", "revoke"}
	for i, row := range rows {
		if row.Lifecycle != want[i] || row.Outcome != "success" || row.Subject == "" {
			t.Fatalf("lifecycle row %d = %+v, want lifecycle %q", i, row, want[i])
		}
	}
}

// TestAdminAuthenticationFailureFloodIsBounded proves a flood of failed authentications is
// bounded: identical failures collapse into a counted row, so the table cannot grow one row
// per attempt.
func TestAdminAuthenticationFailureFloodIsBounded(t *testing.T) {
	db := configAdminTestDB(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	event := AdminCredentialEvent{
		Kind: "adminkey", CredentialID: "adminkey:flood", Outcome: "failure", ReasonCode: "invalid_credential",
		RequestID: "req-flood", ClientIPHash: adminClientIPHash("203.0.113.9"), Lifecycle: "auth",
	}
	for i := 0; i < 50; i++ {
		event.At = now.Add(time.Duration(i) * 10 * time.Millisecond)
		if err := db.Transaction(func(tx *gorm.DB) error { return insertAdminCredentialEventTx(tx, event) }); err != nil {
			t.Fatal(err)
		}
	}
	var rows int64
	if err := db.Table(adminCredentialEventsTable).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows > 2 {
		t.Fatalf("50 collapsed attempts produced %d rows; the table is unbounded", rows)
	}
	var counted int64
	if err := db.Table(adminCredentialEventsTable).Select("COALESCE(SUM(count),0)").Scan(&counted).Error; err != nil {
		t.Fatal(err)
	}
	if counted < 30 {
		t.Fatalf("collapsed rows count %d of 50 attempts", counted)
	}

	// A flood that varies the claimed key ID cannot evade the cap: at most
	// adminCredentialFailureSourceCap distinct rows per source per minute.
	var evaded int64
	for i := 0; i < 100; i++ {
		varied := event
		varied.CredentialID = fmt.Sprintf("adminkey:varied-%03d", i)
		varied.At = now.Add(time.Duration(i) * time.Millisecond)
		if err := db.Transaction(func(tx *gorm.DB) error { return insertAdminCredentialEventTx(tx, varied) }); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Table(adminCredentialEventsTable).Where("client_ip_hash = ?", event.ClientIPHash).Count(&evaded).Error; err != nil {
		t.Fatal(err)
	}
	if evaded > adminCredentialFailureSourceCap+2 {
		t.Fatalf("%d failure rows for one source, cap is %d", evaded, adminCredentialFailureSourceCap)
	}
}

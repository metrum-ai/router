// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func adminTestRequest(method, target string, body string) *http.Request {
	var reader *strings.Reader
	reader = strings.NewReader(body)
	request := httptest.NewRequest(method, target, reader)
	return request.WithContext(WithAdminRequestID(request.Context(), "req_test_0123456789"))
}

func decodeAdminProblem(t *testing.T, rr *httptest.ResponseRecorder) AdminProblem {
	t.Helper()
	var problem AdminProblem
	if err := json.Unmarshal(rr.Body.Bytes(), &problem); err != nil {
		t.Fatalf("body is not a problem document: %v: %s", err, rr.Body.String())
	}
	return problem
}

// TestAdminProblemDocumentShape pins the RFC 9457 document every admin-plane
// error uses (ACP-020).
func TestAdminProblemDocumentShape(t *testing.T) {
	request := adminTestRequest(http.MethodGet, "/admin/v1/config/groups/eu", "")
	recorder := httptest.NewRecorder()
	problem := AdminProblemFor(request, http.StatusPreconditionFailed, "precondition-failed")
	revision := int64(43)
	problem.CurrentRevision = &revision
	problem.CurrentETag = AdminETagForRevision(43)
	problem.Detail = "If-Match does not match the current revision."
	WriteAdminProblem(recorder, request, problem)

	if got := recorder.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("content type = %q", got)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control = %q", got)
	}
	if got := recorder.Header().Get(adminRequestIDHeader); got != "req_test_0123456789" {
		t.Fatalf("request id header = %q", got)
	}
	decoded := decodeAdminProblem(t, recorder)
	if decoded.Status != http.StatusPreconditionFailed || decoded.Code != "precondition-failed" ||
		decoded.Type != adminProblemTypePrefix+"precondition-failed" {
		t.Fatalf("unexpected problem: %+v", decoded)
	}
	if decoded.RequestID != "req_test_0123456789" || decoded.Instance != "/admin/v1/config/groups/eu" {
		t.Fatalf("unexpected correlation fields: %+v", decoded)
	}
	if decoded.CurrentRevision == nil || *decoded.CurrentRevision != 43 || decoded.CurrentETag != `"rev-43"` {
		t.Fatalf("precondition extension missing: %+v", decoded)
	}
}

// TestAdminProblemRequestIDIsServerGenerated proves a client cannot choose or
// reflect a request ID (log-injection and correlation spoofing).
func TestAdminProblemRequestIDIsServerGenerated(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/config/status", nil)
	request.Header.Set("X-Request-Id", "attacker-chosen")
	recorder := httptest.NewRecorder()
	WriteAdminProblem(recorder, request, AdminProblemFor(request, http.StatusForbidden, "admin-forbidden"))

	if strings.Contains(recorder.Body.String(), "attacker-chosen") {
		t.Fatal("client-supplied request ID was echoed into the problem document")
	}
	if got := recorder.Header().Get(adminRequestIDHeader); got == "" || got == "attacker-chosen" {
		t.Fatalf("request id header = %q", got)
	}
	if generated := decodeAdminProblem(t, recorder).RequestID; !strings.HasPrefix(generated, "req_") {
		t.Fatalf("generated request id %q does not use the request ID format", generated)
	}
}

// TestAdminProblemNeverCarriesSecrets proves detail text is redacted even when a
// caller passes a secret-shaped string.
func TestAdminProblemNeverCarriesSecrets(t *testing.T) {
	request := adminTestRequest(http.MethodGet, "/admin/v1/callers", "")
	recorder := httptest.NewRecorder()
	problem := AdminProblemFor(request, http.StatusBadRequest, "invalid-request")
	problem.Detail = "rejected Authorization: Bearer sk-live-9f2c8b7a6d5e4f3c2b1a and token_hash=deadbeefcafe1234"
	WriteAdminProblem(recorder, request, problem)
	body := recorder.Body.String()
	for _, marker := range []string{"sk-live-9f2c8b7a6d5e4f3c2b1a", "deadbeefcafe1234"} {
		if strings.Contains(body, marker) {
			t.Fatalf("problem document leaked %q: %s", marker, body)
		}
	}
}

// TestAdminProblemUnknownCodeFailsClosed proves an unregistered code cannot
// escape as itself.
func TestAdminProblemUnknownCodeFailsClosed(t *testing.T) {
	request := adminTestRequest(http.MethodGet, "/admin/v1/config/status", "")
	problem := AdminProblemFor(request, http.StatusBadRequest, "not-a-registered-code")
	if problem.Code != "internal-error" || problem.Status != http.StatusInternalServerError {
		t.Fatalf("unknown code produced %+v", problem)
	}
	problem = AdminProblemFor(request, http.StatusBadRequest, "invalid code with spaces")
	if problem.Code != "internal-error" {
		t.Fatalf("malformed code produced %+v", problem)
	}
}

// TestAdminRetryAfterHeader proves the throttle and rate-limit responses carry
// Retry-After.
func TestAdminRetryAfterHeader(t *testing.T) {
	request := adminTestRequest(http.MethodPost, "/admin/v1/callers/issue", "")
	recorder := httptest.NewRecorder()
	problem := AdminProblemFor(request, http.StatusTooManyRequests, "rate-limited")
	problem.RetryAfterSeconds = adminRetryAfterSeconds(1500 * time.Millisecond)
	WriteAdminProblem(recorder, request, problem)
	if got := recorder.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("retry-after = %q, want 2", got)
	}
	if decodeAdminProblem(t, recorder).RetryAfterSeconds != 2 {
		t.Fatal("retry_after_seconds missing from the document")
	}
}

// TestAdminETagPreconditions covers the strong-validator rules from ACP-015.
func TestAdminETagPreconditions(t *testing.T) {
	current := AdminETagForRevision(42)
	cases := []struct {
		name    string
		header  string
		outcome adminIfMatchOutcome
	}{
		{"absent", "", adminIfMatchOutcome{}},
		{"quoted", `"rev-42"`, adminIfMatchOutcome{Present: true, Matched: true}},
		{"bare revision", "42", adminIfMatchOutcome{Present: true, Matched: true}},
		{"listed match", `"rev-41", "rev-42"`, adminIfMatchOutcome{Present: true, Matched: true}},
		{"wildcard", "*", adminIfMatchOutcome{Present: true, Matched: true}},
		{"stale", `"rev-41"`, adminIfMatchOutcome{Present: true}},
		{"weak rejected", `W/"rev-42"`, adminIfMatchOutcome{Present: true, Invalid: true}},
		{"garbage", "rev-`42", adminIfMatchOutcome{Present: true, Invalid: true}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPut, "/admin/v1/config/groups/eu/targets", nil)
			if testCase.header != "" {
				request.Header.Set("If-Match", testCase.header)
			}
			if got := checkAdminIfMatch(request, current); got != testCase.outcome {
				t.Fatalf("checkAdminIfMatch(%q) = %+v, want %+v", testCase.header, got, testCase.outcome)
			}
		})
	}
	if got := checkAdminIfMatch(httptest.NewRequest(http.MethodPut, "/", nil), ""); got.Matched {
		t.Fatal("wildcard must not match a missing representation")
	}
}

// TestAdminETagRepresentationAndConditionalGet covers the set/body validators
// and If-None-Match.
func TestAdminETagRepresentationAndConditionalGet(t *testing.T) {
	setETag := AdminETagForSet([]byte("name: seed\n"))
	if setETag == AdminETagForSet([]byte("name: other\n")) {
		t.Fatal("set ETag must change with the representation")
	}
	if setETag != AdminETagForSet([]byte("name: seed\n")) {
		t.Fatal("set ETag must be deterministic")
	}
	if AdminETagForBody([]byte("a")) == AdminETagForBody([]byte("b")) {
		t.Fatal("body ETag must change with the body")
	}
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/config/groups", nil)
	request.Header.Set("If-None-Match", setETag)
	if !adminIfNoneMatchHit(request, setETag) {
		t.Fatal("If-None-Match did not match an identical validator")
	}
	recorder := httptest.NewRecorder()
	adminWriteNotModified(recorder, setETag)
	if recorder.Code != http.StatusNotModified || recorder.Header().Get("ETag") != setETag {
		t.Fatalf("304 response = %d etag=%q", recorder.Code, recorder.Header().Get("ETag"))
	}
}

// TestAdminPaginationValidation covers limit bounds, unknown filters, sort and
// direction validation (ACP-013/014).
func TestAdminPaginationValidation(t *testing.T) {
	registry, err := AdminV1DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	op, ok := registry.Lookup("listConfigSets")
	if !ok {
		t.Fatal("listConfigSets is not registered")
	}
	codec := NewAdminCursorCodec([]byte("test-cursor-key-0123456789abcdef"))
	config := AdminPaginationConfig{}

	cases := []struct {
		name       string
		target     string
		wantStatus int
		wantCode   string
		wantLimit  int
	}{
		{"defaults", "/admin/v1/config/sets", 0, "", 50},
		{"explicit", "/admin/v1/config/sets?limit=7", 0, "", 7},
		{"max", "/admin/v1/config/sets?limit=500", 0, "", 500},
		{"over max", "/admin/v1/config/sets?limit=501", http.StatusBadRequest, "invalid-limit", 0},
		{"zero", "/admin/v1/config/sets?limit=0", http.StatusBadRequest, "invalid-limit", 0},
		{"negative", "/admin/v1/config/sets?limit=-3", http.StatusBadRequest, "invalid-limit", 0},
		{"not a number", "/admin/v1/config/sets?limit=many", http.StatusBadRequest, "invalid-limit", 0},
		{"unknown filter", "/admin/v1/config/sets?project=eu", http.StatusBadRequest, "unknown-query-parameter", 0},
		{"unknown sort", "/admin/v1/config/sets?sort=secret", http.StatusBadRequest, "invalid-request", 0},
		{"unknown direction", "/admin/v1/config/sets?direction=sideways", http.StatusBadRequest, "invalid-request", 0},
		{"tampered cursor", "/admin/v1/config/sets?cursor=abc.def", http.StatusBadRequest, "invalid-cursor", 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, testCase.target, nil)
			page, problem := adminParsePage(request, op, config, codec)
			if testCase.wantStatus == 0 {
				if problem != nil {
					t.Fatalf("unexpected problem: %+v", problem)
				}
				if page.Limit != testCase.wantLimit {
					t.Fatalf("limit = %d, want %d", page.Limit, testCase.wantLimit)
				}
				return
			}
			if problem == nil {
				t.Fatal("expected a problem document")
			}
			if problem.Status != testCase.wantStatus || problem.Code != testCase.wantCode {
				t.Fatalf("problem = %d/%s, want %d/%s", problem.Status, problem.Code, testCase.wantStatus, testCase.wantCode)
			}
		})
	}
}

// TestAdminCursorBoundToQuery proves a cursor cannot be replayed against a
// different operation, filter set or sort.
func TestAdminCursorBoundToQuery(t *testing.T) {
	registry, err := AdminV1DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	op, _ := registry.Lookup("listConfigSets")
	other, _ := registry.Lookup("listCallers")
	codec := NewAdminCursorCodec([]byte("test-cursor-key-0123456789abcdef"))
	config := AdminPaginationConfig{}

	payload := adminCursorPayload{
		Endpoint:    op.ID,
		Sort:        "created_at",
		Direction:   "asc",
		Fingerprint: adminFilterFingerprint(op.ID, map[string]string{}, "created_at", "asc"),
		Values:      []string{"2026-11-06T00:00:00Z", "set-1"},
	}
	encoded, err := codec.Encode(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/config/sets?cursor="+encoded, nil)
	page, problem := adminParsePage(request, op, config, codec)
	if problem != nil {
		t.Fatalf("valid cursor rejected: %+v", problem)
	}
	if !page.HasCursor || page.Cursor.Values[1] != "set-1" {
		t.Fatalf("cursor did not round-trip: %+v", page)
	}

	// Same cursor, different sort: rejected.
	request = httptest.NewRequest(http.MethodGet, "/admin/v1/config/sets?cursor="+encoded+"&sort=name", nil)
	if _, problem := adminParsePage(request, op, config, codec); problem == nil || problem.Code != "invalid-cursor" {
		t.Fatalf("cursor accepted for a different sort: %+v", problem)
	}

	// Same cursor on a different operation: rejected.
	request = httptest.NewRequest(http.MethodGet, "/admin/v1/callers?cursor="+encoded, nil)
	if _, problem := adminParsePage(request, other, config, codec); problem == nil || problem.Code != "invalid-cursor" {
		t.Fatalf("cursor accepted for a different operation: %+v", problem)
	}

	// A cursor signed with another key: rejected.
	if _, err := NewAdminCursorCodec([]byte("another-key-0123456789abcdef")).Decode(encoded); err == nil {
		t.Fatal("cursor verified with the wrong key")
	}

	// Link header points at the next page and keeps the filter set.
	request = httptest.NewRequest(http.MethodGet, "/admin/v1/config/sets?limit=10", nil)
	link := adminNextLink(request, adminPageRequest{Limit: 10, Filters: map[string]string{}}, payload, codec)
	if !strings.Contains(link, `rel="next"`) || !strings.Contains(link, "limit=10") || !strings.Contains(link, "cursor=") {
		t.Fatalf("next link = %q", link)
	}
}

func adminTestIdempotencyDB(t *testing.T) *gorm.DB {
	t.Helper()
	runner, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeDB() })
	applyConfigControlPlaneMigrationsForTest(t, runner)
	return runner.db
}

// TestAdminIdempotencyStoreReplay proves the store's core contract: one
// execution per key, replay of the recorded response, 409 on a changed body, and
// in-doubt refusal while an earlier attempt is unfinished (ACP-016).
func TestAdminIdempotencyStoreReplay(t *testing.T) {
	db := adminTestIdempotencyDB(t)
	store, err := NewSQLAdminIdempotencyStore(db, AdminIdempotencyConfig{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	scope := adminIdempotencyScope{
		PrincipalKind: "adminkey", PrincipalSubject: "adminkey:k1", Domain: "default",
		Operation: "issueCaller", Key: "key-1", RequestHash: "hash-a",
	}
	if _, outcome, err := store.Begin(scope, now); err != nil || outcome != adminIdempotencyProceed {
		t.Fatalf("first begin = %v/%v", outcome, err)
	}
	// A concurrent attempt with the same key and body is in doubt, not executed.
	if _, outcome, _ := store.Begin(scope, now.Add(time.Second)); outcome != adminIdempotencyInDoubt {
		t.Fatalf("concurrent begin = %v, want in-doubt", outcome)
	}
	record := adminIdempotencyRecord{ResponseStatus: 201, ResponseContentType: "application/json", ResponseETag: `"rev-7"`, ResponseBody: `{"ok":true}`}
	if err := store.Complete(scope, record, now); err != nil {
		t.Fatal(err)
	}
	replayed, outcome, err := store.Begin(scope, now.Add(2*time.Second))
	if err != nil || outcome != adminIdempotencyReplay {
		t.Fatalf("replay = %v/%v", outcome, err)
	}
	if replayed.ResponseStatus != 201 || replayed.ResponseBody != `{"ok":true}` || replayed.ResponseETag != `"rev-7"` {
		t.Fatalf("replayed record = %+v", replayed)
	}
	// Same key, different body.
	conflict := scope
	conflict.RequestHash = "hash-b"
	if _, outcome, _ := store.Begin(conflict, now.Add(3*time.Second)); outcome != adminIdempotencyConflict {
		t.Fatalf("changed body = %v, want conflict", outcome)
	}
	// A different domain or operation is a different scope.
	otherDomain := scope
	otherDomain.Domain = "example/prod"
	if _, outcome, _ := store.Begin(otherDomain, now.Add(3*time.Second)); outcome != adminIdempotencyProceed {
		t.Fatalf("other domain = %v, want proceed", outcome)
	}
	otherOperation := scope
	otherOperation.Operation = "rotateCaller"
	if _, outcome, _ := store.Begin(otherOperation, now.Add(3*time.Second)); outcome != adminIdempotencyProceed {
		t.Fatalf("other operation = %v, want proceed", outcome)
	}
}

// TestAdminIdempotencyAcrossRestart proves the record is persisted, not
// process-local.
func TestAdminIdempotencyAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idempotency.sqlite")
	runner, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	applyConfigControlPlaneMigrationsForTest(t, runner)
	store, err := NewSQLAdminIdempotencyStore(runner.db, AdminIdempotencyConfig{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	scope := adminIdempotencyScope{PrincipalKind: "mtls", PrincipalSubject: "spiffe://example/ns/a/sa/b", Domain: "default", Operation: "revokeCaller", Key: "restart-key", RequestHash: "hash"}
	if _, outcome, _ := store.Begin(scope, now); outcome != adminIdempotencyProceed {
		t.Fatalf("begin = %v", outcome)
	}
	if err := store.Complete(scope, adminIdempotencyRecord{ResponseStatus: 200, ResponseBody: `{"done":true}`}, now); err != nil {
		t.Fatal(err)
	}
	_ = closeDB()

	reopened, closeReopened, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeReopened() }()
	store, err = NewSQLAdminIdempotencyStore(reopened.db, AdminIdempotencyConfig{})
	if err != nil {
		t.Fatal(err)
	}
	replayed, outcome, err := store.Begin(scope, now.Add(time.Minute))
	if err != nil || outcome != adminIdempotencyReplay {
		t.Fatalf("replay after restart = %v/%v", outcome, err)
	}
	if replayed.ResponseBody != `{"done":true}` {
		t.Fatalf("replayed body = %q", replayed.ResponseBody)
	}
}

// TestAdminIdempotencyAbortAndPrune covers the release path and retention.
func TestAdminIdempotencyAbortAndPrune(t *testing.T) {
	store, err := NewSQLAdminIdempotencyStore(adminTestIdempotencyDB(t), AdminIdempotencyConfig{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	scope := adminIdempotencyScope{PrincipalKind: "adminkey", PrincipalSubject: "adminkey:k1", Domain: "default", Operation: "issueCaller", Key: "abort-key", RequestHash: "hash"}
	if _, outcome, _ := store.Begin(scope, now); outcome != adminIdempotencyProceed {
		t.Fatalf("begin = %v", outcome)
	}
	if err := store.Abort(scope); err != nil {
		t.Fatal(err)
	}
	if _, outcome, _ := store.Begin(scope, now.Add(time.Second)); outcome != adminIdempotencyProceed {
		t.Fatalf("after abort = %v, want proceed", outcome)
	}
	if _, err := store.PruneExpired(now.Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, outcome, _ := store.Begin(scope, now.Add(3*time.Hour)); outcome != adminIdempotencyProceed {
		t.Fatalf("after prune = %v, want proceed", outcome)
	}
}

// TestAdminIdempotencyOversizeResponseIsNeverReExecuted proves an oversized
// response is recorded as in-doubt instead of allowing a second execution.
func TestAdminIdempotencyOversizeResponseIsNeverReExecuted(t *testing.T) {
	store, err := NewSQLAdminIdempotencyStore(adminTestIdempotencyDB(t), AdminIdempotencyConfig{MaxResponseBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	scope := adminIdempotencyScope{PrincipalKind: "adminkey", PrincipalSubject: "adminkey:k1", Domain: "default", Operation: "exportTrainingData", Key: "big", RequestHash: "hash"}
	if _, _, err := store.Begin(scope, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(scope, adminIdempotencyRecord{ResponseStatus: 200, ResponseBody: strings.Repeat("x", 64)}, now); err != nil {
		t.Fatal(err)
	}
	if _, outcome, _ := store.Begin(scope, now.Add(time.Second)); outcome == adminIdempotencyProceed {
		t.Fatal("an oversized recorded response allowed a second execution")
	}
}

// TestAdminIdempotencyMemoryStoreMatchesSQL documents the semantics of the
// no-database fallback used by unit tests and by planes without a database.
func TestAdminIdempotencyMemoryStoreMatchesSQL(t *testing.T) {
	store := NewMemoryAdminIdempotencyStore(AdminIdempotencyConfig{}, 8)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	scope := adminIdempotencyScope{PrincipalKind: "adminkey", PrincipalSubject: "adminkey:k1", Domain: "default", Operation: "issueCaller", Key: "k", RequestHash: "h"}
	if _, outcome, _ := store.Begin(scope, now); outcome != adminIdempotencyProceed {
		t.Fatalf("begin = %v", outcome)
	}
	if _, outcome, _ := store.Begin(scope, now); outcome != adminIdempotencyInDoubt {
		t.Fatalf("second begin = %v", outcome)
	}
	if err := store.Complete(scope, adminIdempotencyRecord{ResponseStatus: 200, ResponseBody: "{}"}, now); err != nil {
		t.Fatal(err)
	}
	if _, outcome, _ := store.Begin(scope, now.Add(time.Second)); outcome != adminIdempotencyReplay {
		t.Fatalf("replay = %v", outcome)
	}
	conflict := scope
	conflict.RequestHash = "other"
	if _, outcome, _ := store.Begin(conflict, now.Add(2*time.Second)); outcome != adminIdempotencyConflict {
		t.Fatalf("conflict = %v", outcome)
	}
	if _, _, err := NewMemoryAdminIdempotencyStore(AdminIdempotencyConfig{}, 1).Begin(adminIdempotencyScope{PrincipalSubject: "", Operation: "x", Key: "k"}, now); err == nil {
		t.Fatal("incomplete scope was accepted")
	}
	if _, _, err := store.Begin(adminIdempotencyScope{PrincipalKind: "adminkey", PrincipalSubject: "s", Operation: "op", Key: strings.Repeat("k", 200)}, now); err == nil {
		t.Fatal("oversized idempotency key was accepted")
	}
}

// TestAdminRateLimiterPerIdentity proves limits are per client identity, that a
// hit returns a Retry-After, and that one identity cannot spend another's
// budget (ACP-021).
func TestAdminRateLimiterPerIdentity(t *testing.T) {
	enabled := true
	disabledFlag := false
	limiter := NewAdminRateLimiter(AdminRateLimitConfig{Enabled: &enabled, WriteRPS: 1, WriteBurst: 2, ReadRPS: 5, ReadBurst: 5})
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	alice := AdminIdentityKey("adminkey", "adminkey:alice")
	bob := AdminIdentityKey("adminkey", "adminkey:bob")
	for i := 0; i < 2; i++ {
		if _, ok := limiter.Allow(alice, AdminRateClassWrite, now); !ok {
			t.Fatalf("write %d for alice was limited too early", i)
		}
	}
	retryAfter, ok := limiter.Allow(alice, AdminRateClassWrite, now)
	if ok {
		t.Fatal("alice exceeded her write burst without being limited")
	}
	if retryAfter <= 0 {
		t.Fatalf("retryAfter = %v, want a positive cooldown", retryAfter)
	}
	if _, ok := limiter.Allow(bob, AdminRateClassWrite, now); !ok {
		t.Fatal("bob was limited by alice's usage")
	}
	if _, ok := limiter.Allow(alice, AdminRateClassRead, now); !ok {
		t.Fatal("read class was limited by the write class")
	}
	// Tokens refill over time.
	if _, ok := limiter.Allow(alice, AdminRateClassWrite, now.Add(2*time.Second)); !ok {
		t.Fatal("tokens did not refill")
	}
	// A disabled limiter always allows.
	disabled := NewAdminRateLimiter(AdminRateLimitConfig{Enabled: &disabledFlag})
	for i := 0; i < 100; i++ {
		if _, ok := disabled.Allow(alice, AdminRateClassWrite, now); !ok {
			t.Fatal("disabled limiter denied a request")
		}
	}
}

// TestAdminAuthThrottleGraduatedAndNoLockout proves the authentication throttle
// is a bounded, self-clearing cooldown rather than a lockout (ACP-021).
func TestAdminAuthThrottleGraduatedAndNoLockout(t *testing.T) {
	enabled := true
	throttle := NewAdminAuthThrottle(AdminAuthFailureThrottleConfig{
		Enabled: &enabled, Window: "5m", FreeAttempts: 3, BaseDelay: "100ms", MaxDelay: "400ms",
	})
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	identity := "adminkey:alice"
	address := "203.0.113.7"

	for attempt := 1; attempt <= 3; attempt++ {
		if retryAfter, ok := throttle.Allow(identity, address, now); !ok {
			t.Fatalf("free attempt %d was throttled after %v", attempt, retryAfter)
		}
		if cooldown := throttle.RecordFailure(identity, address, now); cooldown != 0 {
			t.Fatalf("free attempt %d produced cooldown %v", attempt, cooldown)
		}
	}
	cooldown := throttle.RecordFailure(identity, address, now)
	if cooldown != 100*time.Millisecond {
		t.Fatalf("first cooldown = %v, want 100ms", cooldown)
	}
	retryAfter, ok := throttle.Allow(identity, address, now.Add(10*time.Millisecond))
	if ok || retryAfter <= 0 {
		t.Fatalf("attempt inside the cooldown was allowed (retryAfter=%v)", retryAfter)
	}
	// The cooldown is capped, so no attacker can turn this into a lockout.
	for attempt := 0; attempt < 40; attempt++ {
		cooldown = throttle.RecordFailure(identity, address, now)
	}
	if cooldown > 400*time.Millisecond {
		t.Fatalf("cooldown %v exceeded the configured maximum", cooldown)
	}
	// After the cap elapses the client may try again.
	if _, ok := throttle.Allow(identity, address, now.Add(500*time.Millisecond)); !ok {
		t.Fatal("client stayed throttled past the maximum cooldown: that is a lockout")
	}
	// A success clears the state.
	throttle.RecordSuccess(identity, address)
	if _, ok := throttle.Allow(identity, address, now.Add(time.Second)); !ok {
		t.Fatal("success did not clear the throttle state")
	}
	// Another identity is unaffected by alice's failures.
	if _, ok := throttle.Allow("adminkey:bob", "198.51.100.9", now); !ok {
		t.Fatal("another identity was throttled by alice's failures")
	}
}

// TestAdminAuthThrottleWindowExpiry proves failures age out.
func TestAdminAuthThrottleWindowExpiry(t *testing.T) {
	enabled := true
	throttle := NewAdminAuthThrottle(AdminAuthFailureThrottleConfig{Enabled: &enabled, Window: "1s", FreeAttempts: 1, BaseDelay: "1s", MaxDelay: "2s"})
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	throttle.RecordFailure("adminkey:alice", "", now)
	throttle.RecordFailure("adminkey:alice", "", now)
	if _, ok := throttle.Allow("adminkey:alice", "", now.Add(time.Millisecond)); ok {
		t.Fatal("throttle did not engage")
	}
	if _, ok := throttle.Allow("adminkey:alice", "", now.Add(5*time.Second)); !ok {
		t.Fatal("throttle did not expire with its window")
	}
}

// TestAdminDeprecationHeaders covers RFC 9745 / RFC 8594 emission and the
// minimum-notice policy (ACP-N05).
func TestAdminDeprecationHeaders(t *testing.T) {
	since := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	sunset := since.Add(100 * 24 * time.Hour)
	deprecation := AdminDeprecation{Since: since, Sunset: sunset, Link: "https://example.invalid/docs/admin-api-v1"}
	if err := deprecation.Validate(); err != nil {
		t.Fatalf("valid window rejected: %v", err)
	}
	recorder := httptest.NewRecorder()
	ApplyAdminDeprecationHeaders(recorder, deprecation)
	if got, want := recorder.Header().Get("Deprecation"), "@"+strconv.FormatInt(since.Unix(), 10); got != want {
		t.Fatalf("deprecation header = %q, want %q", got, want)
	}
	if got := recorder.Header().Get("Sunset"); !strings.Contains(got, "Feb 2027") {
		t.Fatalf("sunset header = %q", got)
	}
	if got := recorder.Header().Get("Link"); !strings.Contains(got, `rel="deprecation"`) {
		t.Fatalf("link header = %q", got)
	}
	if err := (AdminDeprecation{Since: since, Sunset: since.Add(89 * 24 * time.Hour)}).Validate(); err == nil {
		t.Fatal("a sunset inside the 90-day notice was accepted")
	}
	if err := (AdminDeprecation{Since: since, Sunset: since.Add(-time.Hour)}).Validate(); err == nil {
		t.Fatal("a sunset before the deprecation date was accepted")
	}
}

// TestAdminOperationDecisionsAreAnchoredActions rejects a registry action that
// is not one anchored literal action, because the Casbin matcher anchors the
// pattern and a regex would silently widen a guard.
func TestAdminOperationDecisionsAreAnchoredActions(t *testing.T) {
	base := Operation{
		ID: "testOp", Method: "GET", Path: "/admin/v1/config/status", Object: authzObjectAdminConfig,
		Action: "read", Domain: AdminDomainGlobal, RateClass: AdminRateClassRead, HandlerKey: "test.op",
	}
	if err := base.validate(); err != nil {
		t.Fatalf("baseline operation rejected: %v", err)
	}
	for _, action := range []string{"read|write", ".*", "read.*", "", "READ"} {
		invalid := base
		invalid.Action = action
		if err := invalid.validate(); err == nil {
			t.Fatalf("action %q was accepted", action)
		}
	}
	for _, object := range []string{"", "admin:*", "Admin Config"} {
		invalid := base
		invalid.Object = object
		if err := invalid.validate(); err == nil {
			t.Fatalf("object %q was accepted", object)
		}
	}
}

// TestAdminRequestIDContextRoundTrip covers the request-ID helper contract used
// by the mount layer and the listener.
func TestAdminRequestIDContextRoundTrip(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/metrics", nil)
	if AdminRequestIDFrom(request) != "" {
		t.Fatal("an unassigned request reported an ID")
	}
	request = request.WithContext(WithAdminRequestID(request.Context(), "req_assigned"))
	if got := AdminRequestIDFrom(request); got != "req_assigned" {
		t.Fatalf("assigned request ID = %q", got)
	}
	if got := AdminRequestID(request); got != "req_assigned" {
		t.Fatalf("AdminRequestID = %q", got)
	}
	generated := AdminRequestID(httptest.NewRequest(http.MethodGet, "/admin/v1/metrics", nil))
	if !strings.HasPrefix(generated, "req_") {
		t.Fatalf("generated request ID = %q", generated)
	}
	if AdminRequestID(nil) == "" {
		t.Fatal("nil request produced no request ID")
	}
	_ = context.Background()
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test harness: the /admin/v1 mount with fake #814/#812 seams
// ---------------------------------------------------------------------------

// fakeAuthenticator resolves the principal from a test header. It is a test-only
// stand-in for #814's chain, per the approved design.
type fakeAuthenticator struct {
	principal Principal
	err       error
	// failForSubject makes that subject fail with errInvalid for throttle tests.
	throttleOnErr bool
}

func (f *fakeAuthenticator) Authenticate(_ context.Context, r *http.Request) (Principal, error) {
	if f == nil {
		return Principal{}, ErrNoCredential
	}
	if f.err != nil {
		return Principal{}, f.err
	}
	principal := f.principal
	if subject := strings.TrimSpace(r.Header.Get("X-Test-Principal")); subject != "" {
		principal = Principal{Subject: subject, Kind: "adminkey", CredentialID: "cred-1", Issuer: "test"}
	}
	if principal.Subject == "" {
		return Principal{}, ErrNoCredential
	}
	return principal, nil
}

// fakeAuthorizer records every decision so a test can assert the exact number and
// arguments, which is how the single-decision-point rule is enforced.
type fakeAuthorizer struct {
	mu        sync.Mutex
	calls     []fakeAuthorizerCall
	deny      map[string]bool // "domain|object|action" → deny
	denyAll   bool
	failError error
}

type fakeAuthorizerCall struct {
	Principal Principal
	Domain    string
	Object    string
	Action    string
}

func (f *fakeAuthorizer) Authorize(_ context.Context, principal Principal, domain, object, action string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeAuthorizerCall{Principal: principal, Domain: domain, Object: object, Action: action})
	if f.failError != nil {
		return f.failError
	}
	if f.denyAll {
		return errTestAdminForbidden
	}
	if f.deny[domain+"|"+object+"|"+action] {
		return errTestAdminForbidden
	}
	return nil
}

func (f *fakeAuthorizer) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

func (f *fakeAuthorizer) snapshot() []fakeAuthorizerCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeAuthorizerCall, len(f.calls))
	copy(out, f.calls)
	return out
}

type testAdminProblemError struct {
	status int
	code   string
}

func (e testAdminProblemError) Error() string { return e.code }

func (e testAdminProblemError) AdminProblemCode() (int, string) { return e.status, e.code }

var (
	errTestAdminForbidden = testAdminProblemError{status: http.StatusForbidden, code: "admin-forbidden"}
	errTestPrivEscalation = testAdminProblemError{status: http.StatusForbidden, code: "privilege-escalation"}
	errTestPolicyStale    = testAdminProblemError{status: http.StatusServiceUnavailable, code: "policy-stale"}
)

type adminV1Harness struct {
	mux          *http.ServeMux
	registry     *AdminV1Registry
	authenticate *fakeAuthenticator
	authorize    *fakeAuthorizer
	idempotency  AdminIdempotencyKeyStore
	limiter      AdminRateLimiter
	throttle     AdminAuthThrottle
	codec        *AdminCursorCodec
	now          time.Time
	currentETag  func(*http.Request) (string, int64, error)
}

func newAdminV1Harness(t *testing.T) *adminV1Harness {
	t.Helper()
	registry, err := AdminV1DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	configDeps := ConfigAdminDeps{
		DB: nil, RuntimeScope: "prod", FileMode: true, Driver: "sqlite", InstanceID: "replica-test",
		CurrentConfig: func() *Config { return nil },
		LoadedRevision: func() (int64, string, time.Time) {
			return 7, "set-7", time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
		},
	}
	callerDeps := CallerAdminDeps{}
	extra := map[string]http.HandlerFunc{
		"training.export": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"export": "ok"})
		},
		"training.outcomes.label": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"label": "ok"})
		},
		"metrics.scrape": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
			_, _ = w.Write([]byte("router_requests_total 1\n"))
		},
		"content.captures.delete": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"deleted": 1})
		},
		"content.captures.purge": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"deleted": 0})
		},
	}
	harness := &adminV1Harness{
		mux:          http.NewServeMux(),
		registry:     registry,
		authenticate: &fakeAuthenticator{principal: Principal{Subject: "adminkey:test", Kind: "adminkey", CredentialID: "cred-1", Issuer: "test"}},
		authorize:    &fakeAuthorizer{deny: map[string]bool{}},
		idempotency:  NewMemoryAdminIdempotencyStore(AdminIdempotencyConfig{}, 64),
		limiter:      NewAdminRateLimiter(AdminRateLimitConfig{Enabled: boolPointer(false)}),
		throttle:     NewAdminAuthThrottle(AdminAuthFailureThrottleConfig{Enabled: boolPointer(true), FreeAttempts: 100}),
		codec:        NewAdminCursorCodec([]byte("harness-cursor-key-0123456789")),
		now:          time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC),
		currentETag: func(*http.Request) (string, int64, error) {
			return AdminETagForRevision(7), 7, nil
		},
	}
	principalOf := func(r *http.Request) (Principal, string, bool) {
		principal, ok := PrincipalFrom(r.Context())
		if !ok {
			return Principal{}, "", false
		}
		return principal, adminGlobalDomain, true
	}
	authorize := func(principal Principal, domain, object, action string) bool {
		return harness.authorize.Authorize(rContext(), principal, domain, object, action) == nil
	}
	extra = addReportPlaceholderHandlers(registry, extra)
	handlers := AdminV1Handlers(
		AdminV1ConfigAdminDeps(configDeps, principalOf, authorize),
		AdminV1CallerAdminDeps(callerDeps, principalOf, authorize),
		extra,
	)
	err = RegisterAdminV1(harness.mux, AdminV1Deps{
		Registry:     registry,
		Authenticate: harness.authenticate,
		Authorize: func(ctx context.Context, principal Principal, domain, object, action string) error {
			return harness.authorize.Authorize(ctx, principal, domain, object, action)
		},
		Handlers: handlers,
		ResolveDomain: func(Principal) string {
			return adminGlobalDomain
		},
		ResolveCallerDomain: func(r *http.Request) (string, error) {
			if r.PathValue("id") == "missing" {
				return "", nil
			}
			return "example/prod", nil
		},
		ResolveCapturedDomains: func(r *http.Request) ([]string, error) {
			switch r.PathValue("request_id") {
			case "no-domain":
				return nil, nil
			case "unknown-domain":
				return []string{contentCaptureUnknownDomain}, nil
			}
			return []string{"example/prod"}, nil
		},
		CurrentETag:      func(r *http.Request) (string, int64, error) { return harness.currentETag(r) },
		CursorCodec:      harness.codec,
		Pagination:       AdminPaginationConfig{},
		Idempotency:      harness.idempotency,
		IdempotencyOn:    true,
		Limiter:          harness.limiter,
		Throttle:         harness.throttle,
		Denials:          &recordingDenialSink{},
		CredentialEvents: &recordingCredentialSink{},
		Clock:            func() time.Time { return harness.now },
	})
	if err != nil {
		t.Fatalf("mount /admin/v1: %v", err)
	}
	return harness
}

func boolPointer(value bool) *bool { return &value }

// adminV1TestCountCalls counts invocations of the synthetic idempotency fixture
// handler so a test can prove a replay does not execute it again.
var adminV1TestCountCalls int64

func adminV1TestCountHandler(w http.ResponseWriter, r *http.Request) {
	n := atomic.AddInt64(&adminV1TestCountCalls, 1)
	writeJSON(w, http.StatusCreated, map[string]any{"calls": n})
}

// addReportPlaceholderHandlers binds the reports operations to a placeholder so
// the mount and its conformance test cover every registered operation. The real
// reports adapter (which re-dispatches to the existing reports handler) lands with
// that slice; binding is by HandlerKey, so replacing these is additive.
func addReportPlaceholderHandlers(registry *AdminV1Registry, handlers map[string]http.HandlerFunc) map[string]http.HandlerFunc {
	out := make(map[string]http.HandlerFunc, len(handlers)+32)
	for key, handler := range handlers {
		out[key] = handler
	}
	for _, op := range registry.Operations() {
		if op.Adapter != "reports" {
			continue
		}
		if _, ok := out[op.HandlerKey]; ok {
			continue
		}
		opID := op.ID
		out[op.HandlerKey] = func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"report": opID})
		}
	}
	return out
}

var adminTestBackgroundContext = context.Background()

func rContext() context.Context { return adminTestBackgroundContext }

func (h *adminV1Harness) do(t *testing.T, method, target string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	request := httptest.NewRequest(method, target, reader)
	request.Header.Set("X-Test-Principal", "adminkey:test")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)
	return recorder
}

// TestAdminV1RouteConformance asserts the mounted routes and the registry agree:
// every registry operation is mounted, and nothing under /admin/v1 is mounted
// outside the registry.
func TestAdminV1RouteConformance(t *testing.T) {
	harness := newAdminV1Harness(t)
	for _, op := range harness.registry.Operations() {
		probe := op.Path
		probe = strings.ReplaceAll(probe, "{group}", "g1")
		probe = strings.ReplaceAll(probe, "{set_id}", "set-1")
		probe = strings.ReplaceAll(probe, "{request_id}", "req-1")
		probe = strings.ReplaceAll(probe, "{id}", "caller-1")
		probe = strings.ReplaceAll(probe, "{target_id...}", "prov/model")
		handler, pattern := harness.mux.Handler(httptest.NewRequest(op.Method, probe, nil))
		if handler == nil || pattern == "" {
			t.Fatalf("operation %s (%s) is not mounted", op.ID, op.Pattern())
		}
		if pattern != op.Pattern() {
			t.Fatalf("operation %s resolves to pattern %q, want %q", op.ID, pattern, op.Pattern())
		}
	}
	// A registered-looking path outside the registry must 404.
	recorder := harness.do(t, http.MethodGet, "/admin/v1/not-a-real-resource", nil, nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unregistered /admin/v1 path returned %d, want 404", recorder.Code)
	}
}

// TestAdminV1SingleDecisionPoint proves every operation takes exactly one primary
// authorization decision, plus its declared always-checks, and that the decision
// carries the registry's object/action and the resolved domain.
func TestAdminV1SingleDecisionPoint(t *testing.T) {
	harness := newAdminV1Harness(t)
	for _, op := range harness.registry.Operations() {
		harness.authorize.reset()
		target := op.Path
		target = strings.ReplaceAll(target, "{group}", "g1")
		target = strings.ReplaceAll(target, "{set_id}", "set-1")
		target = strings.ReplaceAll(target, "{request_id}", "req-1")
		target = strings.ReplaceAll(target, "{id}", "caller-1")
		target = strings.ReplaceAll(target, "{target_id...}", "prov/model")
		recorder := harness.do(t, op.Method, target, map[string]any{}, map[string]string{"If-Match": `"rev-7"`})
		_ = recorder
		calls := harness.authorize.snapshot()
		if op.Domain == AdminDomainHandler {
			// Reports keep the handler's own decisions; the contract layer must not
			// add a domain-wide gate that would deny domain-scoped administrators.
			continue
		}
		want := 1 + len(op.ExtraChecks)
		if len(calls) != want {
			t.Fatalf("operation %s took %d decisions, want %d: %+v", op.ID, len(calls), want, calls)
		}
		if calls[0].Object != op.Object || calls[0].Action != op.Action {
			t.Fatalf("operation %s primary decision = %s/%s, want %s/%s", op.ID, calls[0].Object, calls[0].Action, op.Object, op.Action)
		}
		for i, check := range op.ExtraChecks {
			if calls[i+1].Object != check.Object || calls[i+1].Action != check.Action {
				t.Fatalf("operation %s extra check %d = %s/%s, want %s/%s", op.ID, i, calls[i+1].Object, calls[i+1].Action, check.Object, check.Action)
			}
		}
		for _, call := range calls {
			if call.Principal.Subject != "adminkey:test" {
				t.Fatalf("operation %s decided for %q", op.ID, call.Principal.Subject)
			}
		}
	}
}

// TestAdminV1CrossDomainDenied proves a domain-scoped deny blocks a
// caller-directory operation in another domain.
func TestAdminV1CrossDomainDenied(t *testing.T) {
	harness := newAdminV1Harness(t)
	harness.authorize.deny["example/prod|admin:callers|rotate"] = true
	recorder := harness.do(t, http.MethodPost, "/admin/v1/callers/caller-1/rotate", map[string]any{}, map[string]string{"If-Match": `"rev-7"`})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	problem := decodeAdminProblem(t, recorder)
	if problem.Code != "admin-forbidden" {
		t.Fatalf("code = %q, want admin-forbidden", problem.Code)
	}
	calls := harness.authorize.snapshot()
	if len(calls) != 1 || calls[0].Domain != "example/prod" {
		t.Fatalf("decision did not use the caller's stored domain: %+v", calls)
	}
}

// TestAdminV1ContentCaptureDomainFromRow proves the content-capture deletion
// decision uses the captured row's domain, and fails closed when it cannot.
func TestAdminV1ContentCaptureDomainFromRow(t *testing.T) {
	harness := newAdminV1Harness(t)
	harness.do(t, http.MethodDelete, "/admin/v1/content-captures/req-1", nil, nil)
	calls := harness.authorize.snapshot()
	if len(calls) != 1 || calls[0].Domain != "example/prod" || calls[0].Object != "content:capture" || calls[0].Action != "delete" {
		t.Fatalf("unexpected decision for captured row: %+v", calls)
	}

	harness.authorize.reset()
	harness.do(t, http.MethodDelete, "/admin/v1/content-captures/unknown-domain", nil, nil)
	calls = harness.authorize.snapshot()
	if len(calls) != 0 {
		t.Fatalf("an unresolvable captured domain still reached a decision: %+v", calls)
	}

	harness.authorize.reset()
	harness.do(t, http.MethodDelete, "/admin/v1/content-captures/no-domain", nil, nil)
	calls = harness.authorize.snapshot()
	if len(calls) != 1 || calls[0].Domain != "*" {
		t.Fatalf("a captured row without a domain must require the whole-plane grant: %+v", calls)
	}
}

// TestAdminV1PolicyStaleSurfacesAs503 proves #812's policy-stale error reaches the
// client through the shared problem renderer.
func TestAdminV1PolicyStaleSurfacesAs503(t *testing.T) {
	harness := newAdminV1Harness(t)
	harness.authorize.failError = errTestPolicyStale
	recorder := harness.do(t, http.MethodGet, "/admin/v1/config/status", nil, nil)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	if problem := decodeAdminProblem(t, recorder); problem.Code != "policy-stale" {
		t.Fatalf("code = %q, want policy-stale", problem.Code)
	}
}

// TestAdminV1AuthenticationRequired proves an unauthenticated request is a 401
// problem document, not a legacy error envelope.
func TestAdminV1AuthenticationRequired(t *testing.T) {
	harness := newAdminV1Harness(t)
	harness.authenticate.err = ErrNoCredential
	recorder := harness.do(t, http.MethodGet, "/admin/v1/config/status", nil, nil)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("content type = %q", got)
	}
	if problem := decodeAdminProblem(t, recorder); problem.Code != "authentication_required" {
		t.Fatalf("code = %q", problem.Code)
	}
	if len(harness.authorize.snapshot()) != 0 {
		t.Fatal("an unauthenticated request reached the authorizer")
	}
}

// TestAdminV1IfMatchRequiredAndStale proves 428/412 on the versioned surface, with
// the current validator in the problem document, and that the legacy route keeps
// its optional precondition.
func TestAdminV1IfMatchRequiredAndStale(t *testing.T) {
	harness := newAdminV1Harness(t)
	body := map[string]any{"targets": []any{}, "reason": "test"}

	recorder := harness.do(t, http.MethodPut, "/admin/v1/config/groups/g1/targets", body, nil)
	if recorder.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match status = %d, want 428", recorder.Code)
	}
	problem := decodeAdminProblem(t, recorder)
	if problem.Code != "precondition-required" || problem.CurrentETag != `"rev-7"` || problem.CurrentRevision == nil || *problem.CurrentRevision != 7 {
		t.Fatalf("unexpected 428 problem: %+v", problem)
	}

	recorder = harness.do(t, http.MethodPut, "/admin/v1/config/groups/g1/targets", body, map[string]string{"If-Match": `"rev-6"`})
	if recorder.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match status = %d, want 412", recorder.Code)
	}
	if problem := decodeAdminProblem(t, recorder); problem.CurrentETag != `"rev-7"` {
		t.Fatalf("412 problem = %+v", problem)
	}

	recorder = harness.do(t, http.MethodPut, "/admin/v1/config/groups/g1/targets", body, map[string]string{"If-Match": `W/"rev-7"`})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("weak If-Match status = %d, want 400", recorder.Code)
	}
}

// TestAdminV1IfMatchRace proves two writers holding the same validator produce one
// commit and one 412.
func TestAdminV1IfMatchRace(t *testing.T) {
	harness := newAdminV1Harness(t)
	revision := int64(7)
	harness.currentETag = func(*http.Request) (string, int64, error) {
		return AdminETagForRevision(revision), revision, nil
	}
	registry, err := NewAdminV1Registry([]Operation{{
		ID: "raceWriter", Method: "PUT", Path: "/admin/v1/tests/race", Object: authzObjectAdminConfig,
		Action: "write", Domain: AdminDomainGlobal, RateClass: AdminRateClassWrite,
		HandlerKey: "test.race", Concurrency: AdminConcurrencyRevision,
	}})
	if err != nil {
		t.Fatal(err)
	}
	harness = newAdminV1HarnessWithRegistry(t, registry)
	harness.currentETag = func(*http.Request) (string, int64, error) {
		return AdminETagForRevision(revision), revision, nil
	}
	first := harness.do(t, http.MethodPut, "/admin/v1/tests/race", map[string]any{"a": 1}, map[string]string{"If-Match": `"rev-7"`})
	if first.Code != http.StatusOK {
		t.Fatalf("first writer status = %d, want 200 (%s)", first.Code, first.Body.String())
	}
	revision = 8 // the first writer committed
	second := harness.do(t, http.MethodPut, "/admin/v1/tests/race", map[string]any{"a": 1}, map[string]string{"If-Match": `"rev-7"`})
	if second.Code != http.StatusPreconditionFailed {
		t.Fatalf("second writer status = %d, want 412", second.Code)
	}
}

// TestAdminV1IdempotentReplayAtHTTP proves a replay returns the recorded response
// without executing the handler again, and that a changed body is a 409.
func TestAdminV1IdempotentReplayAtHTTP(t *testing.T) {
	atomic.StoreInt64(&adminV1TestCountCalls, 0)
	// Mount a synthetic operation through the registry path by rebuilding the
	// mount with an extra operation.
	registry, err := NewAdminV1Registry(append(AdminV1Operations(), Operation{
		ID: "countTestCalls", Method: "POST", Path: "/admin/v1/tests/count",
		Object: authzObjectAdminConfig, Action: "write", Domain: AdminDomainGlobal,
		RateClass: AdminRateClassWrite, HandlerKey: "test.count", SuccessStatus: 201,
		Idempotency: AdminIdempotencyStore, Concurrency: AdminConcurrencyRevision,
	}))
	if err != nil {
		t.Fatal(err)
	}
	harness2 := newAdminV1HarnessWithRegistry(t, registry)

	headers := map[string]string{"If-Match": `"rev-7"`, "Idempotency-Key": "key-1"}
	first := harness2.do(t, http.MethodPost, "/admin/v1/tests/count", map[string]any{"a": 1}, headers)
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d (%s)", first.Code, first.Body.String())
	}
	second := harness2.do(t, http.MethodPost, "/admin/v1/tests/count", map[string]any{"a": 1}, headers)
	if second.Code != http.StatusCreated {
		t.Fatalf("replay status = %d, want 201", second.Code)
	}
	if second.Header().Get("Idempotent-Replay") != "true" {
		t.Fatal("replay was not identified as a replay")
	}
	if second.Body.String() != first.Body.String() {
		t.Fatalf("replayed body %q != original %q", second.Body.String(), first.Body.String())
	}
	if got := atomic.LoadInt64(&adminV1TestCountCalls); got != 1 {
		t.Fatalf("handler ran %d times, want 1", got)
	}
	conflict := harness2.do(t, http.MethodPost, "/admin/v1/tests/count", map[string]any{"a": 2}, headers)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed-body status = %d, want 409", conflict.Code)
	}
	if problem := decodeAdminProblem(t, conflict); problem.Code != "idempotency-key-conflict" {
		t.Fatalf("conflict code = %q", problem.Code)
	}
}

// TestAdminV1QueryValidationAtHTTP proves limit and unknown-filter rejection
// reaches the client as a problem document.
func TestAdminV1QueryValidationAtHTTP(t *testing.T) {
	harness := newAdminV1Harness(t)
	recorder := harness.do(t, http.MethodGet, "/admin/v1/config/sets?limit=501", nil, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("limit=501 status = %d, want 400", recorder.Code)
	}
	if problem := decodeAdminProblem(t, recorder); problem.Code != "invalid-limit" {
		t.Fatalf("code = %q", problem.Code)
	}
	recorder = harness.do(t, http.MethodGet, "/admin/v1/config/sets?bogus=1", nil, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown filter status = %d, want 400", recorder.Code)
	}
	if problem := decodeAdminProblem(t, recorder); problem.Code != "unknown-query-parameter" {
		t.Fatalf("code = %q", problem.Code)
	}
}

// TestAdminV1RateLimitAtHTTP proves a limited identity gets 429 with Retry-After
// and that another identity is unaffected.
func TestAdminV1RateLimitAtHTTP(t *testing.T) {
	harness := newAdminV1Harness(t)
	harness.limiter = NewAdminRateLimiter(AdminRateLimitConfig{
		Enabled: boolPointer(true), ReadRPS: 0.001, ReadBurst: 2, WriteRPS: 0.001, WriteBurst: 2, MaxIdentities: 8,
	})
	harness.mount(t)
	var limited *httptest.ResponseRecorder
	for i := 0; i < 5; i++ {
		recorder := harness.do(t, http.MethodGet, "/admin/v1/config/status", nil, nil)
		if recorder.Code == http.StatusTooManyRequests {
			limited = recorder
			break
		}
	}
	if limited == nil {
		t.Fatal("read rate limit never engaged")
	}
	if limited.Header().Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}
	if problem := decodeAdminProblem(t, limited); problem.Code != "rate-limited" {
		t.Fatalf("code = %q", problem.Code)
	}
	other := harness.do(t, http.MethodGet, "/admin/v1/config/status", nil, map[string]string{"X-Test-Principal": "adminkey:other"})
	if other.Code == http.StatusTooManyRequests {
		t.Fatal("a different identity inherited the first identity's limit")
	}
}

// TestAdminV1HandlerErrorsAreNotRewritten proves a handler's own error response is
// passed through untouched for legacy byte-compatibility, and that the mount does
// not invent a status.
func TestAdminV1HandlerPassesThroughHandlerStatus(t *testing.T) {
	harness := newAdminV1Harness(t)
	recorder := harness.do(t, http.MethodGet, "/admin/v1/config/status", nil, nil)
	recorder = harness.do(t, http.MethodGet, "/admin/v1/config/groups/g1", nil, nil)
	// File mode with a nil live config returns the handler's own conflict.
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want the handler's 409 (%s)", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control = %q", got)
	}
}

// TestAdminV1DeprecationHeadersAtHTTP proves the deprecation headers reach a client
// for a deprecated operation without changing its body.
func TestAdminV1DeprecationHeadersAtHTTP(t *testing.T) {
	since := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	sunset := since.Add(100 * 24 * time.Hour)
	registry, err := NewAdminV1Registry([]Operation{{
		ID: "deprecatedFixture", Method: "GET", Path: "/admin/v1/config/status",
		Object: authzObjectAdminConfig, Action: authzActionRead, Domain: AdminDomainGlobal,
		RateClass: AdminRateClassRead, HandlerKey: "fixture.deprecated",
		Deprecated: &AdminDeprecation{Since: since, Sunset: sunset, Link: "https://example.invalid/docs"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	err = RegisterAdminV1(mux, AdminV1Deps{
		Registry: registry,
		Authenticate: &fakeAuthenticator{principal: Principal{
			Subject: "adminkey:test", Kind: "adminkey",
		}},
		Authorize: func(context.Context, Principal, string, string, string) error { return nil },
		Handlers: map[string]http.HandlerFunc{
			"fixture.deprecated": func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			},
		},
		Limiter:          NewAdminRateLimiter(AdminRateLimitConfig{Enabled: boolPointer(false)}),
		Throttle:         NewAdminAuthThrottle(AdminAuthFailureThrottleConfig{Enabled: boolPointer(false)}),
		Denials:          &recordingDenialSink{},
		CredentialEvents: &recordingCredentialSink{},
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/v1/config/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if recorder.Header().Get("Deprecation") == "" || recorder.Header().Get("Sunset") == "" {
		t.Fatalf("deprecation headers missing: %v", recorder.Header())
	}
	if !strings.Contains(recorder.Body.String(), `"ok":true`) {
		t.Fatalf("body changed: %s", recorder.Body.String())
	}
}

// TestAdminV1MountRejectsUnboundOperation proves the mount refuses to serve a
// registry entry without a handler, so a route can never be mounted ungoverned.
func TestAdminV1MountRejectsUnboundOperation(t *testing.T) {
	registry, err := NewAdminV1Registry([]Operation{{
		ID: "orphan", Method: "GET", Path: "/admin/v1/config/status",
		Object: authzObjectAdminConfig, Action: authzActionRead, Domain: AdminDomainGlobal,
		RateClass: AdminRateClassRead, HandlerKey: "missing.handler",
	}})
	if err != nil {
		t.Fatal(err)
	}
	err = RegisterAdminV1(http.NewServeMux(), AdminV1Deps{
		Registry:     registry,
		Authenticate: &fakeAuthenticator{},
		Authorize:    func(context.Context, Principal, string, string, string) error { return nil },
	})
	if err == nil {
		t.Fatal("mount accepted an operation without a handler")
	}
}

// newAdminV1HarnessWithRegistry rebuilds the harness mount with a custom registry.
func newAdminV1HarnessWithRegistry(t *testing.T, registry *AdminV1Registry) *adminV1Harness {
	t.Helper()
	harness := newAdminV1Harness(t)
	harness.registry = registry
	harness.mount(t)
	return harness
}

// buildDeps composes the mount dependencies (config + caller + extra handlers)
// and applies an optional mutator, so every harness mount shares one binding path.
func (h *adminV1Harness) buildDeps(t *testing.T, extra map[string]http.HandlerFunc, mutate func(*AdminV1Deps)) AdminV1Deps {
	t.Helper()
	configDeps := ConfigAdminDeps{
		RuntimeScope: "prod", FileMode: true, Driver: "sqlite", InstanceID: "replica-test",
		CurrentConfig:  func() *Config { return nil },
		LoadedRevision: func() (int64, string, time.Time) { return 7, "set-7", time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC) },
	}
	principalOf := func(r *http.Request) (Principal, string, bool) {
		principal, ok := PrincipalFrom(r.Context())
		if !ok {
			return Principal{}, "", false
		}
		return principal, adminGlobalDomain, true
	}
	authorize := func(principal Principal, domain, object, action string) bool {
		return h.authorize.Authorize(adminTestBackgroundContext, principal, domain, object, action) == nil
	}
	all := addReportPlaceholderHandlers(h.registry, map[string]http.HandlerFunc{
		"training.export":         func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"training.outcomes.label": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"metrics.scrape":          func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("router_requests_total 1\n")) },
		"content.captures.delete": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"content.captures.purge":  func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"test.count":              adminV1TestCountHandler,
		"test.race":               func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
	})
	for key, handler := range extra {
		all[key] = handler
	}
	handlers := AdminV1Handlers(
		AdminV1ConfigAdminDeps(configDeps, principalOf, authorize),
		AdminV1CallerAdminDeps(CallerAdminDeps{}, principalOf, authorize),
		all,
	)
	deps := h.depsForMount(handlers)
	if mutate != nil {
		mutate(&deps)
	}
	return deps
}

// mountWithExtra reinstalls the mount with additional handler bindings.
func (h *adminV1Harness) mountWithExtra(t *testing.T, extra map[string]http.HandlerFunc) *adminV1Harness {
	t.Helper()
	return h.mountWithDeps(t, extra, nil)
}

// mountWithDeps reinstalls the mount with a deps mutator, which is how a test
// exercises a degraded seam (for example an unavailable idempotency store).
func (h *adminV1Harness) mountWithDeps(t *testing.T, extra map[string]http.HandlerFunc, mutate func(*AdminV1Deps)) *adminV1Harness {
	t.Helper()
	mux := http.NewServeMux()
	if err := RegisterAdminV1(mux, h.buildDeps(t, extra, mutate)); err != nil {
		t.Fatalf("mount /admin/v1: %v", err)
	}
	h.mux = mux
	return h
}

// mount (re)installs the mount on a fresh mux with the harness's current seams.
func (h *adminV1Harness) mount(t *testing.T) *adminV1Harness {
	t.Helper()
	return h.mountWithDeps(t, nil, nil)
}

func (h *adminV1Harness) depsForMount(handlers map[string]http.HandlerFunc) AdminV1Deps {
	return AdminV1Deps{
		Registry:     h.registry,
		Authenticate: h.authenticate,
		Authorize: func(ctx context.Context, principal Principal, domain, object, action string) error {
			return h.authorize.Authorize(ctx, principal, domain, object, action)
		},
		Handlers:            handlers,
		ResolveDomain:       func(Principal) string { return adminGlobalDomain },
		ResolveCallerDomain: func(r *http.Request) (string, error) { return "example/prod", nil },
		ResolveCapturedDomains: func(r *http.Request) ([]string, error) {
			if r.PathValue("request_id") == "unknown-domain" {
				return []string{contentCaptureUnknownDomain}, nil
			}
			return []string{"example/prod"}, nil
		},
		CurrentETag:      func(r *http.Request) (string, int64, error) { return h.currentETag(r) },
		CursorCodec:      h.codec,
		Pagination:       AdminPaginationConfig{},
		Idempotency:      h.idempotency,
		IdempotencyOn:    true,
		Limiter:          h.limiter,
		Throttle:         h.throttle,
		Denials:          &recordingDenialSink{},
		CredentialEvents: &recordingCredentialSink{},
		Clock:            func() time.Time { return h.now },
	}
}

var _ = fmt.Sprintf

// TestAdminV1ReportsAdapterReusesLegacyBranch proves a reports operation reaches
// the existing reports handler with its legacy path, so the handler's own branch
// selection, per-domain authorization and pagination are reused verbatim.
func TestAdminV1ReportsAdapterReusesLegacyBranch(t *testing.T) {
	registry, err := AdminV1DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	var seenPaths []string
	var mu sync.Mutex
	reportsHandler := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenPaths = append(seenPaths, r.URL.Path)
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
	reports := AdminV1ReportsHandlers(registry, reportsHandler)
	if len(reports) != 30 {
		t.Fatalf("bound %d reports operations, want 30", len(reports))
	}
	extra := map[string]http.HandlerFunc{}
	for key, handler := range reports {
		extra[key] = handler
	}
	harness := newAdminV1Harness(t)
	harness.registry = registry
	harness.mountWithExtra(t, extra)

	for _, op := range registry.Operations() {
		if op.Adapter != "reports" {
			continue
		}
		target := strings.ReplaceAll(op.Path, "{request_id}", "req-1")
		recorder := harness.do(t, op.Method, target, nil, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("operation %s status = %d (%s)", op.ID, recorder.Code, recorder.Body.String())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for i, op := range registry.Operations() {
		if op.Adapter != "reports" {
			continue
		}
		_ = i
	}
	if len(seenPaths) != len(reports) {
		t.Fatalf("reports handler saw %d requests, want %d", len(seenPaths), len(reports))
	}
	expectations := map[string]bool{}
	for _, op := range registry.Operations() {
		if op.Adapter == "reports" {
			expectations[op.LegacyPath] = true
		}
	}
	for _, path := range seenPaths {
		if !expectations[path] {
			t.Fatalf("reports handler saw unexpected path %q", path)
		}
	}
}

// TestAdminV1ServiceHandlerComposition proves the composed binding covers every
// registered operation, so the mount can never be missing a handler at startup.
func TestAdminV1ServiceHandlerComposition(t *testing.T) {
	registry, err := AdminV1DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	noop := func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) }
	handlers := AdminV1ServiceHandlers(
		registry,
		ConfigAdminDeps{RuntimeScope: "prod", FileMode: true},
		CallerAdminDeps{},
		noop,
		map[string]http.HandlerFunc{
			"training.export":         noop,
			"training.outcomes.label": noop,
			"metrics.scrape":          noop,
			"content.captures.delete": noop,
			"content.captures.purge":  noop,
		},
	)
	for _, op := range registry.Operations() {
		if handlers[op.HandlerKey] == nil {
			t.Fatalf("operation %s (%s) has no bound handler", op.ID, op.HandlerKey)
		}
	}
	if len(handlers) != len(registry.Operations()) {
		t.Fatalf("bound %d handlers for %d operations", len(handlers), len(registry.Operations()))
	}
}

// TestAdminV1KindIsNeverADecision proves the contract layer passes the principal
// through unchanged and never inspects its Kind for a decision: swapping Kind changes
// no authorization argument, so a lookalike cannot select another identity class,
// while the handler still sees the published principal.
func TestAdminV1KindIsNeverADecision(t *testing.T) {
	registry, err := NewAdminV1Registry([]Operation{{
		ID: "kindFixture", Method: "GET", Path: "/admin/v1/tests/kind",
		Object: authzObjectAdminConfig, Action: authzActionRead, Domain: AdminDomainGlobal,
		RateClass: AdminRateClassRead, HandlerKey: "test.kind",
	}})
	if err != nil {
		t.Fatal(err)
	}
	run := func(kind string) []fakeAuthorizerCall {
		authorizer := &fakeAuthorizer{deny: map[string]bool{}}
		mux := http.NewServeMux()
		err := RegisterAdminV1(mux, AdminV1Deps{
			Registry: registry,
			Authenticate: &fakeAuthenticator{principal: Principal{
				Subject: "adminkey:same", Kind: kind, CredentialID: "cred-1", Issuer: "iss",
			}},
			Authorize: func(ctx context.Context, principal Principal, domain, object, action string) error {
				return authorizer.Authorize(ctx, principal, domain, object, action)
			},
			Handlers: map[string]http.HandlerFunc{
				"test.kind": func(w http.ResponseWriter, r *http.Request) {
					principal, ok := PrincipalFrom(r.Context())
					if !ok || principal.Subject != "adminkey:same" {
						t.Errorf("handler did not see the published principal: %+v", principal)
					}
					writeJSON(w, http.StatusOK, map[string]any{"kind": principal.Kind})
				},
			},
			ResolveDomain:    func(Principal) string { return AdminGlobalDomain },
			Denials:          &recordingDenialSink{},
			CredentialEvents: &recordingCredentialSink{},
			Limiter:          NewAdminRateLimiter(AdminRateLimitConfig{Enabled: boolPointer(false)}),
			Throttle:         NewAdminAuthThrottle(AdminAuthFailureThrottleConfig{Enabled: boolPointer(false)}),
		})
		if err != nil {
			t.Fatal(err)
		}
		mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/admin/v1/tests/kind", nil))
		return authorizer.snapshot()
	}
	asAdminKey := run("adminkey")
	asEmergency := run("emergency")
	asMTLS := run("mtls")
	for _, calls := range [][]fakeAuthorizerCall{asAdminKey, asEmergency, asMTLS} {
		if len(calls) != 1 {
			t.Fatalf("expected one decision, got %+v", calls)
		}
	}
	base := asAdminKey[0]
	for _, other := range []fakeAuthorizerCall{asEmergency[0], asMTLS[0]} {
		if other.Domain != base.Domain || other.Object != base.Object || other.Action != base.Action ||
			other.Principal.Subject != base.Principal.Subject {
			t.Fatalf("changing Kind changed the authorization request: %+v vs %+v", base, other)
		}
	}
}

// TestAdminV1PrincipalFromIsTheOnlyIdentitySource proves the mount never reads an
// authorization credential itself: with an authenticator that reports no credential, a
// valid-looking bearer header and a forged cookie still yield 401 and no decision.
func TestAdminV1PrincipalFromIsTheOnlyIdentitySource(t *testing.T) {
	harness := newAdminV1Harness(t)
	harness.authenticate.err = ErrNoCredential
	harness.mount(t)
	recorder := harness.do(t, http.MethodGet, "/admin/v1/config/status", nil, map[string]string{
		"Authorization": "Bearer rtr_admin_abc_secret",
		"Cookie":        "admin_session=forged",
	})
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if len(harness.authorize.snapshot()) != 0 {
		t.Fatal("an unauthenticated request reached the authorizer")
	}
	if problem := decodeAdminProblem(t, recorder); problem.Code != "authentication_required" {
		t.Fatalf("code = %q", problem.Code)
	}
}

// TestAdminV1CursorWalkReturnsEachRowOnce proves a full cursor walk over a paginated
// collection returns every row exactly once, and that limit=501 is rejected before
// the handler runs (ACP-013/014).
func TestAdminV1CursorWalkReturnsEachRowOnce(t *testing.T) {
	harness := newAdminAuditHarness(t)

	// Seed more sets than one page holds, then walk with limit=5.
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("walk-set-%02d", i)
		doc := strings.Replace(configAdminTestYAML, "server:", "server:\n  # "+name, 1)
		if _, err := ImportCanonicalYAML(harness.db, "prod", name, []byte(doc), "test-seed"); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	seen := map[string]int{}
	target := "/admin/v1/config/sets?limit=5&sort=created_at&direction=asc"
	for page := 0; page < 20; page++ {
		recorder := harness.do(t, http.MethodGet, target, nil, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("page %d status = %d (%s)", page, recorder.Code, recorder.Body.String())
		}
		var body struct {
			Sets []struct {
				SetID string `json:"set_id"`
			} `json:"sets"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Sets) > 5 {
			t.Fatalf("page %d returned %d rows, want at most 5", page, len(body.Sets))
		}
		if len(body.Sets) == 0 {
			break
		}
		for _, set := range body.Sets {
			seen[set.SetID]++
		}
		link := recorder.Header().Get("Link")
		if link == "" {
			break
		}
		start := strings.Index(link, "<")
		end := strings.Index(link, ">")
		if start < 0 || end <= start {
			t.Fatalf("malformed Link header %q", link)
		}
		target = link[start+1 : end]
	}
	if len(seen) < 13 {
		t.Fatalf("walk saw %d sets, want at least 13", len(seen))
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("set %s appeared %d times", id, count)
		}
	}

	recorder := harness.do(t, http.MethodGet, "/admin/v1/config/sets?limit=501", nil, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("limit=501 status = %d, want 400", recorder.Code)
	}
	if problem := decodeAdminProblem(t, recorder); problem.Code != "invalid-limit" {
		t.Fatalf("code = %q", problem.Code)
	}
}

// TestAdminV1MountRequiresAuditSinks proves a mutating surface refuses to mount
// without the durable sinks, so an unauditable admin write cannot exist.
func TestAdminV1MountRequiresAuditSinks(t *testing.T) {
	registry, err := AdminV1DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	deps := AdminV1Deps{
		Registry:         registry,
		Authenticate:     &fakeAuthenticator{principal: Principal{Subject: "adminkey:x", Kind: "adminkey"}},
		Authorize:        func(context.Context, Principal, string, string, string) error { return nil },
		Handlers:         addReportPlaceholderHandlers(registry, nil),
		CredentialEvents: &recordingCredentialSink{},
	}
	if err := RegisterAdminV1(http.NewServeMux(), deps); err == nil {
		t.Fatal("mount accepted a mutating surface without a denial sink")
	}
	deps.Denials = &recordingDenialSink{}
	deps.CredentialEvents = nil
	if err := RegisterAdminV1(http.NewServeMux(), deps); err == nil {
		t.Fatal("mount accepted a surface without a credential event sink")
	}
}

// TestAdminV1ThrottleSlowsFailedAttempts proves the authentication throttle is
// consulted before the credential is validated, so repeated failures are actually
// slowed down (and a success clears the state).
func TestAdminV1ThrottleSlowsFailedAttempts(t *testing.T) {
	harness := newAdminV1Harness(t)
	harness.throttle = NewAdminAuthThrottle(AdminAuthFailureThrottleConfig{
		Enabled: boolPointer(true), Window: "5m", FreeAttempts: 1, BaseDelay: "1s", MaxDelay: "1s",
	})
	harness.authenticate.err = ErrInvalidCredential
	harness.mount(t)

	// The first failure is free; the second arms the cooldown; the third is refused.
	for attempt := 1; attempt <= 2; attempt++ {
		recorder := harness.do(t, http.MethodGet, "/admin/v1/config/status", nil, map[string]string{"Authorization": "Bearer rtr_admin_abc_secret"})
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401", attempt, recorder.Code)
		}
	}
	throttled := harness.do(t, http.MethodGet, "/admin/v1/config/status", nil, map[string]string{"Authorization": "Bearer rtr_admin_abc_secret"})
	if throttled.Code != http.StatusTooManyRequests {
		t.Fatalf("third attempt status = %d, want 429 (throttled)", throttled.Code)
	}
	if throttled.Header().Get("Retry-After") == "" {
		t.Fatal("throttled response without Retry-After")
	}
	if problem := decodeAdminProblem(t, throttled); problem.Code != "authentication-throttled" {
		t.Fatalf("code = %q", problem.Code)
	}

	// A validated success clears the cooldown.
	harness.authenticate.err = nil
	// The mount calls this with the claimed credential and the source address on the
	// first successful validation.
	harness.throttle.RecordSuccess("adminkey:abc", "192.0.2.1")
	after := harness.do(t, http.MethodGet, "/admin/v1/config/status", nil, map[string]string{"Authorization": "Bearer rtr_admin_abc_secret"})
	if after.Code == http.StatusTooManyRequests {
		t.Fatalf("a cleared throttle still refused the request: %d", after.Code)
	}
}

// TestAdminV1LegacyErrorRewrittenAsProblem proves a handler's legacy error envelope
// is rendered as a problem document on the v1 surface.
func TestAdminV1LegacyErrorRewrittenAsProblem(t *testing.T) {
	harness := newAdminV1Harness(t)
	registry, err := NewAdminV1Registry([]Operation{{
		ID: "legacyErrorFixture", Method: "GET", Path: "/admin/v1/tests/legacy-error",
		Object: authzObjectAdminConfig, Action: authzActionRead, Domain: AdminDomainGlobal,
		RateClass: AdminRateClassRead, HandlerKey: "test.legacy_error", Errors: []int{409},
	}})
	if err != nil {
		t.Fatal(err)
	}
	harness.registry = registry
	harness.mountWithDeps(t, map[string]http.HandlerFunc{
		"test.legacy_error": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{"type": "config-no-active-set", "message": "no active set"}})
		},
	}, nil)
	recorder := harness.do(t, http.MethodGet, "/admin/v1/tests/legacy-error", nil, nil)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("content type = %q, want application/problem+json", got)
	}
	problem := decodeAdminProblem(t, recorder)
	if problem.Code != "config-no-active-set" || problem.Status != http.StatusConflict {
		t.Fatalf("problem = %+v", problem)
	}
	if !strings.Contains(problem.Detail, "no active set") {
		t.Fatalf("detail = %q", problem.Detail)
	}
}

// TestAdminV1SensitiveResponseNotRecorded proves a credential-bearing response is
// never stored for replay: a retry answers "in doubt" rather than returning the token
// a second time.
func TestAdminV1SensitiveResponseNotRecorded(t *testing.T) {
	harness := newAdminV1Harness(t)
	harness.mountWithDeps(t, map[string]http.HandlerFunc{
		"callers.issue": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusCreated, map[string]any{"token": "rtr_metrum_once_only"})
		},
		"callers.rotate": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"token": "rtr_metrum_once_only"})
		},
	}, nil)
	headers := map[string]string{"Idempotency-Key": "issue-key", "If-Match": `"rev-7"`}
	first := harness.do(t, http.MethodPost, "/admin/v1/callers/issue", map[string]any{"owner_user": "u", "project": "p", "environment": "e", "key_slug": "k", "allow": []string{}}, headers)
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d (%s)", first.Code, first.Body.String())
	}
	second := harness.do(t, http.MethodPost, "/admin/v1/callers/issue", map[string]any{"owner_user": "u", "project": "p", "environment": "e", "key_slug": "k", "allow": []string{}}, headers)
	if strings.Contains(second.Body.String(), "rtr_metrum_once_only") {
		t.Fatalf("the credential was replayed: %s", second.Body.String())
	}
	if second.Code != http.StatusConflict {
		t.Fatalf("replay status = %d, want 409 in-doubt", second.Code)
	}
}

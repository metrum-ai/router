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

	"gorm.io/gorm"
	"time"
)

// ---------------------------------------------------------------------------
// Test harness: the /admin/v1 mount with fake #814/#812 seams
// ---------------------------------------------------------------------------

// fakeAuthenticator resolves the principal from a test header. It is a test-only
// stand-in for the #814 chain.
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
		return principal, AdminGlobalDomain, true
	}
	authorize := func(ctx context.Context, principal Principal, domain, object, action string) error {
		return harness.authorize.Authorize(ctx, principal, domain, object, action)
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
			return AdminGlobalDomain
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
		CurrentETag:   func(r *http.Request) (string, int64, error) { return harness.currentETag(r) },
		CursorCodec:   harness.codec,
		Pagination:    AdminPaginationConfig{},
		Idempotency:   harness.idempotency,
		IdempotencyOn: true,
		Limiter:       harness.limiter,
		Throttle:      harness.throttle,
		Clock:         func() time.Time { return harness.now },
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

// TestAdminV1SingleDecisionPoint proves every operation takes exactly the decisions it
// declares, that each decision was taken in the operation's resolved domain, and that the
// handler-decided surfaces (reports, the caller listing) take no contract-layer gate.
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
		if op.ID == "getAdminReportRequestEvidence" {
			target += "?request_id=req-1"
		}
		harness.do(t, op.Method, target, map[string]any{}, map[string]string{"If-Match": `"rev-7"`})
		calls := harness.authorize.snapshot()

		// The expected decision set per operation: the primary guard plus the checks that
		// apply to this request's body, or none for the handler-decided surfaces.
		var want []fakeAuthorizerCall
		if op.Domain == AdminDomainHandler {
			// Reports decide per request inside the handler (asserted by the reports test);
			// the contract layer takes no gate of its own.
			want = nil
		} else if op.Domain == AdminDomainCallerTarget && op.Method == "GET" {
			// The caller listing takes its operation guard in the handler and then filters
			// each row in that row's own stored domain (asserted by the DB-backed test).
			want = []fakeAuthorizerCall{{Domain: AdminGlobalDomain, Object: authzObjectAdminCallers, Action: authzActionList}}
		} else {
			domain := AdminGlobalDomain
			switch op.Domain {
			case AdminDomainCallerTarget, AdminDomainCapturedRow:
				// These operations resolve a concrete stored domain.
				switch op.ID {
				case "issueCaller", "rotateCaller", "revokeCaller", "deleteContentCapture":
					domain = "example/prod"
				}
			}
			want = append(want, fakeAuthorizerCall{Domain: domain, Object: op.Object, Action: op.Action})
			for _, check := range op.ChecksFor(map[string]any{}) {
				want = append(want, fakeAuthorizerCall{Domain: domain, Object: check.Object, Action: check.Action})
			}
		}
		if len(calls) != len(want) {
			t.Fatalf("operation %s took %d decisions, want %d: got %+v want %+v", op.ID, len(calls), len(want), calls, want)
		}
		for i, expected := range want {
			if calls[i].Object != expected.Object || calls[i].Action != expected.Action {
				t.Fatalf("operation %s decision %d = %s/%s, want %s/%s", op.ID, i, calls[i].Object, calls[i].Action, expected.Object, expected.Action)
			}
			// Every decision's domain is asserted, including the caller-target and
			// captured-row ones, which must be the concrete stored domain.
			if calls[i].Domain != expected.Domain {
				t.Fatalf("operation %s decision %d domain = %q, want %q", op.ID, i, calls[i].Domain, expected.Domain)
			}
			if calls[i].Principal.Subject != "adminkey:test" {
				t.Fatalf("operation %s decided for %q", op.ID, calls[i].Principal.Subject)
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
		t.Fatalf("a captured row without a domain must require the plane-global grant: %+v", calls)
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
		Limiter:  NewAdminRateLimiter(AdminRateLimitConfig{Enabled: boolPointer(false)}),
		Throttle: NewAdminAuthThrottle(AdminAuthFailureThrottleConfig{Enabled: boolPointer(false)}),
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
		return principal, AdminGlobalDomain, true
	}
	authorize := func(ctx context.Context, principal Principal, domain, object, action string) error {
		return h.authorize.Authorize(ctx, principal, domain, object, action)
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
		ResolveDomain:       func(Principal) string { return AdminGlobalDomain },
		ResolveCallerDomain: func(r *http.Request) (string, error) { return "example/prod", nil },
		ResolveCapturedDomains: func(r *http.Request) ([]string, error) {
			if r.PathValue("request_id") == "unknown-domain" {
				return []string{contentCaptureUnknownDomain}, nil
			}
			return []string{"example/prod"}, nil
		},
		CurrentETag:   func(r *http.Request) (string, int64, error) { return h.currentETag(r) },
		CursorCodec:   h.codec,
		Pagination:    AdminPaginationConfig{},
		Idempotency:   h.idempotency,
		IdempotencyOn: true,
		Limiter:       h.limiter,
		Throttle:      h.throttle,
		Clock:         func() time.Time { return h.now },
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
		if op.ID == "getAdminReportRequestEvidence" {
			target += "?request_id=req-1"
		}
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
			ResolveDomain: func(Principal) string { return AdminGlobalDomain },
			Limiter:       NewAdminRateLimiter(AdminRateLimitConfig{Enabled: boolPointer(false)}),
			Throttle:      NewAdminAuthThrottle(AdminAuthFailureThrottleConfig{Enabled: boolPointer(false)}),
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
	harness := newAdminV1DBHarness(t)

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

// TestAdminV1ReportsDecideThroughAuthorize drives the REAL reports handler for EVERY reports
// operation on the /admin/v1 mount: identity comes from the published principal (no legacy
// credential check) and each operation's decision goes through the single Authorize seam
// with its object, action and concrete domain.
func TestAdminV1ReportsDecideThroughAuthorize(t *testing.T) {
	registry, err := AdminV1DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	reportOps := 0
	for _, op := range registry.Operations() {
		if op.Adapter != "reports" {
			continue
		}
		reportOps++
		target := strings.ReplaceAll(op.Path, "{request_id}", "req-1")
		if op.ID == "getAdminReportRequestEvidence" {
			target += "?request_id=req-1"
		}
		counting := &fakeAuthorizer{deny: map[string]bool{}}
		service := &Service{cfg: &Config{Server: ServerConfig{AdminReports: AdminReportsConfig{
			Enabled: true, PathPrefix: "/admin/reports",
		}}}, authorizer: countingServingAuthorizer(t)}
		// Record every decision the real Authorize seam takes.
		service.authorizationAudit = func(ctx context.Context, principal Principal, domain, object, action string, decision error) error {
			counting.Authorize(ctx, principal, domain, object, action)
			return nil
		}
		harness := newAdminV1Harness(t)
		harness.registry = registry
		extra := map[string]http.HandlerFunc{}
		for key, handler := range AdminV1ReportsHandlers(registry, service.handleAdminReports) {
			extra[key] = handler
		}
		harness.mountWithExtra(t, extra)
		recorder := harness.do(t, http.MethodGet, target, nil, nil)
		if recorder.Code == http.StatusUnauthorized {
			t.Fatalf("operation %s returned 401: reports must not run the legacy credential check (%s)", op.ID, recorder.Body.String())
		}
		calls := counting.snapshot()
		if len(calls) == 0 {
			t.Fatalf("operation %s took no decision through the Authorize seam", op.ID)
		}
		for _, call := range calls {
			if call.Principal.Subject != "adminkey:test" {
				t.Fatalf("operation %s decision principal = %+v", op.ID, call.Principal)
			}
			if call.Domain != AdminGlobalDomain {
				t.Fatalf("operation %s decision domain = %q, want the plane-global %q", op.ID, call.Domain, AdminGlobalDomain)
			}
			if call.Object != op.Object {
				t.Fatalf("operation %s decided object %q, want %q", op.ID, call.Object, op.Object)
			}
			actions := map[string]bool{op.Action: true, authzActionRead: true, authzActionExport: true, authzActionDrilldown: true}
			if !actions[call.Action] {
				t.Fatalf("operation %s decided action %q, which is not one of its declared actions", op.ID, call.Action)
			}
			if strings.HasSuffix(op.Path, "/export.csv") || strings.HasSuffix(op.Path, "/export.md") {
				if call.Action != authzActionExport {
					t.Fatalf("export operation %s decided action %q, want export", op.ID, call.Action)
				}
			}
			if op.Action == authzActionDrilldown && call.Action != authzActionDrilldown {
				t.Fatalf("drilldown operation %s decided action %q", op.ID, call.Action)
			}
		}
	}
	if reportOps != 30 {
		t.Fatalf("exercised %d reports operations, want 30", reportOps)
	}
}

// countingServingAuthorizer wraps a casbin authorizer so every decision the real reports
// handler takes is recorded with its principal, domain, object and action.
func countingServingAuthorizer(t *testing.T) *authorizer {
	t.Helper()
	enforcer, err := newAuthzEnforcer()
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range []string{authzObjectAdminReports, authzObjectSecurityReports} {
		for _, action := range []string{authzActionRead, authzActionExport, authzActionDrilldown} {
			if _, err := enforcer.AddPolicy("adminkey:test", AdminGlobalDomain, object, action); err != nil {
				t.Fatal(err)
			}
		}
	}
	return &authorizer{enforcer: enforcer}
}

// fakeServingAuthorizer builds the casbin authorizer the real reports handler decides
// through: an allowing policy when allow is true, an empty (denying) one otherwise.
func fakeServingAuthorizer(t *testing.T, allow bool) *authorizer {
	t.Helper()
	enforcer, err := newAuthzEnforcer()
	if err != nil {
		t.Fatal(err)
	}
	if allow {
		for _, object := range []string{authzObjectAdminReports, authzObjectSecurityReports} {
			for _, action := range []string{authzActionRead, authzActionExport, authzActionDrilldown} {
				if _, err := enforcer.AddPolicy("adminkey:test", AdminGlobalDomain, object, action); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	return &authorizer{enforcer: enforcer}
}

// TestAdminV1SensitiveResponseNeverStored proves the mount never persists a
// credential-bearing response: the idempotency row holds metadata only, and a retry
// answers "in doubt" (the one-time secret is unavailable) instead of replaying the token.
func TestAdminV1SensitiveResponseNeverStored(t *testing.T) {
	token := "rtr_" + "metrum_" + "9a8b7c6d5e4f3a2b1c0d"
	harness := newAdminV1DBHarnessWith(t, map[string]http.HandlerFunc{
		"callers.issue": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusCreated, map[string]any{"token": token})
		},
	})
	headers := map[string]string{
		"Idempotency-Key": "issue-once",
		"If-Match":        AdminETagForRevision(1),
	}
	first := harness.do(t, http.MethodPost, "/admin/v1/callers/issue", map[string]any{"owner_user": "u", "project": "p", "environment": "e", "key_slug": "k", "allow": []string{}}, headers)
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d (%s)", first.Code, first.Body.String())
	}
	if !strings.Contains(first.Body.String(), token) {
		t.Fatalf("the first response did not carry the credential: %s", first.Body.String())
	}
	var stored []struct {
		Body  string `gorm:"column:response_body"`
		State string `gorm:"column:state"`
	}
	if err := harness.db.Table(adminIdempotencyTable).Find(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored %d idempotency rows, want 1", len(stored))
	}
	if strings.Contains(stored[0].Body, token) || stored[0].Body != "" {
		t.Fatalf("the credential reached the store: %q", stored[0].Body)
	}
	if stored[0].State != "completed_unrecorded" {
		t.Fatalf("state = %q, want completed_unrecorded", stored[0].State)
	}
	second := harness.do(t, http.MethodPost, "/admin/v1/callers/issue", map[string]any{"owner_user": "u", "project": "p", "environment": "e", "key_slug": "k", "allow": []string{}}, headers)
	if strings.Contains(second.Body.String(), token) {
		t.Fatalf("the credential was replayed: %s", second.Body.String())
	}
	if second.Code != http.StatusConflict {
		t.Fatalf("retry status = %d, want 409 in-doubt (%s)", second.Code, second.Body.String())
	}
}

// adminV1DBHarness mounts the /admin/v1 config operations over a real control-plane
// database with test-only authentication and authorization seams.
type adminV1DBHarness struct {
	db        *gorm.DB
	mux       *http.ServeMux
	authorize *fakeAuthorizer
}

func newAdminV1DBHarness(t *testing.T) *adminV1DBHarness {
	t.Helper()
	return newAdminV1DBHarnessWith(t, nil)
}

// newAdminV1DBHarnessWith mounts the DB harness with extra handler bindings.
func newAdminV1DBHarnessWith(t *testing.T, extraHandlers map[string]http.HandlerFunc) *adminV1DBHarness {
	t.Helper()
	db := configAdminTestDB(t)
	harness := &adminV1DBHarness{db: db, mux: http.NewServeMux(), authorize: &fakeAuthorizer{deny: map[string]bool{}}}
	registry, err := AdminV1DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	principalOf := func(r *http.Request) (Principal, string, bool) {
		principal, ok := PrincipalFrom(r.Context())
		if !ok {
			return Principal{}, "", false
		}
		return principal, AdminGlobalDomain, true
	}
	authorize := func(ctx context.Context, principal Principal, domain, object, action string) error {
		return harness.authorize.Authorize(ctx, principal, domain, object, action)
	}
	configDeps := AdminV1ConfigAdminDeps(ConfigAdminDeps{
		DB: db, RuntimeScope: "prod", Driver: "sqlite", InstanceID: "replica-test",
		CurrentConfig:  func() *Config { return nil },
		LoadedRevision: func() (int64, string, time.Time) { return 1, "set-1", time.Now().UTC() },
	}, principalOf, authorize)
	extra := addReportPlaceholderHandlers(registry, map[string]http.HandlerFunc{
		"training.export":         func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"training.outcomes.label": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"metrics.scrape":          func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"content.captures.delete": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"content.captures.purge":  func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"test.count":              adminV1TestCountHandler,
		"test.race":               func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
	})
	for key, handler := range extraHandlers {
		extra[key] = handler
	}
	handlers := AdminV1Handlers(
		configDeps,
		AdminV1CallerAdminDeps(CallerAdminDeps{DB: db, RuntimeScope: "prod"}, principalOf, authorize),
		extra,
	)
	err = RegisterAdminV1(harness.mux, AdminV1Deps{
		Registry:     registry,
		Authenticate: headerPrincipalAuthenticator{},
		Authorize: func(ctx context.Context, principal Principal, domain, object, action string) error {
			return harness.authorize.Authorize(ctx, principal, domain, object, action)
		},
		Handlers: handlers,
		ResolveDomain: func(Principal) string {
			return AdminGlobalDomain
		},
		ResolveCallerDomain: func(r *http.Request) (string, error) {
			return AdminV1CallerResourceDomain(db, "prod", r)
		},
		CurrentETag: func(r *http.Request) (string, int64, error) {
			revision, err := currentRevisionForScope(db, "prod")
			return AdminETagForRevision(revision), revision, err
		},
		CursorCodec:   NewAdminCursorCodec([]byte("mount-harness-cursor-key-000001")),
		Idempotency:   newAdminV1DBHarnessIdempotency(t, db),
		IdempotencyOn: true,
		Limiter:       NewAdminRateLimiter(AdminRateLimitConfig{Enabled: boolPointer(false)}),
		Throttle:      NewAdminAuthThrottle(AdminAuthFailureThrottleConfig{Enabled: boolPointer(false)}),
		Clock:         func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	return harness
}

// newAdminV1DBHarnessIdempotency returns a store backed by the harness database so a
// test can inspect the persisted row.
func newAdminV1DBHarnessIdempotency(t *testing.T, db *gorm.DB) AdminIdempotencyKeyStore {
	t.Helper()
	store, err := NewSQLAdminIdempotencyStore(db, AdminIdempotencyConfig{})
	if err != nil {
		t.Fatalf("idempotency store: %v", err)
	}
	return store
}

// headerPrincipalAuthenticator is a test authentication seam that reads a header.
type headerPrincipalAuthenticator struct{}

func (headerPrincipalAuthenticator) Authenticate(_ context.Context, r *http.Request) (Principal, error) {
	subject := strings.TrimSpace(r.Header.Get("X-Test-Principal"))
	if subject == "" {
		return Principal{}, ErrNoCredential
	}
	return Principal{Subject: subject, Kind: "adminkey", CredentialID: "cred-mount", Issuer: "test-issuer"}, nil
}

func (h *adminV1DBHarness) do(t *testing.T, method, target string, body any, headers map[string]string) *httptest.ResponseRecorder {
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
	request.Header.Set("X-Test-Principal", "adminkey:mount")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)
	return recorder
}

// TestAdminV1HandlerSideAuthorizeMapsErrors proves a handler-side decision carries the
// request context and maps its typed errors: a denied permission is 403, a stale or
// unavailable policy is 503, and the decision sees the same principal the contract layer
// published (which the real Authorizer requires).
func TestAdminV1HandlerSideAuthorizeMapsErrors(t *testing.T) {
	registry, err := NewAdminV1Registry([]Operation{{
		ID: "handlerAuthzFixture", Method: "POST", Path: "/admin/v1/tests/handler-authz",
		Object: authzObjectAdminConfig, Action: "write", Domain: AdminDomainGlobal,
		RateClass: AdminRateClassWrite, HandlerKey: "test.handler_authz",
	}})
	if err != nil {
		t.Fatal(err)
	}
	run := func(decisionErr error) *httptest.ResponseRecorder {
		harness := newAdminV1Harness(t)
		harness.registry = registry
		var seenCtx bool
		authorize := func(ctx context.Context, principal Principal, domain, object, action string) error {
			published, ok := PrincipalFrom(ctx)
			seenCtx = ok && published == principal && ctx != context.Background()
			if permission := action; object == "admin" && strings.HasSuffix(permission, "config/force") {
				return decisionErr
			}
			return nil
		}
		harness.mountWithDeps(t, map[string]http.HandlerFunc{
			"test.handler_authz": func(w http.ResponseWriter, r *http.Request) {
				h := newConfigAdminHandler(ConfigAdminDeps{
					Decide: func(w http.ResponseWriter, r *http.Request, subject adminAuthSubject, permission string) bool {
						object, action, _ := adminPermissionObjectAction(permission)
						if err := authorize(r.Context(), *subject.principal, subject.domain, object, action); err != nil {
							status, code := adminAuthorizationProblem(err)
							writeAdminProblemCode(w, r, status, code, "")
							return false
						}
						return true
					},
				})
				subject := adminAuthSubject{principal: ptrPrincipal(r)}
				if !h.authorize(w, r, subject, "admin:config/force") {
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			},
		}, nil)
		recorder := harness.do(t, http.MethodPost, "/admin/v1/tests/handler-authz", map[string]any{}, nil)
		if !seenCtx {
			t.Fatal("the handler-side decision did not receive the request context with the published principal")
		}
		return recorder
	}
	if recorder := run(nil); recorder.Code != http.StatusOK {
		t.Fatalf("allowed status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	denied := run(ErrAdminForbidden)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("denied status = %d", denied.Code)
	}
	stale := run(ErrAuthorizationUnavailable)
	if stale.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status = %d, want 503", stale.Code)
	}
	if problem := decodeAdminProblem(t, stale); problem.Code != "authorization-unavailable" {
		t.Fatalf("code = %q", problem.Code)
	}
}

// opExtraObject marks a handler-side permission that carries its own decision error.
const opExtraObject = "admin:config"

func ptrPrincipal(r *http.Request) *Principal {
	if principal, ok := PrincipalFrom(r.Context()); ok {
		return &principal
	}
	return nil
}

// TestAdminV1CallersListUsesRealResolver drives GET /admin/v1/callers with the production
// resolver: a collection has no single target domain, so the listing must not 404 and the
// handler must decide per row in that row's stored domain.
func TestAdminV1CallersListUsesRealResolver(t *testing.T) {
	harness := newAdminV1DBHarness(t)
	recorder := harness.do(t, http.MethodGet, "/admin/v1/callers", nil, nil)
	if recorder.Code == http.StatusNotFound {
		t.Fatalf("the caller listing 404s with the production resolver: %s", recorder.Body.String())
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Callers []struct {
			ID        string `json:"id"`
			Project   string `json:"project"`
			TokenHash string `json:"token_hash"`
			SHA256    string `json:"token_sha256"`
		} `json:"callers"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Callers) == 0 {
		t.Fatal("the seeded caller is missing from the listing")
	}
	for _, caller := range body.Callers {
		if caller.TokenHash != "" || caller.SHA256 != "" {
			t.Fatalf("the listing returned verifier material for %s", caller.ID)
		}
	}
	// The production resolver must not invent a single target domain for a collection.
	domain, err := AdminV1CallerResourceDomain(harness.db, "prod", httptest.NewRequest(http.MethodGet, "/admin/v1/callers", nil))
	if err != nil || domain != "" {
		t.Fatalf("collection domain = %q/%v, want empty", domain, err)
	}
}

// TestAdminV1ForceIsConditionalAtHTTP proves the force grant is required only when the
// request asks for it: a plain write succeeds with the write grant alone, and a forced
// write without the force grant is refused with a problem document.
func TestAdminV1ForceIsConditionalAtHTTP(t *testing.T) {
	harness := newAdminV1DBHarness(t)
	harness.authorize.deny[AdminGlobalDomain+"|admin:config|force"] = true

	plain := harness.do(t, http.MethodPut, "/admin/v1/config/groups/agent-default/targets", map[string]any{
		"targets": []any{map[string]any{"provider": "alpha", "model_ref": "a-two"}},
	}, map[string]string{"If-Match": harness.etag(t)})
	if plain.Code != http.StatusOK {
		t.Fatalf("a plain write was refused: %d (%s)", plain.Code, plain.Body.String())
	}

	forced := harness.do(t, http.MethodPut, "/admin/v1/config/groups/agent-default/targets", map[string]any{
		"targets": []any{map[string]any{"provider": "alpha", "model_ref": "a-two"}},
		"force":   true,
	}, map[string]string{"If-Match": harness.etag(t)})
	if forced.Code != http.StatusForbidden {
		t.Fatalf("a forced write without the force grant returned %d, want 403 (%s)", forced.Code, forced.Body.String())
	}
	if problem := decodeAdminProblem(t, forced); problem.Code != "admin-forbidden" {
		t.Fatalf("code = %q", problem.Code)
	}
}

// etag returns the current configuration validator for a write.
func (h *adminV1DBHarness) etag(t *testing.T) string {
	t.Helper()
	revision, err := currentRevisionForScope(h.db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	return AdminETagForRevision(revision)
}

// TestAdminV1UnrecordedHandlerGateCallsAuthorizer proves, over HTTP, that a handler gate
// cannot reuse a decision that was not taken. The handler is the REAL config handler with
// the REAL AdminV1ConfigAdminDeps decision seam and the real target-replacement handler: a
// NON-forced write never evaluates force, so the force check inside handleReplaceTargets
// must reach the counting Authorizer.
func TestAdminV1UnrecordedHandlerGateCallsAuthorizer(t *testing.T) {
	var forceCalls int
	var harness *adminV1DBHarness
	db := configAdminTestDB(t)
	harness = &adminV1DBHarness{db: db, mux: http.NewServeMux(), authorize: &fakeAuthorizer{deny: map[string]bool{}}}
	registry, err := AdminV1DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	principalOf := func(r *http.Request) (Principal, string, bool) {
		principal, ok := PrincipalFrom(r.Context())
		if !ok {
			return Principal{}, "", false
		}
		return principal, AdminGlobalDomain, true
	}
	authorize := func(ctx context.Context, principal Principal, domain, object, action string) error {
		if action == "force" {
			forceCalls++
		}
		return harness.authorize.Authorize(ctx, principal, domain, object, action)
	}
	// The real handler dependencies; the force requirement is the production one
	// (RequireValidationStatus empty, so a forced write needs the force grant).
	configDeps := AdminV1ConfigAdminDeps(ConfigAdminDeps{
		DB: db, RuntimeScope: "prod", Driver: "sqlite", InstanceID: "replica-test",
		CurrentConfig:  func() *Config { return nil },
		LoadedRevision: func() (int64, string, time.Time) { return 1, "set-1", time.Now().UTC() },
	}, principalOf, authorize)
	extra := addReportPlaceholderHandlers(registry, map[string]http.HandlerFunc{
		"training.export":         func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"training.outcomes.label": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"metrics.scrape":          func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"content.captures.delete": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"content.captures.purge":  func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
		"test.count":              adminV1TestCountHandler,
		"test.race":               func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) },
	})
	handlers := AdminV1Handlers(
		configDeps,
		AdminV1CallerAdminDeps(CallerAdminDeps{DB: db, RuntimeScope: "prod"}, principalOf, authorize),
		extra,
	)
	err = RegisterAdminV1(harness.mux, AdminV1Deps{
		Registry:     registry,
		Authenticate: headerPrincipalAuthenticator{},
		Authorize: func(ctx context.Context, principal Principal, domain, object, action string) error {
			return harness.authorize.Authorize(ctx, principal, domain, object, action)
		},
		Handlers: handlers,
		ResolveDomain: func(Principal) string {
			return AdminGlobalDomain
		},
		ResolveCallerDomain: func(r *http.Request) (string, error) {
			return AdminV1CallerResourceDomain(db, "prod", r)
		},
		CurrentETag: func(r *http.Request) (string, int64, error) {
			revision, err := currentRevisionForScope(db, "prod")
			return AdminETagForRevision(revision), revision, err
		},
		CursorCodec:   NewAdminCursorCodec([]byte("mount-harness-cursor-key-000001")),
		Idempotency:   newAdminV1DBHarnessIdempotency(t, db),
		IdempotencyOn: true,
		Limiter:       NewAdminRateLimiter(AdminRateLimitConfig{Enabled: boolPointer(false)}),
		Throttle:      NewAdminAuthThrottle(AdminAuthFailureThrottleConfig{Enabled: boolPointer(false)}),

		Clock: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}

	recorder := harness.do(t, http.MethodPut, "/admin/v1/config/groups/agent-default/targets", map[string]any{
		"targets": []any{map[string]any{"provider": "alpha", "model_ref": "a-two"}},
	}, map[string]string{"If-Match": harness.etag(t)})
	if recorder.Code != http.StatusOK {
		t.Fatalf("plain write status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	for _, call := range harness.authorize.snapshot() {
		if call.Object == authzObjectAdminConfig && call.Action == "force" {
			t.Fatalf("a plain write evaluated force: %+v", call)
		}
	}
	// A forced write on the same handler must gate force through the same seam exactly once:
	// the mount evaluates it for the declared body condition and the handler's own check
	// reuses that recorded decision instead of asking twice.
	harness.authorize.reset()
	forced := harness.do(t, http.MethodPut, "/admin/v1/config/groups/agent-default/targets", map[string]any{
		"targets": []any{map[string]any{"provider": "alpha", "model_ref": "a-two"}},
		"force":   true,
		"dry_run": true,
	}, map[string]string{"If-Match": harness.etag(t)})
	if forced.Code != http.StatusOK {
		t.Fatalf("forced write status = %d (%s)", forced.Code, forced.Body.String())
	}
	forceDecisions := 0
	for _, call := range harness.authorize.snapshot() {
		if call.Object == authzObjectAdminConfig && call.Action == "force" {
			forceDecisions++
			if call.Domain != AdminGlobalDomain {
				t.Fatalf("force decision domain = %q", call.Domain)
			}
		}
	}
	if forceDecisions != 1 {
		t.Fatalf("the forced write took %d force decisions, want exactly 1", forceDecisions)
	}
	if forceCalls != 0 {
		t.Fatalf("the handler repeated a decision the mount already took (%d extra calls)", forceCalls)
	}
}

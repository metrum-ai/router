// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// adminV1Target substitutes the registry path placeholders with concrete values
// so a request can be routed through the mount.
func adminV1Target(op Operation) string {
	target := op.Path
	for _, replacement := range [][2]string{
		{"{group}", "g1"},
		{"{set_id}", "set-1"},
		{"{request_id}", "req-1"},
		{"{id}", "caller-1"},
		{"{target_id...}", "prov/model"},
	} {
		target = strings.ReplaceAll(target, replacement[0], replacement[1])
	}
	return target
}

// freshnessMountService builds a Service whose authorizer grants the operator
// everything and whose authority is a PostgreSQL-dialect handle. The handle is
// only classified, never queried: the gate compares the recorded synchronization
// time with the supplied clock.
func freshnessMountService(t *testing.T, principalSubject string) *Service {
	t.Helper()
	a, err := newAuthorizer(AdminAuthorizationConfig{
		Enabled: true,
		Policy:  []string{"p, " + principalSubject + ", *, *, .*"},
	}, nil, nil, AdminAuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Source: "db"}
	snap := NewConfigSnapshot()
	snap.Store(cfg)
	return &Service{
		authorizer:         a,
		configSnapshot:     snap,
		usage:              &usageStore{db: postgresGateHandle()},
		policyFreshness:    newPolicyFreshnessState(time.Minute),
		authorizationAudit: func(context.Context, Principal, string, string, string, error) error { return nil },
	}
}

// postgresGateHandle classifies the authority as PostgreSQL without connecting:
// the freshness gate only inspects the dialector name.
func postgresGateHandle() *gorm.DB {
	return &gorm.DB{Config: &gorm.Config{Dialector: postgres.New(postgres.Config{})}}
}

// doAs authenticates as the harness authenticator's configured principal: the
// shared do helper always injects the adminkey test header, which would replace
// the mtls/emergency identity these freshness tests need.
func (h *adminV1Harness) doAs(t *testing.T, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(""))
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)
	return recorder
}

// TestAdminV1MountMarksEveryMutatingOperation proves the trusted registry — not a
// header, the request URL or the authentication result — selects mutation mode,
// that marking happens before the decision, and that reads stay unmarked.
func TestAdminV1MountMarksEveryMutatingOperation(t *testing.T) {
	harness := newAdminV1Harness(t)
	var marked bool
	var sawMarker bool
	harness.authorizeOverride = func(ctx context.Context, _ Principal, _, _, _ string) error {
		value, present := ctx.Value(adminPolicyMutationKey{}).(bool)
		marked, sawMarker = present && value, present
		return nil
	}
	mutating, reads := 0, 0
	for _, op := range harness.registry.Operations() {
		marked, sawMarker = false, false
		harness.do(t, op.Method, adminV1Target(op), nil, nil)
		wantMutating := adminOperationMutates(op)
		if wantMutating {
			mutating++
		} else {
			reads++
		}
		if wantMutating != sawMarker {
			t.Fatalf("operation %s (%s): marker present = %v, want %v", op.ID, op.Method, sawMarker, wantMutating)
		}
		if marked != wantMutating {
			t.Fatalf("operation %s (%s): marked = %v, want %v", op.ID, op.Method, marked, wantMutating)
		}
	}
	if mutating == 0 || reads == 0 {
		t.Fatalf("registry has %d mutating and %d read operations; the test must cover both", mutating, reads)
	}
}

// TestAdminV1StalePolicyRefusesEveryMutationThroughMount drives the real
// Service.Authorize behind the mount: with an expired authoritative clock every
// non-GET /admin/v1 operation is refused with 503 policy-stale before its
// handler runs, while every GET still works.
func TestAdminV1StalePolicyRefusesEveryMutationThroughMount(t *testing.T) {
	harness := newAdminV1Harness(t)
	harness.authenticate.principal = Principal{Subject: "operator", Kind: "mtls"}
	svc := freshnessMountService(t, "operator")
	svc.policyFreshness.observeSuccess(4, time.Now().Add(-2*time.Minute))
	harness.authorizeOverride = svc.Authorize

	mutations, reads := 0, 0
	for _, op := range harness.registry.Operations() {
		recorder := harness.doAs(t, op.Method, adminV1Target(op))
		if adminOperationMutates(op) {
			mutations++
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("mutation %s (%s) = %d, want 503 policy-stale", op.ID, op.Method, recorder.Code)
			}
			if problem := decodeAdminProblem(t, recorder); problem.Code != "policy-stale" {
				t.Fatalf("mutation %s code = %q, want policy-stale", op.ID, problem.Code)
			}
			if recorder.Body.String() != "" && strings.Contains(recorder.Body.String(), `"data"`) {
				t.Fatalf("mutation %s produced a handler payload: %s", op.ID, recorder.Body.String())
			}
			continue
		}
		reads++
		// Reads may fail for their own reasons (an unavailable caller directory,
		// an unknown row), but never because the policy clock is old. The
		// freshness gate must not even run: no 503 policy-stale on a read.
		if recorder.Code >= 400 {
			if problem := decodeAdminProblem(t, recorder); problem.Code == "policy-stale" {
				t.Fatalf("read %s (%s) was refused as policy-stale", op.ID, op.Method)
			}
		}
		if recorder.Code == http.StatusServiceUnavailable && strings.Contains(recorder.Body.String(), "policy") {
			t.Fatalf("read %s (%s) = 503 mentioning policy: %s", op.ID, op.Method, recorder.Body.String())
		}
	}
	if mutations == 0 || reads == 0 {
		t.Fatalf("covered %d mutations and %d reads; both are required", mutations, reads)
	}

	// The same authority with a current clock lets the mutations through, so the
	// refusals above are the freshness gate and not an unrelated denial.
	svc.policyFreshness.observeSuccess(4, time.Now())
	for _, op := range harness.registry.Operations() {
		if !adminOperationMutates(op) {
			continue
		}
		recorder := harness.doAs(t, op.Method, adminV1Target(op))
		if recorder.Code == http.StatusServiceUnavailable {
			t.Fatalf("fresh mutation %s = 503: %s", op.ID, recorder.Body.String())
		}
	}
}

// TestAdminV1StalePolicyRefusesEmergencyMutation proves the emergency principal
// has no freshness bypass through the mount.
func TestAdminV1StalePolicyRefusesEmergencyMutation(t *testing.T) {
	harness := newAdminV1Harness(t)
	harness.authenticate.principal = Principal{Subject: "recovery", Kind: "emergency", CredentialID: "offline-certificate"}
	svc := freshnessMountService(t, "recovery")
	svc.policyFreshness.observeSuccess(4, time.Now().Add(-2*time.Minute))
	harness.authorizeOverride = svc.Authorize

	for _, op := range harness.registry.Operations() {
		if !adminOperationMutates(op) {
			continue
		}
		recorder := harness.doAs(t, op.Method, adminV1Target(op))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("emergency mutation %s = %d, want 503 policy-stale", op.ID, recorder.Code)
		}
		if problem := decodeAdminProblem(t, recorder); problem.Code != "policy-stale" {
			t.Fatalf("emergency mutation %s code = %q, want policy-stale", op.ID, problem.Code)
		}
	}

	// A fresh emergency decision still works, so the refusal above is freshness.
	svc.policyFreshness.observeSuccess(4, time.Now())
	recorder := harness.doAs(t, http.MethodPost, "/admin/v1/config/sets")
	if recorder.Code == http.StatusServiceUnavailable {
		t.Fatalf("fresh emergency mutation = 503: %s", recorder.Body.String())
	}
}

// TestAdminV1WrappedTypedAuthorizationErrorsThroughMount proves the mount maps
// wrapped typed errors with errors.As: a %w wrapper must keep the documented
// status and problem code instead of degrading to a generic 403.
func TestAdminV1WrappedTypedAuthorizationErrorsThroughMount(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sentinel   error
		wantStatus int
		wantCode   string
	}{
		{"stale", ErrPolicyStale, http.StatusServiceUnavailable, "policy-stale"},
		{"last admin", ErrLastPolicyAdmin, http.StatusConflict, "last-policy-admin"},
		{"invalid policy", ErrInvalidPolicy, http.StatusUnprocessableEntity, "invalid-policy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			harness := newAdminV1Harness(t)
			wrapped := fmt.Errorf("authorization seam: %w", tc.sentinel)
			harness.authorize.failError = wrapped
			recorder := harness.do(t, http.MethodPost, "/admin/v1/config/sets", nil, nil)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			if problem := decodeAdminProblem(t, recorder); problem.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", problem.Code, tc.wantCode)
			}
		})
	}
	// The unwrapped sentinel and a plain wrapped refusal keep their behaviour.
	harness := newAdminV1Harness(t)
	harness.authorize.failError = fmt.Errorf("denied: %w", ErrAdminForbidden)
	recorder := harness.do(t, http.MethodPost, "/admin/v1/config/sets", nil, nil)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("wrapped forbidden = %d, want 403", recorder.Code)
	}
	if problem := decodeAdminProblem(t, recorder); problem.Code != "admin-forbidden" {
		t.Fatalf("wrapped forbidden code = %q", problem.Code)
	}
}

var _ = errors.Is

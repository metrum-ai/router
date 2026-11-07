// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthorizationErrorsHaveSafeProblemMapping(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{ErrAdminForbidden, 403, "admin-forbidden"},
		{ErrPrivilegeEscalation, 403, "privilege-escalation"},
		{ErrLastPolicyAdmin, 409, "last-policy-admin"},
		{ErrInvalidPolicy, 422, "invalid-policy"},
		{ErrAuthorizationUnavailable, 503, "authorization-unavailable"},
		{ErrPolicyStale, 503, "policy-stale"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			wrapped := fmt.Errorf("private-dsn-and-policy-body: %w", tc.err)
			if !errors.Is(wrapped, tc.err) {
				t.Fatal("sentinel identity lost")
			}
			var typed *AdminAuthorizationError
			if !errors.As(wrapped, &typed) {
				t.Fatal("wrapped error lacks shared authorization type")
			}
			status, code := typed.AdminProblemCode()
			if status != tc.status || code != tc.code {
				t.Fatalf("mapping = %d %q", status, code)
			}
			r := httptest.NewRequest("POST", "/admin/v1/policy-sets", nil)
			w := httptest.NewRecorder()
			WriteAdminProblem(w, r, AdminProblemFor(r, status, code))
			if w.Code != tc.status || w.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("problem response %d %v", w.Code, w.Header())
			}
			if strings.Contains(w.Body.String(), "private-dsn") || strings.Contains(w.Body.String(), "policy-body") {
				t.Fatal("private error detail leaked")
			}
			if !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
				t.Fatalf("missing stable code: %s", w.Body.String())
			}
		})
	}
}

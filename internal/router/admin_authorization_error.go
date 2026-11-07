// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// AdminAuthorizationError carries only a safe, stable problem identity. The
// contract mount can discover it through errors.As without exposing a wrapped
// storage or policy error's private detail.
type AdminAuthorizationError struct {
	status int
	code   string
}

func (e *AdminAuthorizationError) Error() string {
	_, code := e.AdminProblemCode()
	return code
}

func (e *AdminAuthorizationError) AdminProblemCode() (int, string) {
	if e == nil || e.status == 0 || e.code == "" {
		return 503, "authorization-unavailable"
	}
	return e.status, e.code
}

var ErrPolicyStale = &AdminAuthorizationError{status: 503, code: "policy-stale"}

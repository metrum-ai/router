// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"errors"
	"net/http"
	"strings"
)

// Authentication only. The operation registry must still invoke the sole
// Authorizer with the trusted target domain/object/action before dispatch.
func adminMachineAuthenticationMiddleware(auth Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var p Principal
			err := ErrNoCredential
			if auth != nil {
				p, err = auth.Authenticate(r.Context(), r)
			}
			if err == nil && !validMachinePrincipal(p) {
				err = ErrInvalidCredential
			}
			if err != nil {
				code := "invalid_credential"
				switch {
				case errors.Is(err, ErrNoCredential):
					code = "authentication_required"
				case errors.Is(err, ErrUnsupportedAuthentication):
					code = "unsupported_authentication"
				case errors.Is(err, ErrSenderConstraintFailed):
					code = "sender_constraint_failed"
				case errors.Is(err, ErrInvalidCredential) && adminMissingDPoPProof(r):
					code = "sender_constraint_failed"
				}
				w.Header().Set("WWW-Authenticate", `Bearer realm="router-admin"`)
				fields := strings.Fields(adminSingleAuthorization(r))
				if code == "sender_constraint_failed" && len(fields) == 2 && strings.EqualFold(fields[0], "DPoP") {
					w.Header().Set("WWW-Authenticate", `DPoP error="invalid_dpop_proof"`)
				}
				WriteAdminProblem(w, r, AdminProblemFor(r, http.StatusUnauthorized, code))
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
		})
	}
}

// The chain rejects malformed envelopes before validators. Preserve its public
// error contract while rendering missing/ambiguous DPoP proof as the dedicated
// sender failure. This does not perform proof cryptography before token checks.
func adminMissingDPoPProof(r *http.Request) bool {
	fields := strings.Fields(adminSingleAuthorization(r))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "DPoP") {
		return false
	}
	count := 0
	valid := false
	for name, values := range r.Header {
		if strings.EqualFold(name, "DPoP") {
			count += len(values)
			if len(values) == 1 {
				valid = len(values[0]) > 0 && len(values[0]) <= maxMachineCredentialBytes
			}
		}
	}
	return count != 1 || !valid
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"testing"
)

// Exercise the serving-port handler, not only the runtime builder or machine
// adapter. All cases use valid keys and the same sufficient route permission.
func TestAdminKeyServingPortStatusCaseInsensitive(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		want         int
	}{
		{"active", "active", http.StatusOK},
		{"disabled-uppercase", "DISABLED", http.StatusUnauthorized},
		{"disabled-titlecase", "Disabled", http.StatusUnauthorized},
		{"disabled-spaces", " disabled ", http.StatusUnauthorized},
		{"disabled-canonical", "disabled", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, keys := newAdminKeyService(t, []AdminAPIKeyConfig{
				{KeyID: "status-control", Domain: "local/test", Permissions: []string{"admin:auth:read"}, Status: "active"},
				{KeyID: "status-test", Domain: "local/test", Permissions: []string{"admin:auth:read"}, Status: tc.status},
			})
			// Keep the legacy route enabled even when the candidate is disabled.
			if rr := adminKeyMeRequest(t, svc, "Bearer "+keys["status-control"].Token); rr.Code != http.StatusOK {
				t.Fatalf("active control response=%d, want=200", rr.Code)
			}
			rr := adminKeyMeRequest(t, svc, "Bearer "+keys["status-test"].Token)
			if rr.Code != tc.want {
				t.Fatalf("status %q: serving-port response=%d, want=%d", tc.status, rr.Code, tc.want)
			}
		})
	}
}

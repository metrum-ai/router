// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "testing"

// Legacy Basic permissions are arbitrary strings, unlike the admin-key
// allowlist. They were exact map keys, never Casbin wildcard/regex patterns.
func TestBasicCompatibilityPermissionsRemainLiteral(t *testing.T) {
	cfg := AdminAuthConfig{Basic: AdminBasicAuthConfig{Enabled: true, Users: []AdminBasicAuthUser{{Username: "operator", Subject: "basic:operator", Domain: "project/prod", Permissions: []string{"*:read", "admin:callers:.*", "config:read|write"}}}}}
	a, err := newAuthorizer(AdminAuthorizationConfig{}, nil, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{authorizer: a}
	subject := adminAuthSubject{subject: "basic:operator", source: "basic", credentialID: "operator", domain: "project/prod"}
	for _, check := range []struct{ object, action string }{{"metrics", "read"}, {"admin:callers", "list"}, {"config", "read"}} {
		if svc.authorizeAdminObjectAction(subject, check.object, check.action) {
			t.Errorf("literal map key broadened into %s/%s grant", check.object, check.action)
		}
	}
	// The odd literal entries themselves still retain exact helper semantics.
	for _, check := range []struct{ object, action string }{{"*", "read"}, {"admin:callers", ".*"}, {"config", "read|write"}} {
		if !svc.authorizeAdminObjectAction(subject, check.object, check.action) {
			t.Errorf("literal grant %s/%s lost", check.object, check.action)
		}
	}
}

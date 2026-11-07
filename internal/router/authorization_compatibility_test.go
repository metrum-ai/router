// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "testing"

func TestCompatibilityCredentialIsolationAndRouteModes(t *testing.T) {
	cfg := AdminAuthConfig{
		APIKeys: []AdminAPIKeyConfig{
			{KeyID: "writer", Subject: "shared", Domain: "project/prod", Permissions: []string{"config:write", "admin:training:export", "admin:reports:read"}},
			{KeyID: "reader", Subject: "shared", Domain: "project/prod", Permissions: []string{"config:read"}},
		},
		Basic: AdminBasicAuthConfig{Enabled: true, Users: []AdminBasicAuthUser{
			{Username: "one", Subject: "shared", Domain: "project/prod", Permissions: []string{"admin:auth:read"}},
			{Username: "two", Subject: "shared", Domain: "project/prod"},
		}},
		Authorization: AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, shared, project/prod, admin:auth, read", "p, shared, project/prod, admin:config, force"}},
	}
	old, err := v47FixtureAuthorizer(cfg.Authorization, nil)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := newAuthorizer(cfg.Authorization, nil, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{authorizer: compiled}
	forged := adminAuthSubject{subject: compatibilityIdentity(adminAuthSubject{subject: "shared", source: "admin_key", credentialID: "writer"}, "permission"), domain: "project/prod", source: "oauth"}
	if svc.authorizeAdmin(forged, "admin:config", "write") || svc.authorizeAdminPermission(forged, "config:write") {
		t.Fatal("public principal selected reserved compatibility identity")
	}
	reserved := cfg
	reserved.APIKeys = []AdminAPIKeyConfig{{KeyID: "forged", Subject: forged.subject, Domain: "project/prod"}}
	if _, err := newAuthorizer(reserved.Authorization, nil, nil, reserved); err == nil {
		t.Fatal("reserved public subject accepted")
	}
	subjects := []adminAuthSubject{
		{subject: "shared", domain: "project/prod", source: "admin_key", credentialID: "writer", permissions: map[string]bool{"config:write": true, "admin:training:export": true, "admin:reports:read": true}},
		{subject: "shared", domain: "project/prod", source: "admin_key", credentialID: "reader", permissions: map[string]bool{"config:read": true}},
		{subject: "shared", domain: "project/prod", source: "basic", credentialID: "one", permissions: map[string]bool{"admin:auth:read": true}},
		{subject: "shared", domain: "project/prod", source: "basic", credentialID: "two"},
	}
	for _, subject := range subjects {
		t.Run(subject.source+"/"+subject.credentialID, func(t *testing.T) {
			for permission := range knownAdminKeyPermissions {
				object, action, ok := adminPermissionObjectAction(permission)
				if !ok {
					t.Fatalf("unmapped permission %s", permission)
				}
				// Frozen v4.7 helper semantics: direct permission OR public policy.
				want := subject.permissions[permission] || old.enforce(authzSubjectForAdmin(subject), object, action)
				if got := svc.authorizeAdminPermission(subject, permission); got != want {
					t.Errorf("%s: got %v want %v", permission, got, want)
				}
				want = subject.permissions[object+":"+action] || old.enforce(authzSubjectForAdmin(subject), object, action)
				if got := svc.authorizeAdminObjectAction(subject, object, action); got != want {
					t.Errorf("%s/%s: got %v want %v", object, action, got, want)
				}
			}
			// Reports and training did not consult direct maps in v4.7. Retain
			// their distinction from the compatibility-aware config/caller APIs.
			for _, object := range []string{"admin:reports", "content:training", "admin:training"} {
				for _, action := range []string{"read", "export", "label"} {
					if got, want := svc.authorizeAdmin(subject, object, action), old.enforce(authzSubjectForAdmin(subject), object, action); got != want {
						t.Errorf("public %s/%s: got %v want %v", object, action, got, want)
					}
				}
			}
			if subject.source == "basic" {
				if got, want := svc.authorizeCompatibility(subject, "admin:auth", "read", "direct"), subject.permissions["admin:auth:read"]; got != want {
					t.Errorf("Basic-only auth: got %v want %v", got, want)
				}
			}
		})
	}
}

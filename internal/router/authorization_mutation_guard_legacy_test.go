// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"testing"
)

func TestLegacyLiteralInternalObjectsRemainData(t *testing.T) {
	cfg := AdminAuthConfig{Basic: AdminBasicAuthConfig{Enabled: true, Users: []AdminBasicAuthUser{{Username: "ops", Domain: "team/prod", Permissions: []string{"router-internal:unrelated:read"}}}}}
	svc := mutationGuardService(t, nil, cfg)
	subject := adminAuthSubject{subject: "basic:ops", domain: "team/prod", source: "basic", credentialID: "ops"}
	if !svc.authorizeAdminPermission(subject, "router-internal:unrelated:read") {
		t.Fatal("ordinary legacy literal permission changed")
	}
	if !svc.authorizeAdminObjectAction(subject, "router-internal:unrelated", "read") {
		t.Fatal("ordinary legacy raw literal permission changed")
	}
	if svc.authorizeAdmin(subject, "router-internal:unrelated", "read") {
		t.Fatal("public-policy-only helper inherited a permission-map grant")
	}
	wildcard := mutationGuardService(t, []string{"p, basic:ops, *, *, .*"}, cfg)
	for _, object := range []string{"router-internal:unrelated", securityAdministratorCapability} {
		if !wildcard.authorizeAdmin(subject, object, "administer") {
			t.Fatal("ordinary legacy wildcard query changed:", object)
		}
	}
}

func TestLegacyMapCannotSelectProtectedManagementAuthority(t *testing.T) {
	cfg := AdminAuthConfig{Basic: AdminBasicAuthConfig{Enabled: true, Users: []AdminBasicAuthUser{{Username: "ops", Domain: AdminGlobalDomain, Permissions: []string{securityAdministratorCapability + ":administer"}}}}}
	actor := Principal{Subject: "basic:ops", CredentialID: "ops"}
	svc := mutationGuardService(t, []string{"p, basic:ops, *, *, .*"}, cfg)
	properRole := mutationGuardService(t, []string{"g, basic:ops, security-admin, *"}, cfg)
	for _, mode := range []string{"public", "permission", "object", "direct", "permission-raw"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), legacyAuthorizationKey{}, legacyAuthorizationAdapter{"basic", mode})
			ctx = context.WithValue(ctx, authorizationGuardKey{}, true)
			if err := svc.Authorize(ctx, actor, AdminGlobalDomain, securityAdministratorCapability, "administer"); !errors.Is(err, ErrAdminForbidden) {
				t.Fatalf("permission map impersonated security-admin role: %v", err)
			}
			if err := properRole.Authorize(ctx, actor, AdminGlobalDomain, securityAdministratorCapability, "administer"); err != nil {
				t.Fatalf("real legacy security-admin role lost: %v", err)
			}
		})
	}
}

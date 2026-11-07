// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"testing"
)

func TestCompatibilityPreservesRoleDepth(t *testing.T) {
	for _, policyDomain := range []string{"project/prod", "*"} {
		for depth := 1; depth <= 15; depth++ {
			cfg := AdminAuthConfig{APIKeys: []AdminAPIKeyConfig{{KeyID: "reader", Subject: "shared", Domain: "project/prod"}}, Authorization: AdminAuthorizationConfig{Enabled: true}}
			parent := "shared"
			for i := 0; i < depth; i++ {
				role := fmt.Sprintf("role-%d", i)
				cfg.Authorization.Policy = append(cfg.Authorization.Policy, "g, "+parent+", "+role+", "+policyDomain)
				parent = role
			}
			cfg.Authorization.Policy = append(cfg.Authorization.Policy, "p, "+parent+", "+policyDomain+", metrics, read")
			old, err := v47FixtureAuthorizer(cfg.Authorization, nil)
			if err != nil {
				t.Fatal(err)
			}
			compiled, err := newAuthorizer(cfg.Authorization, nil, nil, cfg)
			if err != nil {
				t.Fatal(err)
			}
			subject := adminAuthSubject{subject: "shared", domain: "project/prod", source: "admin_key", credentialID: "reader"}
			want := old.enforce(authzSubjectForAdmin(subject), "metrics", "read")
			if got := (&Service{authorizer: compiled}).authorizeAdminPermission(subject, "metrics:read"); got != want {
				t.Errorf("domain %s depth %d got %v legacy %v", policyDomain, depth, got, want)
			}
		}
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResourcePolicyExampleLoadsAndCoversDocumentedScopes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "authz", "policy.example.csv"))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	authorizer, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: lines}, nil, nil, AdminAuthConfig{})
	if err != nil {
		t.Fatalf("load config/authz/policy.example.csv: %v", err)
	}
	subject := authorizationSubject{subject: "adminkey:ops-admin"}
	allowed := []struct{ domain, object, action string }{
		{"*", authzObjectAdminProviders, "read"},
		{"*", authzObjectAdminProviders, "write"},
		{"*", authzObjectAdminCatalog, "read"},
		{"*", authzObjectAdminProjects, "read"},
		{"*", authzObjectAdminProjects, "write"},
		{"*", authzObjectAdminBudgets, "read"},
		{"*", authzObjectAdminBudgets, "write"},
		{"example/prod", authzObjectAdminMemberships, "read"},
		{"example/prod", authzObjectAdminMemberships, "write"},
		{"example/prod", authzObjectAdminQuotas, "read"},
		{"example/prod", authzObjectAdminQuotas, "write"},
		{"example/prod", authzObjectAdminBudgets, "read"},
		{"example/prod", authzObjectAdminBudgets, "write"},
		{"*", authzObjectAdminRetention, "write"},
		{"*", authzObjectAdminRetention, "force"},
	}
	for _, tc := range allowed {
		allowed, err := authorizer.enforcer.Enforce(subject.subject, tc.domain, tc.object, tc.action)
		if err != nil || !allowed {
			t.Errorf("policy denied %s/%s in %s: allowed=%v err=%v", tc.object, tc.action, tc.domain, allowed, err)
		}
	}
	for _, tc := range []struct{ domain, object, action string }{
		{"other/prod", authzObjectAdminMemberships, "read"},
		{"other/prod", authzObjectAdminQuotas, "write"},
	} {
		allowed, err := authorizer.enforcer.Enforce(subject.subject, tc.domain, tc.object, tc.action)
		if err != nil || allowed {
			t.Errorf("policy unexpectedly allowed %s/%s in %s: allowed=%v err=%v", tc.object, tc.action, tc.domain, allowed, err)
		}
	}
	scoped, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: []string{
		"p, adminkey:scoped-operator, example/prod, admin:projects, read",
	}}, nil, nil, AdminAuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	scopedAllowed, err := scoped.enforcer.Enforce("adminkey:scoped-operator", AdminGlobalDomain, authzObjectAdminProjects, "read")
	if err != nil || scopedAllowed {
		t.Fatalf("a project-domain grant authorized a plane-global project route: allowed=%v err=%v", scopedAllowed, err)
	}
}

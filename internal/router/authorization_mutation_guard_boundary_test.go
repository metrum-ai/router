// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestPolicyGuardSimulationEntryIsPrivate(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "authorization_mutation_guard.go" {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if id, ok := node.(*ast.Ident); ok && id.Name == "simulatePolicyGuard" {
				t.Errorf("%s references private guard simulation entry", name)
			}
			return true
		})
	}
}

func TestPolicyGuardRejectsManagementRolePrincipalAndMappings(t *testing.T) {
	if validMachinePrincipal(Principal{Subject: securityAdministratorRole, Kind: "oauth"}) {
		t.Fatal("reserved role accepted as machine mapping subject")
	}
	for _, cfg := range []AdminAuthConfig{
		{APIKeys: []AdminAPIKeyConfig{{KeyID: "key", Subject: securityAdministratorRole, Domain: "team/prod"}}},
		{Basic: AdminBasicAuthConfig{Enabled: true, Users: []AdminBasicAuthUser{{Username: "user", Subject: securityAdministratorRole, Domain: "team/prod"}}}},
	} {
		if _, err := newAuthorizer(AdminAuthorizationConfig{}, nil, nil, cfg); err == nil {
			t.Fatal("reserved management-role subject accepted at construction")
		}
	}
}

func TestPolicyGuardCandidateBoundaries(t *testing.T) {
	actor := Principal{Subject: "actor", Kind: "oauth"}
	base := mutationGuardBase(actor.Subject)
	ctx := WithPrincipal(context.Background(), actor)
	rows := append([]string(nil), base...)
	for len(rows) < maxPolicyGuardCandidateRows {
		rows = append(rows, "p, actor, team/prod, admin:reports, read")
	}
	if err := guardPolicyMutation(ctx, actor, mutationGuardService(t, base), mutationGuardService(t, rows), []Principal{actor}, "activate"); err != nil {
		t.Fatalf("row limit boundary denied: %v", err)
	}
	groups := append([]string(nil), base...)
	for i := 1; i < maxPolicyGuardMemberships; i++ {
		groups = append(groups, "g, actor, security-admin, *")
	}
	if err := guardPolicyMutation(ctx, actor, mutationGuardService(t, base), mutationGuardService(t, groups), []Principal{actor}, "activate"); err != nil {
		t.Fatalf("membership limit boundary denied: %v", err)
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// A verified identity is not a grant. Compatibility permissions must be
// compiled into the enforcer at load time, not accepted by a handler.
func TestAdminAuthorizationRejectsUncompiledPermissionMaps(t *testing.T) {
	a, err := newAuthorizer(AdminAuthorizationConfig{}, nil, nil, AdminAuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{authorizer: a}
	subject := adminAuthSubject{subject: "adminkey:test", domain: "project/prod", source: "admin_key", permissions: map[string]bool{"config:write": true, "admin:reports:read": true}}
	t.Run("permission", func(t *testing.T) {
		if s.authorizeAdminPermission(subject, "config:write") {
			t.Fatal("permission map bypassed empty enforcer")
		}
	})
	t.Run("object-action", func(t *testing.T) {
		if s.authorizeAdminObjectAction(subject, "admin:reports", "read") {
			t.Fatal("permission map bypassed empty enforcer")
		}
	})
}

// Guard the known legacy bypasses at source level, including the Basic-only
// /admin/auth branch. This supplements (not replaces) route coverage.
func TestAdminAuthorizationNoLegacyMapDecision(t *testing.T) {
	for _, path := range []string{"admin_api_key.go", "service.go"} {
		t.Run(path, func(t *testing.T) {
			fs := token.NewFileSet()
			file, err := parser.ParseFile(fs, path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				branch, ok := n.(*ast.IfStmt)
				if !ok {
					return true
				}
				ast.Inspect(branch.Cond, func(n ast.Node) bool {
					index, ok := n.(*ast.IndexExpr)
					if !ok {
						return true
					}
					field, ok := index.X.(*ast.SelectorExpr)
					if ok && field.Sel.Name == "permissions" {
						t.Errorf("%s: permission map used as authorization condition", fs.Position(index.Pos()))
					}
					return true
				})
				return true
			})
		})
	}
}

func TestAdminPolicyRejectsPartialDomainWildcards(t *testing.T) {
	for _, fields := range [][]string{
		{"p", "role", "project/*", "admin:config", "read"},
		{"g", "subject", "role", "project/*"},
		{"p", "role", "*/prod", "metrics", "read"},
	} {
		if err := validateCasbinPolicyMutationFields(fields, "test policy"); err == nil {
			t.Errorf("accepted partial domain wildcard: %v", fields)
		}
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// Omitting or merging compatibility configurations must be a compile error.
var _ func(AdminAuthorizationConfig, map[string]*callerRuntime, *usageStore, AdminAuthConfig) (*authorizer, error) = newAuthorizer

func TestCreateAuthzPolicySetRejectsPartialDomain(t *testing.T) {
	for _, policy := range []string{"p, role, project/*, admin:config, read", "g, subject, role, project/*", "p, role, */prod, metrics, read"} {
		t.Run(policy, func(t *testing.T) {
			store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			err = store.CreateAuthzPolicySet(authzPolicySetInput{ID: "invalid", Name: "invalid", Version: 1, Policy: []string{"p, control, project/prod, metrics, read", policy}})
			if err == nil {
				t.Fatal("online store accepted partial domain")
			}
			var count int64
			if err := store.db.Model(&authzPolicySetRecord{}).Where("id = ?", "invalid").Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("failed creation retained a draft")
			}
		})
	}
}

func TestCreateAuthzPolicySetAcceptsExactAndWholeDomains(t *testing.T) {
	for _, domain := range []string{"project/prod", "*"} {
		t.Run(domain, func(t *testing.T) {
			store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if err := store.CreateAuthzPolicySet(authzPolicySetInput{ID: "valid", Name: "valid", Version: 1, Policy: []string{"p, role, " + domain + ", metrics, read", "g, subject, role, " + domain}}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReloadPublishesAuthorizationBeforeCredentials(t *testing.T) {
	fs := token.NewFileSet()
	file, err := parser.ParseFile(fs, "service.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot, keys token.Pos
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "reloadActiveConfigFromDB" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "Store" {
					if field, ok := selector.X.(*ast.SelectorExpr); ok && field.Sel.Name == "authorizerSnapshot" {
						snapshot = call.Pos()
					}
				}
			}
			if assignment, ok := n.(*ast.AssignStmt); ok {
				for _, lhs := range assignment.Lhs {
					if field, ok := lhs.(*ast.SelectorExpr); ok && field.Sel.Name == "adminAPIKeys" {
						keys = assignment.Pos()
					}
				}
			}
			return true
		})
	}
	if snapshot == token.NoPos || keys == token.NoPos || snapshot > keys {
		t.Fatal("new credentials published before their authorization snapshot")
	}
}

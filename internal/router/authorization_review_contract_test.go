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

// TestReloadUsesTheSingleAuthorizationPublisher pins the #812/#871 correction:
// the reload must not store an authorizer, credentials or a machine generation
// of its own. It PREPARES the authoritative snapshot, then installs the snapshot,
// the caller directory, the serving-port key catalogue, the configuration and the
// machine generation through exactly one publication call, so a failed reload
// publishes nothing and a successful one publishes everything together.
func TestReloadUsesTheSingleAuthorizationPublisher(t *testing.T) {
	fs := token.NewFileSet()
	file, err := parser.ParseFile(fs, "service.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var prepared, published token.Pos
	publications, stores, credentialWrites := 0, 0, 0
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "reloadActiveConfigFromDB" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
					switch selector.Sel.Name {
					case "preparePolicyAuthorizationLocked":
						prepared = call.Pos()
					case "syncPolicyAuthorizationLocked":
						t.Error("reload delegates to the poller path instead of preparing and publishing once")
					case "publishMachinePlane":
						publications++
						published = call.Pos()
					case "publishAdminMachineGeneration", "replaceAdminMachineGeneration":
						t.Errorf("reload uses a second publisher: %s", selector.Sel.Name)
					case "Store":
						if field, ok := selector.X.(*ast.SelectorExpr); ok && field.Sel.Name == "authorizerSnapshot" {
							stores++
						}
					}
				}
			}
			if assignment, ok := n.(*ast.AssignStmt); ok {
				for _, lhs := range assignment.Lhs {
					if field, ok := lhs.(*ast.SelectorExpr); ok && field.Sel.Name == "adminAPIKeys" {
						credentialWrites++
					}
				}
			}
			return true
		})
	}
	if prepared == token.NoPos {
		t.Fatal("reload does not prepare the authoritative authorizer snapshot")
	}
	if publications != 1 {
		t.Fatalf("reload publications = %d, want exactly one", publications)
	}
	if prepared > published {
		t.Fatal("machine plane published before the authoritative snapshot was prepared")
	}
	if stores != 0 {
		t.Fatal("reload stores an authorizer of its own: the machine authorizer must have one publisher")
	}
	if credentialWrites != 0 {
		t.Fatal("reload publishes replacement credentials outside the single publication")
	}
}

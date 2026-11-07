// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestAuthorizationRequestPinsOneRevision(t *testing.T) {
	allowed, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, workload, project/prod, admin:config, read|write"}}, nil, nil, AdminAuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := newAuthorizer(AdminAuthorizationConfig{}, nil, nil, AdminAuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{authorizer: allowed}
	p := Principal{Subject: "workload", Kind: "oauth"}
	pinned := svc.PinAuthorization(WithPrincipal(context.Background(), p))
	if err := svc.Authorize(pinned, p, "project/prod", "admin:config", "read"); err != nil {
		t.Fatal(err)
	}
	svc.authorizerSnapshot.Store(empty)
	if err := svc.Authorize(pinned, p, "project/prod", "admin:config", "write"); err != nil {
		t.Fatal("in-flight request changed revisions:", err)
	}
	next := svc.PinAuthorization(WithPrincipal(context.Background(), p))
	if err := svc.Authorize(next, p, "project/prod", "admin:config", "read"); !errors.Is(err, ErrAdminForbidden) {
		t.Fatal("next request retained revoked revision:", err)
	}
	other := &Service{authorizer: empty}
	if err := other.Authorize(pinned, p, "project/prod", "admin:config", "read"); !errors.Is(err, ErrAdminForbidden) {
		t.Fatal("another service consumed foreign pinned revision:", err)
	}
}

func TestEnforcerDecisionCallsStayInsideAuthorizer(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fs := token.NewFileSet()
		file, err := parser.ParseFile(fs, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch selector.Sel.Name {
			case "enforce", "enforceInDomain":
				if name != "admin_authorizer.go" {
					t.Errorf("%s: decision outside sole Authorizer", fs.Position(call.Pos()))
				}
			case "Enforce":
				if name != "authorization.go" {
					t.Errorf("%s: raw Casbin call outside engine", fs.Position(call.Pos()))
				}
			}
			return true
		})
	}
}

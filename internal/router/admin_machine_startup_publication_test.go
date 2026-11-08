// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// Startup must prepare the initial generation INSIDE the publication, from the
// configuration and authorizer current under the lock: the policy poller and its
// LISTEN/NOTIFY callback already run at that point, so a generation prepared
// before the lock could be pinned to an authorizer a synchronization superseded.
func TestStartupPreparesInsideTheSinglePublication(t *testing.T) {
	fs := token.NewFileSet()
	file, err := parser.ParseFile(fs, "service.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var publishCall ast.Node
	publishCalls, prepareCalls, prepareInsidePublication, prebuiltCalls := 0, 0, 0, 0
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "startAdminMachineGeneration" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch selector.Sel.Name {
			case "publishMachinePlane":
				publishCalls++
				publishCall = call
			case "prepareAdminMachineConfigGeneration":
				prepareCalls++
			case "publishAdminMachineGeneration", "replaceAdminMachineGeneration":
				prebuiltCalls++
			}
			return true
		})
	}
	if publishCall != nil {
		// The preparation must be an argument of the publication call (inside its
		// closure), not a sibling statement before it.
		ast.Inspect(publishCall, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "prepareAdminMachineConfigGeneration" {
				prepareInsidePublication++
			}
			return true
		})
	}
	if publishCalls != 1 {
		t.Fatalf("startup publication calls = %d, want exactly one", publishCalls)
	}
	if prepareCalls != 1 || prepareInsidePublication != 1 {
		t.Fatalf("startup prepares=%d inside-publication=%d, want one preparation inside the publication", prepareCalls, prepareInsidePublication)
	}
	if prebuiltCalls != 0 {
		t.Fatal("startup publishes a prebuilt generation instead of preparing under the lock")
	}
}

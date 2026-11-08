// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"encoding/json"
	"testing"
)

func TestPolicyOperationContractHasCompleteBindingsAndSchemas(t *testing.T) {
	specs := policyOperationSpecs()
	if len(specs) != 19 {
		t.Fatalf("policy operation inventory=%d want19", len(specs))
	}
	composed := append(AdminV1Operations(), specs...)
	registry, err := NewAdminV1Registry(composed)
	if err != nil {
		t.Fatal(err)
	}
	handlers := newPolicyAdminHandlers(nil, policyMutationDeps{}, nil)
	for _, op := range specs {
		if handlers[op.HandlerKey] == nil {
			t.Errorf("unbound policy operation %s", op.ID)
		}
		if op.Request == nil && (op.Method == "POST" || op.Method == "PUT") {
			t.Errorf("untyped mutation request %s", op.ID)
		}
		if op.Response == nil {
			t.Errorf("untyped response %s", op.ID)
		}
	}
	encoded, err := GenerateAdminOpenAPI(registry.Operations())
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	for _, op := range specs {
		found := false
		for _, method := range document.Paths[op.Path] {
			var entry struct {
				OperationID string `json:"operationId"`
			}
			if json.Unmarshal(method, &entry) == nil && entry.OperationID == op.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("missing generated operation %s", op.ID)
		}
	}
}

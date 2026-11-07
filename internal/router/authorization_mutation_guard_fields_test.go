// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"testing"
)

func TestPolicyGuardValidatesUnchangedSubmittedRows(t *testing.T) {
	actor := Principal{Subject: "actor", Kind: "oauth"}
	// Legacy loading preserves this as a literal domain, but every new activation
	// must reject partial wildcard syntax even if its effective rows are unchanged.
	legacy := append(mutationGuardBase(actor.Subject), "p, old-role, team/*, admin:reports, read")
	current := mutationGuardService(t, legacy)
	candidate := mutationGuardService(t, legacy)
	if err := guardPolicyMutation(WithPrincipal(context.Background(), actor), actor, current, candidate, []Principal{actor}, "activate"); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("unchanged submitted fields escaped online validation: %v", err)
	}
}

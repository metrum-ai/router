// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestPolicyGuardDoesNotNormalizeOpaqueRoleIdentity(t *testing.T) {
	actor := Principal{Subject: "actor", Kind: "oauth"}
	victim := Principal{Subject: "victim", Kind: "oauth"}
	// CSV permits opaque bytes in legacy role names. JSON's replacement-rune
	// normalization must not turn two distinct role records into the same key.
	base := append(mutationGuardBase(actor.Subject), "g, victim, role\xff, team/prod", "p, role\ufffd, team/prod, admin:reports, write")
	current := mutationGuardService(t, base)
	if err := current.Authorize(context.Background(), victim, "team/prod", "admin:reports", "write"); !errors.Is(err, ErrAdminForbidden) {
		t.Fatal("legacy distinct roles unexpectedly equal:", err)
	}
	changed := slices.Clone(base)
	changed[len(changed)-2] = "g, victim, role\ufffd, team/prod"
	candidate := mutationGuardService(t, changed)
	if err := candidate.Authorize(context.Background(), victim, "team/prod", "admin:reports", "write"); err != nil {
		t.Fatal("candidate widening not demonstrated:", err)
	}
	if err := guardPolicyMutation(context.Background(), actor, current, candidate, []Principal{actor}, "activate"); !errors.Is(err, ErrPrivilegeEscalation) {
		t.Fatalf("lossy record comparison skipped the new grant: %v", err)
	}
}

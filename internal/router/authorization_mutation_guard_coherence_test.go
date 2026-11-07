// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestPolicyGuardCannotMixPolicyAndRoleRevisions(t *testing.T) {
	actor := Principal{Subject: "actor", Kind: "oauth"}
	base := append(mutationGuardBase(actor.Subject), "p, writer, team/prod, admin:reports, write.*")
	current := mutationGuardService(t, base)
	replacement := mutationGuardService(t, append(slices.Clone(base), "g, actor, writer, team/prod"))
	current.authorizationAudit = func(context.Context, Principal, string, string, string, error) error {
		current.authorizerSnapshot.Store(replacement.currentAuthorizer())
		return nil
	}
	candidate := mutationGuardService(t, append(slices.Clone(base), "p, other, team/prod, admin:reports, write.*"))
	if err := guardPolicyMutation(context.Background(), actor, current, candidate, []Principal{actor}, "activate"); !errors.Is(err, ErrPrivilegeEscalation) {
		t.Fatalf("mixed old policies with replacement role graph: %v", err)
	}
}

func TestPolicyGuardCannotCountAReplacementCandidate(t *testing.T) {
	actor := Principal{Subject: "actor", Kind: "oauth"}
	base := mutationGuardBase(actor.Subject)
	current := mutationGuardService(t, base)
	candidate := mutationGuardService(t, base[1:])
	replacement := mutationGuardService(t, base)
	current.authorizationAudit = func(context.Context, Principal, string, string, string, error) error {
		candidate.authorizerSnapshot.Store(replacement.currentAuthorizer())
		return nil
	}
	if err := guardPolicyMutation(context.Background(), actor, current, candidate, []Principal{actor}, "activate"); !errors.Is(err, ErrLastPolicyAdmin) {
		t.Fatalf("counted replacement instead of actual candidate: %v", err)
	}
}

func TestPolicyGuardSimulationDoesNotEmitDecisionAudits(t *testing.T) {
	actor := Principal{Subject: "actor", Kind: "oauth"}
	base := mutationGuardBase(actor.Subject)
	current := mutationGuardService(t, base)
	candidate := mutationGuardService(t, append(slices.Clone(base), "p, other, team/prod, admin:reports, read"))
	calls := 0
	candidate.authorizationAudit = func(context.Context, Principal, string, string, string, error) error {
		calls++
		return errors.New("unavailable test sink")
	}
	if err := guardPolicyMutation(context.Background(), actor, current, candidate, []Principal{actor}, "activate"); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("simulation emitted %d real decision audits", calls)
	}
}

func TestPolicyGuardPreservesUnavailableDuringCeilingCheck(t *testing.T) {
	actor := Principal{Subject: "actor", Kind: "oauth"}
	base := mutationGuardBase(actor.Subject)
	current := mutationGuardService(t, base)
	calls := 0
	current.authorizationAudit = func(context.Context, Principal, string, string, string, error) error {
		calls++
		if calls == 3 {
			return errors.New("unavailable test sink")
		}
		return nil
	}
	candidate := mutationGuardService(t, append(slices.Clone(base), "p, other, team/prod, admin:reports, read"))
	if err := guardPolicyMutation(context.Background(), actor, current, candidate, []Principal{actor}, "activate"); !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatalf("lost safe unavailable classification: %v", err)
	}
}

func TestPolicyGuardChecksExistingInheritedRolePermissions(t *testing.T) {
	actor := Principal{Subject: "actor", Kind: "oauth"}
	base := append(mutationGuardBase(actor.Subject), "g, reader, writer, team/prod", "p, writer, team/prod, admin:reports, write")
	candidate := mutationGuardService(t, append(slices.Clone(base), "g, other, reader, team/prod"))
	if err := guardPolicyMutation(context.Background(), actor, mutationGuardService(t, base), candidate, []Principal{actor}, "activate"); !errors.Is(err, ErrPrivilegeEscalation) {
		t.Fatalf("inherited role escaped the actor ceiling: %v", err)
	}
}

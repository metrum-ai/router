// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"testing"
)

func TestPolicyGuardRequiresAuthenticatedActor(t *testing.T) {
	actor := Principal{Subject: "actor", Kind: "oauth"}
	lines := mutationGuardBase(actor.Subject)
	for _, ctx := range []context.Context{context.Background(), WithPrincipal(context.Background(), Principal{Subject: "other", Kind: "oauth"})} {
		if err := guardPolicyMutation(ctx, actor, mutationGuardService(t, lines), mutationGuardService(t, lines), []Principal{actor}, "activate"); !errors.Is(err, ErrAdminForbidden) {
			t.Errorf("unverified actor accepted: %v", err)
		}
	}
}

func TestPolicyGuardReservesManagementRoleSubject(t *testing.T) {
	actor := Principal{Subject: securityAdministratorRole, Kind: "oauth"}
	lines := []string{"p, security-admin, *, admin:policy, write|activate|rollback"}
	if err := guardPolicyMutation(WithPrincipal(context.Background(), actor), actor, mutationGuardService(t, lines), mutationGuardService(t, lines), []Principal{actor}, "activate"); !errors.Is(err, ErrAdminForbidden) {
		t.Errorf("role name impersonated membership: %v", err)
	}
}

func TestPolicyGuardBoundsCandidateRowsAndMemberships(t *testing.T) {
	actor := Principal{Subject: "actor", Kind: "oauth"}
	base := mutationGuardBase(actor.Subject)
	for _, test := range []struct {
		name, row string
		count     int
	}{{"rows", "p, actor, team/prod, admin:reports, read", 1025}, {"memberships", "g, actor, security-admin, *", 513}} {
		t.Run(test.name, func(t *testing.T) {
			lines := append([]string(nil), base...)
			for i := 0; i < test.count; i++ {
				lines = append(lines, test.row)
			}
			err := guardPolicyMutation(WithPrincipal(context.Background(), actor), actor, mutationGuardService(t, base), mutationGuardService(t, lines), []Principal{actor}, "activate")
			if !errors.Is(err, ErrInvalidPolicy) {
				t.Fatalf("oversized candidate accepted: %v", err)
			}
		})
	}
}

func TestPolicyGuardReadOnlyCannotDelegateAlternation(t *testing.T) {
	actor := Principal{Subject: "actor", Kind: "oauth"}
	base := mutationGuardBase(actor.Subject)
	candidate := append(append([]string(nil), base...), "p, recipient, team/prod, admin:reports, read|write")
	if err := guardPolicyMutation(WithPrincipal(context.Background(), actor), actor, mutationGuardService(t, base), mutationGuardService(t, candidate), []Principal{actor}, "activate"); !errors.Is(err, ErrPrivilegeEscalation) {
		t.Fatalf("read-only actor delegated write: %v", err)
	}
}

func TestPolicyGuardEmergencyMutation(t *testing.T) {
	keeper := Principal{Subject: "keeper", Kind: "oauth"}
	actor := Principal{Subject: "break-glass", Kind: "emergency"}
	base := mutationGuardBase(keeper.Subject)
	candidate := append(append([]string(nil), base...), "p, recipient, other/prod, admin:reports, read")
	if err := guardPolicyMutation(WithPrincipal(context.Background(), actor), actor, mutationGuardService(t, base), mutationGuardService(t, candidate), []Principal{keeper}, "activate"); err != nil {
		t.Fatalf("authenticated emergency mutation denied: %v", err)
	}
}

func TestPolicyGuardRoleInheritsUserSubjectGrants(t *testing.T) {
	actor := Principal{Subject: "actor", Kind: "oauth"}
	base := append(mutationGuardBase(actor.Subject), "p, user, team/prod, admin:reports, write")
	candidate := append(append([]string(nil), base...), "g, new-role, user, team/prod")
	if err := guardPolicyMutation(WithPrincipal(context.Background(), actor), actor, mutationGuardService(t, base), mutationGuardService(t, candidate), []Principal{actor}, "activate"); !errors.Is(err, ErrPrivilegeEscalation) {
		t.Fatalf("inherited user grant escaped role ceiling: %v", err)
	}
}

func TestPolicyGuardCeilingSimulationIsAuditFree(t *testing.T) {
	actor := Principal{Subject: "actor", Kind: "oauth"}
	base := mutationGuardBase(actor.Subject)
	current := mutationGuardService(t, base)
	audited := false
	current.authorizationAudit = func(_ context.Context, p Principal, domain, object, action string, _ error) error {
		if p != actor || domain != AdminGlobalDomain || (object != "admin:policy" && object != securityAdministratorCapability) {
			return errors.New("synthetic query reached real audit")
		}
		audited = true
		return nil
	}
	candidate := append(append([]string(nil), base...), "p, recipient, team/prod, admin:reports, read")
	if err := guardPolicyMutation(WithPrincipal(context.Background(), actor), actor, current, mutationGuardService(t, candidate), []Principal{actor}, "activate"); err != nil {
		t.Fatalf("simulation used real audit: %v", err)
	}
	if !audited {
		t.Fatal("real actor reauthorization was not audited")
	}
}

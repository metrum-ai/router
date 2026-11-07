// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func mutationGuardService(t *testing.T, lines []string, legacy ...AdminAuthConfig) *Service {
	t.Helper()
	if len(legacy) > 1 {
		t.Fatal("mutation guard fixture accepts at most one compatibility configuration")
	}
	var config AdminAuthConfig
	if len(legacy) == 1 {
		config = legacy[0]
	}
	a, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: lines}, nil, nil, config)
	if err != nil {
		t.Fatal(err)
	}
	return &Service{authorizer: a, authorizationAudit: func(context.Context, Principal, string, string, string, error) error { return nil }}
}

func mutationGuardBase(subject string) []string {
	return []string{"g, " + subject + ", security-admin, *", "p, security-admin, *, admin:policy, write|activate|rollback", "p, " + subject + ", team/prod, admin:reports, read"}
}

func TestPolicyMutationGuardCeilingAndRoleAuthority(t *testing.T) {
	actor := Principal{Subject: "actor", Kind: "oauth"}
	base := mutationGuardBase(actor.Subject)
	for _, test := range []struct {
		name          string
		before, added []string
		want          error
	}{
		{"contained literal", base, []string{"p, other, team/prod, admin:reports, read"}, nil},
		{"self ceiling raise", base, []string{"p, actor, team/prod, admin:reports, write"}, ErrPrivilegeEscalation},
		{"foreign domain", base, []string{"p, other, foreign/prod, admin:reports, read"}, ErrPrivilegeEscalation},
		{"wildcard domain", base, []string{"p, other, *, admin:reports, read"}, ErrPrivilegeEscalation},
		{"wildcard object", base, []string{"p, other, team/prod, *, read"}, ErrPrivilegeEscalation},
		{"broad data permissions are not security admin", []string{"p, actor, *, *, .*"}, []string{"g, other, security-admin, *"}, ErrAdminForbidden},
		{"partial domain online", base, []string{"p, other, team/*, admin:reports, read"}, ErrInvalidPolicy},
		{"unprovable regex", base, []string{"p, other, team/prod, admin:reports, read.*"}, ErrPrivilegeEscalation},
		{"same regex already owned", append(slices.Clone(base), "p, actor, team/prod, admin:reports, read.*"), []string{"p, other, team/prod, admin:reports, read.*"}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := mutationGuardService(t, test.before)
			candidate := mutationGuardService(t, append(slices.Clone(test.before), test.added...))
			err := guardPolicyMutation(context.Background(), actor, current, candidate, []Principal{actor}, "activate")
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v want %v", err, test.want)
			}
		})
	}
}

func TestPolicyMutationGuardRoleBundles(t *testing.T) {
	actor := Principal{Subject: "actor", Kind: "oauth"}
	base := append(mutationGuardBase(actor.Subject), "p, reader, team/prod, admin:reports, read", "p, other, team/prod, admin:reports, write", "p, reader, foreign/prod, admin:reports, write")
	for _, test := range []struct {
		name  string
		added []string
		want  error
	}{
		{"independent reader role ignores recipients old write", []string{"g, other, reader, team/prod"}, nil},
		{"global role binding exposes foreign grant", []string{"g, other, reader, *"}, ErrPrivilegeEscalation},
		{"nested role write", []string{"g, reader, writer, team/prod", "p, writer, team/prod, admin:reports, write", "g, other, reader, team/prod"}, ErrPrivilegeEscalation},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := mutationGuardService(t, base)
			candidate := mutationGuardService(t, append(slices.Clone(base), test.added...))
			err := guardPolicyMutation(context.Background(), actor, current, candidate, []Principal{actor}, "activate")
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v want %v", err, test.want)
			}
		})
	}
}

func TestLastNormalPolicyAdministratorGuard(t *testing.T) {
	actor := Principal{Subject: "actor", Kind: "oauth"}
	survivor := Principal{Subject: "survivor", Kind: "mtls"}
	emergency := Principal{Subject: "break-glass", Kind: "emergency"}
	base := mutationGuardBase(actor.Subject)
	withoutActor := slices.Clone(base[1:])
	for _, test := range []struct {
		name    string
		lines   []string
		enabled []Principal
		want    error
	}{
		{"last self removal", withoutActor, []Principal{actor}, ErrLastPolicyAdmin},
		{"emergency does not count", withoutActor, []Principal{actor, emergency}, ErrLastPolicyAdmin},
		{"ghost role is not enabled principal", append(slices.Clone(withoutActor), "g, ghost, security-admin, *"), []Principal{actor, emergency}, ErrLastPolicyAdmin},
		{"handover to enabled survivor", append(slices.Clone(withoutActor), "g, survivor, security-admin, *"), []Principal{actor, survivor}, nil},
		{"disabled survivor does not count", append(slices.Clone(withoutActor), "g, survivor, security-admin, *"), []Principal{actor}, ErrLastPolicyAdmin},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := guardPolicyMutation(context.Background(), actor, mutationGuardService(t, base), mutationGuardService(t, test.lines), test.enabled, "activate")
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v want %v", err, test.want)
			}
		})
	}
}

func TestLiteralGlobalPermitIsNotWildcardDomainCeiling(t *testing.T) {
	actor := Principal{Subject: "adminkey:operator", Kind: "adminkey", CredentialID: "operator"}
	cfg := AdminAuthConfig{APIKeys: []AdminAPIKeyConfig{{KeyID: "operator", Domain: "team/prod", Permissions: []string{"config:read"}}}}
	base := mutationGuardBase(actor.Subject)
	current := mutationGuardService(t, base, cfg)
	if err := current.Authorize(context.Background(), actor, AdminGlobalDomain, "admin:config", "read"); err != nil {
		t.Fatal(err)
	}
	candidate := mutationGuardService(t, append(slices.Clone(base), "p, other, *, admin:config, read"), cfg)
	if err := guardPolicyMutation(context.Background(), actor, current, candidate, []Principal{actor}, "activate"); !errors.Is(err, ErrPrivilegeEscalation) {
		t.Fatal("literal global grant became all-domain ceiling:", err)
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestPolicyGuardChecksDerivedGlobalProjection(t *testing.T) {
	for _, test := range []struct {
		kind string
		want error
	}{{"oauth", ErrPrivilegeEscalation}, {"adminkey", nil}} {
		t.Run(test.kind, func(t *testing.T) {
			actor := Principal{Subject: "adminkey:operator", Kind: test.kind, CredentialID: "operator"}
			cfg := AdminAuthConfig{APIKeys: []AdminAPIKeyConfig{
				{KeyID: "operator", Domain: "team/prod", Permissions: []string{"config:read"}},
				{KeyID: "target", Domain: "team/prod", Permissions: []string{"metrics:read"}},
			}}
			base := append(mutationGuardBase(actor.Subject), "p, adminkey:operator, team/prod, admin:config, read")
			current := mutationGuardService(t, base, cfg)
			if err := current.Authorize(WithPrincipal(context.Background(), actor), actor, "team/prod", "admin:config", "read"); err != nil {
				t.Fatal(err)
			}
			candidate := mutationGuardService(t, append(slices.Clone(base), "p, adminkey:target, team/prod, admin:config, read"), cfg)
			target := Principal{Subject: "adminkey:target", Kind: "adminkey", CredentialID: "target"}
			if err := candidate.Authorize(WithPrincipal(context.Background(), target), target, AdminGlobalDomain, "admin:config", "read"); err != nil {
				t.Fatal("candidate projection did not materialize:", err)
			}
			err := guardPolicyMutation(WithPrincipal(context.Background(), actor), actor, current, candidate, []Principal{actor}, "activate")
			if !errors.Is(err, test.want) {
				t.Fatalf("derived global grant exceeded actor's actual global ceiling: got %v want %v", err, test.want)
			}
		})
	}
}

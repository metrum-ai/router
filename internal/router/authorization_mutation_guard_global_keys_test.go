// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestPolicyGuardExplicitGlobalCallerCeiling(t *testing.T) {
	for _, test := range []struct {
		name, keyDomain, grantDomain, kind string
		want                               error
	}{
		{"real original global row scope", AdminGlobalDomain, AdminGlobalDomain, "adminkey", nil},
		{"project cannot mint global row scope", "team/prod", AdminGlobalDomain, "adminkey", ErrPrivilegeEscalation},
		{"project can delegate its own rows", "team/prod", "team/prod", "adminkey", nil},
		{"another kind cannot inherit key map", AdminGlobalDomain, AdminGlobalDomain, "oauth", ErrPrivilegeEscalation},
	} {
		t.Run(test.name, func(t *testing.T) {
			actor := Principal{Subject: "adminkey:operator", Kind: test.kind, CredentialID: "operator"}
			cfg := AdminAuthConfig{APIKeys: []AdminAPIKeyConfig{{KeyID: "operator", Domain: test.keyDomain, Permissions: []string{"admin:callers:rotate"}}}}
			base := mutationGuardBase(actor.Subject)
			current := mutationGuardService(t, base, cfg)
			if test.kind == "adminkey" {
				if err := current.Authorize(WithPrincipal(context.Background(), actor), actor, "team/prod", authzObjectAdminCallers, "rotate"); err != nil {
					t.Fatal("original caller ceiling lost:", err)
				}
			}
			candidate := mutationGuardService(t, append(slices.Clone(base), "p, other, "+test.grantDomain+", admin:callers, rotate"), cfg)
			err := guardPolicyMutation(WithPrincipal(context.Background(), actor), actor, current, candidate, []Principal{actor}, "activate")
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v want %v", err, test.want)
			}
		})
	}
}

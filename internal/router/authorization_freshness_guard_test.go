// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPolicyGuardPreservesRealMutationFreshness(t *testing.T) {
	s := freshnessGateService(t, "db", "postgres")
	a, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: []string{
		"p, operator, *, admin:policy, read|write|activate",
		"g, operator, security-admin, *",
	}}, nil, nil, AdminAuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	s.authorizer = a
	p := Principal{Subject: "operator", Kind: "mtls"}
	ctx := WithAdminPolicyMutation(WithPrincipal(context.Background(), p))
	s.policyFreshness.observeSuccess(1, time.Now())
	if err := guardPolicyMutation(ctx, p, s, &Service{authorizer: a}, []Principal{p}, "write"); err != nil {
		t.Fatalf("fresh real guard with isolated simulations: %v", err)
	}
	s.policyFreshness = newPolicyFreshnessState(30 * time.Second)
	s.policyFreshness.observeSuccess(1, time.Now().Add(-31*time.Second))
	if err := guardPolicyMutation(ctx, p, s, &Service{authorizer: a}, []Principal{p}, "write"); !errors.Is(err, ErrPolicyStale) {
		t.Fatalf("real guard must not bypass freshness: %v", err)
	}
}

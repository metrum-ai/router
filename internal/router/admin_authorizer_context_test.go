// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"testing"
)

func TestMachineAuthorizerRequiresAuthenticatedContext(t *testing.T) {
	cfg := AdminAuthConfig{APIKeys: []AdminAPIKeyConfig{{KeyID: "reader", Domain: "team/prod", Permissions: []string{"config:read"}}}}
	a, err := newAuthorizer(cfg.Authorization, nil, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{authorizer: a, authorizationAudit: func(context.Context, Principal, string, string, string, error) error { return nil }}
	for _, p := range []Principal{{Subject: "break-glass", Kind: "emergency"}, {Subject: "adminkey:reader", Kind: "adminkey", CredentialID: "reader"}} {
		for _, ctx := range []context.Context{context.Background(), WithPrincipal(context.Background(), Principal{Subject: "different", Kind: "oauth"})} {
			if err := svc.Authorize(ctx, p, "team/prod", "admin:config", "read"); !errors.Is(err, ErrAdminForbidden) {
				t.Errorf("unauthenticated/mismatched argument acquired %s authority: %v", p.Kind, err)
			}
		}
		if err := svc.Authorize(WithPrincipal(context.Background(), p), p, "team/prod", "admin:config", "read"); err != nil {
			t.Errorf("authenticated %s denied: %v", p.Kind, err)
		}
	}
}

func TestCallerSubjectPrefixDoesNotSelectGlobalMetrics(t *testing.T) {
	caller := &callerRuntime{cfg: CallerConfig{ID: "operator", Project: "team", Environment: "prod", MetricsAdmin: true}}
	a, err := newAuthorizer(AdminAuthorizationConfig{}, map[string]*callerRuntime{"operator": caller}, nil, AdminAuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{authorizer: a}
	for _, kind := range []string{"mtls", "oauth", "federated"} {
		p := Principal{Subject: "caller:operator", Kind: kind, CredentialID: "different-credential"}
		if err := svc.Authorize(WithPrincipal(context.Background(), p), p, AdminGlobalDomain, "metrics", "read"); !errors.Is(err, ErrAdminForbidden) {
			t.Errorf("%s acquired caller-token global projection: %v", kind, err)
		}
	}
}

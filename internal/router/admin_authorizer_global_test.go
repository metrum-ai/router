// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"testing"
)

func TestMachineGlobalCompatibilityPreservesLegacyOperations(t *testing.T) {
	if AdminGlobalDomain != "*" {
		t.Fatal("global domain changed from legacy training/report scope")
	}
	cfg := AdminAuthConfig{APIKeys: []AdminAPIKeyConfig{{KeyID: "reader", Domain: "team/prod", Permissions: []string{"config:read", "admin:callers:list", "metrics:read", "admin:reports:read", "admin:training:export"}}}, Authorization: AdminAuthorizationConfig{Enabled: true, Policy: []string{"g, adminkey:reader, ops, team/prod", "p, ops, team/prod, admin:config, force"}}}
	caller := &callerRuntime{cfg: CallerConfig{ID: "operator", Project: "team", Environment: "prod", MetricsAdmin: true, ContentAdmin: true}}
	a, err := newAuthorizer(cfg.Authorization, map[string]*callerRuntime{"operator": caller}, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{authorizer: a}
	if !svc.authorizeCallerInDomain(caller, AdminGlobalDomain, "metrics", "read") {
		t.Fatal("authenticated serving caller-token lost global metrics projection")
	}
	key := Principal{Subject: "adminkey:reader", Kind: "adminkey", CredentialID: "reader"}
	for _, check := range []struct{ object, action string }{{"admin:config", "read"}, {"admin:config", "force"}, {"admin:callers", "list"}, {"metrics", "read"}} {
		if err := svc.Authorize(WithPrincipal(context.Background(), key), key, AdminGlobalDomain, check.object, check.action); err != nil {
			t.Errorf("legacy global %s/%s lost: %v", check.object, check.action, err)
		}
		if err := svc.Authorize(WithPrincipal(context.Background(), key), key, "foreign/prod", check.object, check.action); !errors.Is(err, ErrAdminForbidden) {
			t.Errorf("global alias widened to foreign domain: %s/%s %v", check.object, check.action, err)
		}
	}
	for _, check := range []struct{ object, action string }{{"admin:config", "write"}, {"admin:reports", "read"}, {"content:training", "export"}, {"admin:training", "export"}} {
		if err := svc.Authorize(WithPrincipal(context.Background(), key), key, AdminGlobalDomain, check.object, check.action); !errors.Is(err, ErrAdminForbidden) {
			t.Errorf("ungranted/policy-only global operation allowed: %s/%s %v", check.object, check.action, err)
		}
	}
	lookalike := key
	lookalike.Kind = "oauth"
	if err := svc.Authorize(WithPrincipal(context.Background(), lookalike), lookalike, AdminGlobalDomain, "admin:config", "read"); !errors.Is(err, ErrAdminForbidden) {
		t.Fatal("another credential kind inherited key-local global grant:", err)
	}
	workload := Principal{Subject: "caller:operator", Kind: "mtls", CredentialID: "certificate", Issuer: "test-ca"}
	for _, check := range []struct {
		domain, object, action string
		allow                  bool
	}{
		{AdminGlobalDomain, "metrics", "read", false},
		{"foreign/prod", "metrics", "read", false},
		{"team/prod", "content:capture", "delete", true},
		{"foreign/prod", "content:capture", "delete", false},
		{AdminGlobalDomain, "content:capture", "delete", false},
	} {
		err := svc.Authorize(WithPrincipal(context.Background(), workload), workload, check.domain, check.object, check.action)
		if (err == nil) != check.allow {
			t.Errorf("caller compatibility %+v: %v", check, err)
		}
	}
}

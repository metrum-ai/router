// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"testing"
)

func TestMachineAuthorizerSoleDecisionAndEmergency(t *testing.T) {
	cfg := AdminAuthConfig{Authorization: AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, workload, project/prod, admin:config, read"}}, APIKeys: []AdminAPIKeyConfig{{KeyID: "reader", Domain: "project/prod", Permissions: []string{"config:read", "admin:reports:read", "admin:training:export"}}}}
	a, err := newAuthorizer(cfg.Authorization, nil, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{authorizer: a}
	var contract Authorizer = svc
	normal := Principal{Subject: "workload", Kind: "oauth", CredentialID: "client", Issuer: "https://issuer.example"}
	for _, test := range []struct {
		domain, object, action string
		allow                  bool
	}{{"project/prod", "admin:config", "read", true}, {"other/prod", "admin:config", "read", false}, {"project/prod", "admin:config", "write", false}, {"project/prod", "admin:config", "xreadx", false}} {
		err := contract.Authorize(WithPrincipal(context.Background(), normal), normal, test.domain, test.object, test.action)
		if (err == nil) != test.allow {
			t.Errorf("%+v: %v", test, err)
		}
	}
	key := Principal{Subject: "adminkey:reader", Kind: "adminkey", CredentialID: "reader"}
	if err := contract.Authorize(WithPrincipal(context.Background(), key), key, "project/prod", "admin:config", "read"); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct{ object, action string }{{"admin:reports", "read"}, {"content:training", "export"}, {"admin:training", "export"}} {
		if err := contract.Authorize(WithPrincipal(context.Background(), key), key, "project/prod", check.object, check.action); !errors.Is(err, ErrAdminForbidden) {
			t.Errorf("key acquired policy-only %s/%s: %v", check.object, check.action, err)
		}
	}
	emergency := Principal{Subject: "break-glass", Kind: "emergency", CredentialID: "offline-certificate", Issuer: "local-ca"}
	if err := contract.Authorize(WithPrincipal(context.Background(), normal), emergency, "other/prod", "admin:policy", "activate"); !errors.Is(err, ErrAdminForbidden) {
		t.Fatalf("mismatched authenticated context selected emergency: %v", err)
	}
	ctx := WithPrincipal(context.Background(), emergency)
	if err := contract.Authorize(ctx, emergency, "other/prod", "admin:policy", "activate"); !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatalf("unaudited emergency allowed: %v", err)
	}
	audited := false
	svc.authorizationAudit = func(_ context.Context, p Principal, domain, object, action string, decision error) error {
		if p != emergency || domain != "other/prod" || object != "admin:policy" || action != "activate" || decision != nil {
			t.Error("incomplete emergency decision audit")
		}
		audited = true
		return nil
	}
	if err := contract.Authorize(ctx, emergency, "other/prod", "admin:policy", "activate"); err != nil {
		t.Fatal(err)
	}
	empty, err := newAuthorizer(AdminAuthorizationConfig{}, nil, nil, AdminAuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	svc.authorizerSnapshot.Store(empty)
	if err := contract.Authorize(ctx, emergency, "other/prod", "admin:policy", "activate"); err != nil {
		t.Fatal("policy replacement revoked emergency:", err)
	}
	if !audited {
		t.Fatal("emergency decision was not audited")
	}
	svc.authorizationAudit = nil
	for _, forged := range []Principal{{Subject: "router-internal:emergency", Kind: "oauth"}, {Subject: "break-glass", Kind: "oauth", CredentialID: emergency.CredentialID, Issuer: emergency.Issuer}, {Subject: "workload", Kind: "basic"}} {
		if err := contract.Authorize(WithPrincipal(context.Background(), forged), forged, "other/prod", "admin:policy", "activate"); !errors.Is(err, ErrAdminForbidden) {
			t.Errorf("spoofed emergency allowed: %+v %v", forged, err)
		}
	}
}

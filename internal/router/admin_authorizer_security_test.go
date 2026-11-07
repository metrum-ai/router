// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"testing"
)

func TestEmergencyAuthorizationHasNoLocalAllowBypass(t *testing.T) {
	a, err := newAuthorizer(AdminAuthorizationConfig{}, nil, nil, AdminAuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	audits := 0
	svc := &Service{authorizer: a, authorizationAudit: func(context.Context, Principal, string, string, string, error) error { audits++; return nil }}
	p := Principal{Subject: "break-glass", Kind: "emergency", CredentialID: "offline"}
	if err := svc.Authorize(context.Background(), p, "*", "admin:keys", "issue"); err != nil {
		t.Fatal(err)
	}
	// Test-only corruption of an unpublished snapshot proves authority really
	// depends on Casbin, rather than a Kind-based early allow in Authorize.
	if _, err := a.enforcer.RemovePolicy(emergencyAuthorizationRole, "*", "*", ".*"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Authorize(context.Background(), p, "*", "admin:keys", "issue"); !errors.Is(err, ErrAdminForbidden) {
		t.Fatalf("emergency bypassed missing enforcer grant: %v", err)
	}
	if audits != 2 {
		t.Fatalf("audits = %d", audits)
	}
}

func TestEmergencyAuditFailureAndNamespaceCollisions(t *testing.T) {
	for _, line := range []string{
		"p, router-internal:emergency, *, *, .*",
		"p, router-internal:security-admin, *, *, .*",
		"g, workload, router-internal:security-admin, *",
		"g, router-internal:emergency, role, *",
	} {
		if _, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: []string{line}}, nil, nil, AdminAuthConfig{}); err == nil {
			t.Errorf("editable policy selected reserved identity: %s", line)
		}
	}
	a, err := newAuthorizer(AdminAuthorizationConfig{}, nil, nil, AdminAuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	private := errors.New("private audit backend failure")
	svc := &Service{authorizer: a, authorizationAudit: func(context.Context, Principal, string, string, string, error) error { return private }}
	if err := svc.Authorize(context.Background(), Principal{Subject: "break-glass", Kind: "emergency"}, "*", "admin:policy", "activate"); err != ErrAuthorizationUnavailable {
		t.Fatalf("audit failure did not fail closed with safe error: %v", err)
	}
}

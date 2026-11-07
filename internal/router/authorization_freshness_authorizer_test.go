// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// These database handles classify authority only; this test never queries one.
func freshnessGateService(t *testing.T, source, driver string) *Service {
	t.Helper()
	a, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, operator, *, admin:config, read|write"}}, nil, nil, AdminAuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Source: source}
	snap := NewConfigSnapshot()
	snap.Store(cfg)
	var dialect gorm.Dialector = postgres.New(postgres.Config{})
	if driver == "sqlite" {
		dialect = sqlite.Open(":memory:")
	}
	return &Service{authorizer: a, configSnapshot: snap, usage: &usageStore{db: &gorm.DB{Config: &gorm.Config{Dialector: dialect}}}, policyFreshness: newPolicyFreshnessState(30 * time.Second)}
}

func TestPolicyFreshnessAuthorizerGate(t *testing.T) {
	p := Principal{Subject: "operator", Kind: "mtls"}
	for _, tc := range []struct {
		name, source, driver string
		fresh                bool
		want                 error
	}{
		{"postgres unknown", "db", "postgres", false, ErrPolicyStale},
		{"normalized db authority", " DB ", "postgres", false, ErrPolicyStale},
		{"postgres successful", "db", "postgres", true, nil},
		{"sqlite exempt", "db", "sqlite", false, nil},
		{"static exempt", "inline", "postgres", false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := freshnessGateService(t, tc.source, tc.driver)
			if tc.fresh {
				s.policyFreshness.observeSuccess(1, time.Now())
			}
			ctx := WithAdminPolicyMutation(WithPrincipal(context.Background(), p))
			if err := s.Authorize(ctx, p, AdminGlobalDomain, "admin:config", "write"); !errors.Is(err, tc.want) {
				t.Fatalf("write = %v; want %v", err, tc.want)
			}
			if err := s.Authorize(WithPrincipal(context.Background(), p), p, AdminGlobalDomain, "admin:config", "read"); err != nil {
				t.Fatalf("unmarked read affected: %v", err)
			}
		})
	}
}

func TestPolicyFreshnessExpiredEvenWithEqualRevisions(t *testing.T) {
	s := freshnessGateService(t, "db", "postgres")
	s.policyFreshness.observeSuccess(2, time.Now().Add(-31*time.Second))
	p := Principal{Subject: "operator", Kind: "mtls"}
	ctx := WithAdminPolicyMutation(WithPrincipal(context.Background(), p))
	if err := s.Authorize(ctx, p, AdminGlobalDomain, "admin:config", "write"); !errors.Is(err, ErrPolicyStale) {
		t.Fatalf("expired equal revision = %v", err)
	}
	s.policyFreshness.observeSuccess(2, time.Now())
	if err := s.Authorize(ctx, p, AdminGlobalDomain, "admin:config", "write"); err != nil {
		t.Fatalf("successful unchanged recovery = %v", err)
	}
}

func TestPolicyFreshnessRequiresPublishedConfig(t *testing.T) {
	s := freshnessGateService(t, "db", "postgres")
	s.configSnapshot = nil
	p := Principal{Subject: "operator", Kind: "mtls"}
	ctx := WithAdminPolicyMutation(WithPrincipal(context.Background(), p))
	if err := s.Authorize(ctx, p, AdminGlobalDomain, "admin:config", "write"); !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatalf("missing published config = %v", err)
	}
}

func TestPolicyFreshnessEmergencyDoesNotBypass(t *testing.T) {
	s := freshnessGateService(t, "db", "postgres")
	s.authorizationAudit = func(context.Context, Principal, string, string, string, error) error { return nil }
	p := Principal{Subject: "recovery", Kind: "emergency"}
	ctx := WithAdminPolicyMutation(WithPrincipal(context.Background(), p))
	if err := s.Authorize(ctx, p, AdminGlobalDomain, "admin:config", "write"); !errors.Is(err, ErrPolicyStale) {
		t.Fatalf("emergency stale = %v", err)
	}
	s.policyFreshness.observeSuccess(1, time.Now())
	if err := s.Authorize(ctx, p, AdminGlobalDomain, "admin:config", "write"); err != nil {
		t.Fatalf("fresh authenticated emergency = %v", err)
	}
}

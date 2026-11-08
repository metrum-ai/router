// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"testing"
)

func TestPolicyMutationTransactionAuthorizesBeforeUntrustedWork(t *testing.T) {
	s, deps, _, _ := policyMutationFixture(t)
	p := Principal{Subject: "outsider", Kind: "mtls"}
	for _, rev := range []int64{0, 999, -1} {
		called := false
		_, err := s.withPolicyMutation(WithPrincipal(context.Background(), p), p, deps, policyMutation{Operation: "write", ExpectedRevision: rev,
			Candidate: func(*gorm.DB, *Config) (*Service, error) { called = true; return nil, ErrInvalidPolicy },
			Apply:     func(*gorm.DB) error { called = true; return nil }, Audit: func(*gorm.DB, int64) error { called = true; return nil }})
		if !errors.Is(err, ErrAdminForbidden) {
			t.Errorf("revision %d: unauthorized outcome=%v", rev, err)
		}
		if called {
			t.Errorf("revision %d: unauthorized candidate/apply/audit called", rev)
		}
	}
}
func TestPolicyMutationTransactionNormalizesUnknownFailures(t *testing.T) {
	for _, stage := range []string{"builder", "apply", "constraint", "cancel"} {
		t.Run(stage, func(t *testing.T) {
			s, deps, p, lines := policyMutationFixture(t)
			a, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: lines}, nil, nil, AdminAuthConfig{})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(WithPrincipal(context.Background(), p))
			defer cancel()
			private := errors.New("private database diagnostic")
			_, err = s.withPolicyMutation(ctx, p, deps, policyMutation{Operation: "write", ExpectedRevision: 0,
				Candidate: func(*gorm.DB, *Config) (*Service, error) {
					if stage == "builder" {
						return nil, private
					}
					return &Service{authorizer: a}, nil
				},
				Apply: func(tx *gorm.DB) error {
					if err := tx.Create(&authzPolicySetRecord{ID: "attempt", Name: "x", Version: 1, Status: "draft"}).Error; err != nil {
						return err
					}
					if stage == "apply" {
						return private
					}
					if stage == "constraint" {
						return tx.Create(&authzPolicySetRecord{ID: "attempt", Name: "x", Version: 1, Status: "draft"}).Error
					}
					cancel()
					return ctx.Err()
				}, Audit: func(*gorm.DB, int64) error { t.Fatal("failed mutation reached audit"); return nil }})
			if !errors.Is(err, ErrAuthorizationUnavailable) {
				t.Fatalf("%s error=%v", stage, err)
			}
			if err.Error() != ErrAuthorizationUnavailable.Error() {
				t.Fatalf("diagnostic escaped: %v", err)
			}
			var n int64
			s.db.Model(&authzPolicySetRecord{}).Where("id = ?", "attempt").Count(&n)
			if n != 0 {
				t.Fatal("failure did not roll back rows")
			}
			var row policyMutationRevision
			s.db.First(&row, 1)
			if row.Revision != 0 {
				t.Fatal("failure advanced revision")
			}
		})
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
)

func policyMutationFixture(t *testing.T) (*usageStore, policyMutationDeps, Principal, []string) {
	t.Helper()
	store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "policy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.db.AutoMigrate(&policyMutationRevision{}); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&policyMutationRevision{ID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	actor := Principal{Subject: "root", Kind: "mtls", CredentialID: "normal"}
	lines := []string{"p, root, *, admin:policy, read|write|activate", "p, root, *, admin:reports, read", "g, root, security-admin, *"}
	build := func(lines []string) *Service {
		a, e := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: lines}, nil, nil, AdminAuthConfig{})
		if e != nil {
			t.Fatal(e)
		}
		return &Service{authorizer: a, authorizationAudit: func(context.Context, Principal, string, string, string, error) error { return nil }}
	}
	cfg := &Config{}
	deps := policyMutationDeps{
		PublishedConfig: func() *Config { return cfg },
		Enabled: func(got *Config) ([]Principal, error) {
			if got != cfg {
				t.Error("inventory not derived from published snapshot")
			}
			return []Principal{actor}, nil
		},
		Current: func(*gorm.DB, *Config) (*Service, error) { return build(lines), nil },
	}
	return store, deps, actor, lines
}

func TestPolicyMutationTransactionGuardFailuresDoNotPersist(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []string
		want  error
	}{
		{"escalation", []string{"p, root, *, admin:policy, read|write|activate", "g, root, security-admin, *", "p, other, *, admin:reports, write"}, ErrPrivilegeEscalation},
		{"last admin", []string{"p, root, *, admin:policy, read|write|activate"}, ErrLastPolicyAdmin},
		{"invalid", []string{"p, root, partial/*, admin:policy, write"}, ErrInvalidPolicy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, deps, p, _ := policyMutationFixture(t)
			called := false
			mutation := policyMutation{Operation: "write", ExpectedRevision: 0,
				Candidate: func(*gorm.DB, *Config) (*Service, error) {
					a, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: tc.lines}, nil, nil, AdminAuthConfig{})
					if err != nil {
						return nil, ErrInvalidPolicy
					}
					return &Service{authorizer: a}, nil
				},
				Apply: func(*gorm.DB) error { called = true; return nil },
				Audit: func(*gorm.DB, int64) error { called = true; return nil },
			}
			_, err := s.withPolicyMutation(WithPrincipal(context.Background(), p), p, deps, mutation)
			if !errors.Is(err, tc.want) {
				t.Fatalf("guard = %v, want %v", err, tc.want)
			}
			if called {
				t.Fatal("rejected candidate reached persistence or audit")
			}
			var row policyMutationRevision
			s.db.First(&row, 1)
			if row.Revision != 0 {
				t.Fatal("rejection advanced revision")
			}
		})
	}
}

func TestPolicyMutationTransactionMissingConfigFailsClosed(t *testing.T) {
	s, deps, p, _ := policyMutationFixture(t)
	reached := false
	deps.PublishedConfig = func() *Config { reached = true; return nil }
	deps.Current = func(*gorm.DB, *Config) (*Service, error) {
		t.Fatal("missing snapshot reached current factory")
		return nil, nil
	}
	_, err := s.withPolicyMutation(WithPrincipal(context.Background(), p), p, deps, policyMutation{Operation: "write",
		Candidate: func(*gorm.DB, *Config) (*Service, error) {
			t.Fatal("missing snapshot reached candidate")
			return nil, nil
		},
		Apply: func(*gorm.DB) error { t.Fatal("missing snapshot persisted"); return nil },
		Audit: func(*gorm.DB, int64) error { t.Fatal("missing snapshot audited"); return nil },
	})
	if !reached {
		t.Fatal("published config callback was not reached")
	}
	if !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatalf("missing snapshot = %v", err)
	}
}

func TestPolicyMutationTransactionEmergencyAndAuditRollback(t *testing.T) {
	s, deps, _, lines := policyMutationFixture(t)
	p := Principal{Subject: "recovery", Kind: "emergency"}
	a, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: lines}, nil, nil, AdminAuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("audit unavailable")
	mutation := policyMutation{Operation: "write", ExpectedRevision: 0, Candidate: func(*gorm.DB, *Config) (*Service, error) { return &Service{authorizer: a}, nil },
		Apply: func(tx *gorm.DB) error {
			return tx.Create(&authzPolicySetRecord{ID: "attempt", Name: "test", Version: 1, Status: "draft"}).Error
		},
		Audit: func(*gorm.DB, int64) error { return failure },
	}
	_, err = s.withPolicyMutation(WithPrincipal(context.Background(), p), p, deps, mutation)
	if !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatalf("audit failure = %v", err)
	}
	var count int64
	s.db.Model(&authzPolicySetRecord{}).Where("id = ?", "attempt").Count(&count)
	if count != 0 {
		t.Fatal("audit failure did not roll back data")
	}
	var row policyMutationRevision
	s.db.First(&row, 1)
	if row.Revision != 0 {
		t.Fatal("audit failure advanced revision")
	}
	mutation.Audit = func(tx *gorm.DB, revision int64) error {
		return insertAuthzPolicyAudit(tx, "attempt", p.Subject, "create", "req", "", time.Unix(1, 0).UTC())
	}
	if _, err = s.withPolicyMutation(WithPrincipal(context.Background(), p), p, deps, mutation); err != nil {
		t.Fatalf("authenticated emergency mutation = %v", err)
	}
}

func TestPolicyMutationTransactionSerializesSnapshots(t *testing.T) {
	s, deps, p, lines := policyMutationFixture(t)
	var in, max, candidates, audits atomic.Int32
	var wg sync.WaitGroup
	current := deps.Current
	deps.Current = func(tx *gorm.DB, cfg *Config) (*Service, error) {
		n := in.Add(1)
		defer in.Add(-1)
		for old := max.Load(); n > old && !max.CompareAndSwap(old, n); old = max.Load() {
		}
		return current(tx, cfg)
	}
	deps.Enabled = func(*Config) ([]Principal, error) { return []Principal{p}, nil }
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mutation := policyMutation{Operation: "write", ExpectedRevision: 0,
				Candidate: func(*gorm.DB, *Config) (*Service, error) {
					candidates.Add(1)
					a, e := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: lines}, nil, nil, AdminAuthConfig{})
					return &Service{authorizer: a}, e
				},
				Apply: func(*gorm.DB) error { return nil }, Audit: func(*gorm.DB, int64) error { audits.Add(1); return nil }}
			if _, err := s.withPolicyMutation(WithPrincipal(context.Background(), p), p, deps, mutation); err != nil && !errors.Is(err, errPolicyPreconditionFailed) {
				t.Errorf("mutation: %v", err)
			}
		}()
	}
	wg.Wait()
	if max.Load() != 1 {
		t.Fatalf("snapshots overlapped: %d", max.Load())
	}
	if in.Load() != 0 || candidates.Load() != 1 || audits.Load() != 1 {
		t.Fatalf("unbalanced snapshots or stale request work: in=%d candidates=%d audits=%d", in.Load(), candidates.Load(), audits.Load())
	}
	var row policyMutationRevision
	s.db.First(&row, 1)
	if row.Revision != 1 {
		t.Fatalf("concurrent stale ETags must commit once: revision=%d", row.Revision)
	}
}

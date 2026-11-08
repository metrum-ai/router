// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"math"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errPolicyPreconditionFailed = &AdminAuthorizationError{status: 412, code: "precondition-failed"}

// policyMutationRevision is the persistent cross-replica serialization point.
// Its schema/initialization belongs to the policy migration, never a handler.
type policyMutationRevision struct {
	ID             int    `gorm:"primaryKey;autoIncrement:false;check:chk_authz_policy_runtime_singleton,id = 1"`
	Revision       int64  `gorm:"not null;check:chk_authz_policy_runtime_revision,revision >= 0"`
	ConfigRevision int64  `gorm:"not null;default:0;check:chk_authz_policy_runtime_config_revision,config_revision >= 0"`
	ActiveSetID    string `gorm:"not null;default:''"`
}

func (policyMutationRevision) TableName() string { return "authz_policy_runtime" }

// Factories must build complete views from tx and the supplied published config,
// not request pins. The runtime adapter owns credential extraction and audit.
type policyMutationDeps struct {
	PublishedConfig func() *Config
	Enabled         func(*Config) ([]Principal, error)
	Current         func(*gorm.DB, *Config) (*Service, error)
	Build           func(*gorm.DB, *Config, []string) (*Service, error)
}
type policyMutation struct {
	Operation        string
	ExpectedRevision int64
	Candidate        func(*gorm.DB, *Config) (*Service, error)
	Apply            func(*gorm.DB) error
	// The HTTP adapter binds #853's usage audit writer to this exact transaction.
	Audit func(*gorm.DB, int64) error
}

func (s *usageStore) withPolicyMutation(ctx context.Context, actor Principal, deps policyMutationDeps, mutation policyMutation) (int64, error) {
	if s == nil || s.db == nil || ctx == nil || ctx.Err() != nil {
		return 0, ErrAuthorizationUnavailable
	}
	authenticated, ok := PrincipalFrom(ctx)
	if !ok || authenticated != actor || !validMachinePrincipal(actor) {
		return 0, ErrAdminForbidden
	}
	if deps.PublishedConfig == nil || deps.Enabled == nil || deps.Current == nil || mutation.Candidate == nil || mutation.Apply == nil || mutation.Audit == nil {
		return 0, ErrAuthorizationUnavailable
	}
	if mutation.Operation == "" {
		return 0, ErrInvalidPolicy
	}
	// Config publication/credential removal must take this SAME local lock.
	// PostgreSQL additionally serializes other replicas on the singleton row;
	// SQLite holds its write transaction through guard, data and audit writes.
	s.policyMutationMu.Lock()
	defer s.policyMutationMu.Unlock()
	if ctx.Err() != nil {
		return 0, ErrAuthorizationUnavailable
	}
	var revision int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var runtime policyMutationRevision
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&runtime, 1).Error; err != nil {
			return ErrAuthorizationUnavailable
		}
		if runtime.Revision < 0 || runtime.Revision == math.MaxInt64 {
			return ErrAuthorizationUnavailable
		}
		cfg := deps.PublishedConfig()
		if cfg == nil {
			return ErrAuthorizationUnavailable
		}
		current, err := deps.Current(tx, cfg)
		if err != nil || current == nil {
			return ErrAuthorizationUnavailable
		}
		authority, err := authorizePolicyMutationActor(ctx, actor, current, mutation.Operation)
		if err != nil {
			return err
		}
		if mutation.ExpectedRevision < 0 {
			return ErrInvalidPolicy
		}
		if runtime.Revision != mutation.ExpectedRevision {
			return errPolicyPreconditionFailed
		}
		enabled, err := deps.Enabled(cfg)
		if err != nil {
			return ErrAuthorizationUnavailable
		}
		candidate, err := mutation.Candidate(tx, cfg)
		if err != nil {
			return err
		}
		if candidate == nil {
			return ErrAuthorizationUnavailable
		}
		if err := authority.guard(candidate, enabled); err != nil {
			return err
		}
		if err := mutation.Apply(tx); err != nil {
			return err
		}
		revision = runtime.Revision + 1
		result := tx.Model(&policyMutationRevision{}).Where("id = ? AND revision = ?", 1, runtime.Revision).Update("revision", revision)
		if result.Error != nil || result.RowsAffected != 1 {
			return ErrAuthorizationUnavailable
		}
		if err := mutation.Audit(tx, revision); err != nil {
			return ErrAuthorizationUnavailable
		}
		// Reuse commit-scoped hints; authoritative polling remains the fallback.
		queueConfigRevisionNotification(tx, policyNotificationScope)
		return nil
	})
	if err != nil {
		return 0, normalizePolicyMutationError(err)
	}
	return revision, nil
}

// Do not let builders, SQL drivers or context cancellation expose diagnostics
// or make the HTTP adapter guess whether an opaque error should be a 503.
func normalizePolicyMutationError(err error) error {
	if err == nil {
		return nil
	}
	for _, domain := range []error{ErrAdminForbidden, ErrPrivilegeEscalation, ErrLastPolicyAdmin, ErrInvalidPolicy, ErrAuthorizationUnavailable, ErrPolicyStale, errPolicyPreconditionFailed, errPolicySetImmutable, errPolicyResourceNotFound} {
		if errors.Is(err, domain) {
			return domain
		}
	}
	return ErrAuthorizationUnavailable
}

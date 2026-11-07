// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"log"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const policyNotificationScope = "admin-policy"
const defaultPolicyPollInterval = 5 * time.Second

// ConfigureAdminPolicyFreshness applies the listener-owned, validated duration.
// It changes the threshold, never the last successful synchronization time.
func (s *Service) ConfigureAdminPolicyFreshness(maxAge time.Duration) error {
	if s == nil || s.policyFreshness == nil {
		return ErrAuthorizationUnavailable
	}
	if maxAge <= 0 {
		return ErrInvalidPolicy
	}
	s.policyFreshness.mu.Lock()
	s.policyFreshness.maxAge = maxAge
	s.policyFreshness.mu.Unlock()
	return nil
}

func (s *Service) startPolicySynchronization(ctx context.Context, interval time.Duration) (func(), error) {
	if ctx == nil || interval <= 0 || s == nil || s.policyFreshness == nil {
		return nil, ErrInvalidPolicy
	}
	failed := s.syncPolicyAuthorization(ctx) != nil
	if failed {
		log.Printf("admin policy synchronization unavailable; policy mutations remain fail-closed")
	}
	cancel, done := startRevisionPolling(ctx, interval, func(pollCtx context.Context) {
		err := s.syncPolicyAuthorization(pollCtx)
		if err != nil && !failed {
			log.Printf("admin policy synchronization unavailable; successful-sync time unchanged")
		}
		failed = err != nil
	})
	return func() { cancel(); <-done }, nil
}

// This read projection shares #813's migrated singleton table. It never creates
// schema on the request or synchronization path.
type policySyncRevisionRow struct {
	ID             int `gorm:"primaryKey;autoIncrement:false"`
	Revision       int64
	ConfigRevision int64
}

func (policySyncRevisionRow) TableName() string { return "authz_policy_runtime" }

func (s *Service) PolicyFreshness() (desired, applied int64, observedAt time.Time, ok bool) {
	if s == nil {
		return 0, 0, time.Time{}, false
	}
	return s.policyFreshness.PolicyFreshness()
}

func (s *Service) syncPolicyAuthorization(ctx context.Context) error {
	if s == nil || ctx == nil || ctx.Err() != nil || s.usage == nil || s.usage.db == nil || s.configSnapshot == nil || s.policyFreshness == nil {
		return ErrAuthorizationUnavailable
	}
	s.usage.policyMutationMu.Lock()
	defer s.usage.policyMutationMu.Unlock()
	cfg := s.configSnapshot.Load()
	if cfg == nil {
		return ErrAuthorizationUnavailable
	}
	authz := cfg.Server.AdminAuth.Authorization
	if !authz.Enabled || !strings.EqualFold(strings.TrimSpace(authz.Source), "db") {
		return nil
	}
	if s.quota == nil {
		return ErrAuthorizationUnavailable
	}
	previousDesired, previousApplied, _, _ := s.PolicyFreshness()
	var candidate *authorizer
	var revision policySyncRevisionRow
	err := s.usage.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Writers lock this same row before touching active policy or child rows.
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&revision, 1).Error; err != nil {
			return ErrAuthorizationUnavailable
		}
		if revision.Revision < 0 || revision.Revision < previousDesired || revision.Revision < previousApplied {
			return ErrAuthorizationUnavailable
		}
		s.policyFreshness.observeDesired(revision.Revision)
		var err error
		candidate, err = newAuthorizer(authz, s.quota.callers, &usageStore{db: tx}, cfg.Server.AdminAuth)
		if err != nil {
			return ErrAuthorizationUnavailable
		}
		return nil
	})
	if err != nil || candidate == nil {
		return ErrAuthorizationUnavailable
	}
	_, previous, _, known := s.PolicyFreshness()
	if local, _, _ := s.loadedConfigRevision(); revision.ConfigRevision != 0 && local != revision.ConfigRevision && (!known || previous != revision.Revision) {
		log.Printf("admin policy/config revision mismatch: policy_config_revision=%d published_config_revision=%d; replicas must publish identical configuration", revision.ConfigRevision, local)
	}
	// Publication and acknowledgement stay under the local governance lock.
	// Failed reads/builds/transactions cannot touch either pointer or timestamp.
	s.authorizerSnapshot.Store(candidate)
	s.policyFreshness.observeSuccess(revision.Revision, time.Now())
	return nil
}

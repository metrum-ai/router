// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
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
		// Configuration/startup error, never a request-time 422 invalid-policy.
		return ErrInvalidPolicyFreshness
	}
	s.policyFreshness.mu.Lock()
	s.policyFreshness.maxAge = maxAge
	s.policyFreshness.mu.Unlock()
	return nil
}

func (s *Service) startPolicySynchronization(ctx context.Context, interval time.Duration) (func(), error) {
	if ctx == nil || interval <= 0 || s == nil || s.policyFreshness == nil {
		return nil, ErrInvalidPolicyFreshness
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
	if s == nil || s.usage == nil {
		return ErrAuthorizationUnavailable
	}
	s.usage.policyMutationMu.Lock()
	defer s.usage.policyMutationMu.Unlock()
	return s.syncPolicyAuthorizationLocked(ctx, nil)
}

// syncPolicyAuthorizationLocked builds and publishes the machine authorizer from
// the authoritative singleton revision. It is the SINGLE publisher of that
// snapshot: config reloads call it instead of storing their own authorizer.
// The caller must hold s.usage.policyMutationMu.
//
// cfg overrides the published configuration for a reload that has not been
// published yet; nil reads the current published configuration.
func (s *Service) syncPolicyAuthorizationLocked(ctx context.Context, cfg *Config) error {
	if s == nil || ctx == nil || ctx.Err() != nil || s.configSnapshot == nil {
		return ErrAuthorizationUnavailable
	}
	// explicit marks a reload supplying a not-yet-published configuration. The
	// periodic poller passes nil and reads the published configuration, so it
	// never republishes an unchanged static authority.
	explicit := cfg != nil
	if cfg == nil {
		cfg = s.configSnapshot.Load()
	}
	if cfg == nil {
		return ErrAuthorizationUnavailable
	}
	authz := cfg.Server.AdminAuth.Authorization
	var callers map[string]*callerRuntime
	if s.quota != nil {
		callers = s.quota.callers
	}
	if !authz.Enabled || !strings.EqualFold(strings.TrimSpace(authz.Source), "db") {
		// Static/disabled authority has no revision to read or observe. A reload
		// still publishes here, so this path remains the single publisher; the
		// poller itself leaves the unchanged published authorizer in place.
		if !explicit {
			return nil
		}
		staticCandidate, err := newAuthorizer(authz, callers, s.usage, cfg.Server.AdminAuth)
		if err != nil {
			return ErrAuthorizationUnavailable
		}
		s.authorizerSnapshot.Store(staticCandidate)
		return nil
	}
	if s.usage == nil || s.usage.db == nil || s.policyFreshness == nil || s.quota == nil {
		return ErrAuthorizationUnavailable
	}
	previousDesired, previousApplied, _, known := s.PolicyFreshness()
	var candidate *authorizer
	var revision policySyncRevisionRow
	// revisionKnown distinguishes an authoritative revision from a deployment
	// whose runtime row does not exist yet (before the singleton migration has
	// run). The authorizer can still be built and published from the store, but
	// freshness is not claimed from a revision that was never read.
	revisionKnown := false
	// The observation timestamp is taken BEFORE the share-locked read, so a slow
	// read can only make the recorded synchronization look older than the data
	// actually observed, never fresher.
	observedAt := time.Now()
	err := s.usage.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The snapshot must not read a half-written policy. Writers of this
		// singleton row (the guarded CRUD slice, #866) take it FOR UPDATE; this
		// read-only synchronization takes SHARE. This slice contains no writer,
		// so the SHARE clause protects against a concurrent writer rather than
		// against another reader.
		if !tx.Migrator().HasTable(&policySyncRevisionRow{}) {
			if known {
				// The runtime table existed and is now gone: that is a failed
				// authoritative read, not a fresh pre-migration deployment.
				return ErrAuthorizationUnavailable
			}
			// Pre-migration deployment: no singleton table yet. Publish from the
			// store below, but claim no freshness that no revision proves.
			revision = policySyncRevisionRow{}
			built, err := newAuthorizer(authz, s.quota.callers, &usageStore{db: tx}, cfg.Server.AdminAuth)
			if err != nil {
				return ErrAuthorizationUnavailable
			}
			candidate = built
			return nil
		}
		locked := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&revision, 1).Error
		switch {
		case locked == nil:
			revisionKnown = true
			if revision.Revision < 0 || revision.Revision < previousDesired || revision.Revision < previousApplied {
				return ErrAuthorizationUnavailable
			}
			s.policyFreshness.observeDesired(revision.Revision)
		case errors.Is(locked, gorm.ErrRecordNotFound):
			// Pre-migration deployment: publish an authorizer from the store but
			// never claim freshness that no authoritative revision proves.
			revision = policySyncRevisionRow{}
		default:
			return ErrAuthorizationUnavailable
		}
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
	if !revisionKnown {
		return nil
	}
	s.policyFreshness.observeSuccess(revision.Revision, observedAt)
	return nil
}

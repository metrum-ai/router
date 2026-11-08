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

// applyAdminPolicyFreshness resolves the configured freshness window and applies
// it to the running gate. Startup and every successful reload use this single
// path, so an operator's max_staleness is never silently replaced by the
// default. An invalid value is a configuration error, not a request problem.
func (s *Service) applyAdminPolicyFreshness(cfg *Config) error {
	if cfg == nil {
		return ErrAuthorizationUnavailable
	}
	maxAge, err := adminPolicyMaxStaleness(cfg.Server.AdminAuth.Authorization)
	if err != nil {
		return err
	}
	return s.ConfigureAdminPolicyFreshness(maxAge)
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
	return s.syncPolicyAuthorizationLocked(ctx)
}

// preparedPolicyAuthorization is the complete, UNPUBLISHED result of one
// authoritative policy read. Preparing it changes no published state, so a
// caller can build the machine generation from this exact authorizer and then
// publish both together, or abandon the whole preparation without a trace.
type preparedPolicyAuthorization struct {
	authorizer  *authorizer
	observation *policySyncAcknowledgement
	// changed reports whether the authoritative policy differs from the
	// currently published one. When it does not, publication must not touch the
	// machine generation: rebuilding it would repeat JWKS discovery for an
	// identical policy, and replacing the pinned authorizer would break the
	// invariant that a published generation is pinned to the published snapshot.
	changed bool
	// authoritative reports that the configuration uses the DB policy store, so
	// the cross-store administrator guard applies to it.
	authoritative bool
}

// preparePolicyAuthorizationLocked builds the candidate authorizer for the
// authoritative policy revision and reads that revision's proof. It publishes
// nothing: no snapshot, no generation, no freshness acknowledgement. The caller
// must hold s.usage.policyMutationMu.
//
// cfg overrides the configuration to build against (a reload candidate that is
// not published yet); nil reads the published configuration. callers overrides
// the caller directory; nil reads the live directory.
func (s *Service) preparePolicyAuthorizationLocked(ctx context.Context, cfg *Config, callers map[string]*callerRuntime) (*preparedPolicyAuthorization, error) {
	if s == nil || ctx == nil || ctx.Err() != nil || s.configSnapshot == nil {
		return nil, ErrAuthorizationUnavailable
	}
	// explicit marks a reload supplying a not-yet-published configuration. The
	// periodic poller passes nil and reads the published configuration.
	explicit := cfg != nil
	if cfg == nil {
		cfg = s.configSnapshot.Load()
	}
	if cfg == nil {
		return nil, ErrAuthorizationUnavailable
	}
	authz := cfg.Server.AdminAuth.Authorization
	if callers == nil && s.quota != nil {
		callers = s.quota.callers
	}
	if !authz.Enabled || !strings.EqualFold(strings.TrimSpace(authz.Source), "db") {
		// Static/disabled authority has no revision to read or observe. A reload
		// still builds here; the poller itself leaves the unchanged published
		// authorizer in place.
		if !explicit {
			return nil, nil
		}
		staticCandidate, err := newAuthorizer(authz, callers, s.usage, cfg.Server.AdminAuth)
		if err != nil {
			return nil, ErrAuthorizationUnavailable
		}
		return &preparedPolicyAuthorization{authorizer: staticCandidate, changed: !samePublishedPolicy(s.currentAuthorizer(), staticCandidate)}, nil
	}
	if s.usage == nil || s.usage.db == nil || s.policyFreshness == nil || s.quota == nil {
		return nil, ErrAuthorizationUnavailable
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
			built, err := newAuthorizer(authz, callers, &usageStore{db: tx}, cfg.Server.AdminAuth)
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
		candidate, err = newAuthorizer(authz, callers, &usageStore{db: tx}, cfg.Server.AdminAuth)
		if err != nil {
			return ErrAuthorizationUnavailable
		}
		return nil
	})
	if err != nil || candidate == nil {
		return nil, ErrAuthorizationUnavailable
	}
	_, previous, _, known := s.PolicyFreshness()
	if local, _, _ := s.loadedConfigRevision(); revision.ConfigRevision != 0 && local != revision.ConfigRevision && (!known || previous != revision.Revision) {
		log.Printf("admin policy/config revision mismatch: policy_config_revision=%d published_config_revision=%d; replicas must publish identical configuration", revision.ConfigRevision, local)
	}
	return &preparedPolicyAuthorization{
		authorizer:    candidate,
		observation:   &policySyncAcknowledgement{Revision: revision.Revision, ObservedAt: observedAt, Known: revisionKnown},
		changed:       !samePublishedPolicy(s.currentAuthorizer(), candidate),
		authoritative: true,
	}, nil
}

// syncPolicyAuthorizationLocked prepares one authoritative snapshot and
// publishes it through the single machine-plane publisher. It is the periodical
// and notification path; a configuration reload calls
// preparePolicyAuthorizationLocked directly so it can publish its configuration
// and generation in the same publication. The caller must hold
// s.usage.policyMutationMu.
//
// When the authoritative policy changed, the machine generation is republished
// pinned to the new snapshot in that same publication, so a grant revoked in the
// policy store stops authorizing machine requests on the next request - not on
// the next configuration reload.
func (s *Service) syncPolicyAuthorizationLocked(ctx context.Context) error {
	prepared, err := s.preparePolicyAuthorizationLocked(ctx, nil, nil)
	if err != nil {
		return err
	}
	if prepared == nil {
		return nil
	}
	return s.publishMachinePlane(func(current machinePlaneSnapshot) (*machinePlanePublication, error) {
		pub := &machinePlanePublication{Policy: prepared.observation}
		if prepared.changed {
			pub.Authorizer = prepared.authorizer
		}
		// A changed policy must reach machine decisions. Republishing the whole
		// generation keeps the published invariant "generation.authorizer is the
		// published authorizer" true, and reuses the same complete-factory path as
		// every other publication. It is skipped when nothing changed, when no
		// machine plane is running, and when the poller found no authoritative
		// revision for a pre-migration deployment whose policy has already been
		// applied (an unchanged generation would be republished needlessly).
		if !prepared.changed || current.Generation == nil || s.machineGenerationBuilder == nil {
			return pub, nil
		}
		if current.Config == nil {
			return nil, ErrAuthorizationUnavailable
		}
		generation, err := s.prepareAdminMachineConfigGeneration(current.Config, prepared.authorizer)
		if err != nil {
			return nil, err
		}
		if generation != nil {
			pub.Generation = generation
		}
		return pub, nil
	})
}

// samePublishedPolicy reports whether two authorizers carry identical authored
// policy content and the same active policy set. Caller-derived grants and
// feature flags come from the configuration, which a reload publishes explicitly
// and a poller never changes, so they cannot differ between a candidate built
// from the same configuration and the published snapshot.
func samePublishedPolicy(published, candidate *authorizer) bool {
	if published == nil || candidate == nil || published.enforcer == nil || candidate.enforcer == nil {
		return false
	}
	if published.activePolicySet != candidate.activePolicySet || len(published.authoredPolicy) != len(candidate.authoredPolicy) {
		return false
	}
	for i := range published.authoredPolicy {
		left, right := published.authoredPolicy[i], candidate.authoredPolicy[i]
		if len(left) != len(right) {
			return false
		}
		for j := range left {
			if left[j] != right[j] {
				return false
			}
		}
	}
	return true
}

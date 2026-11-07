// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const defaultPolicyMaxStaleness = 30 * time.Second

// adminPolicyMaxStaleness resolves the configured freshness window. Empty keeps
// the documented default; anything else must be a positive Go duration. The
// error is a configuration error (startup), never a request-time problem.
func adminPolicyMaxStaleness(cfg AdminAuthorizationConfig) (time.Duration, error) {
	raw := strings.TrimSpace(cfg.MaxStaleness)
	if raw == "" {
		return defaultPolicyMaxStaleness, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("must be a duration such as 30s: %w", err)
	}
	if parsed <= 0 {
		return 0, ErrInvalidPolicyFreshness
	}
	return parsed, nil
}

// ErrInvalidPolicyFreshness reports a configuration problem with the freshness
// window. It is deliberately not an AdminAuthorizationError: a bad duration is
// a startup/configuration error, never a request-time 422 invalid-policy.
var ErrInvalidPolicyFreshness = errors.New("admin policy freshness: duration must be positive")

type adminPolicyMutationKey struct{}

// WithAdminPolicyMutation marks a mutating operation selected by the trusted
// admin registry. Authentication, method/path strings and request headers never
// select this mode. Serving-port adapters do not install it.
func WithAdminPolicyMutation(ctx context.Context) context.Context {
	return context.WithValue(ctx, adminPolicyMutationKey{}, true)
}

// adminOperationMutates reports whether the trusted admin registry entry
// declares a mutating operation. Only the entry's declared method is consulted
// (reads are GET/HEAD by contract); request headers, the request URL and the
// authenticated principal never select this mode.
func adminOperationMutates(op Operation) bool {
	switch strings.ToUpper(strings.TrimSpace(op.Method)) {
	case "", http.MethodGet, http.MethodHead:
		return false
	}
	return true
}

func (s *Service) checkAdminPolicyFreshness(now time.Time) error {
	if s == nil || s.configSnapshot == nil {
		return ErrAuthorizationUnavailable
	}
	cfg := s.configSnapshot.Load()
	if cfg == nil {
		return ErrAuthorizationUnavailable
	}
	authz := cfg.Server.AdminAuth.Authorization
	if !authz.Enabled || !strings.EqualFold(strings.TrimSpace(authz.Source), "db") {
		return nil
	}
	if s.usage == nil || s.usage.db == nil || s.usage.db.Dialector == nil {
		return ErrAuthorizationUnavailable
	}
	switch s.usage.db.Dialector.Name() {
	case "sqlite":
		return nil
	case "postgres":
		if s.policyFreshness.stale(now) {
			return ErrPolicyStale
		}
		return nil
	default:
		return ErrAuthorizationUnavailable
	}
}

// policyFreshnessState separates desired observations from successful complete
// synchronization. A failed read/build/publication must never call observeSuccess.
type policyFreshnessState struct {
	mu               sync.RWMutex
	maxAge           time.Duration
	desired, applied int64
	observedAt       time.Time
	ok               bool
}

func newPolicyFreshnessState(maxAge time.Duration) *policyFreshnessState {
	if maxAge == 0 {
		maxAge = defaultPolicyMaxStaleness
	}
	return &policyFreshnessState{maxAge: maxAge}
}

func (s *policyFreshnessState) observeDesired(revision int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if revision > s.desired {
		s.desired = revision
	}
}

// observeSuccess is called only after a coherent authoritative read and complete
// snapshot publication, including an unchanged successful revision check.
func (s *policyFreshnessState) observeSuccess(revision int64, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if revision < 0 || revision < s.desired || at.IsZero() || at.Before(s.observedAt) {
		return
	}
	s.desired = revision
	s.applied = revision
	s.observedAt = at
	s.ok = true
}

func (s *policyFreshnessState) PolicyFreshness() (desired, applied int64, observedAt time.Time, ok bool) {
	if s == nil {
		return 0, 0, time.Time{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.desired, s.applied, s.observedAt, s.ok
}

// invalidate clears the successful-synchronization marker without rewinding the
// recorded revision or observation time. Mutating admin requests then fail
// closed until a complete authoritative synchronization succeeds again. It is
// used when a configuration reload cannot publish a matching authorizer.
func (s *policyFreshnessState) invalidate() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.ok = false
	s.mu.Unlock()
}

func (s *policyFreshnessState) stale(now time.Time) bool {
	if s == nil {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.ok || s.maxAge <= 0 || now.Before(s.observedAt) || now.Sub(s.observedAt) > s.maxAge
}

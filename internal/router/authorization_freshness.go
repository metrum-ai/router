// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"strings"
	"sync"
	"time"
)

const defaultPolicyMaxStaleness = 30 * time.Second

type adminPolicyMutationKey struct{}

// WithAdminPolicyMutation marks a mutating operation selected by the trusted
// admin registry. Authentication, method/path strings and request headers never
// select this mode. Serving-port adapters do not install it.
func WithAdminPolicyMutation(ctx context.Context) context.Context {
	return context.WithValue(ctx, adminPolicyMutationKey{}, true)
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

func (s *policyFreshnessState) stale(now time.Time) bool {
	if s == nil {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.ok || s.maxAge <= 0 || now.Before(s.observedAt) || now.Sub(s.observedAt) > s.maxAge
}

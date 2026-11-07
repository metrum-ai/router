// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPolicyFreshnessUsesSuccessfulSyncAge(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	state := newPolicyFreshnessState(30 * time.Second)
	if !state.stale(now) {
		t.Fatal("unknown sync must fail closed")
	}
	state.observeSuccess(4, now)
	if state.stale(now.Add(30 * time.Second)) {
		t.Fatal("age equal to limit is allowed")
	}
	if !state.stale(now.Add(30*time.Second + time.Nanosecond)) {
		t.Fatal("age above limit must be stale even when revisions match")
	}
	state.observeDesired(5) // A read/build/publication failure cannot acknowledge sync.
	desired, applied, at, ok := state.PolicyFreshness()
	if desired != 5 || applied != 4 || !at.Equal(now) || !ok {
		t.Fatalf("failed apply changed observation: %d %d %v %v", desired, applied, at, ok)
	}
	if !state.stale(now.Add(31 * time.Second)) {
		t.Fatal("failed apply refreshed successful-sync clock")
	}
	state.observeSuccess(5, now.Add(32*time.Second))
	state.observeSuccess(5, now.Add(60*time.Second)) // Successful unchanged poll.
	if state.stale(now.Add(61 * time.Second)) {
		t.Fatal("unchanged successful sync did not recover freshness")
	}
	_, _, at, _ = state.PolicyFreshness()
	if !at.Equal(now.Add(60 * time.Second)) {
		t.Fatal("successful unchanged sync did not refresh observation")
	}
}

func TestPolicyFreshnessDefaultAndClockRollback(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	state := newPolicyFreshnessState(0)
	state.observeSuccess(1, now)
	if state.stale(now.Add(30 * time.Second)) {
		t.Fatal("default must be 30 seconds")
	}
	if !state.stale(now.Add(31 * time.Second)) {
		t.Fatal("default age limit not enforced")
	}
	if !state.stale(now.Add(-time.Second)) {
		t.Fatal("future observation cannot prove freshness")
	}
}

func TestPolicyFreshnessConfigurationDoesNotAcknowledgeSync(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	s := &Service{policyFreshness: newPolicyFreshnessState(0)}
	s.policyFreshness.observeSuccess(4, now)
	if err := s.ConfigureAdminPolicyFreshness(time.Second); err != nil {
		t.Fatal(err)
	}
	if !s.policyFreshness.stale(now.Add(2 * time.Second)) {
		t.Fatal("configured max age not applied")
	}
	_, _, observed, _ := s.PolicyFreshness()
	if !observed.Equal(now) {
		t.Fatal("threshold change acknowledged a sync")
	}
	for _, invalid := range []time.Duration{0, -time.Second} {
		err := s.ConfigureAdminPolicyFreshness(invalid)
		if err == nil {
			t.Fatal("nonpositive max age accepted")
		}
		// A configuration error, never a request-time 422 invalid-policy.
		if !errors.Is(err, ErrInvalidPolicyFreshness) {
			t.Fatalf("invalid max age = %v, want ErrInvalidPolicyFreshness", err)
		}
		if errors.Is(err, ErrInvalidPolicy) {
			t.Fatal("invalid max age must not surface as invalid-policy")
		}
	}
}

// TestPolicyFreshnessConfigValidation proves a bad configured window fails the
// configuration at startup instead of becoming a request-time problem.
func TestPolicyFreshnessConfigValidation(t *testing.T) {
	base := func() Config {
		cfg := Config{}
		cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{
			Enabled: true, Source: "static", Policy: []string{"p, operator, *, admin:config, read"},
		}
		return cfg
	}
	for _, tc := range []struct{ name, value string }{
		{"zero", "0s"}, {"negative", "-5s"}, {"garbage", "thirty seconds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			cfg.Server.AdminAuth.Authorization.MaxStaleness = tc.value
			err := validateAdminAuthorization(cfg.Server.AdminAuth.Authorization, UsageDBConfig{})
			if err == nil {
				t.Fatal("invalid max_staleness accepted")
			}
			if errors.Is(err, ErrInvalidPolicy) {
				t.Fatal("configuration error surfaced as invalid-policy")
			}
			if !strings.Contains(err.Error(), "max_staleness") {
				t.Fatalf("error does not name the field: %v", err)
			}
		})
	}
	t.Run("valid", func(t *testing.T) {
		cfg := base()
		cfg.Server.AdminAuth.Authorization.MaxStaleness = "45s"
		if err := validateAdminAuthorization(cfg.Server.AdminAuth.Authorization, UsageDBConfig{}); err != nil {
			t.Fatal(err)
		}
		parsed, err := adminPolicyMaxStaleness(cfg.Server.AdminAuth.Authorization)
		if err != nil || parsed != 45*time.Second {
			t.Fatalf("parsed = %v, %v", parsed, err)
		}
	})
	t.Run("default", func(t *testing.T) {
		parsed, err := adminPolicyMaxStaleness(base().Server.AdminAuth.Authorization)
		if err != nil || parsed != defaultPolicyMaxStaleness {
			t.Fatalf("default = %v, %v", parsed, err)
		}
	})
}

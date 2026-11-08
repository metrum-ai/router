// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
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

// TestConfiguredMaxStalenessAppliesAtConstruction proves the configured window
// is used by a service that has NEVER reloaded: a real config file with
// max_staleness 10s is loaded through LoadConfig and passed to New, and the
// resulting gate is fresh at exactly 10s and stale at 10s+1ns.
func TestConfiguredMaxStalenessAppliesAtConstruction(t *testing.T) {
	cfg := loadFreshnessConfigFile(t, "10s")
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(svc.Close)
	if got := svc.policyFreshness.maxAge; got != 10*time.Second {
		t.Fatalf("constructed max age = %v, want the configured 10s", got)
	}
	// The constructed service's authority is SQLite, which is age-exempt, so the
	// boundary is asserted on its configured state (pure math over the maxAge that
	// construction applied) and on the gate itself through a PostgreSQL authority
	// that receives the same parsed configuration.
	// A constructed service may already have observed a successful startup
	// synchronization, so the boundary is measured from a current observation.
	observed := time.Now().UTC()
	svc.policyFreshness.observeSuccess(8, observed)
	if svc.policyFreshness.stale(observed.Add(10 * time.Second)) {
		t.Fatal("exactly 10s must still be within the configured window")
	}
	if !svc.policyFreshness.stale(observed.Add(10*time.Second + time.Nanosecond)) {
		t.Fatal("10s+1ns must be stale with a configured 10s window")
	}
	gated := freshnessGateService(t, "db", "postgres")
	gated.policyFreshness = newPolicyFreshnessState(defaultPolicyMaxStaleness)
	if err := gated.applyAdminPolicyFreshness(cfg); err != nil {
		t.Fatal(err)
	}
	gated.policyFreshness.observeSuccess(1, time.Now().Add(-10*time.Second-time.Nanosecond))
	if err := gated.checkAdminPolicyFreshness(time.Now()); !errors.Is(err, ErrPolicyStale) {
		t.Fatalf("configured 10s gate at 10s+1ns = %v, want ErrPolicyStale", err)
	}
	// A bad configured value must fail configuration/startup, never fall back to
	// the default and never surface as a request-time invalid-policy.
	_, err = LoadConfig(writeFreshnessConfigFile(t, "0s"))
	if err == nil {
		t.Fatal("LoadConfig accepted a non-positive max_staleness")
	}
	if errors.Is(err, ErrInvalidPolicy) {
		t.Fatal("configuration error surfaced as invalid-policy")
	}
	if !strings.Contains(err.Error(), "max_staleness") {
		t.Fatalf("configuration error does not name the field: %v", err)
	}
}

// TestConfiguredMaxStalenessAppliesOnReload proves a reload applies its own
// window through the same path, so a changed value takes effect without restart.
func TestConfiguredMaxStalenessAppliesOnReload(t *testing.T) {
	cfg := loadFreshnessConfigFile(t, "10s")
	tightened := loadFreshnessConfigFile(t, "2s")
	s := freshnessGateService(t, "db", "postgres")
	s.policyFreshness = newPolicyFreshnessState(defaultPolicyMaxStaleness)
	if err := s.applyAdminPolicyFreshness(cfg); err != nil {
		t.Fatal(err)
	}
	if s.policyFreshness.maxAge != 10*time.Second {
		t.Fatalf("max age = %v, want 10s", s.policyFreshness.maxAge)
	}
	if err := s.applyAdminPolicyFreshness(tightened); err != nil {
		t.Fatal(err)
	}
	observed := time.Now()
	s.policyFreshness.observeSuccess(1, observed)
	if !s.policyFreshness.stale(observed.Add(2*time.Second + time.Nanosecond)) {
		t.Fatal("reloaded 2s window not applied")
	}
	if s.policyFreshness.stale(observed.Add(2 * time.Second)) {
		t.Fatal("exactly 2s must still be within the reloaded window")
	}
}

// loadFreshnessConfigFile writes a real config document with the given
// max_staleness and loads it through the configuration loader, so tests exercise
// parsing, validation and the documented field rather than a constructed struct.
func loadFreshnessConfigFile(t *testing.T, window string) *Config {
	t.Helper()
	path := writeFreshnessConfigFile(t, window)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.Server.AdminAuth.Authorization.MaxStaleness; got != window {
		t.Fatalf("parsed max_staleness = %q, want %q", got, window)
	}
	return cfg
}

// writeFreshnessConfigFile writes a real config document with the given window
// and returns its path.
func writeFreshnessConfigFile(t *testing.T, window string) string {
	t.Helper()
	dir := t.TempDir()
	usagePath := filepath.Join(dir, "usage.sqlite")
	// A DB-sourced policy authority needs one active set; seed it in the same file
	// the constructed service opens so startup itself is the code under test.
	seed, err := openTestUsageStorePath(usagePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.db.Create(&authzPolicySetRecord{
		ID: "freshness-set", Name: "freshness", Version: 1, Status: authzPolicyStatusActive,
		CreatedBy: "test", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		ActivatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := seed.db.Create(&authzPolicyRuleRecord{
		PolicySetID: "freshness-set", Sequence: 1, PType: "p", SubjectOrRole: "operator",
		Domain: "*", Object: "admin:config", Action: "read",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	usageEnabled := true
	base := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	base.Server.UsageDB = UsageDBConfig{
		Driver: "sqlite", Path: usagePath,
		Enable: &usageEnabled, MigrationPolicy: usageDBMigrationPolicyAutoSafe,
	}
	base.Server.AdminAuth.Authorization = AdminAuthorizationConfig{
		Enabled: true, Source: "db", MaxStaleness: window,
	}
	raw, err := yaml.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

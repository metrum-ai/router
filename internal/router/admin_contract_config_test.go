// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAdminAPIConfigDefaults proves an absent admin.api block yields the
// documented defaults and that they all validate.
func TestAdminAPIConfigDefaults(t *testing.T) {
	var block AdminAPIConfig
	block.setDefaults()
	if err := block.validate(); err != nil {
		t.Fatalf("defaults do not validate: %v", err)
	}
	settings := block.resolve()
	if settings.pagination.DefaultLimit != 50 || settings.pagination.MaxLimit != 500 {
		t.Fatalf("pagination defaults = %+v", settings.pagination)
	}
	if settings.idempotency.TTL != 24*time.Hour || settings.idempotency.MaxKeyBytes != 128 {
		t.Fatalf("idempotency defaults = %+v", settings.idempotency)
	}
	if !settings.rateLimit.isEnabled() || settings.rateLimit.ReadRPS != 20 || settings.rateLimit.WriteRPS != 5 {
		t.Fatalf("rate limit defaults = %+v", settings.rateLimit)
	}
	if !settings.authFailureThrottle.isEnabled() || settings.authFailureThrottle.FreeAttempts != 5 {
		t.Fatalf("throttle defaults = %+v", settings.authFailureThrottle)
	}
}

// TestAdminAPIConfigValidation rejects impossible combinations instead of
// silently clamping them.
func TestAdminAPIConfigValidation(t *testing.T) {
	valid := func() AdminAPIConfig {
		var block AdminAPIConfig
		block.setDefaults()
		return block
	}
	cases := []struct {
		name   string
		mutate func(*AdminAPIConfig)
		want   string
	}{
		{"default limit above max", func(c *AdminAPIConfig) { c.Pagination.DefaultLimit = 600; c.Pagination.MaxLimit = 500 }, "default_limit"},
		{"max limit too large", func(c *AdminAPIConfig) { c.Pagination.MaxLimit = 100000 }, "max_limit"},
		{"zero ttl", func(c *AdminAPIConfig) { c.Idempotency.TTL = "0s" }, "ttl"},
		{"unparseable ttl", func(c *AdminAPIConfig) { c.Idempotency.TTL = "one day" }, "ttl"},
		{"ttl too long", func(c *AdminAPIConfig) { c.Idempotency.TTL = "720h" }, "ttl"},
		{"zero key bytes", func(c *AdminAPIConfig) { c.Idempotency.MaxKeyBytes = 0 }, "max_key_bytes"},
		{"max response too large", func(c *AdminAPIConfig) { c.Idempotency.MaxResponseBytes = 64 << 20 }, "max_response_bytes"},
		{"zero rate", func(c *AdminAPIConfig) { c.RateLimit.ReadRPS = 0 }, "rate_limit"},
		{"zero burst", func(c *AdminAPIConfig) { c.RateLimit.WriteBurst = 0 }, "rate_limit"},
		{"max delay below base", func(c *AdminAPIConfig) { c.AuthFailureThrottle.MaxDelay = "1ms" }, "max_delay"},
		{"lockout length delay", func(c *AdminAPIConfig) { c.AuthFailureThrottle.MaxDelay = "10m" }, "lockout"},
		{"no free attempts", func(c *AdminAPIConfig) { c.AuthFailureThrottle.FreeAttempts = 0 }, "free_attempts"},
		{"unparseable window", func(c *AdminAPIConfig) { c.AuthFailureThrottle.Window = "soon" }, "window"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			block := valid()
			testCase.mutate(&block)
			err := block.validate()
			if err == nil {
				t.Fatalf("invalid configuration was accepted: %+v", block)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error %q does not mention %q", err, testCase.want)
			}
		})
	}
}

// TestAdminAPIConfigDisabledPaths proves a disabled limiter and throttle still
// validate (they are switched off, not misconfigured).
func TestAdminAPIConfigDisabledPaths(t *testing.T) {
	var block AdminAPIConfig
	block.setDefaults()
	disabled := false
	block.RateLimit.Enabled = &disabled
	block.RateLimit.ReadRPS = 0
	block.AuthFailureThrottle.Enabled = &disabled
	block.AuthFailureThrottle.MaxDelay = ""
	if err := block.validate(); err != nil {
		t.Fatalf("disabled limiter/throttle were rejected: %v", err)
	}
}

// TestAdminIdempotencyMigration verifies the phase-10 migration created the
// table and that the store works on a migrated database.
func TestAdminIdempotencyMigration(t *testing.T) {
	runner, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsForTest(t, runner)
	if !runner.db.Migrator().HasTable(adminIdempotencyTable) {
		t.Fatalf("migration did not create %s", adminIdempotencyTable)
	}
	status, err := runner.Verify()
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if status.SchemaVersion != configControlPlaneCompatibility.MaxSchema || status.State != "current" {
		t.Fatalf("schema status = %+v", status)
	}
	found := false
	for _, entry := range status.Entries {
		if entry.MigrationID == configControlPlanePhase10MigrationID {
			found = true
		}
	}
	if !found {
		t.Fatal("phase 10 is not in the migration ledger")
	}
}

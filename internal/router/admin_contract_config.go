// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// The admin.api configuration block of the /admin/v1 contract (ACP-013..016,
// ACP-021).
//
// It nests inside the `admin:` block the admin listener owns (#809), so there
// is one top-level admin block and one applyDefaults pass:
//
//	admin:
//	  api:
//	    pagination: { default_limit: 50, max_limit: 500 }
//	    idempotency: { ttl: 24h, max_key_bytes: 128, max_response_bytes: 1048576 }
//	    rate_limit: { enabled: true, read_rps: 20, read_burst: 40, write_rps: 5, write_burst: 10, max_identities: 4096 }
//	    auth_failure_throttle: { enabled: true, window: 5m, free_attempts: 5, base_delay: 250ms, max_delay: 4s }
//
// Omitting the block is valid and yields the defaults above. Durations are
// strings in YAML (Go's yaml decoder does not parse time.Duration), so the YAML
// form is resolved into typed settings once at startup and validation rejects an
// impossible combination instead of silently clamping it.

import (
	"fmt"
	"strings"
	"time"
)

// AdminAPIIdempotencyConfig is the YAML form of the idempotency block.
type AdminAPIIdempotencyConfig struct {
	TTL              string `yaml:"ttl" json:"ttl"`
	MaxKeyBytes      int    `yaml:"max_key_bytes" json:"maxKeyBytes"`
	MaxResponseBytes int    `yaml:"max_response_bytes" json:"maxResponseBytes"`
}

// AdminAPIConfig is the YAML form of the admin.api block.
type AdminAPIConfig struct {
	Pagination          AdminPaginationConfig          `yaml:"pagination" json:"pagination"`
	Idempotency         AdminAPIIdempotencyConfig      `yaml:"idempotency" json:"idempotency"`
	RateLimit           AdminRateLimitConfig           `yaml:"rate_limit" json:"rateLimit"`
	AuthFailureThrottle AdminAuthFailureThrottleConfig `yaml:"auth_failure_throttle" json:"authFailureThrottle"`
}

// adminAPISettings is the resolved, typed form used by the contract layer.
type adminAPISettings struct {
	pagination          AdminPaginationConfig
	idempotency         AdminIdempotencyConfig
	rateLimit           AdminRateLimitConfig
	authFailureThrottle AdminAuthFailureThrottleConfig
}

// DefaultAdminAPIConfig returns the documented defaults.
func DefaultAdminAPIConfig() AdminAPIConfig {
	return AdminAPIConfig{
		Pagination: AdminPaginationConfig{DefaultLimit: AdminV1DefaultLimit, MaxLimit: AdminV1MaxLimit},
		Idempotency: AdminAPIIdempotencyConfig{
			TTL: "24h", MaxKeyBytes: 128, MaxResponseBytes: 1 << 20,
		},
	}
}

// setDefaults fills an empty block and every empty field.
func (c *AdminAPIConfig) setDefaults() {
	if c == nil {
		return
	}
	defaults := DefaultAdminAPIConfig()
	if c.Pagination.DefaultLimit == 0 {
		c.Pagination.DefaultLimit = defaults.Pagination.DefaultLimit
	}
	if c.Pagination.MaxLimit == 0 {
		c.Pagination.MaxLimit = defaults.Pagination.MaxLimit
	}
	if strings.TrimSpace(c.Idempotency.TTL) == "" {
		c.Idempotency.TTL = defaults.Idempotency.TTL
	}
	if c.Idempotency.MaxKeyBytes == 0 {
		c.Idempotency.MaxKeyBytes = defaults.Idempotency.MaxKeyBytes
	}
	if c.Idempotency.MaxResponseBytes == 0 {
		c.Idempotency.MaxResponseBytes = defaults.Idempotency.MaxResponseBytes
	}
	c.RateLimit = c.RateLimit.normalized()
	c.AuthFailureThrottle = c.AuthFailureThrottle.normalized()
}

// validate rejects impossible combinations. A value that cannot be honoured is a
// startup error, never a silent clamp.
func (c AdminAPIConfig) validate() error {
	if c.Pagination.DefaultLimit <= 0 {
		return fmt.Errorf("server.admin.api.pagination.default_limit must be positive")
	}
	if c.Pagination.MaxLimit <= 0 || c.Pagination.MaxLimit > 10000 {
		return fmt.Errorf("server.admin.api.pagination.max_limit must be between 1 and 10000")
	}
	if c.Pagination.DefaultLimit > c.Pagination.MaxLimit {
		return fmt.Errorf("server.admin.api.pagination.default_limit must not exceed max_limit")
	}
	ttl, err := time.ParseDuration(c.Idempotency.TTL)
	if err != nil || ttl <= 0 || ttl > 7*24*time.Hour {
		return fmt.Errorf("server.admin.api.idempotency.ttl must be a duration between 1s and 168h")
	}
	if ttl < time.Minute {
		return fmt.Errorf("server.admin.api.idempotency.ttl must be at least 1m so a retry can be replayed")
	}
	if c.Idempotency.MaxKeyBytes <= 0 || c.Idempotency.MaxKeyBytes > 1024 {
		return fmt.Errorf("server.admin.api.idempotency.max_key_bytes must be between 1 and 1024")
	}
	if c.Idempotency.MaxResponseBytes <= 0 || c.Idempotency.MaxResponseBytes > 16<<20 {
		return fmt.Errorf("server.admin.api.idempotency.max_response_bytes must be between 1 and 16777216")
	}
	if c.RateLimit.isEnabled() {
		if c.RateLimit.ReadRPS <= 0 || c.RateLimit.WriteRPS <= 0 {
			return fmt.Errorf("server.admin.api.rate_limit rates must be positive when enabled")
		}
		if c.RateLimit.ReadBurst <= 0 || c.RateLimit.WriteBurst <= 0 {
			return fmt.Errorf("server.admin.api.rate_limit bursts must be positive when enabled")
		}
		if c.RateLimit.MaxIdentities <= 0 {
			return fmt.Errorf("server.admin.api.rate_limit.max_identities must be positive when enabled")
		}
	}
	if c.AuthFailureThrottle.isEnabled() {
		window, err := time.ParseDuration(c.AuthFailureThrottle.Window)
		if err != nil || window <= 0 {
			return fmt.Errorf("server.admin.api.auth_failure_throttle.window must be a positive duration")
		}
		base, err := time.ParseDuration(c.AuthFailureThrottle.BaseDelay)
		if err != nil || base <= 0 {
			return fmt.Errorf("server.admin.api.auth_failure_throttle.base_delay must be a positive duration")
		}
		maxDelay, err := time.ParseDuration(c.AuthFailureThrottle.MaxDelay)
		if err != nil || maxDelay <= 0 {
			return fmt.Errorf("server.admin.api.auth_failure_throttle.max_delay must be a positive duration")
		}
		if maxDelay < base {
			return fmt.Errorf("server.admin.api.auth_failure_throttle.max_delay must not be shorter than base_delay")
		}
		if c.AuthFailureThrottle.FreeAttempts < 1 {
			return fmt.Errorf("server.admin.api.auth_failure_throttle.free_attempts must be at least 1")
		}
		// The cap is what keeps the throttle a delay rather than a lockout.
		if maxDelay > 30*time.Second {
			return fmt.Errorf("server.admin.api.auth_failure_throttle.max_delay must not exceed 30s; a longer cooldown would be an attacker-triggerable lockout")
		}
	}
	return nil
}

// resolve returns the typed settings the contract layer uses.
func (c AdminAPIConfig) resolve() adminAPISettings {
	c.setDefaults()
	ttl, err := time.ParseDuration(c.Idempotency.TTL)
	if err != nil {
		ttl = 24 * time.Hour
	}
	return adminAPISettings{
		pagination:          c.Pagination.normalized(),
		idempotency:         AdminIdempotencyConfig{TTL: ttl, MaxKeyBytes: c.Idempotency.MaxKeyBytes, MaxResponseBytes: c.Idempotency.MaxResponseBytes},
		rateLimit:           c.RateLimit.normalized(),
		authFailureThrottle: c.AuthFailureThrottle.normalized(),
	}
}

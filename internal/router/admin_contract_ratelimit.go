// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Admin-plane rate limiting and authentication-failure throttling (ACP-021).
//
// Rate limits are per client identity (principal kind + subject), never per
// source address and never global, so one noisy automation cannot consume
// another's budget, and a hostile client cannot grow process memory: the
// identity table is bounded and evicts the least recently used entry.
//
// Authentication-failure throttling is the hook #814's authenticator chain
// calls. It is a graduated cooldown, not a lockout: the first
// `free_attempts` failures are answered normally, then a cooldown of
// min(base_delay * 2^(n), max_delay) must elapse before the next attempt for
// that key is processed. A success clears the key, the cooldown is capped at a
// few seconds, and the response is immediate (429 with Retry-After) rather than
// a sleeping handler, so a credential-guessing flood cannot pin goroutines.
// No attacker can lock a legitimate client out, because the cooldown is
// measured in seconds and scoped to a claimed identity plus a source address.

import (
	"math"
	"strings"
	"sync"
	"time"
)

// AdminRateClass selects one rate-limit bucket class for an operation.
type AdminRateClass string

const (
	AdminRateClassRead  AdminRateClass = "read"
	AdminRateClassWrite AdminRateClass = "write"
)

// AdminRateLimitConfig is the admin.api.rate_limit block.
type AdminRateLimitConfig struct {
	// Enabled is a pointer so an absent key means "on": rate limiting is a
	// security control that must apply to a configuration that never mentions it.
	Enabled       *bool   `yaml:"enabled" json:"enabled"`
	ReadRPS       float64 `yaml:"read_rps" json:"readRps"`
	ReadBurst     int     `yaml:"read_burst" json:"readBurst"`
	WriteRPS      float64 `yaml:"write_rps" json:"writeRps"`
	WriteBurst    int     `yaml:"write_burst" json:"writeBurst"`
	MaxIdentities int     `yaml:"max_identities" json:"maxIdentities"`
}

// isEnabled reports the effective switch; nil means enabled.
func (c AdminRateLimitConfig) isEnabled() bool { return c.Enabled == nil || *c.Enabled }

func (c AdminRateLimitConfig) normalized() AdminRateLimitConfig {
	out := c
	if out.ReadRPS <= 0 {
		out.ReadRPS = 20
	}
	if out.ReadBurst <= 0 {
		out.ReadBurst = 40
	}
	if out.WriteRPS <= 0 {
		out.WriteRPS = 5
	}
	if out.WriteBurst <= 0 {
		out.WriteBurst = 10
	}
	if out.MaxIdentities <= 0 {
		out.MaxIdentities = 4096
	}
	return out
}

// AdminRateLimiter decides whether one authenticated admin request may run.
type AdminRateLimiter interface {
	// Allow consumes one token for identity in the operation's class. When it
	// returns false, retryAfter is how long the caller should wait.
	Allow(identity string, class AdminRateClass, now time.Time) (retryAfter time.Duration, ok bool)
}

type adminRateBucket struct {
	tokens   float64
	lastFill time.Time
	lastUsed time.Time
}

type adminTokenBucketLimiter struct {
	mu      sync.Mutex
	cfg     AdminRateLimitConfig
	buckets map[string]*adminRateBucket
}

// NewAdminRateLimiter returns the bounded per-identity limiter. A disabled
// configuration returns a limiter that always allows, so callers never need a
// nil check.
func NewAdminRateLimiter(cfg AdminRateLimitConfig) AdminRateLimiter {
	return &adminTokenBucketLimiter{cfg: cfg.normalized(), buckets: map[string]*adminRateBucket{}}
}

// AdminIdentityKey is the rate-limit identity of a principal: its kind and
// subject. It deliberately excludes the credential ID, so rotating a credential
// does not hand a client a fresh budget.
func AdminIdentityKey(principalKind, principalSubject string) string {
	return strings.TrimSpace(principalKind) + "\x00" + strings.TrimSpace(principalSubject)
}

func (l *adminTokenBucketLimiter) Allow(identity string, class AdminRateClass, now time.Time) (time.Duration, bool) {
	if l == nil || !l.cfg.isEnabled() || strings.TrimSpace(identity) == "" {
		return 0, true
	}
	rate, burst := l.cfg.WriteRPS, l.cfg.WriteBurst
	if class == AdminRateClassRead {
		rate, burst = l.cfg.ReadRPS, l.cfg.ReadBurst
	}
	key := string(class) + "\x00" + identity
	l.mu.Lock()
	defer l.mu.Unlock()
	bucket := l.buckets[key]
	if bucket == nil {
		if len(l.buckets) >= l.cfg.MaxIdentities {
			l.evictLocked(now)
		}
		bucket = &adminRateBucket{tokens: float64(burst), lastFill: now}
		l.buckets[key] = bucket
	}
	if elapsed := now.Sub(bucket.lastFill); elapsed > 0 {
		bucket.tokens = math.Min(float64(burst), bucket.tokens+elapsed.Seconds()*rate)
		bucket.lastFill = now
	}
	bucket.lastUsed = now
	if bucket.tokens >= 1 {
		bucket.tokens--
		return 0, true
	}
	missing := 1 - bucket.tokens
	return time.Duration(missing / rate * float64(time.Second)), false
}

func (l *adminTokenBucketLimiter) evictLocked(now time.Time) {
	oldestKey, oldest := "", int64(1<<62)
	for key, bucket := range l.buckets {
		if bucket.lastUsed.UnixNano() < oldest {
			oldestKey, oldest = key, bucket.lastUsed.UnixNano()
		}
	}
	if oldestKey != "" {
		delete(l.buckets, oldestKey)
	}
	_ = now
}

// ---------------------------------------------------------------------------
// Authentication-failure throttle (#814 hook)
// ---------------------------------------------------------------------------

// AdminAuthFailureThrottleConfig is the admin.api.auth_failure_throttle block.
type AdminAuthFailureThrottleConfig struct {
	// Enabled is a pointer so an absent key means "on".
	Enabled      *bool  `yaml:"enabled" json:"enabled"`
	Window       string `yaml:"window" json:"window"`
	FreeAttempts int    `yaml:"free_attempts" json:"freeAttempts"`
	BaseDelay    string `yaml:"base_delay" json:"baseDelay"`
	MaxDelay     string `yaml:"max_delay" json:"maxDelay"`
}

// isEnabled reports the effective switch; nil means enabled.
func (c AdminAuthFailureThrottleConfig) isEnabled() bool { return c.Enabled == nil || *c.Enabled }

func (c AdminAuthFailureThrottleConfig) normalized() AdminAuthFailureThrottleConfig {
	out := c
	if strings.TrimSpace(out.Window) == "" {
		out.Window = "5m"
	}
	if out.FreeAttempts <= 0 {
		out.FreeAttempts = 5
	}
	if strings.TrimSpace(out.BaseDelay) == "" {
		out.BaseDelay = "250ms"
	}
	if strings.TrimSpace(out.MaxDelay) == "" {
		out.MaxDelay = "4s"
	}
	return out
}

// AdminAuthThrottle is the hook #814's chain calls around a credential
// attempt. Implementations must never return a permanent denial.
type AdminAuthThrottle interface {
	// Allow reports whether an authentication attempt may proceed now. When it
	// may not, retryAfter is the remaining cooldown.
	Allow(identity, sourceAddr string, now time.Time) (retryAfter time.Duration, ok bool)
	// RecordFailure records a failed attempt and returns the new cooldown.
	RecordFailure(identity, sourceAddr string, now time.Time) time.Duration
	// RecordSuccess clears the failure state for both keys.
	RecordSuccess(identity, sourceAddr string)
}

type adminAuthFailureState struct {
	failures    int
	windowStart time.Time
	nextAllowed time.Time
}

type adminAuthThrottle struct {
	mu      sync.Mutex
	cfg     AdminAuthFailureThrottleConfig
	window  time.Duration
	base    time.Duration
	max     time.Duration
	entries map[string]*adminAuthFailureState
	limit   int
}

// NewAdminAuthThrottle returns the graduated-cooldown throttle. Invalid
// durations fall back to the defaults rather than disabling the mechanism.
func NewAdminAuthThrottle(cfg AdminAuthFailureThrottleConfig) AdminAuthThrottle {
	cfg = cfg.normalized()
	window, err := time.ParseDuration(cfg.Window)
	if err != nil || window <= 0 {
		window = 5 * time.Minute
	}
	base, err := time.ParseDuration(cfg.BaseDelay)
	if err != nil || base <= 0 {
		base = 250 * time.Millisecond
	}
	maxDelay, err := time.ParseDuration(cfg.MaxDelay)
	if err != nil || maxDelay <= 0 || maxDelay < base {
		maxDelay = 4 * time.Second
	}
	return &adminAuthThrottle{cfg: cfg, window: window, base: base, max: maxDelay, entries: map[string]*adminAuthFailureState{}, limit: 8192}
}

func adminAuthThrottleKeys(identity, sourceAddr string) []string {
	keys := make([]string, 0, 2)
	if id := strings.TrimSpace(identity); id != "" {
		keys = append(keys, "id\x00"+id)
	}
	if addr := strings.TrimSpace(sourceAddr); addr != "" {
		keys = append(keys, "addr\x00"+addr)
	}
	return keys
}

func (t *adminAuthThrottle) Allow(identity, sourceAddr string, now time.Time) (time.Duration, bool) {
	if t == nil || !t.cfg.isEnabled() {
		return 0, true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var retry time.Duration
	for _, key := range adminAuthThrottleKeys(identity, sourceAddr) {
		state := t.entries[key]
		if state == nil {
			continue
		}
		if now.Sub(state.windowStart) > t.window {
			delete(t.entries, key)
			continue
		}
		if now.Before(state.nextAllowed) {
			if remaining := state.nextAllowed.Sub(now); remaining > retry {
				retry = remaining
			}
		}
	}
	if retry > 0 {
		return retry, false
	}
	return 0, true
}

func (t *adminAuthThrottle) RecordFailure(identity, sourceAddr string, now time.Time) time.Duration {
	if t == nil || !t.cfg.isEnabled() {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var cooldown time.Duration
	for _, key := range adminAuthThrottleKeys(identity, sourceAddr) {
		state := t.entries[key]
		if state == nil || now.Sub(state.windowStart) > t.window {
			if len(t.entries) >= t.limit {
				t.evictLocked()
			}
			state = &adminAuthFailureState{windowStart: now}
			t.entries[key] = state
		}
		state.failures++
		if state.failures > t.cfg.FreeAttempts {
			exponent := state.failures - t.cfg.FreeAttempts - 1
			if exponent > 16 {
				exponent = 16
			}
			delay := time.Duration(float64(t.base) * math.Pow(2, float64(exponent)))
			if delay > t.max {
				delay = t.max
			}
			state.nextAllowed = now.Add(delay)
			if delay > cooldown {
				cooldown = delay
			}
		}
	}
	return cooldown
}

func (t *adminAuthThrottle) RecordSuccess(identity, sourceAddr string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, key := range adminAuthThrottleKeys(identity, sourceAddr) {
		delete(t.entries, key)
	}
}

func (t *adminAuthThrottle) evictLocked() {
	oldestKey := ""
	var oldest time.Time
	for key, state := range t.entries {
		if oldestKey == "" || state.windowStart.Before(oldest) {
			oldestKey, oldest = key, state.windowStart
		}
	}
	if oldestKey != "" {
		delete(t.entries, oldestKey)
	}
}

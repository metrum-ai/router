// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The caller must first check the operator-configured upstream_kind. This is
// not proof of identity: operators must secure and authenticate the hop.
func trustedRouterRateLimit(status int, raw []byte, class string) bool {
	if status == http.StatusTooManyRequests {
		// Do not recast a recognized exhausted quota/billing response as a rate limit.
		return class == "upstream_rate_limited"
	}
	if status != http.StatusServiceUnavailable {
		return false
	}
	var body struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	return json.Unmarshal(raw, &body) == nil && body.Error.Type == "upstream-rate-limited"
}

// Preserve only syntactically valid Retry-After values from an explicitly
// trusted hop. A bounded header length avoids relaying arbitrary data.
func validTrustedRetryAfterHeader(value string) string {
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > 64 {
		return ""
	}
	digits := len(value) <= 18
	for _, r := range value {
		if r < '0' || r > '9' {
			digits = false
			break
		}
	}
	if digits {
		return value
	}
	if _, err := http.ParseTime(value); err == nil {
		return value
	}
	return ""
}

// Bound the local shaping duration independently of the validated header
// relayed to the client; never multiply attacker-supplied integers unchecked.
func trustedRouterRetryAfter(value string, now time.Time) time.Duration {
	const capDuration = 24 * time.Hour
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseUint(value, 10, 64); err == nil {
		if seconds > uint64(capDuration/time.Second) {
			return capDuration
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil && when.After(now) {
		d := when.Sub(now)
		if d > capDuration {
			return capDuration
		}
		return d
	}
	return 0
}

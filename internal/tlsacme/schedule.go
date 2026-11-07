// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package tlsacme

import (
	"math/rand"
	"time"
)

// defaultRenewalWindowRatio renews a certificate at two thirds of its life,
// which leaves a third of the lifetime to notice and repair a failure.
const defaultRenewalWindowRatio = 2.0 / 3.0

// maxJitterFraction caps the renewal jitter at a tenth of the certificate
// lifetime, so a short-lived (for example 90 minute test) certificate does not
// drift into its expiry window.
const maxJitterFraction = 0.1

// renewalDeadline computes when a certificate should be renewed:
// notBefore + ratio*(notAfter-notBefore), displaced uniformly by up to the
// configured jitter (capped at a tenth of the lifetime).
//
// roll returns a non-negative integer below n; tests inject a deterministic
// function, production uses the package random source.
func renewalDeadline(notBefore, notAfter time.Time, cfg RenewalConfig, roll func(n int64) int64) time.Time {
	lifetime := notAfter.Sub(notBefore)
	if lifetime <= 0 {
		return notAfter
	}
	ratio := cfg.WindowRatio
	if ratio <= 0 || ratio >= 1 {
		ratio = defaultRenewalWindowRatio
	}
	deadline := notBefore.Add(time.Duration(float64(lifetime) * ratio))
	jitter := cfg.Jitter
	if cap := time.Duration(float64(lifetime) * maxJitterFraction); jitter > cap {
		jitter = cap
	}
	if jitter > 0 && roll != nil {
		span := int64(2 * jitter)
		deadline = deadline.Add(time.Duration(roll(span)) - jitter)
	}
	if !deadline.Before(notBefore) && deadline.After(notAfter) {
		deadline = notAfter
	}
	return deadline
}

// randomRoll is the production jitter source.
func randomRoll(n int64) int64 {
	if n <= 0 {
		return 0
	}
	return rand.Int63n(n) //nolint:gosec // jitter, not a cryptographic decision
}

// retryDelay computes the renewal retry backoff for the given attempt: an
// exponential step capped at cfg.RetryMax, then full jitter in [delay/2, delay]
// so a fleet does not retry in lockstep. attempt 0 returns the base delay.
func retryDelay(attempt int, cfg RenewalConfig, roll func(n int64) int64) time.Duration {
	const base = 30 * time.Second
	delay := base
	for i := 0; i < attempt; i++ {
		delay *= 2
		if cfg.RetryMax > 0 && delay >= cfg.RetryMax {
			delay = cfg.RetryMax
			break
		}
	}
	if cfg.RetryMax > 0 && delay > cfg.RetryMax {
		delay = cfg.RetryMax
	}
	if delay <= 0 {
		return base
	}
	if roll == nil {
		return delay
	}
	half := int64(delay / 2)
	if half <= 0 {
		return delay
	}
	return delay/2 + time.Duration(roll(half))
}

// renewalDueOrExpired reports whether now is at or past the deadline.
func renewalDueOrExpired(deadline, now time.Time) bool {
	return !now.Before(deadline)
}

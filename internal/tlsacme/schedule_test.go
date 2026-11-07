// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package tlsacme

import (
	"testing"
	"time"
)

func TestRenewalDeadlineIsTwoThirdsOfLifetime(t *testing.T) {
	notBefore := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		lifetime time.Duration
		want     time.Duration
	}{
		{"90 day certificate", 90 * 24 * time.Hour, 60 * 24 * time.Hour},
		{"47 day certificate", 47 * 24 * time.Hour, 31*24*time.Hour + 8*time.Hour},
		{"6 day test certificate", 6 * 24 * time.Hour, 4 * 24 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := RenewalConfig{}
			deadline := renewalDeadline(notBefore, notBefore.Add(tc.lifetime), cfg, nil)
			if got := deadline.Sub(notBefore); got != tc.want {
				t.Fatalf("deadline offset = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRenewalDeadlineJitter(t *testing.T) {
	notBefore := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	lifetime := 90 * 24 * time.Hour
	cfg := RenewalConfig{Jitter: 15 * time.Minute}
	base := notBefore.Add(time.Duration(float64(lifetime) * (2.0 / 3.0)))

	if got := renewalDeadline(notBefore, notBefore.Add(lifetime), cfg, func(int64) int64 { return 0 }); !got.Equal(base.Add(-cfg.Jitter)) {
		t.Fatalf("lower jitter bound = %v, want %v", got, base.Add(-cfg.Jitter))
	}
	span := int64(2 * cfg.Jitter)
	if got := renewalDeadline(notBefore, notBefore.Add(lifetime), cfg, func(n int64) int64 { return n - 1 }); got.Sub(base) >= cfg.Jitter {
		t.Fatalf("upper jitter bound = %v, want below %v", got, base.Add(cfg.Jitter))
	}
	_ = span
}

func TestRenewalDeadlineCapsJitterAtTenPercentOfLifetime(t *testing.T) {
	notBefore := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	lifetime := time.Hour
	cfg := RenewalConfig{Jitter: 30 * time.Minute}
	base := notBefore.Add(time.Duration(float64(lifetime) * (2.0 / 3.0)))
	got := renewalDeadline(notBefore, notBefore.Add(lifetime), cfg, func(int64) int64 { return 0 })
	if want := base.Add(-lifetime / 10); !got.Equal(want) {
		t.Fatalf("capped deadline = %v, want %v", got, want)
	}
}

func TestRenewalDeadlineDefaultsForAnInvalidRatio(t *testing.T) {
	notBefore := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	lifetime := 90 * 24 * time.Hour
	deadline := renewalDeadline(notBefore, notBefore.Add(lifetime), RenewalConfig{WindowRatio: 5}, nil)
	if want := notBefore.Add(time.Duration(float64(lifetime) * (2.0 / 3.0))); !deadline.Equal(want) {
		t.Fatalf("deadline = %v, want %v", deadline, want)
	}
}

func TestRetryDelayBacksOffAndCaps(t *testing.T) {
	cfg := RenewalConfig{RetryMax: 10 * time.Minute}
	// With roll = 0 the delay is half of the exponential step.
	want := []time.Duration{
		15 * time.Second, // 30s / 2
		30 * time.Second,
		time.Minute,
		2 * time.Minute,
		4 * time.Minute,
		5 * time.Minute, // capped at 10m, then halved
		5 * time.Minute,
	}
	for attempt, expected := range want {
		if got := retryDelay(attempt, cfg, func(int64) int64 { return 0 }); got != expected {
			t.Fatalf("attempt %d delay = %v, want %v", attempt, got, expected)
		}
	}
	// Full jitter stays inside [delay/2, delay].
	for i := 0; i < 20; i++ {
		got := retryDelay(3, cfg, func(n int64) int64 { return n - 1 })
		if got < 2*time.Minute || got > 4*time.Minute {
			t.Fatalf("jittered delay %v outside [2m, 4m]", got)
		}
	}
}

func TestRenewalDueOrExpired(t *testing.T) {
	deadline := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	if renewalDueOrExpired(deadline, deadline.Add(-time.Second)) {
		t.Fatal("not due before the deadline")
	}
	if !renewalDueOrExpired(deadline, deadline) {
		t.Fatal("due at the deadline")
	}
	if !renewalDueOrExpired(deadline, deadline.Add(time.Hour)) {
		t.Fatal("due after the deadline")
	}
}

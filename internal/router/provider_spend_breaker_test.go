// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"testing"
	"time"
)

func TestProviderSpendBreakerTripsAndResets(t *testing.T) {
	b := newProviderSpendBreaker()
	now := time.Date(2026, 11, 16, 10, 59, 0, 0, time.UTC)
	b.nowFunc = func() time.Time { return now }
	cfg := ProviderSpendBreakerConfig{HourlyUSD: 2, DailyUSD: 3}
	if !b.allow("alpha", cfg) {
		t.Fatal("new breaker must allow traffic")
	}
	b.add("alpha", cfg, 1.25)
	if !b.allow("alpha", cfg) {
		t.Fatal("breaker tripped below ceiling")
	}
	b.add("alpha", cfg, .75)
	if b.allow("alpha", cfg) {
		t.Fatal("hourly ceiling did not trip")
	}
	now = now.Add(time.Hour)
	if !b.allow("alpha", cfg) {
		t.Fatal("hourly window did not reset")
	}
	b.add("alpha", cfg, 3)
	if b.allow("alpha", cfg) {
		t.Fatal("daily ceiling did not trip")
	}
	now = now.Add(24 * time.Hour)
	if !b.allow("alpha", cfg) {
		t.Fatal("daily window did not reset")
	}
}

func TestProviderSpendBreakerIgnoresInvalidCosts(t *testing.T) {
	b := newProviderSpendBreaker()
	b.nowFunc = func() time.Time { return time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC) }
	cfg := ProviderSpendBreakerConfig{HourlyUSD: 1}
	for _, cost := range []float64{-1, 0} {
		b.add("alpha", cfg, cost)
	}
	if !b.allow("alpha", cfg) {
		t.Fatal("invalid costs must not trip breaker")
	}
}

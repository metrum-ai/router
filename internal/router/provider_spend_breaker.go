// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"log"
	"math"
	"sync"
	"time"
)

type providerSpendWindow struct {
	key     string
	spent   float64
	tripped bool
}

type providerSpendBreaker struct {
	mu      sync.Mutex
	hourly  map[string]providerSpendWindow
	daily   map[string]providerSpendWindow
	nowFunc func() time.Time
	emit    func(provider, action, window string, ceiling float64)
}

func newProviderSpendBreaker() *providerSpendBreaker {
	return &providerSpendBreaker{hourly: map[string]providerSpendWindow{}, daily: map[string]providerSpendWindow{}, nowFunc: time.Now}
}

func (b *providerSpendBreaker) allow(provider string, cfg ProviderSpendBreakerConfig) bool {
	if b == nil || !cfg.enabled() {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.nowFunc().UTC()
	hour := now.Format("2006-01-02T15")
	day := now.Format("2006-01-02")
	h := b.windowLocked(b.hourly, provider, hour, "hour")
	d := b.windowLocked(b.daily, provider, day, "day")
	b.hourly[provider] = h
	b.daily[provider] = d
	return !h.tripped && !d.tripped
}

func (b *providerSpendBreaker) add(provider string, cfg ProviderSpendBreakerConfig, cost float64) {
	if b == nil || !cfg.enabled() || math.IsNaN(cost) || math.IsInf(cost, 0) || cost <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.nowFunc().UTC()
	hour := now.Format("2006-01-02T15")
	day := now.Format("2006-01-02")
	h := b.windowLocked(b.hourly, provider, hour, "hour")
	d := b.windowLocked(b.daily, provider, day, "day")
	h.spent = roundUSD(h.spent + cost)
	d.spent = roundUSD(d.spent + cost)
	if cfg.HourlyUSD > 0 && !h.tripped && h.spent >= cfg.HourlyUSD {
		h.tripped = true
		b.event(provider, "trip", "hour", cfg.HourlyUSD)
	}
	if cfg.DailyUSD > 0 && !d.tripped && d.spent >= cfg.DailyUSD {
		d.tripped = true
		b.event(provider, "trip", "day", cfg.DailyUSD)
	}
	b.hourly[provider] = h
	b.daily[provider] = d
}

func (b *providerSpendBreaker) windowLocked(windows map[string]providerSpendWindow, provider, key, label string) providerSpendWindow {
	w := windows[provider]
	if w.key == key {
		return w
	}
	if w.key != "" && w.tripped {
		b.event(provider, "reset", label, 0)
	}
	return providerSpendWindow{key: key}
}

func (b *providerSpendBreaker) event(provider, action, window string, ceiling float64) {
	log.Printf("event=provider_spend_breaker action=%s provider=%q window=%s ceiling_usd=%.6f", action, provider, window, ceiling)
	if b.emit != nil {
		b.emit(provider, action, window, ceiling)
	}
}

func hasFilterReason(reasons [][]string, want string) bool {
	for _, group := range reasons {
		for _, reason := range group {
			if reason == want {
				return true
			}
		}
	}
	return false
}

func (c ProviderSpendBreakerConfig) enabled() bool {
	return c.HourlyUSD > 0 || c.DailyUSD > 0
}

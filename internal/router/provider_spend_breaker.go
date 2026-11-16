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
		log.Printf("provider spend breaker tripped provider=%q window=hour ceiling_usd=%.6f", provider, cfg.HourlyUSD)
	}
	if cfg.DailyUSD > 0 && !d.tripped && d.spent >= cfg.DailyUSD {
		d.tripped = true
		log.Printf("provider spend breaker tripped provider=%q window=day ceiling_usd=%.6f", provider, cfg.DailyUSD)
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
		log.Printf("provider spend breaker reset provider=%q window=%s", provider, label)
	}
	return providerSpendWindow{key: key}
}

func (c ProviderSpendBreakerConfig) enabled() bool {
	return c.HourlyUSD > 0 || c.DailyUSD > 0
}

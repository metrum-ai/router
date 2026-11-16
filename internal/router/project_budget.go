// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"log"
	"math"
	"sort"
	"sync"
	"time"
)

type projectBudgetWindow struct {
	key     string
	spent   float64
	alerts  map[int]bool
	tripped bool
}

type projectBudgetManager struct {
	mu      sync.Mutex
	daily   map[string]projectBudgetWindow
	monthly map[string]projectBudgetWindow
	now     func() time.Time
}

func newProjectBudgetManager() *projectBudgetManager {
	return &projectBudgetManager{daily: map[string]projectBudgetWindow{}, monthly: map[string]projectBudgetWindow{}, now: time.Now}
}

func (m *projectBudgetManager) allow(project string, cfg ProjectBudgetConfig) bool {
	if m == nil || cfg.DailyUSD <= 0 && cfg.MonthlyUSD <= 0 {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	d := m.window(m.daily, project, now.Format("2006-01-02"))
	mo := m.window(m.monthly, project, now.Format("2006-01"))
	// Config reloads can raise or remove a ceiling without changing the spend
	// window. Derive the trip from today's limits on every admission so the
	// breaker recovers immediately when the operator changes the config.
	d.tripped = cfg.DailyUSD > 0 && d.spent >= cfg.DailyUSD
	mo.tripped = cfg.MonthlyUSD > 0 && mo.spent >= cfg.MonthlyUSD
	m.daily[project], m.monthly[project] = d, mo
	return !d.tripped && !mo.tripped
}

func (m *projectBudgetManager) add(project string, cfg ProjectBudgetConfig, cost float64) {
	if m == nil || project == "" || math.IsNaN(cost) || math.IsInf(cost, 0) || cost <= 0 || cfg.DailyUSD <= 0 && cfg.MonthlyUSD <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	d := m.window(m.daily, project, now.Format("2006-01-02"))
	mo := m.window(m.monthly, project, now.Format("2006-01"))
	thresholds := append([]int(nil), cfg.AlertThresholds...)
	sort.Ints(thresholds)
	for _, item := range []struct {
		window  string
		state   *projectBudgetWindow
		ceiling float64
	}{{"day", &d, cfg.DailyUSD}, {"month", &mo, cfg.MonthlyUSD}} {
		item.state.tripped = item.ceiling > 0 && item.state.spent >= item.ceiling
		if item.ceiling <= 0 {
			continue
		}
		item.state.spent = roundUSD(item.state.spent + cost)
		for _, pct := range thresholds {
			if !item.state.alerts[pct] && item.state.spent >= item.ceiling*float64(pct)/100 {
				item.state.alerts[pct] = true
				log.Printf("event=project_budget action=threshold project=%q window=%s threshold_percent=%d ceiling_usd=%.6f", project, item.window, pct, item.ceiling)
			}
		}
		if !item.state.tripped && item.state.spent >= item.ceiling {
			item.state.tripped = true
			log.Printf("event=project_budget action=trip project=%q window=%s ceiling_usd=%.6f", project, item.window, item.ceiling)
		}
	}
	m.daily[project], m.monthly[project] = d, mo
}

func (m *projectBudgetManager) window(windows map[string]projectBudgetWindow, project, key string) projectBudgetWindow {
	w := windows[project]
	if w.key == key {
		return w
	}
	return projectBudgetWindow{key: key, alerts: map[int]bool{}}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"github.com/metrum-ai/router/pkg/detector"
)

const (
	maxRuleKeys          = 10000
	maxObservations      = 256
	evidenceLimit        = 8
	authFailureLimit     = 25
	tinyRequestLimit     = 20
	deniedGroupLimit     = 5
	spendPaceMultiple    = 6
	spendBaselineMinutes = 15
)

type observation struct {
	at          time.Time
	requestID   string
	deniedGroup string
	tokens      int64
}

type ruleState struct {
	events      []observation
	spend       map[int64]int64
	lastSignal  time.Time
	lastUpdated time.Time
}

type ruleDetector struct {
	mu     sync.Mutex
	states map[string]*ruleState
	now    func() time.Time
}

func newRuleDetector() *ruleDetector {
	return &ruleDetector{states: make(map[string]*ruleState), now: time.Now}
}

func (d *ruleDetector) Detect(_ context.Context, event detector.Event) ([]detector.Signal, error) {
	if err := detector.ValidateEvent(event); err != nil {
		return nil, err
	}
	now := d.now().UTC()
	at := now
	if event.ObservedAt != "" {
		parsed, err := time.Parse(time.RFC3339Nano, event.ObservedAt)
		if err != nil {
			return nil, err
		}
		at = parsed.UTC()
		if at.Before(now.Add(-spendBaselineMinutes*time.Minute)) || at.After(now.Add(time.Minute)) {
			return nil, nil
		}
	}
	obs := observation{at: at, requestID: event.RequestID, deniedGroup: event.DeniedGroup, tokens: event.Tokens}
	var signals []detector.Signal

	d.mu.Lock()
	defer d.mu.Unlock()
	d.prune(now)
	switch event.Kind {
	case "auth_failure":
		if event.KeyID != "" {
			if signal := d.record("auth_fail_storm", event.KeyID, obs); signal != nil {
				signals = append(signals, *signal)
			}
		}
		if event.SourceIP != "" {
			if signal := d.record("auth_fail_storm", "ip-"+event.SourceIP, obs); signal != nil {
				signals = append(signals, *signal)
			}
		}
	case "request":
		if event.TinyRequest && event.KeyID != "" {
			if signal := d.record("validation_sweep", event.KeyID, obs); signal != nil {
				signals = append(signals, *signal)
			}
		}
	case "model_denied":
		if event.KeyID != "" && event.DeniedGroup != "" {
			if signal := d.record("model_enumeration", event.KeyID, obs); signal != nil {
				signals = append(signals, *signal)
			}
		}
	case "usage":
		if event.KeyID != "" && event.Tokens > 0 {
			if signal := d.record("spend_spike", event.KeyID, obs); signal != nil {
				signals = append(signals, *signal)
			}
		}
	}
	return signals, nil
}

func (d *ruleDetector) record(rule, key string, event observation) *detector.Signal {
	stateKey := rule + ":" + key
	state := d.states[stateKey]
	if state == nil {
		if len(d.states) >= maxRuleKeys {
			d.evictOldest()
		}
		state = &ruleState{}
		d.states[stateKey] = state
	}
	state.events = append(state.events, event)
	if len(state.events) > maxObservations {
		state.events = append([]observation(nil), state.events[len(state.events)-maxObservations:]...)
	}
	state.lastUpdated = event.at
	if rule == "spend_spike" && event.tokens > 0 {
		if state.spend == nil {
			state.spend = make(map[int64]int64)
		}
		minute := event.at.Truncate(time.Minute).Unix()
		state.spend[minute] += event.tokens
	}

	threshold := time.Minute
	minimum := 0
	score := 0.5
	switch rule {
	case "auth_fail_storm":
		minimum = authFailureLimit
	case "validation_sweep":
		minimum = tinyRequestLimit
	case "model_enumeration":
		threshold = 5 * time.Minute
		minimum = deniedGroupLimit
	case "spend_spike":
		return d.spendSignal(rule, key, state, event)
	}
	active := activeObservations(state.events, event.at.Add(-threshold))
	triggered := false
	if rule == "model_enumeration" {
		groups := make(map[string]bool)
		for _, item := range active {
			if item.deniedGroup != "" {
				groups[item.deniedGroup] = true
			}
		}
		triggered = len(groups) >= minimum
		score = minFloat(1, float64(len(groups))/float64(minimum*2))
	} else {
		triggered = len(active) >= minimum
		score = minFloat(1, float64(len(active))/float64(minimum*2))
	}
	if !triggered || event.at.Sub(state.lastSignal) < threshold {
		return nil
	}
	state.lastSignal = event.at
	return makeSignal(rule, key, score, active)
}

func (d *ruleDetector) spendSignal(rule, key string, state *ruleState, event observation) *detector.Signal {
	minute := event.at.Truncate(time.Minute)
	current := state.spend[minute.Unix()]
	baselineTotal := int64(0)
	baselineMinutes := 0
	active := make([]observation, 0, evidenceLimit)
	for _, item := range state.events {
		itemMinute := item.at.Truncate(time.Minute)
		if itemMinute.Equal(minute) {
			active = append(active, item)
		}
	}
	for minuteKey := minute.Add(-spendBaselineMinutes * time.Minute).Unix(); minuteKey < minute.Unix(); minuteKey += 60 {
		if value := state.spend[minuteKey]; value > 0 {
			baselineTotal += value
			baselineMinutes++
		}
	}
	if baselineMinutes < 5 || baselineTotal <= 0 {
		return nil
	}
	baseline := float64(baselineTotal) / float64(baselineMinutes)
	if float64(current) < baseline*spendPaceMultiple || event.at.Sub(state.lastSignal) < time.Minute {
		return nil
	}
	state.lastSignal = event.at
	return makeSignal(rule, key, minFloat(1, float64(current)/(baseline*spendPaceMultiple*2)), active)
}

func activeObservations(events []observation, since time.Time) []observation {
	active := make([]observation, 0, len(events))
	for _, event := range events {
		if !event.at.Before(since) {
			active = append(active, event)
		}
	}
	return active
}

func makeSignal(rule, key string, score float64, events []observation) *detector.Signal {
	ids := make([]string, 0, evidenceLimit)
	for i := len(events) - 1; i >= 0 && len(ids) < evidenceLimit; i-- {
		if events[i].requestID != "" {
			ids = append(ids, events[i].requestID)
		}
	}
	sum := sha256.Sum256([]byte(rule + ":" + key))
	return &detector.Signal{SchemaVersion: detector.SchemaVersion, DetectorID: rule, Severity: "high", Score: score, Key: hex.EncodeToString(sum[:16]), EvidenceRequestIDs: ids}
}

func (d *ruleDetector) prune(now time.Time) {
	for key, state := range d.states {
		cutoff := now.Add(-spendBaselineMinutes * time.Minute)
		state.events = activeObservations(state.events, cutoff)
		for minute := range state.spend {
			if minute < cutoff.Truncate(time.Minute).Unix() {
				delete(state.spend, minute)
			}
		}
		if len(state.events) == 0 && now.Sub(state.lastUpdated) > spendBaselineMinutes*time.Minute {
			delete(d.states, key)
		}
	}
}

func (d *ruleDetector) evictOldest() {
	keys := make([]string, 0, len(d.states))
	for key := range d.states {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return d.states[keys[i]].lastUpdated.Before(d.states[keys[j]].lastUpdated) })
	if len(keys) > 0 {
		delete(d.states, keys[0])
	}
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"container/list"
	"context"
	"sync"
	"time"

	"github.com/metrum-ai/router/pkg/detector"
)

const (
	authFailureLimit     = 25
	tinyRequestLimit     = 20
	deniedGroupLimit     = 5
	spendPaceMultiple    = 6
	maxRuleKeys          = 10000
	maxObservations      = 256
	evidenceLimit        = 8
	spendBaselineMinutes = 15
	maxActiveWindow      = 15 * time.Minute
	authKeyState         = "auth_fail_storm/key"
	authIPState          = "auth_fail_storm/ip"
)

type thresholds struct {
	authFailures, tinyRequests, deniedGroups, spendMultiple int
}

var defaultThresholds = thresholds{authFailures: authFailureLimit, tinyRequests: tinyRequestLimit, deniedGroups: deniedGroupLimit, spendMultiple: spendPaceMultiple}

type observation struct {
	at          time.Time
	requestID   string
	deniedGroup string
	tokens      int64
}

type ruleState struct {
	events     []observation
	spend      map[int64]int64
	lastSignal time.Time
	element    *list.Element
}

type ruleStates struct {
	byKey map[string]*ruleState
	lru   *list.List
}

type ruleDetector struct {
	mu         sync.Mutex
	states     map[string]*ruleStates
	now        func() time.Time
	thresholds thresholds
	maxKeys    int
}

func newRuleDetector() *ruleDetector {
	return newRuleDetectorWithThresholds(defaultThresholds)
}

func newRuleDetectorWithThresholds(config thresholds) *ruleDetector {
	return &ruleDetector{states: make(map[string]*ruleStates), now: time.Now, thresholds: config, maxKeys: maxRuleKeys}
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
		if at.Before(now.Add(-spendBaselineMinutes*time.Minute)) || at.After(now) {
			return nil, nil
		}
	}
	obs := observation{at: at, requestID: event.RequestID, deniedGroup: event.DeniedGroup, tokens: event.Tokens}
	var signals []detector.Signal

	d.mu.Lock()
	defer d.mu.Unlock()
	switch event.Kind {
	case "auth_failure":
		if event.KeyID != "" {
			if signal := d.recordInScope("auth_fail_storm", authKeyState, event.KeyID, event.KeyID, obs, now); signal != nil {
				signals = append(signals, *signal)
			}
		}
		if event.SourceIP != "" {
			if signal := d.recordInScope("auth_fail_storm", authIPState, "ip-"+event.SourceIP, "", obs, now); signal != nil {
				signals = append(signals, *signal)
			}
		}
	case "request":
		if event.TinyRequest && event.KeyID != "" {
			if signal := d.record("validation_sweep", event.KeyID, event.KeyID, obs, now); signal != nil {
				signals = append(signals, *signal)
			}
		}
	case "model_denied":
		if event.KeyID != "" && event.DeniedGroup != "" {
			if signal := d.record("model_enumeration", event.KeyID, event.KeyID, obs, now); signal != nil {
				signals = append(signals, *signal)
			}
		}
	case "usage":
		if event.KeyID != "" && event.Tokens > 0 {
			if signal := d.record("spend_spike", event.KeyID, event.KeyID, obs, now); signal != nil {
				signals = append(signals, *signal)
			}
		}
	}
	return signals, nil
}

func (d *ruleDetector) record(rule, key, signalKey string, event observation, now time.Time) *detector.Signal {
	return d.recordInScope(rule, rule, key, signalKey, event, now)
}

func (d *ruleDetector) recordInScope(rule, scope, key, signalKey string, event observation, now time.Time) *detector.Signal {
	groupStates := d.states[scope]
	if groupStates == nil {
		groupStates = &ruleStates{byKey: make(map[string]*ruleState), lru: list.New()}
		d.states[scope] = groupStates
	}
	state := groupStates.byKey[key]
	if state == nil {
		for groupStates.lru.Len() >= d.maxKeys {
			oldest := groupStates.lru.Back()
			delete(groupStates.byKey, oldest.Value.(string))
			groupStates.lru.Remove(oldest)
		}
		state = &ruleState{}
		groupStates.byKey[key] = state
		state.element = groupStates.lru.PushFront(key)
	} else {
		groupStates.lru.MoveToFront(state.element)
	}
	cutoff := now.Add(-maxActiveWindow)
	first := 0
	for first < len(state.events) && state.events[first].at.Before(cutoff) {
		first++
	}
	if first > 0 {
		state.events = append([]observation(nil), state.events[first:]...)
	}
	state.events = append(state.events, event)
	if len(state.events) > maxObservations {
		state.events = append([]observation(nil), state.events[len(state.events)-maxObservations:]...)
	}
	if rule == "spend_spike" && event.tokens > 0 {
		if state.spend == nil {
			state.spend = make(map[int64]int64)
		}
		minute := event.at.Truncate(time.Minute).Unix()
		state.spend[minute] += event.tokens
		oldestMinute := now.Add(-maxActiveWindow).Truncate(time.Minute).Unix()
		for recordedMinute := range state.spend {
			if recordedMinute < oldestMinute {
				delete(state.spend, recordedMinute)
			}
		}
	}

	threshold := time.Minute
	minimum := 0
	score := 0.5
	switch rule {
	case "auth_fail_storm":
		minimum = d.thresholds.authFailures
		score = .85
	case "validation_sweep":
		minimum = d.thresholds.tinyRequests
	case "model_enumeration":
		threshold = 5 * time.Minute
		minimum = d.thresholds.deniedGroups
		score = .55
	case "spend_spike":
		return d.spendSignal(rule, signalKey, state, event)
	}
	active := activeObservations(state.events, now.Add(-threshold), now)
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
	severity := "high"
	if rule == "model_enumeration" {
		severity = "medium"
	}
	return makeSignal(rule, signalKey, severity, score, active)
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
	if float64(current) < baseline*float64(d.thresholds.spendMultiple) || event.at.Sub(state.lastSignal) < time.Minute {
		return nil
	}
	state.lastSignal = event.at
	return makeSignal(rule, key, "medium", minFloat(1, float64(current)/(baseline*float64(d.thresholds.spendMultiple*2))), active)
}

func activeObservations(events []observation, since, evaluatedAt time.Time) []observation {
	active := make([]observation, 0, len(events))
	for _, event := range events {
		if !event.at.Before(since) && !event.at.After(evaluatedAt) {
			active = append(active, event)
		}
	}
	return active
}

func makeSignal(rule, key, severity string, score float64, events []observation) *detector.Signal {
	ids := make([]string, 0, evidenceLimit)
	seen := make(map[string]bool, evidenceLimit)
	for i := len(events) - 1; i >= 0 && len(ids) < evidenceLimit; i-- {
		if events[i].requestID != "" && !seen[events[i].requestID] {
			ids = append(ids, events[i].requestID)
			seen[events[i].requestID] = true
		}
	}
	return &detector.Signal{SchemaVersion: detector.SchemaVersion, DetectorID: rule, Severity: severity, Score: score, Key: key, EvidenceRequestIDs: ids}
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

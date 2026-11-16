// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/metrum-ai/router/pkg/detector"
)

func TestAuthFailureStormTriggersAtThreshold(t *testing.T) {
	service := newRuleDetector()
	service.now = func() time.Time { return time.Date(2026, 11, 16, 1, 0, 30, 0, time.UTC) }
	var signals []detector.Signal
	for i := 1; i <= authFailureLimit; i++ {
		got, err := service.Detect(context.Background(), testEvent("auth_failure", i, ""))
		if err != nil {
			t.Fatal(err)
		}
		signals = append(signals, got...)
	}
	assertRule(t, signals, "auth_fail_storm")
}

func TestAuthFailureStormSignalsOpaqueKeyAndIPScopes(t *testing.T) {
	service := newRuleDetector()
	service.now = func() time.Time { return time.Date(2026, 11, 16, 1, 0, 30, 0, time.UTC) }
	event := testEvent("auth_failure", 1, "")
	var signals []detector.Signal
	for i := 1; i <= authFailureLimit; i++ {
		event.RequestID = fmt.Sprintf("scope-%02d", i)
		got, err := service.Detect(context.Background(), event)
		if err != nil {
			t.Fatal(err)
		}
		signals = append(signals, got...)
	}
	var keySignal, ipSignal *detector.Signal
	for i := range signals {
		signal := &signals[i]
		if signal.DetectorID != "auth_fail_storm" {
			continue
		}
		if signal.Key == event.KeyID {
			keySignal = signal
		}
		if signal.Key == "" {
			ipSignal = signal
		}
	}
	if keySignal == nil || ipSignal == nil {
		t.Fatalf("expected opaque-key and empty-key IP signals, got %+v", signals)
	}
	if len(keySignal.EvidenceRequestIDs) != evidenceLimit || len(ipSignal.EvidenceRequestIDs) != evidenceLimit {
		t.Fatalf("evidence lists are not bounded at %d: key=%+v ip=%+v", evidenceLimit, keySignal.EvidenceRequestIDs, ipSignal.EvidenceRequestIDs)
	}
}

func TestValidationSweepTriggersAndBenignRequestsDoNot(t *testing.T) {
	service := newRuleDetector()
	service.now = func() time.Time { return time.Date(2026, 11, 16, 1, 0, 30, 0, time.UTC) }
	for i := 0; i < tinyRequestLimit-1; i++ {
		event := testEvent("request", i, "")
		event.TinyRequest = false
		if got, err := service.Detect(context.Background(), event); err != nil || len(got) != 0 {
			t.Fatalf("benign request signals=%v err=%v", got, err)
		}
	}
	for i := 1; i <= tinyRequestLimit; i++ {
		event := testEvent("request", i, "")
		event.TinyRequest = true
		got, err := service.Detect(context.Background(), event)
		if err != nil {
			t.Fatal(err)
		}
		if i == tinyRequestLimit {
			assertRule(t, got, "validation_sweep")
		}
	}
}

func TestModelEnumerationNeedsDistinctDeniedGroups(t *testing.T) {
	service := newRuleDetector()
	service.now = func() time.Time { return time.Date(2026, 11, 16, 1, 0, 30, 0, time.UTC) }
	for i := 1; i <= deniedGroupLimit; i++ {
		event := testEvent("model_denied", i, "")
		event.DeniedGroup = fmt.Sprintf("group-%d", i)
		got, err := service.Detect(context.Background(), event)
		if err != nil {
			t.Fatal(err)
		}
		if i < deniedGroupLimit && len(got) != 0 {
			t.Fatalf("signal before distinct-group threshold: %v", got)
		}
		if i == deniedGroupLimit {
			assertRule(t, got, "model_enumeration")
		}
	}
}

func TestSpendSpikeWaitsForBaselineThenTriggers(t *testing.T) {
	base := time.Date(2026, 11, 16, 1, 0, 30, 0, time.UTC)
	service := newRuleDetector()
	service.now = func() time.Time { return base.Add(6 * time.Minute) }
	for i := 0; i < 5; i++ {
		event := testEvent("usage", i, base.Add(time.Duration(i)*time.Minute).Format(time.RFC3339Nano))
		event.Tokens = 100
		got, err := service.Detect(context.Background(), event)
		if err != nil || len(got) != 0 {
			t.Fatalf("baseline signal=%v err=%v", got, err)
		}
	}
	event := testEvent("usage", 9, base.Add(5*time.Minute).Format(time.RFC3339Nano))
	event.Tokens = 600
	got, err := service.Detect(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	assertRule(t, got, "spend_spike")
}

func TestNewKeysHaveNoSpendBaseline(t *testing.T) {
	service := newRuleDetector()
	service.now = func() time.Time { return time.Date(2026, 11, 16, 1, 0, 30, 0, time.UTC) }
	event := testEvent("usage", 1, "")
	event.Tokens = 10_000
	got, err := service.Detect(context.Background(), event)
	if err != nil || len(got) != 0 {
		t.Fatalf("cold-start signal=%v err=%v", got, err)
	}
}

func TestBenignTrafficTriggersNothing(t *testing.T) {
	service := newRuleDetector()
	service.now = func() time.Time { return time.Date(2026, 11, 16, 1, 0, 30, 0, time.UTC) }
	for i := 0; i < 100; i++ {
		event := testEvent("request", i, "")
		event.TinyRequest = false
		got, err := service.Detect(context.Background(), event)
		if err != nil || len(got) != 0 {
			t.Fatalf("benign event signals=%v err=%v", got, err)
		}
	}
}

func TestRealisticMixedBenignTrafficTriggersNothing(t *testing.T) {
	service := newRuleDetector()
	base := time.Date(2026, 11, 16, 1, 0, 30, 0, time.UTC)
	for i := 0; i < 15; i++ {
		at := base.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano)
		inputs := []detector.Event{testEvent("auth_failure", i, at)}
		normal := testEvent("request", i, at)
		normal.TinyRequest = false
		inputs = append(inputs, normal)
		if i < 5 {
			tiny := testEvent("request", 100+i, at)
			tiny.TinyRequest = true
			inputs = append(inputs, tiny)
		}
		if i < deniedGroupLimit-1 {
			denied := testEvent("model_denied", 200+i, at)
			denied.DeniedGroup = fmt.Sprintf("group-%d", i)
			inputs = append(inputs, denied)
		}
		usage := testEvent("usage", i, at)
		usage.Tokens = 100
		inputs = append(inputs, usage)
		service.now = func() time.Time { return base.Add(time.Minute) }
		for _, event := range inputs {
			got, err := service.Detect(context.Background(), event)
			if err != nil || len(got) != 0 {
				t.Fatalf("benign mixed %s event signaled: %v err=%v", event.Kind, got, err)
			}
		}
	}
}

func TestRuleKeyEvictionIsIsolated(t *testing.T) {
	service := newRuleDetector()
	service.maxKeys = 2
	service.now = func() time.Time { return time.Date(2026, 11, 16, 1, 0, 30, 0, time.UTC) }
	for i := 0; i < 2; i++ {
		event := testEvent("usage", i, "")
		event.KeyID = fmt.Sprintf("%032x", i+1)
		event.Tokens = 100
		_, _ = service.Detect(context.Background(), event)
	}
	for i := 0; i < 3; i++ {
		event := testEvent("model_denied", 100+i, "")
		event.KeyID = fmt.Sprintf("%032x", i+10)
		event.DeniedGroup = fmt.Sprintf("group-%d", i)
		_, _ = service.Detect(context.Background(), event)
	}
	if len(service.states["spend_spike"].byKey) != 2 {
		t.Fatalf("enumeration churn evicted spend state: %d keys remain", len(service.states["spend_spike"].byKey))
	}
	if len(service.states["model_enumeration"].byKey) != 2 {
		t.Fatalf("enumeration LRU did not enforce its own cap: %d keys", len(service.states["model_enumeration"].byKey))
	}
}

func testEvent(kind string, id int, observed string) detector.Event {
	event := detector.Event{
		SchemaVersion: detector.SchemaVersion,
		RequestID:     fmt.Sprintf("request-%d", id),
		CallerID:      "caller-1",
		Kind:          kind,
		KeyID:         strings.Repeat("a", 32),
		SourceIP:      "203.0.113.8",
		ObservedAt:    observed,
	}
	return event
}

func assertRule(t *testing.T, signals []detector.Signal, rule string) {
	t.Helper()
	for _, signal := range signals {
		if signal.DetectorID == rule {
			if signal.Severity == "" || signal.Key == "" || len(signal.EvidenceRequestIDs) == 0 {
				t.Fatalf("incomplete %s signal: %+v", rule, signal)
			}
			wantSeverity := "high"
			if rule == "model_enumeration" || rule == "spend_spike" {
				wantSeverity = "medium"
			}
			if signal.Severity != wantSeverity {
				t.Fatalf("%s severity=%q, want %q", rule, signal.Severity, wantSeverity)
			}
			return
		}
	}
	t.Fatalf("missing %s signal in %+v", rule, signals)
}

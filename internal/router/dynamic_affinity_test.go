// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"testing"
	"time"
)

func TestSA1DynamicScoreSameCallerPrefixStaysOnFirstTarget(t *testing.T) {
	svc, group, caller := dynamicAffinityTestSetup(t)
	turn1 := &IRRequest{System: "be concise", Messages: []IRMessage{{Role: "user", Content: "same task"}}}
	first, err := svc.pick(nil, "adaptive", group, turn1, "openai-chat", caller, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Target.Model != "first" {
		t.Fatalf("first target=%q, want first", first.Target.Model)
	}
	pinSuccessfulAffinity(svc, first, group)

	warmDynamicAffinityCandidates(svc)
	turn2 := &IRRequest{System: "be concise", Messages: []IRMessage{
		{Role: "user", Content: "same task"},
		{Role: "assistant", Content: "first answer"},
		{Role: "user", Content: "follow up"},
	}}
	second, err := svc.pick(nil, "adaptive", group, turn2, "openai-chat", caller, "")
	if err != nil {
		t.Fatal(err)
	}
	if second.Target.Model != "first" {
		t.Fatalf("second target=%q, want affinity-pinned first", second.Target.Model)
	}
	if !routingSignalsContain(second.RoutingSignals, "affinity_hit") {
		t.Fatalf("missing affinity_hit telemetry: %#v", second.RoutingSignals)
	}
}

func TestSA2DynamicScoreRepicksAfterAffinityTTL(t *testing.T) {
	svc, group, caller := dynamicAffinityTestSetup(t)
	now := time.Now().UTC()
	svc.affinity.now = func() time.Time { return now }
	turn1 := &IRRequest{Messages: []IRMessage{{Role: "user", Content: "stable prefix"}}}
	decision, err := svc.pick(nil, "adaptive", group, turn1, "openai-chat", caller, "")
	if err != nil {
		t.Fatal(err)
	}
	pinSuccessfulAffinity(svc, decision, group)
	warmDynamicAffinityCandidates(svc)
	now = now.Add(2 * time.Second)
	turn2 := &IRRequest{Messages: []IRMessage{{Role: "user", Content: "stable prefix"}, {Role: "assistant", Content: "answer"}, {Role: "user", Content: "next"}}}
	decision, err = svc.pick(nil, "adaptive", group, turn2, "openai-chat", caller, "")
	if err != nil {
		t.Fatal(err)
	}
	if decision.Target.Model != "cheapest" {
		t.Fatalf("target after TTL=%q, want cheapest re-pick", decision.Target.Model)
	}
	if !routingSignalsContain(decision.RoutingSignals, "affinity_expired") {
		t.Fatalf("missing affinity_expired telemetry: %#v", decision.RoutingSignals)
	}
}

func TestSA3DynamicScoreAffinityIsolatedByCaller(t *testing.T) {
	svc, group, callerA := dynamicAffinityTestSetup(t)
	req := &IRRequest{Messages: []IRMessage{{Role: "user", Content: "shared prefix"}}}
	decisionA, err := svc.pick(nil, "adaptive", group, req, "openai-chat", callerA, "")
	if err != nil {
		t.Fatal(err)
	}
	pinSuccessfulAffinity(svc, decisionA, group)
	warmDynamicAffinityCandidates(svc)
	callerB := &callerRuntime{cfg: CallerConfig{ID: "caller-b"}}
	decision, err := svc.pick(nil, "adaptive", group, req, "openai-chat", callerB, "")
	if err != nil {
		t.Fatal(err)
	}
	if decision.Target.Model != "cheapest" {
		t.Fatalf("caller B inherited caller A pin: target=%q", decision.Target.Model)
	}
	if routingSignalsContain(decision.RoutingSignals, "affinity_hit") {
		t.Fatalf("caller B unexpectedly hit caller A affinity: %#v", decision.RoutingSignals)
	}
}

func TestSA4ChatToResponsesBridgeStickinessUnaffected(t *testing.T) {
	svc, group, caller := dynamicAffinityTestSetup(t)
	req := &IRRequest{Messages: []IRMessage{{Role: "user", Content: "bridge regression"}}}
	_, _ = svc.pick(nil, "adaptive", group, req, "openai-chat", caller, "")
	target := Target{Provider: "responses", Model: "responses-model", Dialect: "openai-responses"}
	rc := &requestContext{caller: caller, rec: logRecord{TokenID: "token-public"}}
	key := bridgeSessionKey(rc, "bridge-group", target, "session-alpha")
	if err := svc.bridgeSessions.memory.Set(context.Background(), key, "resp_previous", time.Minute, 10); err != nil {
		t.Fatal(err)
	}
	entry, ok, err := svc.bridgeSessions.memory.Get(context.Background(), key)
	if err != nil || !ok || entry.PreviousResponseID != "resp_previous" {
		t.Fatalf("bridge session changed by dynamic affinity: entry=%#v ok=%v err=%v", entry, ok, err)
	}
	if got := bridgeSessionKey(rc, "bridge-group", target, "session-alpha"); got != key {
		t.Fatalf("bridge key changed: got=%q want=%q", got, key)
	}
}

func pinSuccessfulAffinity(svc *Service, dec decision, group ModelGroup) {
	if svc == nil || svc.affinity == nil || dec.AffinityKey == "" {
		return
	}
	for i, target := range group.Targets {
		if target.Provider == dec.Target.Provider && target.Model == dec.Target.Model {
			svc.affinity.set(dec.AffinityKey, dynamicCandidate{Target: target, Index: i}, dynamicAffinityTTL(dec.AffinityConfig), dynamicAffinityMaxEntries(dec.AffinityConfig))
			return
		}
	}
}

func TestDynamicScoreAffinitySkipsIneligiblePinnedTarget(t *testing.T) {
	svc, group, caller := dynamicAffinityTestSetup(t)
	req := &IRRequest{Messages: []IRMessage{{Role: "user", Content: "pin then drop"}}}
	decision, err := svc.pick(nil, "adaptive", group, req, "openai-chat", caller, "")
	if err != nil {
		t.Fatal(err)
	}
	pinSuccessfulAffinity(svc, decision, group)
	warmDynamicAffinityCandidates(svc)
	// Drop the pinned target from eligibility by requiring tools the pin lacks.
	group.Targets[0].ToolSupport = ToolSupport{}
	group.Targets[1].ToolSupport = ToolSupport{OpenAIChat: []string{"tools"}}
	toolReq := &IRRequest{
		Messages: []IRMessage{{Role: "user", Content: "pin then drop"}, {Role: "assistant", Content: "ok"}, {Role: "user", Content: "with tools"}},
		Tools:    []map[string]any{{"type": "function", "function": map[string]any{"name": "lookup"}}},
	}
	decision, err = svc.pick(nil, "adaptive", group, toolReq, "openai-chat", caller, "")
	if err != nil {
		t.Fatal(err)
	}
	if decision.Target.Model != "cheapest" {
		t.Fatalf("ineligible pin selected %q, want cheapest", decision.Target.Model)
	}
	if routingSignalsContain(decision.RoutingSignals, "affinity_hit") {
		t.Fatalf("affinity must not widen eligibility: %#v", decision.RoutingSignals)
	}
}

func TestDynamicScoreAffinityCanBeDisabled(t *testing.T) {
	svc, group, caller := dynamicAffinityTestSetup(t)
	disabled := false
	group.RoutingPolicy.DynamicScore.Affinity.Enabled = &disabled
	req := &IRRequest{Messages: []IRMessage{{Role: "user", Content: "no pin"}}}
	if _, err := svc.pick(nil, "adaptive", group, req, "openai-chat", caller, ""); err != nil {
		t.Fatal(err)
	}
	warmDynamicAffinityCandidates(svc)
	decision, err := svc.pick(nil, "adaptive", group, req, "openai-chat", caller, "")
	if err != nil {
		t.Fatal(err)
	}
	if decision.Target.Model != "cheapest" {
		t.Fatalf("disabled affinity selected %q, want cheapest", decision.Target.Model)
	}
	if !routingSignalsContain(decision.RoutingSignals, "affinity_disabled") {
		t.Fatalf("missing affinity_disabled telemetry: %#v", decision.RoutingSignals)
	}
}

func dynamicAffinityTestSetup(t *testing.T) (*Service, ModelGroup, *callerRuntime) {
	t.Helper()
	svc := newTestService(t, "http://127.0.0.1:1", "provider-key")
	t.Cleanup(svc.Close)
	group := ModelGroup{
		Strategy: "dynamic_score",
		RoutingPolicy: RoutingPolicyConfig{DynamicScore: DynamicScoreConfig{
			MinObservations:           2,
			MaxScoreAdjustmentPercent: 100,
			Affinity:                  DynamicScoreAffinityConfig{TTLSeconds: 1, MaxEntries: 100},
			ScoreTerms: []DynamicScoreTerm{{
				Name:       "cost",
				Expression: "1.0 * cost_score",
			}},
		}},
		Targets: []Target{
			{Provider: "mock", Model: "first", Weight: 100, InputPricePerMillionUSD: 10, OutputPricePerMillionUSD: 10},
			{Provider: "mock", Model: "cheapest", Weight: 1, InputPricePerMillionUSD: 0.1, OutputPricePerMillionUSD: 0.1},
		},
	}
	return svc, group, &callerRuntime{cfg: CallerConfig{ID: "caller-a"}}
}

func warmDynamicAffinityCandidates(svc *Service) {
	now := time.Now().UTC()
	if svc != nil && svc.affinity != nil && svc.affinity.now != nil {
		now = svc.affinity.now().UTC()
	}
	for _, model := range []string{"first", "cheapest"} {
		svc.observations.add(dynamicObservationKey("adaptive", "mock", model), dynamicObservation{TS: now, Status: 200, LatencyMS: 100}, defaultDynamicObservationWindow)
	}
}

func routingSignalsContain(signals []routingSignalLogRecord, name string) bool {
	for _, signal := range signals {
		if signal.SignalName == name {
			return true
		}
	}
	return false
}

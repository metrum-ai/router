// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDynamicScoreColdStartUsesConfiguredWeightsDeterministically(t *testing.T) {
	svc := newTestService(t, "http://127.0.0.1:1", "provider-key")
	defer svc.Close()
	group := ModelGroup{
		Strategy: "dynamic_score",
		RoutingPolicy: RoutingPolicyConfig{DynamicScore: DynamicScoreConfig{
			MinObservations: 20,
		}},
		Targets: []Target{
			{Provider: "mock", Model: "cheap", Weight: 10},
			{Provider: "mock", Model: "preferred", Weight: 50},
			{Provider: "mock", Model: "expensive", Weight: 1},
		},
	}
	req := &IRRequest{Messages: []IRMessage{{Role: "user", Content: "hello"}}}
	for i := 0; i < 10; i++ {
		dec, err := svc.pick(nil, "default", group, req, "openai-chat", nil, "")
		if err != nil {
			t.Fatal(err)
		}
		if dec.Target.Model != "preferred" {
			t.Fatalf("pick %d selected %q, want configured highest weight", i, dec.Target.Model)
		}
		if !strings.Contains(dec.DecisionTrace, `"cold_start":true`) {
			t.Fatalf("decision trace did not mark cold start: %s", dec.DecisionTrace)
		}
	}
}

func TestDynamicScoreRanksHealthyCheapTargetAfterObservations(t *testing.T) {
	svc := newTestService(t, "http://127.0.0.1:1", "provider-key")
	defer svc.Close()
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		svc.observations.add(dynamicObservationKey("default", "mock", "cheap-fast"), dynamicObservation{
			TS:                 now,
			Status:             http.StatusOK,
			LatencyMS:          900,
			UpstreamMS:         850,
			OutputTokensPerSec: 80,
		}, defaultDynamicObservationWindow)
		svc.observations.add(dynamicObservationKey("default", "mock", "expensive-fastest"), dynamicObservation{
			TS:                 now,
			Status:             http.StatusOK,
			LatencyMS:          700,
			UpstreamMS:         650,
			OutputTokensPerSec: 100,
		}, defaultDynamicObservationWindow)
	}
	group := ModelGroup{
		Strategy: "dynamic_score",
		RoutingPolicy: RoutingPolicyConfig{DynamicScore: DynamicScoreConfig{
			MinObservations:           2,
			MaxScoreAdjustmentPercent: 100,
			Signals: DynamicScoreSignals{
				Cost:                DynamicSignalNamedFields{Enabled: true},
				ObservedPerformance: DynamicSignalObservedPerformance{Enabled: true},
			},
			ScoreTerms: []DynamicScoreTerm{{
				Name:       "cheapest_fast_enough",
				Expression: "0.80 * cost_score + 0.10 * latency_score + 0.10 * throughput_score",
			}},
		}},
		Targets: []Target{
			{Provider: "mock", Model: "cheap-fast", Weight: 1, InputPricePerMillionUSD: 0.1, OutputPricePerMillionUSD: 0.2},
			{Provider: "mock", Model: "expensive-fastest", Weight: 1, InputPricePerMillionUSD: 5, OutputPricePerMillionUSD: 10},
		},
	}
	dec, err := svc.pick(nil, "default", group, &IRRequest{Input: "summarize this"}, "openai-chat", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Target.Model != "cheap-fast" {
		t.Fatalf("selected %q, want cheaper healthy target", dec.Target.Model)
	}
}

func TestDynamicScoreZeroAdjustmentKeepsConfiguredWeightsAfterWarmup(t *testing.T) {
	svc := newTestService(t, "http://127.0.0.1:1", "provider-key")
	defer svc.Close()
	now := time.Now().UTC()
	for _, model := range []string{"configured-primary", "cheap-dynamic"} {
		svc.observations.add(dynamicObservationKey("default", "mock", model), dynamicObservation{TS: now, Status: http.StatusOK, LatencyMS: 100}, defaultDynamicObservationWindow)
	}
	group := ModelGroup{
		Strategy: "dynamic_score",
		RoutingPolicy: RoutingPolicyConfig{DynamicScore: DynamicScoreConfig{
			MinObservations:           1,
			MaxScoreAdjustmentPercent: 0,
			ScoreTerms: []DynamicScoreTerm{{
				Name:       "cost",
				Expression: "1.0 * cost_score",
			}},
		}},
		Targets: []Target{
			{Provider: "mock", Model: "configured-primary", Weight: 100, InputPricePerMillionUSD: 10, OutputPricePerMillionUSD: 10},
			{Provider: "mock", Model: "cheap-dynamic", Weight: 1, InputPricePerMillionUSD: 0.1, OutputPricePerMillionUSD: 0.1},
		},
	}
	dec, err := svc.pick(nil, "default", group, &IRRequest{Input: "hello"}, "openai-chat", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Target.Model != "configured-primary" {
		t.Fatalf("selected %q, want configured-weight target when max adjustment is zero", dec.Target.Model)
	}
}

func TestDynamicScoreThresholdsFilterUnhealthyTargetInsideRequestedGroup(t *testing.T) {
	svc := newTestService(t, "http://127.0.0.1:1", "provider-key")
	defer svc.Close()
	now := time.Now().UTC()
	for i := 0; i < 4; i++ {
		status := http.StatusOK
		errClass := ""
		if i < 2 {
			status = http.StatusBadGateway
			errClass = "upstream_failed"
		}
		svc.observations.add(dynamicObservationKey("default", "mock", "cheap-unhealthy"), dynamicObservation{
			TS:         now,
			Status:     status,
			LatencyMS:  250,
			ErrorClass: errClass,
		}, defaultDynamicObservationWindow)
		svc.observations.add(dynamicObservationKey("default", "mock", "healthy"), dynamicObservation{
			TS:                 now,
			Status:             http.StatusOK,
			LatencyMS:          600,
			OutputTokensPerSec: 40,
		}, defaultDynamicObservationWindow)
	}
	maxError := 0.10
	group := ModelGroup{
		Strategy: "dynamic_score",
		RoutingPolicy: RoutingPolicyConfig{DynamicScore: DynamicScoreConfig{
			MinObservations: 2,
			Thresholds:      DynamicScoreThresholds{MaxErrorRate: &maxError},
		}},
		Targets: []Target{
			{Provider: "mock", Model: "cheap-unhealthy", Weight: 100, InputPricePerMillionUSD: 0.1, OutputPricePerMillionUSD: 0.2},
			{Provider: "mock", Model: "healthy", Weight: 1, InputPricePerMillionUSD: 2, OutputPricePerMillionUSD: 4},
		},
	}
	dec, err := svc.pick(nil, "default", group, &IRRequest{Input: "hello"}, "openai-chat", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Target.Model != "healthy" {
		t.Fatalf("selected %q, want unhealthy target filtered", dec.Target.Model)
	}
}

func TestDynamicScoreRecordsFailedPrimaryAndSuccessfulFallbackSeparately(t *testing.T) {
	svc := newTestService(t, "http://127.0.0.1:1", "provider-key")
	defer svc.Close()
	svc.cfg.Models["default"] = ModelGroup{
		Strategy: "dynamic_score",
		RoutingPolicy: RoutingPolicyConfig{DynamicScore: DynamicScoreConfig{
			ObservationWindowSeconds: 600,
		}},
		Targets: []Target{
			{Provider: "mock", Model: "primary"},
			{Provider: "mock", Model: "fallback"},
		},
	}
	svc.recordDynamicObservation(logRecord{
		ResolvedGroup: "default",
		Status:        http.StatusOK,
		FallbackUsed:  true,
		AttemptsDetail: []attemptLogRecord{
			{Index: 1, Provider: "mock", Model: "primary", StatusCode: http.StatusBadGateway, DurationMS: 50, ErrorClass: "upstream_failed", FallbackReason: "upstream_failed"},
			{Index: 2, Provider: "mock", Model: "fallback", StatusCode: http.StatusOK, DurationMS: 40, Selected: true},
		},
	})
	primary := svc.observations.stats(dynamicObservationKey("default", "mock", "primary"), defaultDynamicObservationWindow)
	fallback := svc.observations.stats(dynamicObservationKey("default", "mock", "fallback"), defaultDynamicObservationWindow)
	if primary.Count != 1 || primary.ErrorRate != 1 {
		t.Fatalf("primary stats=%#v, want one failed observation", primary)
	}
	if fallback.Count != 1 || fallback.ErrorRate != 0 {
		t.Fatalf("fallback stats=%#v, want one successful observation", fallback)
	}
}

func TestDynamicScoreIgnoresResponseCacheHits(t *testing.T) {
	svc := newTestService(t, "http://127.0.0.1:1", "provider-key")
	defer svc.Close()
	svc.cfg.Models["default"] = ModelGroup{
		Strategy: "dynamic_score",
		RoutingPolicy: RoutingPolicyConfig{DynamicScore: DynamicScoreConfig{
			ObservationWindowSeconds: 600,
		}},
		Targets: []Target{{Provider: "mock", Model: "cached-target"}},
	}
	ttfb := int64(2)
	tps := 0.0
	svc.recordDynamicObservation(logRecord{
		ResolvedGroup:     "default",
		TargetProvider:    "mock",
		TargetModel:       "cached-target",
		Status:            http.StatusOK,
		Cache:             "hit",
		LatencyMS:         2,
		TTFBMS:            &ttfb,
		UpstreamOutputTPS: &tps,
		AttemptsDetail:    nil,
	})
	stats := svc.observations.stats(dynamicObservationKey("default", "mock", "cached-target"), defaultDynamicObservationWindow)
	if stats.Count != 0 {
		t.Fatalf("cache-hit observation counted: %#v", stats)
	}
}

func TestDynamicScoreRetainsConfiguredObservationWindowAtWriteTime(t *testing.T) {
	svc := newTestService(t, "http://127.0.0.1:1", "provider-key")
	defer svc.Close()
	svc.cfg.Models["default"] = ModelGroup{
		Strategy: "dynamic_score",
		RoutingPolicy: RoutingPolicyConfig{DynamicScore: DynamicScoreConfig{
			ObservationWindowSeconds: 3600,
		}},
		Targets: []Target{{Provider: "mock", Model: "slow-traffic"}},
	}
	key := dynamicObservationKey("default", "mock", "slow-traffic")
	svc.observations.records[key] = []dynamicObservation{{
		TS:        time.Now().UTC().Add(-20 * time.Minute),
		Status:    http.StatusOK,
		LatencyMS: 100,
	}}
	svc.recordDynamicObservation(logRecord{
		ResolvedGroup:  "default",
		TargetProvider: "mock",
		TargetModel:    "slow-traffic",
		Status:         http.StatusOK,
		LatencyMS:      120,
		AttemptsDetail: nil,
	})
	stats := svc.observations.stats(key, time.Hour)
	if stats.Count != 2 {
		t.Fatalf("stats.Count=%d, want 2 observations retained for configured one-hour window", stats.Count)
	}
}

func TestDynamicScoreHardFiltersForcedToolsAndStructuredOutput(t *testing.T) {
	svc := newTestService(t, "http://127.0.0.1:1", "provider-key")
	defer svc.Close()
	group := ModelGroup{
		Strategy: "dynamic_score",
		RoutingPolicy: RoutingPolicyConfig{DynamicScore: DynamicScoreConfig{
			MinObservations: 1,
			HardFilters: DynamicScoreHardFilters{
				RequireForcedToolChoiceSupport: true,
				RequireStructuredOutputSupport: true,
			},
		}},
		Targets: []Target{
			{Provider: "mock", Model: "auto-only", Weight: 100, ToolSupport: ToolSupport{OpenAIChat: []string{"tools"}}},
			{Provider: "mock", Model: "forced-and-json", Weight: 1, ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice", "structured_outputs"}}},
		},
	}
	now := time.Now().UTC()
	for _, model := range []string{"auto-only", "forced-and-json"} {
		svc.observations.add(dynamicObservationKey("default", "mock", model), dynamicObservation{TS: now, Status: http.StatusOK, LatencyMS: 100}, defaultDynamicObservationWindow)
	}
	req := &IRRequest{
		Tools: []map[string]any{{"type": "function"}},
		Raw: map[string]any{
			"tool_choice":     map[string]any{"type": "function", "function": map[string]any{"name": "pick"}},
			"response_format": map[string]any{"type": "json_schema"},
		},
	}
	dec, err := svc.pick(nil, "default", group, req, "openai-chat", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Target.Model != "forced-and-json" {
		t.Fatalf("selected %q, want target with forced tool and structured output support", dec.Target.Model)
	}
}

func TestRequestHasStructuredOutputOnlyForSchemaOrJSONFormats(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want bool
	}{
		{
			name: "chat json schema",
			raw:  map[string]any{"response_format": map[string]any{"type": "json_schema"}},
			want: true,
		},
		{
			name: "chat json object",
			raw:  map[string]any{"response_format": map[string]any{"type": "json_object"}},
			want: true,
		},
		{
			name: "chat text format",
			raw:  map[string]any{"response_format": map[string]any{"type": "text"}},
			want: false,
		},
		{
			name: "responses json schema",
			raw:  map[string]any{"text": map[string]any{"format": map[string]any{"type": "json_schema"}}},
			want: true,
		},
		{
			name: "responses json object",
			raw:  map[string]any{"text": map[string]any{"format": map[string]any{"type": "json_object"}}},
			want: true,
		},
		{
			name: "responses text format",
			raw:  map[string]any{"text": map[string]any{"format": map[string]any{"type": "text"}}},
			want: false,
		},
		{
			name: "responses missing format type",
			raw:  map[string]any{"text": map[string]any{"format": map[string]any{"verbosity": "low"}}},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := requestHasStructuredOutput(&IRRequest{Raw: tt.raw})
			if got != tt.want {
				t.Fatalf("requestHasStructuredOutput()=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestDynamicScoreHardFilterRequiresRequestedAPISkin(t *testing.T) {
	svc := newTestService(t, "http://127.0.0.1:1", "provider-key")
	defer svc.Close()
	svc.cfg.Provider["responses"] = ProviderConfig{BaseURL: "http://127.0.0.1:1/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	now := time.Now().UTC()
	for _, item := range []struct {
		provider string
		model    string
	}{
		{"mock", "chat-target"},
		{"responses", "responses-target"},
	} {
		svc.observations.add(dynamicObservationKey("default", item.provider, item.model), dynamicObservation{TS: now, Status: http.StatusOK, LatencyMS: 100}, defaultDynamicObservationWindow)
	}
	group := ModelGroup{
		Strategy: "dynamic_score",
		RoutingPolicy: RoutingPolicyConfig{DynamicScore: DynamicScoreConfig{
			MinObservations: 1,
			HardFilters: DynamicScoreHardFilters{
				RequireRequestedAPISkin: true,
			},
		}},
		Targets: []Target{
			{Provider: "mock", Model: "chat-target", Weight: 100},
			{Provider: "responses", Model: "responses-target", Weight: 1},
		},
	}
	dec, err := svc.pick(nil, "default", group, &IRRequest{Input: "hello"}, "openai-responses", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Target.Model != "responses-target" {
		t.Fatalf("selected %q, want target matching requested API skin", dec.Target.Model)
	}
}

func TestDynamicScoreCannotSelectFromOtherModelGroup(t *testing.T) {
	svc := newTestService(t, "http://127.0.0.1:1", "provider-key")
	defer svc.Close()
	svc.cfg.Models["default"] = ModelGroup{
		Strategy: "dynamic_score",
		RoutingPolicy: RoutingPolicyConfig{DynamicScore: DynamicScoreConfig{
			MinObservations: 1,
			ScoreTerms: []DynamicScoreTerm{{
				Name:       "cost",
				Expression: "1.0 * cost_score",
			}},
		}},
		Targets: []Target{{Provider: "mock", Model: "allowed-expensive", Weight: 1, InputPricePerMillionUSD: 10, OutputPricePerMillionUSD: 10}},
	}
	svc.cfg.Models["other"] = ModelGroup{
		Strategy: "dynamic_score",
		Targets:  []Target{{Provider: "mock", Model: "forbidden-cheap", Weight: 1, InputPricePerMillionUSD: 0.01, OutputPricePerMillionUSD: 0.01}},
	}
	svc.observations.add(dynamicObservationKey("default", "mock", "allowed-expensive"), dynamicObservation{TS: time.Now().UTC(), Status: http.StatusOK, LatencyMS: 100}, defaultDynamicObservationWindow)
	svc.observations.add(dynamicObservationKey("other", "mock", "forbidden-cheap"), dynamicObservation{TS: time.Now().UTC(), Status: http.StatusOK, LatencyMS: 1}, defaultDynamicObservationWindow)
	dec, err := svc.pick(nil, "default", svc.cfg.Models["default"], &IRRequest{Input: "hello"}, "openai-chat", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Target.Model != "allowed-expensive" {
		t.Fatalf("selected %q outside requested group", dec.Target.Model)
	}
}

func TestDynamicScoreDecisionTraceIsSafeScalarMetadata(t *testing.T) {
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamBody = "ok"
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_dynamic",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 1, "total_tokens": 3},
		})
	}))
	defer upstream.Close()
	svc := newTestService(t, upstream.URL, "provider-key")
	defer svc.Close()
	svc.cfg.Models["default"] = ModelGroup{
		Strategy: "dynamic_score",
		RoutingPolicy: RoutingPolicyConfig{DynamicScore: DynamicScoreConfig{
			MinObservations: 1,
			Signals: DynamicScoreSignals{
				RequestShape:   DynamicSignalRequestShape{Enabled: true},
				PromptFeatures: DynamicSignalPromptFeatures{Enabled: true, MaxScanBytes: 32, Features: []string{"code"}},
			},
		}},
		Targets: []Target{{Provider: "mock", Model: "mock-model", Weight: 1}},
	}
	svc.observations.add(dynamicObservationKey("default", "mock", "mock-model"), dynamicObservation{TS: time.Now().UTC(), Status: http.StatusOK, LatencyMS: 100}, defaultDynamicObservationWindow)
	secretPrompt := "this raw prompt must not be in traces"
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"`+secretPrompt+`"}],"max_tokens":16}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamBody == "" {
		t.Fatal("upstream was not called")
	}
	var sawDecision bool
	for _, event := range lastLogRecordForTest(t, svc).TraceEvents {
		if event.Event == "routing_decision" {
			sawDecision = true
			if strings.Contains(event.Message, secretPrompt) || strings.Contains(event.Message, "provider-key") || strings.Contains(event.Message, testToken) {
				t.Fatalf("unsafe decision trace: %s", event.Message)
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(event.Message), &payload); err != nil {
				t.Fatalf("decision trace is not json: %v", err)
			}
		}
	}
	if !sawDecision {
		t.Fatal("missing routing_decision trace")
	}
}

func lastLogRecordForTest(t *testing.T, svc *Service) logRecord {
	t.Helper()
	file, err := os.Open(svc.logger.path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var line string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line = scanner.Text()
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if line == "" {
		t.Fatal("request log is empty")
	}
	var rec logRecord
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

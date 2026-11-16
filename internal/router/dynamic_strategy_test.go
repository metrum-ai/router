// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCPUSmallWorkEligibilityAndEscalationHTTP(t *testing.T) {
	configPath := filepath.Join("..", "..", "deploy", "reference", "hierarchy", "config", "dc.yaml")
	rawConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range envReferenceNames(rawConfig) {
		t.Setenv(name, "fixture-provider-value")
	}
	t.Setenv("CPU_MODEL", "cpu-small")
	t.Setenv("FRONTIER_MODEL", "frontier")
	t.Setenv("CPU_ATTEMPT_TIMEOUT_MS", "3000") // leaves room for the normal CPU response under CI load
	t.Setenv("ROUTER_ID_TRANSFORM_KEY", strings.Repeat("ab", 64))
	t.Setenv("ROUTER_ID_TRANSFORM_KEY_ID", "fixture")
	t.Setenv("DC_USAGE_DRIVER", "sqlite")
	t.Setenv("DC_USAGE_PATH", filepath.Join(t.TempDir(), "usage.sqlite"))
	t.Setenv("DC_USAGE_DSN", "")
	t.Setenv("DC_CALLER_RPM", "1200")
	t.Setenv("DC_CALLER_TPM", "1000000")
	t.Setenv("DC_CALLER_CONCURRENT", "64")
	tokenHash, err := HashCallerToken("fixture-token", strings.NewReader(strings.Repeat("s", 32)), callerTokenHashMinIterations)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DC_CALLER_TOKEN_HASH", tokenHash)
	t.Setenv("DC_CALLER_TOKEN_ID", "fixture-caller")
	t.Setenv("DC_CALLER_ID", "fixture-caller")
	t.Setenv("DC_OWNER_USER", "ops")
	t.Setenv("DC_PROJECT", "fixture")
	t.Setenv("DC_ENVIRONMENT", "test")
	t.Setenv("DC_ROUTER_TOKEN", "fixture-token")
	t.Setenv("EDGE_ROUTER_URL", "http://127.0.0.1:2/v1")
	t.Setenv("DC_STATE_PATH", filepath.Join(t.TempDir(), "state.json"))
	t.Setenv("DC_LOG_PATH", filepath.Join(t.TempDir(), "router.log"))
	t.Setenv("MAX_ROUTER_HOPS", "4")
	t.Setenv("DC_UPSTREAM_TIMEOUT_MS", "10000")
	t.Setenv("FRONTIER_BASE_URL", "http://127.0.0.1:1/v1")
	t.Setenv("FRONTIER_API_KEY", "fixture")
	t.Setenv("FRONTIER_INPUT_PRICE_USD_PER_MTOK", "0")
	t.Setenv("FRONTIER_OUTPUT_PRICE_USD_PER_MTOK", "0")
	t.Setenv("FRONTIER_PRICING_SOURCE", "fixture")
	t.Setenv("FRONTIER_PRICING_UPDATED_AT", "2026-11-16")
	t.Setenv("CPU_BASE_URL", "http://127.0.0.1:1/v1")
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	group, ok := cfg.Models["agent-small"]
	if !ok {
		t.Fatal("reference config has no agent-small group")
	}
	if group.RoutingPolicy.DynamicScore.Thresholds.MaxTTFBMS == nil || *group.RoutingPolicy.DynamicScore.Thresholds.MaxTTFBMS != 1000 {
		t.Fatalf("reference latency budget=%+v, want max_ttfb_ms=1000", group.RoutingPolicy.DynamicScore.Thresholds)
	}
	for i := range group.Targets {
		group.Targets[i].Provider = "mock"
	}

	var cpuMode atomic.Int32
	const (
		cpuOK int32 = iota
		cpuDown
		cpuTimeout
	)
	seen := make(chan string, 20)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen <- body.Model
		if body.Model == "cpu-small" {
			switch cpuMode.Load() {
			case cpuDown:
				http.Error(w, "cpu unavailable", http.StatusBadGateway)
				return
			case cpuTimeout:
				time.Sleep(3500 * time.Millisecond)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"reply","object":"chat.completion","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`, body.Model, body.Model)
	}))
	defer upstream.Close()
	svc := newTestService(t, upstream.URL, "provider-key")
	defer svc.Close()
	for name, provider := range svc.cfg.Provider {
		provider.BaseURL = upstream.URL
		svc.cfg.Provider[name] = provider
	}
	svc.cfg.Models["default"] = group
	router := httptest.NewServer(svc.Handler())
	defer router.Close()
	call := func(maxTokens int) (int, string) {
		t.Helper()
		payload := fmt.Sprintf(`{"model":"default","max_tokens":%d,"messages":[{"role":"user","content":"classify receipt"}]}`, maxTokens)
		req, _ := http.NewRequest(http.MethodPost, router.URL+"/v1/chat/completions", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		got, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(got)
	}
	assertLatest := func(want string) {
		t.Helper()
		select {
		case got := <-seen:
			if got != want {
				t.Fatalf("upstream model=%q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("upstream was not called; want %q", want)
		}
	}
	if status, body := call(16); status != http.StatusOK || !strings.Contains(body, `"content":"cpu-small"`) {
		t.Fatalf("compact request status=%d body=%s, want CPU response", status, body)
	}
	assertLatest("cpu-small")
	if status, body := call(256); status != http.StatusOK || !strings.Contains(body, `"content":"frontier"`) {
		t.Fatalf("over-budget request status=%d body=%s, want frontier response", status, body)
	}
	assertLatest("frontier")
	cpuMode.Store(cpuDown)
	if status, body := call(16); status != http.StatusOK || !strings.Contains(body, `"content":"frontier"`) {
		t.Fatalf("CPU-down fallback status=%d body=%s", status, body)
	}
	assertLatest("cpu-small")
	assertLatest("frontier")
	svc.observations = newDynamicObservationStore() // isolate the timeout case from failure-threshold adaptation
	cpuMode.Store(cpuTimeout)
	if status, body := call(16); status != http.StatusOK || !strings.Contains(body, `"content":"frontier"`) {
		t.Fatalf("CPU-timeout fallback status=%d body=%s", status, body)
	}
	assertLatest("cpu-small")
	assertLatest("frontier")
}

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

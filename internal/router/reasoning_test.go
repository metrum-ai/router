// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReasoningIntentDetection(t *testing.T) {
	chat, err := decodeRequest("openai-chat", []byte(`{"model":"default","reasoning_effort":"low","messages":[{"role":"user","content":"x"}]}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	if !chat.Reasoning.Requested || chat.Reasoning.Kind != "effort" || chat.Reasoning.Effort != "low" {
		t.Fatalf("chat reasoning=%#v", chat.Reasoning)
	}

	xhigh, err := decodeRequest("openai-chat", []byte(`{"model":"default","reasoning_effort":"xhigh","messages":[{"role":"user","content":"x"}]}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	if !xhigh.Reasoning.Requested || xhigh.Reasoning.Effort != "xhigh" {
		t.Fatalf("xhigh reasoning=%#v", xhigh.Reasoning)
	}

	plain, err := decodeRequest("openai-chat", []byte(`{"model":"default","max_completion_tokens":16,"messages":[{"role":"user","content":"x"}]}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	if plain.Reasoning.Requested {
		t.Fatalf("max_completion_tokens alone should not request reasoning: %#v", plain.Reasoning)
	}

	responses, err := decodeRequest("openai-responses", []byte(`{"model":"default","reasoning":{"effort":"medium","summary":"auto"},"input":"x"}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	if !responses.Reasoning.Requested || responses.Reasoning.Effort != "medium" || responses.Reasoning.Summary != "auto" {
		t.Fatalf("responses reasoning=%#v", responses.Reasoning)
	}

	anthropic, err := decodeRequest("anthropic", []byte(`{"model":"default","thinking":{"type":"enabled","budget_tokens":4096},"max_tokens":8192,"messages":[{"role":"user","content":"x"}]}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	if !anthropic.Reasoning.Requested || anthropic.Reasoning.Kind != "token_budget" || anthropic.Reasoning.BudgetTokens != 4096 {
		t.Fatalf("anthropic reasoning=%#v", anthropic.Reasoning)
	}

	disabled, err := decodeRequest("anthropic", []byte(`{"model":"default","thinking":{"type":"disabled"},"messages":[{"role":"user","content":"x"}]}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	if !disabled.Reasoning.Disabled || disabled.Reasoning.Requested {
		t.Fatalf("disabled reasoning=%#v", disabled.Reasoning)
	}
}

func TestReasoningMetadataResolvesAndValidates(t *testing.T) {
	cfg := minimalConfig(t)
	provider := cfg.Provider["mock"]
	provider.Models = map[string]ProviderModel{
		"reasoning": {
			Model: "mock-reasoning",
			Reasoning: ReasoningSupport{
				Supported:         true,
				Mode:              reasoningModeOptIn,
				Control:           reasoningControlEffortEnum,
				SupportsSummaries: true,
			},
		},
	}
	cfg.Provider["mock"] = provider
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", ModelRef: "reasoning"}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	target := cfg.Models["default"].Targets[0]
	if !target.Reasoning.Supported || target.Reasoning.Control != reasoningControlEffortEnum || !target.Reasoning.SupportsSummaries {
		t.Fatalf("resolved reasoning=%#v", target.Reasoning)
	}

	cfg = minimalConfig(t)
	provider = cfg.Provider["mock"]
	provider.Models = map[string]ProviderModel{
		"bad": {Model: "bad", Reasoning: ReasoningSupport{Supported: true, Mode: "sometimes", Control: reasoningControlEffortEnum}},
	}
	cfg.Provider["mock"] = provider
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", ModelRef: "bad"}}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "invalid reasoning") {
		t.Fatalf("expected invalid reasoning metadata error, got %v", err)
	}
}

func TestReasoningRequestFiltersTargetsAndTranslates(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      "chatcmpl_reasoning",
			"object":  "chat.completion",
			"created": 1710000000,
			"model":   "reasoning-chat",
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Models["default"] = ModelGroup{Strategy: "failover", Targets: []Target{
		{Provider: "mock", Model: "plain-chat"},
		{Provider: "mock", Model: "reasoning-chat", Reasoning: ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum, RejectsMaxTokens: true}},
	}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req, err := decodeRequest("openai-chat", []byte(`{"model":"default","reasoning_effort":"high","max_tokens":32,"messages":[{"role":"user","content":"x"}]}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	got := svc.targetsForRequest(nil, cfg.Models["default"].Targets, req, "openai-chat")
	if len(got) != 1 || got[0].Model != "reasoning-chat" {
		t.Fatalf("eligible targets=%#v", got)
	}

	rr := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","reasoning_effort":"high","max_tokens":32,"messages":[{"role":"user","content":"x"}]}`))
	httpReq.Header.Set("Authorization", "Bearer "+testToken)
	httpReq.Header.Set("Content-Type", "application/json")
	svc.Handler().ServeHTTP(rr, httpReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamBody["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort=%#v", upstreamBody["reasoning_effort"])
	}
	if _, ok := upstreamBody["max_tokens"]; ok {
		t.Fatalf("max_tokens should have been converted for reasoning target: %#v", upstreamBody)
	}
	if upstreamBody["max_completion_tokens"] != float64(32) {
		t.Fatalf("max_completion_tokens=%#v", upstreamBody["max_completion_tokens"])
	}
}

func TestReasoningSurfaceMatrixPersistsTranslationProof(t *testing.T) {
	type upstreamCall struct {
		Path string
		Body map[string]any
	}
	var calls []upstreamCall
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, upstreamCall{Path: r.URL.Path, Body: body})
		switch r.URL.Path {
		case "/v1/chat/completions":
			writeJSON(w, http.StatusOK, map[string]any{
				"id":      "chatcmpl_reasoning_matrix",
				"object":  "chat.completion",
				"created": 1710000000,
				"model":   body["model"],
				"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "OK"}, "finish_reason": "stop"}},
				"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
			})
		case "/v1/responses":
			writeJSON(w, http.StatusOK, map[string]any{
				"id":          "resp_reasoning_matrix",
				"object":      "response",
				"status":      "completed",
				"model":       body["model"],
				"output_text": "OK",
				"output":      []map[string]any{{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "OK"}}}},
				"usage":       map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2},
			})
		case "/anthropic/v1/messages":
			writeJSON(w, http.StatusOK, map[string]any{
				"id":          "msg_reasoning_matrix",
				"type":        "message",
				"role":        "assistant",
				"model":       body["model"],
				"content":     []map[string]any{{"type": "text", "text": "OK"}},
				"stop_reason": "end_turn",
				"usage":       map[string]any{"input_tokens": 1, "output_tokens": 1},
			})
		default:
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	cfg.Provider["plain_chat"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Provider["reasoning_chat"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Provider["plain_responses"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Provider["reasoning_responses"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Provider["plain_anthropic"] = ProviderConfig{BaseURL: upstream.URL + "/anthropic", Dialect: "anthropic", AuthScheme: "bearer", APIKey: "provider-key"}
	cfg.Provider["reasoning_anthropic"] = ProviderConfig{BaseURL: upstream.URL + "/anthropic", Dialect: "anthropic", AuthScheme: "bearer", APIKey: "provider-key"}
	cfg.Models["reasoning-chat"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "plain_chat", Model: "plain-chat"},
		{Provider: "reasoning_chat", Model: "reasoning-chat", Reasoning: ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum, RejectsMaxTokens: true}},
	}}
	cfg.Models["reasoning-responses"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "plain_responses", Model: "plain-responses"},
		{Provider: "reasoning_responses", Model: "reasoning-responses", Reasoning: ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum, SupportsSummaries: true}},
	}}
	cfg.Models["reasoning-anthropic"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "plain_anthropic", Model: "plain-anthropic"},
		{Provider: "reasoning_anthropic", Model: "reasoning-anthropic", Reasoning: ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlTokenBudget, MinBudgetTokens: 128, BudgetMustBeLessThanMaxTokens: true}},
	}}
	cfg.Callers[0].Allow = []string{"reasoning-chat", "reasoning-responses", "reasoning-anthropic"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	cases := []struct {
		name             string
		path             string
		body             string
		wantPath         string
		wantModel        string
		wantProvider     string
		wantDialect      string
		wantReasoningKey string
		assertBody       func(t *testing.T, body map[string]any)
	}{
		{
			name:             "openai chat",
			path:             "/v1/chat/completions",
			body:             `{"model":"reasoning-chat","reasoning_effort":"high","max_tokens":32,"messages":[{"role":"user","content":"x"}]}`,
			wantPath:         "/v1/chat/completions",
			wantModel:        "reasoning-chat",
			wantProvider:     "reasoning_chat",
			wantDialect:      "openai-chat",
			wantReasoningKey: "reasoning_effort",
			assertBody: func(t *testing.T, body map[string]any) {
				t.Helper()
				if body["reasoning_effort"] != "high" || body["max_completion_tokens"] != float64(32) {
					t.Fatalf("chat reasoning body=%#v", body)
				}
				if _, ok := body["max_tokens"]; ok {
					t.Fatalf("chat reasoning target should rewrite max_tokens: %#v", body)
				}
			},
		},
		{
			name:             "openai responses",
			path:             "/v1/responses",
			body:             `{"model":"reasoning-responses","reasoning":{"effort":"low","summary":"auto"},"max_output_tokens":64,"input":"x"}`,
			wantPath:         "/v1/responses",
			wantModel:        "reasoning-responses",
			wantProvider:     "reasoning_responses",
			wantDialect:      "openai-responses",
			wantReasoningKey: "reasoning",
			assertBody: func(t *testing.T, body map[string]any) {
				t.Helper()
				reasoning, ok := body["reasoning"].(map[string]any)
				if !ok || reasoning["effort"] != "low" || reasoning["summary"] != "auto" {
					t.Fatalf("responses reasoning body=%#v", body)
				}
			},
		},
		{
			name:             "anthropic messages",
			path:             "/v1/messages",
			body:             `{"model":"reasoning-anthropic","max_tokens":1024,"thinking":{"type":"enabled","budget_tokens":512},"messages":[{"role":"user","content":"x"}]}`,
			wantPath:         "/anthropic/v1/messages",
			wantModel:        "reasoning-anthropic",
			wantProvider:     "reasoning_anthropic",
			wantDialect:      "anthropic",
			wantReasoningKey: "thinking",
			assertBody: func(t *testing.T, body map[string]any) {
				t.Helper()
				thinking, ok := body["thinking"].(map[string]any)
				if !ok || thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(512) {
					t.Fatalf("anthropic thinking body=%#v", body)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			startCalls := len(calls)
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Anthropic-Version", "2023-06-01")
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if len(calls) != startCalls+1 {
				t.Fatalf("upstream calls=%d, want %d", len(calls), startCalls+1)
			}
			call := calls[len(calls)-1]
			if call.Path != tc.wantPath || call.Body["model"] != tc.wantModel {
				t.Fatalf("upstream path/model=%s/%#v body=%#v", call.Path, call.Body["model"], call.Body)
			}
			tc.assertBody(t, call.Body)
			assertReasoningTelemetryProof(t, svc, rr.Header().Get("X-Request-Id"), tc.wantProvider, tc.wantModel, tc.wantDialect, tc.wantReasoningKey)
		})
	}
}

func TestReasoningNoEligibleTargetAndModelListMetadata(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "plain-chat"}}}
	cfg.Models["reasoning"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "reasoning-chat", Reasoning: ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum, SupportsSummaries: true}}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "reasoning")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","reasoning_effort":"low","messages":[{"role":"user","content":"x"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), "reasoning") {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	modelReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	modelReq.Header.Set("Authorization", "Bearer "+testToken)
	modelRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(modelRR, modelReq)
	if modelRR.Code != http.StatusOK {
		t.Fatalf("models status=%d body=%s", modelRR.Code, modelRR.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(modelRR.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	foundReasoning := false
	for _, item := range body["data"].([]any) {
		model := item.(map[string]any)
		if model["id"] == "default" {
			assertEmptyCodexReasoningModelMetadata(t, model)
		}
		if model["id"] == "reasoning" {
			foundReasoning = true
			levels := model["supported_reasoning_levels"].([]any)
			if len(levels) != 3 || model["supports_reasoning_summaries"] != true {
				t.Fatalf("reasoning model metadata=%#v", model)
			}
			if model["default_reasoning_summary"] != "none" {
				t.Fatalf("reasoning model default summary=%#v in %#v", model["default_reasoning_summary"], model)
			}
			for _, level := range levels {
				preset, ok := level.(map[string]any)
				if !ok || preset["effort"] == "" || preset["description"] == "" {
					t.Fatalf("reasoning level should be model-catalog preset object, got %#v in %#v", level, model)
				}
			}
		}
	}
	if !foundReasoning {
		t.Fatalf("reasoning group missing from /v1/models: %#v", body)
	}
}

func TestReasoningModelListDoesNotAdvertiseToolOnlyThinking(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "mock", Model: "plain-chat"},
		{Provider: "mock", Model: "anthropic-tool", Dialect: "anthropic", ToolOnly: true, DefaultThinking: map[string]any{"type": "enabled", "budget_tokens": 512}},
	}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	levels, summaries, defaultLevel := svc.reasoningMetadataForGroup("default")
	if len(levels) != 0 || summaries || defaultLevel != "none" {
		t.Fatalf("default reasoning metadata levels=%#v summaries=%v default=%q", levels, summaries, defaultLevel)
	}

	modelReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	modelReq.Header.Set("Authorization", "Bearer "+testToken)
	modelRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(modelRR, modelReq)
	if modelRR.Code != http.StatusOK {
		t.Fatalf("models status=%d body=%s", modelRR.Code, modelRR.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(modelRR.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, item := range body["data"].([]any) {
		model := item.(map[string]any)
		if model["id"] == "default" {
			assertEmptyCodexReasoningModelMetadata(t, model)
			return
		}
	}
	t.Fatalf("default group missing from /v1/models: %#v", body)
}

func TestDisabledDefaultThinkingDoesNotAdvertiseReasoning(t *testing.T) {
	target := Target{DefaultThinking: map[string]any{"type": "disabled"}}
	if targetSupportsReasoning(target) {
		t.Fatalf("disabled default thinking must not advertise reasoning support: %#v", target)
	}
	request := &IRRequest{Reasoning: ReasoningIntent{Requested: true, Kind: "token_budget", BudgetTokens: 512}}
	if targetCanSatisfyReasoning(target, "anthropic", request) {
		t.Fatalf("disabled default thinking must not satisfy an explicit reasoning request: %#v", target)
	}
}

func TestCodexResponsesReasoningToolsEligibilityExampleGroups(t *testing.T) {
	// #1056: Codex CLI sends OpenAI Responses with tools + reasoning. Groups used
	// for coding-agent traffic must keep at least one native Responses target with
	// both function tools and validated reasoning metadata.
	raw, err := os.ReadFile(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	svc := &Service{cfg: &cfg}
	withReasoning := `{"model":"GROUP","input":"Reply OK only.","max_output_tokens":64,"reasoning":{"effort":"low"},"tools":[{"type":"function","name":"echo_marker","parameters":{"type":"object","properties":{"marker":{"type":"string"}},"required":["marker"]}}]}`
	withoutReasoning := `{"model":"GROUP","input":"Reply OK only.","max_output_tokens":64,"tools":[{"type":"function","name":"echo_marker","parameters":{"type":"object","properties":{"marker":{"type":"string"}},"required":["marker"]}}]}`

	for _, groupName := range []string{"big-coder", "high", "agent-tools-smoke"} {
		t.Run(groupName, func(t *testing.T) {
			group, ok := cfg.Models[groupName]
			if !ok {
				t.Fatalf("example config missing group %s", groupName)
			}
			targets := make([]Target, 0, len(group.Targets))
			for _, target := range group.Targets {
				resolved, err := cfg.resolveTarget(groupName, target)
				if err != nil {
					t.Fatal(err)
				}
				targets = append(targets, resolved)
			}

			reqWith, err := decodeRequest("openai-responses", []byte(strings.ReplaceAll(withReasoning, "GROUP", groupName)), nil)
			if err != nil {
				t.Fatal(err)
			}
			eligibleWith := svc.targetsForRequest(nil, targets, reqWith, "openai-responses")
			if len(eligibleWith) == 0 {
				t.Fatalf("%s: Responses+tools+reasoning found no eligible target; requirements=%v", groupName, routingRequirements(reqWith, "openai-responses"))
			}
			for _, target := range eligibleWith {
				if !target.Reasoning.Supported || target.Reasoning.Control != reasoningControlEffortEnum {
					t.Fatalf("%s eligible target missing reasoning metadata: %#v", groupName, target)
				}
				if !supportsAnyCapability(target.ToolSupport.OpenAIResponses, "function") {
					t.Fatalf("%s eligible target missing Responses function tools: %#v", groupName, target)
				}
			}

			reqWithout, err := decodeRequest("openai-responses", []byte(strings.ReplaceAll(withoutReasoning, "GROUP", groupName)), nil)
			if err != nil {
				t.Fatal(err)
			}
			eligibleWithout := svc.targetsForRequest(nil, targets, reqWithout, "openai-responses")
			if len(eligibleWithout) == 0 {
				t.Fatalf("%s: Responses+tools without reasoning found no eligible target", groupName)
			}
		})
	}
}

func TestResponsesReasoningToolsFiltersTargetsWithoutReasoningMetadata(t *testing.T) {
	// Reproduce #1056 filter behavior: Responses+tools without reasoning stays
	// eligible, but adding reasoning removes targets that lack reasoning metadata.
	cfg := Config{
		Provider: map[string]ProviderConfig{
			"responses_plain": {
				BaseURL: "https://responses.example/v1",
				Dialect: "openai-responses",
				Models: map[string]ProviderModel{
					"plain": {
						Model:       "plain-responses",
						ToolSupport: ToolSupport{OpenAIResponses: []string{"function"}},
					},
				},
			},
			"responses_reasoning": {
				BaseURL: "https://responses.example/v1",
				Dialect: "openai-responses",
				Models: map[string]ProviderModel{
					"reasoning": {
						Model:       "reasoning-responses",
						ToolSupport: ToolSupport{OpenAIResponses: []string{"function"}},
						Reasoning: ReasoningSupport{
							Supported: true,
							Mode:      reasoningModeOptIn,
							Control:   reasoningControlEffortEnum,
						},
					},
				},
			},
		},
		Models: map[string]ModelGroup{
			"coding-group": {
				Strategy: "weighted",
				Targets: []Target{
					{Provider: "responses_plain", ModelRef: "plain", Weight: 50},
					{Provider: "responses_reasoning", ModelRef: "reasoning", Weight: 50},
				},
			},
		},
	}
	targets := make([]Target, 0, 2)
	for _, target := range cfg.Models["coding-group"].Targets {
		resolved, err := cfg.resolveTarget("coding-group", target)
		if err != nil {
			t.Fatal(err)
		}
		targets = append(targets, resolved)
	}
	svc := &Service{cfg: &cfg}

	without, err := decodeRequest("openai-responses", []byte(`{"model":"coding-group","input":"x","max_output_tokens":64,"tools":[{"type":"function","name":"echo","parameters":{"type":"object"}}]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	gotWithout := svc.targetsForRequest(nil, targets, without, "openai-responses")
	if len(gotWithout) != 2 {
		t.Fatalf("without reasoning eligible=%d want 2: %#v", len(gotWithout), gotWithout)
	}

	with, err := decodeRequest("openai-responses", []byte(`{"model":"coding-group","input":"x","max_output_tokens":64,"reasoning":{"effort":"low"},"tools":[{"type":"function","name":"echo","parameters":{"type":"object"}}]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	gotWith := svc.targetsForRequest(nil, targets, with, "openai-responses")
	if len(gotWith) != 1 || gotWith[0].Provider != "responses_reasoning" || gotWith[0].Model != "reasoning-responses" {
		t.Fatalf("with reasoning eligible=%#v, want only responses_reasoning", gotWith)
	}
	if reason := reasoningFilterReason(targets[0], "openai-responses", with); reason != "reasoning-support" {
		t.Fatalf("plain target filter reason=%q, want reasoning-support", reason)
	}

	// If every Responses+tools target lacks reasoning metadata, the caller gets
	// no-eligible-target for the Codex default session shape.
	plainOnly := []Target{targets[0]}
	if got := svc.targetsForRequest(nil, plainOnly, with, "openai-responses"); len(got) != 0 {
		t.Fatalf("plain-only group should reject Responses+tools+reasoning: %#v", got)
	}
	reqs := routingRequirements(with, "openai-responses")
	for _, want := range []string{"tools", "openai-responses_tool_passthrough", "reasoning"} {
		if !stringSliceContains(reqs, want) {
			t.Fatalf("requirements=%v missing %q", reqs, want)
		}
	}
}

func assertEmptyCodexReasoningModelMetadata(t *testing.T, model map[string]any) {
	t.Helper()
	for _, key := range []string{
		"default_reasoning_level",
		"default_reasoning_summary",
	} {
		if _, ok := model[key]; ok {
			t.Fatalf("non-reasoning model advertised %s: %#v", key, model)
		}
	}
	levels, ok := model["supported_reasoning_levels"].([]any)
	if !ok || len(levels) != 0 {
		t.Fatalf("non-reasoning model reasoning levels=%#v in %#v", model["supported_reasoning_levels"], model)
	}
	if model["supports_reasoning_summaries"] != false {
		t.Fatalf("non-reasoning model summaries=%#v in %#v", model["supports_reasoning_summaries"], model)
	}
}

func assertReasoningTelemetryProof(t *testing.T, svc *Service, requestID, provider, model, dialect, reasoningControl string) {
	t.Helper()
	if requestID == "" {
		t.Fatal("missing X-Request-Id")
	}
	var usage usageRecord
	flushUsageForTest(t, svc)
	if err := svc.usage.db.Where("request_id = ?", requestID).First(&usage).Error; err != nil {
		t.Fatal(err)
	}
	if usage.Status != http.StatusOK || usage.Attempts != 1 || usage.FallbackUsed {
		t.Fatalf("usage row=%#v", usage)
	}
	var shape requestShapeRecord
	flushUsageForTest(t, svc)
	if err := svc.usage.db.Where("request_id = ?", requestID).First(&shape).Error; err != nil {
		t.Fatal(err)
	}
	if !shape.ReasoningPresent {
		t.Fatalf("request shape did not persist reasoning: %#v", shape)
	}
	var translated requestTranslationShapeRecord
	flushUsageForTest(t, svc)
	if err := svc.usage.db.Where("request_id = ? AND attempt_index = ?", requestID, 1).First(&translated).Error; err != nil {
		t.Fatal(err)
	}
	if translated.Provider != provider || translated.Model != model || translated.Dialect != dialect || translated.TranslatedReasoningControl != reasoningControl {
		t.Fatalf("translation proof=%#v", translated)
	}
	var candidates []decisionTargetCandidateRecord
	flushUsageForTest(t, svc)
	if err := svc.usage.db.Where("request_id = ?", requestID).Order("candidate_index ASC").Find(&candidates).Error; err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidate count=%d rows=%#v", len(candidates), candidates)
	}
	if candidates[0].Eligible || candidates[0].Selected || candidates[0].ReasoningSupport {
		t.Fatalf("plain candidate should be filtered for reasoning: %#v", candidates[0])
	}
	if !candidates[1].Eligible || !candidates[1].Selected || !candidates[1].ReasoningSupport {
		t.Fatalf("reasoning candidate should be selected: %#v", candidates[1])
	}
	var reasons []decisionTargetFilterReasonRecord
	flushUsageForTest(t, svc)
	if err := svc.usage.db.Where("request_id = ?", requestID).Find(&reasons).Error; err != nil {
		t.Fatal(err)
	}
	if !filterReasonsContain(reasons, "request_shape", "reasoning-support") {
		t.Fatalf("missing reasoning-support filter reason: %#v", reasons)
	}
}

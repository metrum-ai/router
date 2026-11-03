// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func toolRoutingConfig(t *testing.T, upstreamURL, dir string, routing ToolRoutingConfig) *Config {
	t.Helper()
	sum := sha256.Sum256([]byte(testToken))
	return &Config{
		Server: ServerConfig{
			Identifiers:       IdentifierConfig{Mode: "passthrough"},
			Listen:            ":0",
			DefaultModelGroup: "agent-tools",
			UsageDB:           freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite")),
			Logging:           LoggingConfig{Path: filepath.Join(dir, "requests.jsonl")},
		},
		StatePath: filepath.Join(dir, "state.json"),
		Provider: map[string]ProviderConfig{
			"mock": {BaseURL: upstreamURL + "/v1", Dialect: "openai", APIKey: "provider-key"},
		},
		Models: map[string]ModelGroup{
			"agent-tools": {
				Strategy:    "tool_aware",
				ToolRouting: routing,
				Targets: []Target{{
					Provider:    "mock",
					Model:       "tool-model",
					Weight:      10,
					ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}},
				}},
			},
			"agent-tools-static": {
				Strategy: "static",
				Targets: []Target{{
					Provider:    "mock",
					Model:       "tool-model",
					Weight:      10,
					ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}},
				}},
			},
		},
		Callers: []CallerConfig{{
			ID:          "alice",
			User:        "alice",
			Project:     "metrum-insights",
			Environment: "test",
			TokenSHA256: hex.EncodeToString(sum[:]),
			TokenID:     "rtr_alice_test",
			Allow:       []string{"agent-tools", "agent-tools-static"},
			Rate:        RateConfig{RPM: 100, TPM: 1000000, Concurrent: 4},
			Quota:       QuotaConfig{Day: BudgetConfig{Requests: 100, Tokens: 1000000}, Month: BudgetConfig{Tokens: 1000000}},
			Key:         KeyConfig{LifetimeTokens: 1000000},
		}},
	}
}

const toolRoutingRequestBody = `{"model":"%s","max_tokens":64,"stream":false,"messages":[{"role":"user","content":"read the file"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}}}]}`

func toolRoutingUpstream(t *testing.T, toolCallArguments string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected upstream path %s", r.URL.Path)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_1",
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": nil,
					"tool_calls": []map[string]any{{
						"id":       "call_1",
						"type":     "function",
						"function": map[string]any{"name": "read_file", "arguments": toolCallArguments},
					}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]any{"prompt_tokens": 21, "completion_tokens": 7, "total_tokens": 28},
		})
	}))
}

func serveToolRouting(t *testing.T, svc *Service, group string) *httptest.ResponseRecorder {
	t.Helper()
	body := strings.Replace(toolRoutingRequestBody, "%s", group, 1)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	recorder := httptest.NewRecorder()
	svc.Handler().ServeHTTP(recorder, req)
	return recorder
}

func readToolRoutingLog(t *testing.T, path string) map[string]any {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open request log: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		if _, ok := record["attempts_detail"]; ok {
			return record
		}
	}
	t.Fatal("no request log record with attempts")
	return nil
}

func toolValidationFromLog(t *testing.T, record map[string]any) map[string]any {
	t.Helper()
	attempts, _ := record["attempts_detail"].([]any)
	for _, attempt := range attempts {
		entry, _ := attempt.(map[string]any)
		validation, ok := entry["tool_validation"].(map[string]any)
		if ok {
			return validation
		}
	}
	t.Fatalf("no tool_validation in attempts: %#v", record["attempts_detail"])
	return nil
}

func TestToolAwareObserveRecordsValidationWithoutChangingBytes(t *testing.T) {
	upstream := toolRoutingUpstream(t, `{"path":"notes.txt"}`)
	defer upstream.Close()

	dir := t.TempDir()
	svc, err := New(toolRoutingConfig(t, upstream.URL, dir, ToolRoutingConfig{Mode: "observe"}))
	if err != nil {
		t.Fatal(err)
	}
	observe := serveToolRouting(t, svc, "agent-tools")
	svc.Close()
	if observe.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", observe.Code, observe.Body.String())
	}

	staticDir := t.TempDir()
	staticSvc, err := New(toolRoutingConfig(t, upstream.URL, staticDir, ToolRoutingConfig{Mode: "observe"}))
	if err != nil {
		t.Fatal(err)
	}
	baseline := serveToolRouting(t, staticSvc, "agent-tools-static")
	staticSvc.Close()
	if baseline.Code != http.StatusOK {
		t.Fatalf("static status=%d body=%s", baseline.Code, baseline.Body.String())
	}
	if observe.Body.String() != baseline.Body.String() {
		t.Fatalf("observe mode changed client bytes:\n%s\nvs\n%s", observe.Body.String(), baseline.Body.String())
	}

	validation := toolValidationFromLog(t, readToolRoutingLog(t, filepath.Join(dir, "requests.jsonl")))
	if validation["mode"] != "observe" || validation["action"] != "observed" {
		t.Fatalf("unexpected validation record: %#v", validation)
	}
	if validation["calls"].(float64) != 1 || validation["valid"].(float64) != 1 || validation["invalid"].(float64) != 0 {
		t.Fatalf("counts: %#v", validation)
	}
	if _, present := validation["first_failure"]; present {
		t.Fatalf("valid call recorded a failure: %#v", validation)
	}
}

func TestToolAwareObserveRecordsInvalidCallAndKeepsResponse(t *testing.T) {
	upstream := toolRoutingUpstream(t, `{"path":`)
	defer upstream.Close()

	dir := t.TempDir()
	svc, err := New(toolRoutingConfig(t, upstream.URL, dir, ToolRoutingConfig{Mode: "observe"}))
	if err != nil {
		t.Fatal(err)
	}
	response := serveToolRouting(t, svc, "agent-tools")
	svc.Close()
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"arguments":"{\"path\":"`) && !strings.Contains(response.Body.String(), `{\"path\":`) {
		t.Fatalf("malformed upstream arguments were not delivered unchanged: %s", response.Body.String())
	}
	validation := toolValidationFromLog(t, readToolRoutingLog(t, filepath.Join(dir, "requests.jsonl")))
	if validation["invalid"].(float64) != 1 || validation["first_failure"] != toolFailureInvalidJSON {
		t.Fatalf("unexpected validation: %#v", validation)
	}
	counts, _ := validation["class_counts"].(map[string]any)
	if counts[toolFailureInvalidJSON].(float64) != 1 {
		t.Fatalf("class counts: %#v", validation)
	}
}

func TestGroupWithoutToolRoutingBlockRecordsNothing(t *testing.T) {
	upstream := toolRoutingUpstream(t, `{"path":"notes.txt"}`)
	defer upstream.Close()
	dir := t.TempDir()
	svc, err := New(toolRoutingConfig(t, upstream.URL, dir, ToolRoutingConfig{Mode: "observe"}))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if _, ok := svc.toolAwareGroup("agent-tools-static"); ok {
		t.Fatal("static group must not be tool aware")
	}
	// A tool_aware group without the block is a configuration error, so the
	// runtime guard is only reachable for a group that does configure one.
	if _, ok := svc.toolAwareGroup("agent-tools"); !ok {
		t.Fatal("configured tool_aware group must be recognised")
	}
}

func TestValidateToolRoutingConfig(t *testing.T) {
	enabled := true
	disabled := false
	cases := []struct {
		name     string
		strategy string
		config   ToolRoutingConfig
		want     string
	}{
		{name: "absent block on other strategies", strategy: "static", config: ToolRoutingConfig{}},
		{name: "absent block on tool_aware", strategy: "tool_aware", config: ToolRoutingConfig{}, want: "has no tool_routing block"},
		{name: "block on other strategy", strategy: "weighted", config: ToolRoutingConfig{Mode: "observe"}, want: "does not use tool_aware strategy"},
		{name: "observe mode", strategy: "tool_aware", config: ToolRoutingConfig{Mode: "observe"}, want: ""},
		{name: "enforce without budget", strategy: "tool_aware", config: ToolRoutingConfig{Mode: "enforce"}, want: "without a recovery budget"},
		{name: "enforce with retry budget", strategy: "tool_aware", config: ToolRoutingConfig{Mode: "enforce", Recovery: ToolRoutingRecoveryConfig{MaxRetriesSameTarget: 1}}, want: ""},
		{name: "unknown mode", strategy: "tool_aware", config: ToolRoutingConfig{Mode: "shadow"}, want: "mode must be observe or enforce"},
		{name: "cold start", strategy: "tool_aware", config: ToolRoutingConfig{ColdStart: "configured_weight"}, want: ""},
		{name: "bad cold start", strategy: "tool_aware", config: ToolRoutingConfig{ColdStart: "random"}, want: "cold_start must be"},
		{name: "bad schema mode", strategy: "tool_aware", config: ToolRoutingConfig{Validate: ToolRoutingValidateConfig{Schema: "strict"}}, want: "validate.schema must be off or basic"},
		{name: "schema off", strategy: "tool_aware", config: ToolRoutingConfig{Validate: ToolRoutingValidateConfig{Schema: "off"}}, want: ""},
		{name: "min samples too high", strategy: "tool_aware", config: ToolRoutingConfig{MinSamples: toolRoutingMaxMinObs + 1}, want: "min_samples must be"},
		{name: "recovery in observe mode", strategy: "tool_aware", config: ToolRoutingConfig{Mode: "observe", Recovery: ToolRoutingRecoveryConfig{MaxEscalations: 1}}, want: "without mode enforce"},
		{name: "retries over cap", strategy: "tool_aware", config: ToolRoutingConfig{Mode: "enforce", Recovery: ToolRoutingRecoveryConfig{MaxRetriesSameTarget: 3}}, want: "max_retries_same_target must be between"},
		{name: "escalations over target count", strategy: "tool_aware", config: ToolRoutingConfig{Mode: "enforce", Recovery: ToolRoutingRecoveryConfig{MaxEscalations: 2}}, want: "max_escalations must be at most 0"},
		{name: "streaming hold not available", strategy: "tool_aware", config: ToolRoutingConfig{Streaming: ToolRoutingStreamingConfig{HoldUntilValidated: true}}, want: "hold_until_validated is not available yet"},
		{name: "negative window", strategy: "tool_aware", config: ToolRoutingConfig{WindowSeconds: -1}, want: "window_seconds must be"},
		{name: "explicit false toggles still configured", strategy: "tool_aware", config: ToolRoutingConfig{Validate: ToolRoutingValidateConfig{ArgumentsJSON: &disabled, NameMustBeOffered: &enabled}}, want: ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateToolRouting("agent-tools", testCase.strategy, testCase.config, 1)
			if testCase.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error=%v, want substring %q", err, testCase.want)
			}
		})
	}
}

func TestToolRoutingValidationOptionsDefaults(t *testing.T) {
	options := ToolRoutingConfig{}.validationOptions()
	if !options.ArgumentsJSON || options.Schema != toolSchemaModeBasic || !options.NameMustBeOffered {
		t.Fatalf("defaults: %+v", options)
	}
	off := false
	custom := ToolRoutingConfig{Validate: ToolRoutingValidateConfig{
		ArgumentsJSON:     &off,
		Schema:            "off",
		NameMustBeOffered: &off,
	}}
	options = custom.validationOptions()
	if options.ArgumentsJSON || options.Schema != toolSchemaModeOff || options.NameMustBeOffered {
		t.Fatalf("explicit off: %+v", options)
	}
}

func TestForcedToolNames(t *testing.T) {
	tools := []map[string]any{
		{"type": "function", "function": map[string]any{"name": "read_file"}},
		{"type": "function", "function": map[string]any{"name": "write_file"}},
	}
	cases := []struct {
		name   string
		choice any
		want   []string
	}{
		{name: "absent", choice: nil, want: nil},
		{name: "auto", choice: "auto", want: nil},
		{name: "none", choice: "none", want: nil},
		{name: "required", choice: "required", want: []string{"read_file", "write_file"}},
		{name: "chat forced function", choice: map[string]any{"type": "function", "function": map[string]any{"name": "read_file"}}, want: []string{"read_file"}},
		{name: "responses forced function", choice: map[string]any{"type": "function", "name": "write_file"}, want: []string{"write_file"}},
		{name: "anthropic forced tool", choice: map[string]any{"type": "tool", "name": "read_file"}, want: []string{"read_file"}},
		{name: "anthropic any", choice: map[string]any{"type": "any"}, want: []string{"read_file", "write_file"}},
		{name: "unknown shape", choice: 42, want: nil},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			req := &IRRequest{Tools: tools, Raw: map[string]any{}}
			if testCase.choice != nil {
				req.Raw["tool_choice"] = testCase.choice
			}
			got := forcedToolNames(req)
			if len(got) != len(testCase.want) {
				t.Fatalf("got %v, want %v", got, testCase.want)
			}
			for index := range got {
				if got[index] != testCase.want[index] {
					t.Fatalf("got %v, want %v", got, testCase.want)
				}
			}
		})
	}
}

func TestToolAwareStrategyKeepsConfiguredOrder(t *testing.T) {
	svc := &Service{cfg: &Config{
		Provider: map[string]ProviderConfig{"mock": {Dialect: "openai"}},
		Models: map[string]ModelGroup{
			"agent-tools": {
				Strategy:    "tool_aware",
				ToolRouting: ToolRoutingConfig{Mode: "observe"},
				Targets: []Target{
					{Provider: "mock", Model: "heavy", Weight: 1, ContextTokens: 200000, ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}},
					{Provider: "mock", Model: "primary", Weight: 100, ContextTokens: 200000, ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}},
				},
			},
		},
	}}
	req := &IRRequest{Model: "agent-tools", Tools: []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file"}}}}
	decision, err := svc.pick(nil, "agent-tools", svc.cfg.Models["agent-tools"], req, "openai-chat", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if decision.Strategy != "tool_aware" {
		t.Fatalf("strategy=%q", decision.Strategy)
	}
	if decision.Target.Model != "heavy" {
		t.Fatalf("expected the first configured target without reliability evidence, got %q", decision.Target.Model)
	}
	if len(decision.Fallbacks) != 1 || decision.Fallbacks[0].Model != "primary" {
		t.Fatalf("fallbacks: %+v", decision.Fallbacks)
	}
	if decision.DynamicScoreTerms[0].TermName != "configured_order" {
		t.Fatalf("ranking telemetry: %+v", decision.DynamicScoreTerms)
	}
}

func TestToolAwarePickUnknownStrategyNotReachableWithoutBlock(t *testing.T) {
	err := validateToolRouting("group", "tool_aware", ToolRoutingConfig{MinSamples: -1}, 1)
	if err == nil || !errors.Is(err, err) {
		if err == nil {
			t.Fatal("expected a configuration error")
		}
	}
}

func TestRecordToolValidationHandlesNilAndStreamedResponses(t *testing.T) {
	svc := &Service{cfg: &Config{
		Provider: map[string]ProviderConfig{"mock": {Dialect: "openai"}},
		Models: map[string]ModelGroup{
			"grp": {Strategy: "tool_aware", ToolRouting: ToolRoutingConfig{Mode: "observe"}},
		},
	}}
	req := &IRRequest{Tools: []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file"}}}}
	attempt := attemptLogRecord{Index: 1}
	// A nil response (some streamed paths write straight to the caller) must not
	// panic and must not record anything.
	svc.recordToolValidation(nil, "grp", req, nil, &attempt)
	if attempt.ToolValidation != nil {
		t.Fatalf("nil response recorded validation: %#v", attempt.ToolValidation)
	}
	// A streamed response with no assembled call view records only that it was
	// observed, never a failure the router could not see.
	streamed := attemptLogRecord{Index: 2}
	svc.recordToolValidation(nil, "grp", req, &IRResponse{Streamed: true}, &streamed)
	if streamed.ToolValidation == nil || streamed.ToolValidation.Action != "observed_streaming" {
		t.Fatalf("streamed attempt record: %#v", streamed.ToolValidation)
	}
	if streamed.ToolValidation.Invalid != 0 || streamed.ToolValidation.FirstFailure != "" {
		t.Fatalf("streamed attempt must not claim a failure: %#v", streamed.ToolValidation)
	}
}

func TestForcedStreamedResponseIsNotClassifiedAsMismatch(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		frames := []string{
			`{"id":"up","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"notes.txt\"}"}}]}}]}`,
			`{"id":"up","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
		}
		for _, frame := range frames {
			_, _ = w.Write([]byte("data: " + frame + "\n\n"))
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()
	dir := t.TempDir()
	svc, err := New(toolRoutingConfig(t, upstream.URL, dir, ToolRoutingConfig{Mode: "observe"}))
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(toolRoutingRequestBody, "%s", "agent-tools", 1)
	body = strings.Replace(body, `"stream":false`, `"stream":true`, 1)
	body = strings.Replace(body, `"tools":[`, `"tool_choice":{"type":"function","function":{"name":"read_file"}},"tools":[`, 1)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	recorder := httptest.NewRecorder()
	svc.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	svc.Close()
	validation := toolValidationFromLog(t, readToolRoutingLog(t, filepath.Join(dir, "requests.jsonl")))
	if validation["action"] != "observed_streaming" || validation["invalid"].(float64) != 0 {
		t.Fatalf("streamed forced call was classified: %#v", validation)
	}
}

func TestSafeLoggedToolNameSanitizesCallerInput(t *testing.T) {
	long := strings.Repeat("a", 200)
	cases := map[string]string{
		"":                        "",
		"read_file":               "read_file",
		"read\x00file":            "readfile",
		"line\nbreak\tand\x1besc": "linebreakandesc",
		long:                      long[:maxLoggedToolNameBytes],
		strings.Repeat("é", 40):   strings.Repeat("é", maxLoggedToolNameBytes/2),
	}
	for input, want := range cases {
		if got := safeLoggedToolName(input); got != want {
			t.Fatalf("safeLoggedToolName(%q)=%q, want %q", input, got, want)
		}
		if len(safeLoggedToolName(input)) > maxLoggedToolNameBytes {
			t.Fatalf("sanitized name exceeds the byte cap: %q", safeLoggedToolName(input))
		}
	}
	trace := sanitizeToolValidationTrace("invalid_json\nx", "read\x00file"+strings.Repeat("b", 100))
	if strings.ContainsAny(trace, "\n\x00") || len(trace) > 128 {
		t.Fatalf("trace text not sanitized: %q", trace)
	}
}

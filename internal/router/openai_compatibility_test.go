// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestResponsesBodyOnChatEndpointDisabledRejectsWhileChatWorks(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "chatcmpl_ok",
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": "chat ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 2, "total_tokens": 4},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	responsesReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","input":"hi","max_output_tokens":8}`))
	responsesReq.Header.Set("Authorization", "Bearer "+testToken)
	responsesRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(responsesRR, responsesReq)
	if responsesRR.Code != http.StatusBadRequest || !strings.Contains(responsesRR.Body.String(), openAIChatEndpointResponsesShapeDisabledError) {
		t.Fatalf("status=%d body=%s", responsesRR.Code, responsesRR.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls=%d, want none for disabled compatibility", calls.Load())
	}

	chatReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}],"max_tokens":8}`))
	chatReq.Header.Set("Authorization", "Bearer "+testToken)
	chatRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(chatRR, chatReq)
	if chatRR.Code != http.StatusOK {
		t.Fatalf("ordinary chat status=%d body=%s", chatRR.Code, chatRR.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("ordinary chat upstream calls=%d, want 1", calls.Load())
	}
}

func TestResponsesBodyOnChatEndpointEnabledNativeResponsesStructuredOutput(t *testing.T) {
	var gotPath string
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, responsesCompatUpstreamResponse(stringValue(upstreamBody["model"]), "native ok"))
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.OpenAICompatibility.TolerateResponsesBodyOnChatEndpoint = true
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Provider["responses"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Models["compat"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:    "responses",
		Model:       "responses-model",
		ToolSupport: ToolSupport{OpenAIResponses: []string{"structured_outputs"}},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "compat")
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"compat","input":"hi","max_tokens":7,"top_p":0.25,"parallel_tool_calls":false,"response_format":{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object"},"strict":true}}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("upstream path=%q, want /v1/responses", gotPath)
	}
	if upstreamBody["max_output_tokens"] != float64(7) {
		t.Fatalf("max_output_tokens not preserved: %#v", upstreamBody)
	}
	if _, ok := upstreamBody["max_tokens"]; ok {
		t.Fatalf("max_tokens leaked to Responses upstream: %#v", upstreamBody)
	}
	if upstreamBody["top_p"] != float64(0.25) || upstreamBody["parallel_tool_calls"] != false {
		t.Fatalf("sampling controls not preserved: %#v", upstreamBody)
	}
	if _, ok := upstreamBody["response_format"]; ok {
		t.Fatalf("response_format leaked to Responses upstream: %#v", upstreamBody)
	}
	text, _ := upstreamBody["text"].(map[string]any)
	format, _ := text["format"].(map[string]any)
	if format["type"] != "json_schema" || format["name"] != "answer" || format["strict"] != true {
		t.Fatalf("text.format not normalized: %#v", upstreamBody)
	}

	var usage usageRecord
	flushUsageForTest(t, svc)
	if err := svc.usage.db.First(&usage).Error; err != nil {
		t.Fatal(err)
	}
	if usage.InboundDialect != "openai-responses" || usage.TargetDialect != "openai-responses" || usage.TargetModel != "responses-model" {
		t.Fatalf("usage dialect/model mismatch: %#v", usage)
	}
	var shape requestShapeRecord
	flushUsageForTest(t, svc)
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).First(&shape).Error; err != nil {
		t.Fatal(err)
	}
	if shape.InboundDialect != "openai-responses" || !shape.StructuredOutputPresent || shape.RequestedOutputCapField != "max_output_tokens" {
		t.Fatalf("request shape=%#v", shape)
	}
	assertCompatibilityTrace(t, svc, usage.RequestID, "mode=enabled")
}

func TestResponsesBodyOnChatEndpointEnabledResponsesToChatReasoningTools(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "chatcmpl_bridge",
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": "bridge ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 4, "completion_tokens": 3, "total_tokens": 7},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.OpenAICompatibility.TolerateResponsesBodyOnChatEndpoint = true
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	cfg.Provider["chat"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["bridge"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:        "chat",
		Model:           "chat-model",
		ToolSupport:     ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}},
		Reasoning:       ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum},
		ResponsesToChat: ResponsesToChatBridge{Enabled: true, Text: true, FunctionTools: true, ToolChoice: true, Reasoning: true, ValidationStatus: "passed"},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "bridge")
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"bridge","input":"hi","reasoning":{"effort":"high"},"max_output_tokens":9,"tools":[{"type":"function","name":"echo","parameters":{"type":"object"}}],"tool_choice":"auto"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamBody["max_tokens"] != float64(9) || upstreamBody["reasoning_effort"] != "high" || upstreamBody["tool_choice"] != "auto" {
		t.Fatalf("upstream body controls not preserved: %#v", upstreamBody)
	}
	if tools := valueAsSlice(upstreamBody["tools"]); len(tools) != 1 {
		t.Fatalf("translated tools=%#v", upstreamBody["tools"])
	}
	var downstream map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &downstream); err != nil {
		t.Fatal(err)
	}
	if downstream["object"] != "response" || downstream["output_text"] != "bridge ok" {
		t.Fatalf("downstream response=%#v", downstream)
	}
	var usage usageRecord
	flushUsageForTest(t, svc)
	if err := svc.usage.db.First(&usage).Error; err != nil {
		t.Fatal(err)
	}
	var translated requestTranslationShapeRecord
	flushUsageForTest(t, svc)
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).First(&translated).Error; err != nil {
		t.Fatal(err)
	}
	if translated.BridgeDirection != responsesToChatBridgeDirection || translated.Dialect != "openai-chat" || translated.TranslatedReasoningControl != "reasoning_effort" {
		t.Fatalf("translation shape=%#v", translated)
	}
}

func TestResponsesBodyOnChatEndpointRejectsUnsupportedSubsetBeforeUpstream(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"id": "unexpected"})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Server.OpenAICompatibility.TolerateResponsesBodyOnChatEndpoint = true
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "unsupported field", body: `{"model":"default","input":"hi","include":["output_text"]}`, want: openAIChatEndpointResponsesShapeUnsupportedFieldError},
		{name: "hosted tool", body: `{"model":"default","input":"hi","tools":[{"type":"web_search"}]}`, want: openAIChatEndpointResponsesShapeUnsupportedToolError},
		{name: "mixed body", body: `{"model":"default","messages":[{"role":"user","content":"hi"}],"input":"hi"}`, want: openAIChatEndpointResponsesShapeMixedError},
		{name: "ambiguous cap", body: `{"model":"default","input":"hi","max_tokens":1,"max_output_tokens":2}`, want: openAIChatEndpointResponsesShapeOutputCapConflictError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), tc.want) {
				t.Fatalf("status=%d body=%s want %s", rr.Code, rr.Body.String(), tc.want)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls=%d, want none", calls.Load())
	}
}

func TestResponsesBodyOnChatEndpointPreviousResponseIDRequiresNativeResponsesTarget(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"id": "unexpected"})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Server.OpenAICompatibility.TolerateResponsesBodyOnChatEndpoint = true
	cfg.Provider["chat"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["bridge"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:        "chat",
		Model:           "chat-model",
		ResponsesToChat: ResponsesToChatBridge{Enabled: true, Text: true, ValidationStatus: "passed"},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "bridge")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"bridge","input":"hi","previous_response_id":"resp_prior"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), openAIChatEndpointPreviousResponseUnsupportedError) {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls=%d, want none", calls.Load())
	}
}

func TestResponsesBodyOnChatEndpointPreservesPreviousResponseIDForNativeResponses(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, responsesCompatUpstreamResponse(stringValue(upstreamBody["model"]), "continued ok"))
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Server.OpenAICompatibility.TolerateResponsesBodyOnChatEndpoint = true
	cfg.Provider["responses"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Models["compat"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "responses", Model: "responses-model"}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "compat")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"compat","input":"hi","previous_response_id":"resp_prior","max_output_tokens":5}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamBody["previous_response_id"] != "resp_prior" || upstreamBody["max_output_tokens"] != float64(5) {
		t.Fatalf("upstream body=%#v", upstreamBody)
	}
}

func TestResponsesBodyOnChatEndpointStreamingUsesResponsesSSE(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_compat\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"responses-model\"}}\n\n")
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"stream ok\"}\n\n")
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_compat\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"responses-model\",\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n")
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Server.OpenAICompatibility.TolerateResponsesBodyOnChatEndpoint = true
	cfg.Provider["responses"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Models["compat"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "responses", Model: "responses-model"}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "compat")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"compat","input":"hi","stream":true,"max_output_tokens":6}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(rr.Body.String(), "response.created") {
		t.Fatalf("not a Responses SSE stream: headers=%v body=%s", rr.Header(), rr.Body.String())
	}
	// Compatibility remaps the Chat route to Responses dialect; same-dialect
	// native streaming asks the upstream for stream=true.
	if upstreamBody["stream"] != true {
		t.Fatalf("upstream stream=%#v, want true for native Responses streaming", upstreamBody["stream"])
	}
}

func TestResponsesBodyOnChatEndpointImplementationHasNoClientNameAllowlist(t *testing.T) {
	raw, err := os.ReadFile("openai_compatibility.go")
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(raw))
	for _, forbidden := range []string{"codex", "cursor", "claude", "opencode"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("compatibility implementation contains client-name special case %q", forbidden)
		}
	}
}

func responsesCompatUpstreamResponse(model, text string) map[string]any {
	return map[string]any{
		"id":          "resp_compat",
		"object":      "response",
		"created_at":  1710000000,
		"status":      "completed",
		"model":       model,
		"output_text": text,
		"output": []map[string]any{{
			"type": "message",
			"role": "assistant",
			"content": []map[string]any{{
				"type": "output_text",
				"text": text,
			}},
		}},
		"usage": map[string]any{"input_tokens": 3, "output_tokens": 2, "total_tokens": 5},
	}
}

func assertCompatibilityTrace(t *testing.T, svc *Service, requestID, want string) {
	t.Helper()
	var events []requestTraceEventRecord
	flushUsageForTest(t, svc)
	if err := svc.usage.db.Where("request_id = ? AND event = ?", requestID, "openai_compatibility_shape").Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if strings.Contains(event.Message, want) && strings.Contains(event.Message, "endpoint=/v1/chat/completions") {
			return
		}
	}
	t.Fatalf("compatibility trace missing %q in %#v", want, events)
}

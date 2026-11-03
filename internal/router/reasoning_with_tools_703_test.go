// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Issue #703: a target can declare that its outbound dialect rejects a
// reasoning control together with function tools. Such a request is refused
// before dispatch with a typed 4xx that names the caller's own reasoning field,
// never a provider, model or target identifier.

func TestReasoningWithToolsFilterReasonTable(t *testing.T) {
	no, yes := false, true
	requested := &IRRequest{Tools: []map[string]any{{"type": "function"}}, Reasoning: ReasoningIntent{Requested: true, Effort: "high"}}
	disabled := &IRRequest{Tools: []map[string]any{{"type": "function"}}, Reasoning: ReasoningIntent{Disabled: true, Source: "openai_chat_reasoning_effort", Effort: "none"}}
	noTools := &IRRequest{Reasoning: ReasoningIntent{Requested: true, Effort: "high"}}

	cases := []struct {
		name       string
		withTools  *bool
		req        *IRRequest
		outDialect string
		want       string
	}{
		{"declared-false-chat", &no, requested, "openai-chat", "reasoning-with-tools"},
		{"declared-false-responses", &no, requested, "openai-responses", "reasoning-with-tools"},
		{"declared-false-anthropic", &no, requested, "anthropic", "reasoning-with-tools"},
		{"declared-true-chat", &yes, requested, "openai-chat", ""},
		{"undeclared-chat", nil, requested, "openai-chat", ""},
		{"disabled-reasoning", &no, disabled, "openai-chat", ""},
		{"no-tools", &no, noTools, "openai-chat", ""},
		{"nil-request", &no, nil, "openai-chat", ""},
		{"other-dialect", &no, requested, "gemini-generate-content", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := Target{Reasoning: ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum, WithTools: tc.withTools}}
			if got := reasoningWithToolsFilterReason(target, tc.outDialect, tc.req); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestValidateReasoningWithToolsRequiresReasoningSupport(t *testing.T) {
	no := false
	cases := []struct {
		name string
		rs   ReasoningSupport
		ok   bool
	}{
		{"supported-with-tools-false", ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum, WithTools: &no}, true},
		{"unsupported-with-tools-false", ReasoningSupport{WithTools: &no}, false},
		{"unsupported-with-tools-true", ReasoningSupport{WithTools: boolPtr(true)}, false},
		{"unsupported-plain", ReasoningSupport{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateReasoningSupport(tc.rs)
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v want ok=%t", err, tc.ok)
			}
		})
	}
}

// reasoningWithToolsTestService builds a group whose single target is a Chat
// dialect reasoning model that declares whether it accepts tools with
// reasoning.
func reasoningWithToolsTestService(t *testing.T, upstreamURL string, mutate func(*Target)) *Service {
	t.Helper()
	dir := t.TempDir()
	cfg := testConfig(t, upstreamURL, "provider-key", dir)
	cfg.Provider["chat_reasoning"] = ProviderConfig{BaseURL: upstreamURL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	target := Target{
		Provider: "chat_reasoning",
		Model:    "gpt-chat-reasoning",
		ToolSupport: ToolSupport{
			OpenAIChat:        []string{"tools", "tool_choice"},
			AnthropicMessages: []string{"client_tools", "tool_choice"},
		},
		RequestShapeSupport: RequestShapeSupport{
			SupportedInboundDialects: []string{"anthropic", "openai-chat"},
			ValidationStatus:         "passed",
		},
		Reasoning: ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum},
	}
	if mutate != nil {
		mutate(&target)
	}
	cfg.Models["declared"] = ModelGroup{Strategy: "static", Targets: []Target{target}}
	cfg.Callers[0].Allow = []string{"declared"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func TestReasoningWithToolsRefusedBeforeDispatch(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	no := false
	svc := reasoningWithToolsTestService(t, upstream.URL, func(target *Target) {
		target.Reasoning.WithTools = &no
	})

	cases := []struct {
		name      string
		path      string
		body      string
		token     string
		wantParam string
	}{
		{
			name:      "anthropic-caller-thinking",
			path:      "/v1/messages",
			token:     "X-API-Key",
			wantParam: "thinking",
			body:      `{"model":"declared","max_tokens":256,"thinking":{"type":"enabled","budget_tokens":128},"tools":[{"name":"f","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name:      "chat-caller-reasoning-effort",
			path:      "/v1/chat/completions",
			token:     "Authorization",
			wantParam: "reasoning_effort",
			body:      `{"model":"declared","reasoning_effort":"high","max_tokens":256,"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"hi"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
			if tc.token == "Authorization" {
				req.Header.Set("Authorization", "Bearer "+testToken)
			} else {
				req.Header.Set("X-API-Key", testToken)
			}
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s, want typed 400", w.Code, w.Body.String())
			}
			body := w.Body.String()
			if !strings.Contains(body, `"code":"unsupported_capability"`) {
				t.Fatalf("body=%s, want unsupported_capability", body)
			}
			if !strings.Contains(body, `"param":"`+tc.wantParam+`"`) {
				t.Fatalf("body=%s, want param %q", body, tc.wantParam)
			}
			for _, leak := range []string{"chat_reasoning", "gpt-chat-reasoning", "provider-key"} {
				if strings.Contains(body, leak) {
					t.Fatalf("body=%s leaks target detail %q", body, leak)
				}
			}
			if hits.Load() != 0 {
				t.Fatalf("upstream hits=%d, want 0", hits.Load())
			}
		})
	}
}

func TestReasoningWithToolsUndeclaredTargetStaysEligible(t *testing.T) {
	for _, withTools := range []*bool{nil, boolPtr(true)} {
		name := "undeclared"
		if withTools != nil {
			name = "declared-true"
		}
		t.Run(name, func(t *testing.T) {
			var hits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				fmt.Fprint(w, `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
			}))
			defer upstream.Close()
			svc := reasoningWithToolsTestService(t, upstream.URL, func(target *Target) {
				target.Reasoning.WithTools = withTools
			})
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"declared","reasoning_effort":"high","max_tokens":256,"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, req)
			if w.Code != http.StatusOK || hits.Load() != 1 {
				t.Fatalf("status=%d hits=%d body=%s, want a served request", w.Code, hits.Load(), w.Body.String())
			}
		})
	}
}

// A Responses-dialect target that does not declare the limitation keeps
// serving tools with reasoning.
func TestReasoningWithToolsResponsesControlPasses(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, `{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Provider["resp_reasoning"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	no := false
	cfg.Models["responses-control"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:    "resp_reasoning",
		Model:       "gpt-responses-reasoning",
		ToolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}},
		Reasoning:   ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum},
	}}}
	cfg.Models["chat-declared-no"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:    "resp_reasoning",
		Model:       "gpt-chat-reasoning",
		Dialect:     "openai-chat",
		ToolSupport: ToolSupport{OpenAIChat: []string{"tools"}},
		Reasoning:   ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum, WithTools: &no},
	}}}
	cfg.Callers[0].Allow = []string{"responses-control", "chat-declared-no"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"responses-control","input":"hi","reasoning":{"effort":"low"},"tools":[{"type":"function","name":"f","parameters":{"type":"object"}}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK || hits.Load() != 1 {
		t.Fatalf("responses control status=%d hits=%d body=%s", w.Code, hits.Load(), w.Body.String())
	}
}

// A Chat caller with tools and an explicit Responses surface keeps working; the
// new filter only removes targets that declare the combination unsupported.
func TestReasoningWithToolsChatCallerControlPasses(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, `{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Provider["resp_reasoning"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Models["bridge-control"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:    "resp_reasoning",
		Model:       "gpt-responses-reasoning",
		ToolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}},
		Reasoning:   ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum},
		Bridges:     BridgeSupport{ChatToResponses: DialectBridgeSupport{Enabled: true, Tools: true, ToolChoice: true, Reasoning: true}},
	}}}
	cfg.Callers[0].Allow = []string{"bridge-control"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"bridge-control","reasoning_effort":"high","max_tokens":256,"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK || hits.Load() != 1 {
		t.Fatalf("bridge control status=%d hits=%d body=%s, want the Responses target to serve it", w.Code, hits.Load(), w.Body.String())
	}
}

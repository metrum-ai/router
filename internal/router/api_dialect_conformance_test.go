// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestAPIDialectConformanceMatrix(t *testing.T) {
	tests := []struct {
		name          string
		inbound       string
		outbound      string
		raw           string
		target        Target
		assertEncoded func(t *testing.T, body map[string]any)
	}{
		{
			name:     "openai chat plain text streams and max tokens",
			inbound:  "openai-chat",
			outbound: "openai-chat",
			raw: `{
				"model":"default",
				"stream":true,
				"max_tokens":7,
				"messages":[{"role":"user","content":"Reply OK only."}]
			}`,
			assertEncoded: func(t *testing.T, body map[string]any) {
				assertConformanceValue(t, body, "model", "upstream-model")
				assertConformanceValue(t, body, "stream", false)
				assertConformanceValue(t, body, "max_tokens", float64(7))
				if _, ok := body["max_completion_tokens"]; ok {
					t.Fatalf("max_completion_tokens should not be emitted when max_tokens wins: %#v", body)
				}
			},
		},
		{
			name:     "openai chat max completion token cap",
			inbound:  "openai-chat",
			outbound: "openai-chat",
			raw: `{
				"model":"default",
				"max_completion_tokens":1,
				"messages":[{"role":"user","content":"Reply OK only."}]
			}`,
			assertEncoded: func(t *testing.T, body map[string]any) {
				assertConformanceValue(t, body, "max_tokens", float64(1))
				if _, ok := body["max_completion_tokens"]; ok {
					t.Fatalf("max_completion_tokens should be normalized for ordinary OpenAI-compatible targets: %#v", body)
				}
			},
		},
		{
			name:     "anthropic messages thinking and cap passthrough",
			inbound:  "anthropic",
			outbound: "anthropic",
			raw: `{
				"model":"default",
				"max_tokens":11,
				"thinking":{"type":"enabled","budget_tokens":256},
				"messages":[{"role":"user","content":"Reply OK only."}]
			}`,
			target: Target{Reasoning: ReasoningSupport{Supported: true, Control: reasoningControlTokenBudget}},
			assertEncoded: func(t *testing.T, body map[string]any) {
				assertConformanceValue(t, body, "max_tokens", float64(11))
				if _, ok := body["thinking"].(map[string]any); !ok {
					t.Fatalf("missing thinking passthrough: %#v", body)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := decodeRequest(tc.inbound, []byte(tc.raw), http.Header{})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := encodeUpstreamForTarget(tc.outbound, "upstream-model", req, tc.target)
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			tc.assertEncoded(t, body)
		})
	}
}

func TestOpenAIChatConformancePassthroughToolsStructuredOutputAndReasoning(t *testing.T) {
	req, err := decodeRequest("openai-chat", []byte(`{
		"model":"default",
		"messages":[{"role":"user","content":"Extract the ticket."}],
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],
		"tool_choice":"auto",
		"reasoning_effort":"low",
		"response_format":{"type":"json_schema","json_schema":{"name":"ticket","schema":{"type":"object"}}}
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeChatPassthrough("upstream-model", req, Target{Reasoning: ReasoningSupport{Supported: true, Control: reasoningControlEffortEnum}})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if tools, ok := body["tools"].([]any); !ok || len(tools) != 1 {
		t.Fatalf("tools=%#v, want one function tool", body["tools"])
	}
	assertConformanceValue(t, body, "tool_choice", "auto")
	assertConformanceValue(t, body, "reasoning_effort", "low")
	if _, ok := body["response_format"].(map[string]any); !ok {
		t.Fatalf("missing response_format passthrough: %#v", body)
	}
}

func TestNativeToolPassthroughPreservesExplicitNullToolChoice(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dialect string
		body    string
		encode  func(*IRRequest) ([]byte, error)
	}{
		{
			name:    "chat",
			dialect: "openai-chat",
			body:    `{"model":"default","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}],"tool_choice":null}`,
			encode: func(req *IRRequest) ([]byte, error) {
				return encodeChatPassthrough("upstream-model", req, Target{})
			},
		},
		{
			name:    "responses",
			dialect: "openai-responses",
			body:    `{"model":"default","input":"hi","tools":[{"type":"function","name":"lookup"}],"tool_choice":null}`,
			encode: func(req *IRRequest) ([]byte, error) {
				return encodeResponsesPassthrough("upstream-model", req, Target{})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := decodeRequest(tc.dialect, []byte(tc.body), http.Header{})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := tc.encode(req)
			if err != nil {
				t.Fatal(err)
			}
			var encoded map[string]any
			if err := json.Unmarshal(raw, &encoded); err != nil {
				t.Fatal(err)
			}
			value, present := encoded["tool_choice"]
			if !present || value != nil {
				t.Fatalf("tool_choice=%#v present=%v, want explicit null", value, present)
			}
		})
	}
}

func TestResponsesConformancePassthroughRejectsHostedToolDescriptors(t *testing.T) {
	req, err := decodeRequest("openai-responses", []byte(`{
		"model":"default",
		"input":"Use local tools only.",
		"max_output_tokens":37,
		"tool_choice":"auto",
		"text":{"format":{"type":"json_schema","name":"answer","schema":{"type":"object"}}},
		"tools":[
			{"type":"function","name":"lookup","parameters":{"type":"object"}},
			{"type":"namespace","namespace":"shell"},
			{"type":"web_search"},
			{"type":"image_generation"}
		]
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := encodeResponsesPassthrough("upstream-model", req, Target{}); err == nil {
		t.Fatal("hosted tools must reject, never silently strip")
	}
}

func TestResponsesConformanceRejectsRemoteHostedToolDescriptors(t *testing.T) {
	req, err := decodeRequest("openai-responses", []byte(`{
		"model":"default",
		"input":"Use remote hosted tools.",
		"tools":[{"type":"mcp","server_url":"https://mcp.example.test"}]
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	if !requestHasForbiddenProviderHostedTools(req) {
		t.Fatalf("remote hosted tool should be rejected before upstream")
	}
}

func assertConformanceValue(t *testing.T, body map[string]any, key string, want any) {
	t.Helper()
	if got := body[key]; got != want {
		t.Fatalf("%s=%#v, want %#v; body=%#v", key, got, want, body)
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestResponsesToChatBridgeEncodesTextAndFunctionTools(t *testing.T) {
	req, err := decodeRequest("openai-responses", []byte(`{
		"model":"bridge",
		"instructions":"Use short answers.",
		"input":"say hi",
		"max_output_tokens":7,
		"temperature":0.2,
		"tools":[{"type":"function","name":"echo","description":"Echo text","parameters":{"type":"object"}}],
		"tool_choice":{"type":"function","name":"echo"},
		"parallel_tool_calls":false
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeResponsesToChatBridge("chat-upstream", req, Target{
		ResponsesToChat: ResponsesToChatBridge{Enabled: true, Text: true, FunctionTools: true, ToolChoice: true},
		ToolSupport:     ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "chat-upstream" || body["stream"] != false || body["max_tokens"] != float64(7) {
		t.Fatalf("unexpected bridge body: %#v", body)
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" || msgs[1].(map[string]any)["content"] != "say hi" {
		t.Fatalf("messages=%#v", msgs)
	}
	tools := body["tools"].([]any)
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "echo" || fn["description"] != "Echo text" {
		t.Fatalf("tool function=%#v", fn)
	}
	choice := body["tool_choice"].(map[string]any)
	if choice["type"] != "function" || choice["function"].(map[string]any)["name"] != "echo" {
		t.Fatalf("tool_choice=%#v", choice)
	}
	if body["parallel_tool_calls"] != false {
		t.Fatalf("parallel_tool_calls=%#v, want false", body["parallel_tool_calls"])
	}
}

func TestResponsesToChatBridgeDistinguishesOmittedAndNullToolChoiceWithoutTools(t *testing.T) {
	target := Target{ResponsesToChat: ResponsesToChatBridge{Enabled: true, Text: true}}

	omitted, err := decodeRequest("openai-responses", []byte(`{"model":"bridge","input":"hi"}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeResponsesToChatBridge("chat-upstream", omitted, target)
	if err != nil {
		t.Fatalf("omitted tool_choice rejected: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if _, present := body["tool_choice"]; present {
		t.Fatalf("omitted tool_choice encoded as %#v", body["tool_choice"])
	}

	explicitNull, err := decodeRequest("openai-responses", []byte(`{"model":"bridge","input":"hi","tool_choice":null}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	if got := responsesToChatBridgeFilterReason(target, explicitNull, "openai-responses", "openai-chat"); got != "responses-to-chat-tool-choice-null-unsupported" {
		t.Fatalf("explicit null filter reason=%q", got)
	}
	if _, err := encodeResponsesToChatBridge("chat-upstream", explicitNull, target); err == nil || !strings.Contains(err.Error(), "responses-to-chat-tool-choice-null-unsupported") {
		t.Fatalf("explicit null err=%v", err)
	}
}

func TestResponsesToChatBridgeRejectsExplicitNullToolChoiceWithTools(t *testing.T) {
	req, err := decodeRequest("openai-responses", []byte(`{"model":"bridge","input":"hi","tools":[{"type":"function","name":"echo"}],"tool_choice":null}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = encodeResponsesToChatBridge("chat-upstream", req, Target{
		ToolSupport:     ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}},
		ResponsesToChat: ResponsesToChatBridge{Enabled: true, Text: true, FunctionTools: true, ToolChoice: true},
	})
	if err == nil || !strings.Contains(err.Error(), "responses-to-chat-tool-choice-null-unsupported") {
		t.Fatalf("err=%v", err)
	}
}

func TestResponsesToolChoiceToChatPreservesSupportedNonNullModes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		choice any
		want   any
	}{
		{name: "string auto", choice: "auto", want: "auto"},
		{name: "string none", choice: "none", want: "none"},
		{name: "string required", choice: "required", want: "required"},
		{name: "object auto", choice: map[string]any{"type": "auto"}, want: map[string]any{"type": "auto"}},
		{name: "object none", choice: map[string]any{"type": "none"}, want: map[string]any{"type": "none"}},
		{name: "object required", choice: map[string]any{"type": "required"}, want: map[string]any{"type": "required"}},
		{
			name:   "forced function",
			choice: map[string]any{"type": "function", "name": "echo"},
			want:   map[string]any{"type": "function", "function": map[string]any{"name": "echo"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := responsesToolChoiceToChat(tc.choice)
			if err != nil {
				t.Fatalf("supported tool_choice %#v rejected: %v", tc.choice, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("tool_choice=%#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestResponsesToChatBridgeEncodesReasoning(t *testing.T) {
	req, err := decodeRequest("openai-responses", []byte(`{
		"model":"bridge",
		"input":"reason",
		"reasoning":{"effort":"low"},
		"max_output_tokens":32
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{
		ResponsesToChat: ResponsesToChatBridge{Enabled: true, Text: true, Reasoning: true},
		Reasoning:       ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum},
	}
	raw, err := encodeResponsesToChatBridge("chat-reasoning", req, target)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["reasoning_effort"] != "low" || body["max_tokens"] != float64(32) {
		t.Fatalf("reasoning bridge body=%#v", body)
	}

	target.ResponsesToChat.Reasoning = false
	if _, err := encodeResponsesToChatBridge("chat-reasoning", req, target); err == nil || !strings.Contains(err.Error(), "responses-to-chat-reasoning") {
		t.Fatalf("disabled reasoning bridge err=%v", err)
	}
}

func TestResponsesToChatBridgeRejectsUnsupportedShapes(t *testing.T) {
	target := Target{ResponsesToChat: ResponsesToChatBridge{Enabled: true, Text: true, FunctionTools: true}}
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "previous response", body: `{"model":"bridge","input":"hi","previous_response_id":"resp_123"}`, want: "responses-to-chat-previous-response-id"},
		{name: "hosted tool", body: `{"model":"bridge","input":"hi","tools":[{"type":"web_search"}]}`, want: "responses-to-chat-hosted-tools"},
		{name: "reasoning", body: `{"model":"bridge","input":"hi","reasoning":{"effort":"high"}}`, want: "responses-to-chat-reasoning"},
		{name: "structured output", body: `{"model":"bridge","input":"hi","text":{"format":{"type":"json_schema","name":"answer","schema":{"type":"object"}}}}`, want: "responses-to-chat-structured-output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := decodeRequest("openai-responses", []byte(tc.body), http.Header{})
			if err != nil {
				t.Fatal(err)
			}
			if got := responsesToChatBridgeFilterReason(target, req, "openai-responses", "openai-chat"); got != tc.want {
				t.Fatalf("reason=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestResponsesToChatBridgeDecodesChatTextAndToolCalls(t *testing.T) {
	resp, err := decodeResponsesToChatBridge([]byte(`{
		"id":"chatcmpl_1",
		"choices":[{
			"message":{
				"role":"assistant",
				"content":"",
				"tool_calls":[{"id":"call_1","type":"function","function":{"name":"echo","arguments":"{\"text\":\"hi\"}"}}]
			},
			"finish_reason":"tool_calls"
		}],
		"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}
	}`), "chat-upstream")
	if err != nil {
		t.Fatal(err)
	}
	if !resp.RawResponse || resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 3 || resp.StopReason != "tool_calls" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	output := resp.Raw["output"].([]map[string]any)
	if len(output) != 1 || output[0]["type"] != "function_call" || output[0]["name"] != "echo" || output[0]["call_id"] != "call_1" {
		t.Fatalf("output=%#v", output)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "echo" || resp.ToolCalls[0].Arguments != `{"text":"hi"}` {
		t.Fatalf("normalized tool calls=%#v", resp.ToolCalls)
	}
}

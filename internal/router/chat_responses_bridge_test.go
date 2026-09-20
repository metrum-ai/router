// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEncodeChatToResponsesBridgeMapsTextAndControls(t *testing.T) {
	req, err := decodeRequest("openai-chat", []byte(`{
		"model":"bridge",
		"messages":[
			{"role":"system","content":"system rules"},
			{"role":"developer","content":"developer rules"},
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"hi back"}
		],
		"max_completion_tokens":32,
		"temperature":0.2
	}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeChatToResponsesBridge("upstream-resp", req, Target{Bridges: BridgeSupport{ChatToResponses: DialectBridgeSupport{Enabled: true}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "upstream-resp" || body["instructions"] != "system rules\ndeveloper rules" || body["max_output_tokens"].(float64) != 32 {
		t.Fatalf("unexpected translated body: %s", raw)
	}
	input := valueAsSlice(body["input"])
	if len(input) != 2 {
		t.Fatalf("input=%#v", input)
	}
	first := input[0].(map[string]any)
	if first["role"] != "user" || contentToText(first["content"]) != "hello" {
		t.Fatalf("first input=%#v", first)
	}
	second := input[1].(map[string]any)
	if second["role"] != "assistant" || contentToText(second["content"]) != "hi back" {
		t.Fatalf("second input=%#v", second)
	}
}

func TestEncodeChatToResponsesBridgeMapsReasoning(t *testing.T) {
	req, err := decodeRequest("openai-chat", []byte(`{
		"model":"bridge",
		"messages":[{"role":"user","content":"reason"}],
		"reasoning_effort":"high",
		"max_tokens":64
	}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{
		Reasoning: ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum},
		Bridges:   BridgeSupport{ChatToResponses: DialectBridgeSupport{Enabled: true, Reasoning: true}},
	}
	raw, err := encodeChatToResponsesBridge("responses-reasoning", req, target, "")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	reasoning := body["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" || body["max_output_tokens"] != float64(64) {
		t.Fatalf("reasoning bridge body=%#v", body)
	}

	target.Bridges.ChatToResponses.Reasoning = false
	if _, err := encodeChatToResponsesBridge("responses-reasoning", req, target, ""); err == nil || !strings.Contains(err.Error(), "chat-to-responses-reasoning-unsupported") {
		t.Fatalf("disabled reasoning bridge err=%v", err)
	}
}

func TestEncodeChatToResponsesBridgeMapsFunctionToolsAndToolResults(t *testing.T) {
	req, err := decodeRequest("openai-chat", []byte(`{
		"model":"bridge",
		"messages":[
			{"role":"user","content":"call echo"},
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"echo","arguments":"{\"text\":\"hi\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"{\"text\":\"hi\"}"}
		],
		"tools":[{"type":"function","function":{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}}}}}],
		"tool_choice":{"type":"function","function":{"name":"echo"}},
		"parallel_tool_calls":true,
		"max_tokens":64
	}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{
		ToolSupport: ToolSupport{OpenAIResponses: []string{"function"}},
		Bridges:     BridgeSupport{ChatToResponses: DialectBridgeSupport{Enabled: true, Tools: true, ToolChoice: true, ParallelToolCalls: true}},
	}
	raw, err := encodeChatToResponsesBridge("responses-tools", req, target, "")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	tools := valueAsSlice(body["tools"])
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "echo" {
		t.Fatalf("tools=%#v", tools)
	}
	choice := body["tool_choice"].(map[string]any)
	if choice["type"] != "function" || choice["name"] != "echo" || body["parallel_tool_calls"] != true {
		t.Fatalf("tool choice/body=%#v", body)
	}
	input := valueAsSlice(body["input"])
	if len(input) != 3 {
		t.Fatalf("input=%#v", input)
	}
	if input[1].(map[string]any)["type"] != "function_call" || input[2].(map[string]any)["type"] != "function_call_output" {
		t.Fatalf("input=%#v", input)
	}
}

func TestEncodeChatToResponsesBridgeRejectsExplicitNullToolChoice(t *testing.T) {
	req, err := decodeRequest("openai-chat", []byte(`{"model":"bridge","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"echo"}}],"tool_choice":null}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = encodeChatToResponsesBridge("responses-tools", req, Target{
		ToolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}},
		Bridges:     BridgeSupport{ChatToResponses: DialectBridgeSupport{Enabled: true, Tools: true, ToolChoice: true}},
	}, "")
	if err == nil || !strings.Contains(err.Error(), "chat-to-responses-tool-choice-null-unsupported") {
		t.Fatalf("err=%v", err)
	}
}

func TestEncodeChatToResponsesBridgePreservesJSONSchemaFormatType(t *testing.T) {
	req, err := decodeRequest("openai-chat", []byte(`{
		"model":"bridge",
		"messages":[{"role":"user","content":"return json"}],
		"response_format":{
			"type":"json_schema",
			"json_schema":{
				"name":"answer",
				"schema":{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false},
				"strict":true
			}
		}
	}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{
		ToolSupport: ToolSupport{OpenAIResponses: []string{"structured_outputs"}},
		Bridges:     BridgeSupport{ChatToResponses: DialectBridgeSupport{Enabled: true, StructuredOutputs: true}},
	}
	raw, err := encodeChatToResponsesBridge("responses-json", req, target, "resp_prior")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	format := body["text"].(map[string]any)["format"].(map[string]any)
	if format["type"] != "json_schema" || format["name"] != "answer" || format["strict"] != true {
		t.Fatalf("format=%#v", format)
	}
	if _, ok := format["schema"].(map[string]any); !ok {
		t.Fatalf("format schema missing: %#v", format)
	}
	if body["previous_response_id"] != "resp_prior" {
		t.Fatalf("previous_response_id not injected: %#v", body)
	}
}

func TestChatToResponsesBridgeRejectsUnsupportedStreamingShapes(t *testing.T) {
	req, err := decodeRequest("openai-chat", []byte(`{"model":"bridge","stream":true,"n":2,"messages":[{"role":"user","content":"hi"}]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = encodeChatToResponsesBridge("responses", req, Target{Bridges: BridgeSupport{ChatToResponses: DialectBridgeSupport{Enabled: true}}}, "")
	if err == nil || !strings.Contains(err.Error(), "chat-to-responses-unsupported-field") {
		t.Fatalf("err=%v", err)
	}
}

func TestDecodeChatToResponsesBridgeResponseMapsTextToolCallsAndUsage(t *testing.T) {
	resp, err := decodeChatToResponsesBridgeResponse([]byte(`{
		"id":"resp_123",
		"object":"response",
		"status":"completed",
		"model":"responses-tools",
		"output":[
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Use the tool."}]},
			{"type":"function_call","call_id":"call_1","name":"echo","arguments":"{\"text\":\"hi\"}"}
		],
		"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14}
	}`), "responses-tools")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Use the tool." || resp.StopReason != "tool_calls" || resp.Usage.TotalTokens != 14 {
		t.Fatalf("resp=%#v", resp)
	}
	choices := valueAsSlice(resp.Raw["choices"])
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	calls := valueAsSlice(msg["tool_calls"])
	if len(calls) != 1 || calls[0].(map[string]any)["id"] != "call_1" {
		t.Fatalf("chat raw=%#v", resp.Raw)
	}
}

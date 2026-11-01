// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const piResponsePrefix = `{"model":"m","max_output_tokens":512,"stream":true,"store":false,"prompt_cache_key":"synthetic","tools":[{"type":"function","name":"read","description":"Read file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}],`

func TestPiResponsesToolContinuationChatBridge(t *testing.T) {
	req, err := decodeRequest("openai-responses", []byte(piResponsePrefix+`"input":[{"role":"user","content":"Read quote.txt"},{"type":"function_call","call_id":"call_1","name":"read","arguments":"{\"path\":\"quote.txt\"}"},{"type":"function_call_output","call_id":"call_1","output":"E2E-TOOL-RESULT-7842"}]}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ResponsesToChat: ResponsesToChatBridge{Enabled: true, Text: true, FunctionTools: true, ToolChoice: true, Streaming: true, ValidationStatus: "passed"}, ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}}
	if reason := responsesToChatBridgeFilterReason(target, req, "openai-responses", "openai-chat"); reason != "" {
		t.Fatalf("valid tool continuation rejected: %s", reason)
	}
	raw, err := encodeResponsesToChatBridge("upstream", req, target)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	messages := valueAsSlice(body["messages"])
	if len(messages) != 3 {
		t.Fatalf("messages=%v", messages)
	}
	result, _ := messages[2].(map[string]any)
	if result["role"] != "tool" || result["tool_call_id"] != "call_1" || result["content"] != "E2E-TOOL-RESULT-7842" {
		t.Fatalf("bad result %v", result)
	}
	target.ResponsesToChat.FunctionTools = false
	if reason := responsesToChatBridgeFilterReason(target, req, "openai-responses", "openai-chat"); reason == "" {
		t.Fatal("tool history accepted without validated capability")
	}
}

func TestPiResponsesChatRejectsMalformedHistory(t *testing.T) {
	target := Target{ResponsesToChat: ResponsesToChatBridge{Enabled: true, Text: true, FunctionTools: true, Streaming: true}, ToolSupport: ToolSupport{OpenAIChat: []string{"tools"}}}
	for _, input := range []string{
		`[{"type":"function_call","call_id":"c1","name":"read","arguments":"not-json"}]`,
		`[{"type":"function_call_output","call_id":"c1","output":{"type":"unrecognized","data":"not text"}}]`,
		`[{"type":"function_call_output","output":"missing id"}]`,
	} {
		req, err := decodeRequest("openai-responses", []byte(piResponsePrefix+`"input":`+input+`}`), http.Header{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := encodeResponsesToChatBridge("upstream", req, target); err == nil {
			t.Fatalf("malformed history accepted: %s", input)
		}
	}
}

func TestPiResponsesSystemRejectsUnrepresentableShapes(t *testing.T) {
	for _, input := range []string{
		`[{"role":"user","content":"hi"},{"role":"system","content":"late"}]`,
		`[{"role":"system","content":[{"type":"input_image","image_url":"https://example.invalid/image.png"}]},{"role":"user","content":"hi"}]`,
	} {
		req, err := decodeRequest("openai-responses", []byte(piResponsePrefix+`"input":`+input+`}`), http.Header{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := encodeOpenAIToolsToAnthropic("upstream", "openai-responses", req, anthropicToolTarget()); err == nil {
			t.Fatalf("unrepresentable system accepted: %s", input)
		}
	}
}

func TestPiResponsesSystemToolRequestAnthropic(t *testing.T) {
	req, err := decodeRequest("openai-responses", []byte(piResponsePrefix+`"input":[{"role":"system","content":"You are a coding assistant."},{"role":"user","content":[{"type":"input_text","text":"Read quote.txt"}]}]}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	target := anthropicToolTarget()
	raw, err := encodeOpenAIToolsToAnthropic("upstream", "openai-responses", req, target)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["system"] != "You are a coding assistant." || len(valueAsSlice(body["messages"])) != 1 || !strings.Contains(string(raw), "Read quote.txt") {
		t.Fatalf("wrong translation: %s", raw)
	}
}

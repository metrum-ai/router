// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestOpenAIAnthropicToolRequestTranslation(t *testing.T) {
	for _, tc := range []struct{ dialect, input string }{
		{"openai-chat", `{"model":"group","messages":[{"role":"user","content":"weather?"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Paris\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"sunny"}],"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object","properties":{}}}}],"tool_choice":{"type":"function","function":{"name":"weather"}},"parallel_tool_calls":false}`},
		{"openai-responses", `{"model":"group","input":[{"role":"user","content":"weather?"},{"type":"function_call","call_id":"call_1","name":"weather","arguments":"{\"city\":\"Paris\"}"},{"type":"function_call_output","call_id":"call_1","output":"sunny"}],"tools":[{"type":"function","name":"weather","parameters":{"type":"object","properties":{}}}],"tool_choice":{"type":"function","name":"weather"},"parallel_tool_calls":false}`},
	} {
		t.Run(tc.dialect, func(t *testing.T) {
			req, err := decodeRequest(tc.dialect, []byte(tc.input), http.Header{})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := encodeOpenAIToolsToAnthropic("claude", tc.dialect, req, Target{})
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			if body["model"] != "claude" {
				t.Fatalf("model: %s", raw)
			}
			messages := valueAsSlice(body["messages"])
			if len(messages) != 3 {
				t.Fatalf("messages: %s", raw)
			}
			assistant := messages[1].(map[string]any)
			call := valueAsSlice(assistant["content"])[0].(map[string]any)
			if call["type"] != "tool_use" || call["id"] != "call_1" || call["name"] != "weather" || call["input"].(map[string]any)["city"] != "Paris" {
				t.Fatalf("tool call: %s", raw)
			}
			result := valueAsSlice(messages[2].(map[string]any)["content"])[0].(map[string]any)
			if result["type"] != "tool_result" || result["tool_use_id"] != "call_1" {
				t.Fatalf("tool result: %s", raw)
			}
			choice := body["tool_choice"].(map[string]any)
			if choice["type"] != "tool" || choice["name"] != "weather" || choice["disable_parallel_tool_use"] != true {
				t.Fatalf("choice: %s", raw)
			}
			if len(valueAsSlice(body["tools"])) != 1 {
				t.Fatalf("tools: %s", raw)
			}
		})
	}
}

func TestOpenAIAnthropicToolRequestRejectsLossyShapes(t *testing.T) {
	for _, input := range []string{
		`{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"not-json"}}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"unsupported","text":"do not drop"}]}]}`,
		`{"messages":[{"role":"tool","content":"missing id"}]}`,
		`{"messages":[{"role":"assistant","tool_calls":"bad"}]}`,
		`{"messages":[{"role":"assistant","refusal":"cannot do this"}]}`,
		`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","strict":true,"parameters":{"type":"object"}}}]}`,
	} {
		req, err := decodeRequest("openai-chat", []byte(input), http.Header{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := encodeOpenAIToolsToAnthropic("claude", "openai-chat", req, Target{}); err == nil {
			t.Fatalf("lossy request accepted: %s", input)
		}
	}
}

func TestAnthropicToolResponseTranslationAndIDs(t *testing.T) {
	transform := testIdentifierTransform(t)
	raw := []byte(`{"id":"msg_up","content":[{"type":"text","text":"checking"},{"type":"tool_use","id":"toolu_1","name":"weather","input":{"city":"Paris"}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`)
	for _, dialect := range []string{"openai-chat", "openai-responses"} {
		t.Run(dialect, func(t *testing.T) {
			resp, err := decodeAnthropicToolsToOpenAI(raw, dialect, "claude", transform)
			if err != nil {
				t.Fatal(err)
			}
			if resp.ID == "msg_up" || resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 {
				t.Fatalf("bad response: %+v", resp)
			}
			var callID string
			if dialect == "openai-chat" {
				choice := valueAsSlice(resp.Raw["choices"])[0].(map[string]any)
				if choice["finish_reason"] != "tool_calls" {
					t.Fatal(choice)
				}
				msg := choice["message"].(map[string]any)
				callID = stringValue(valueAsSlice(msg["tool_calls"])[0].(map[string]any)["id"])
			} else {
				output := valueAsSlice(resp.Raw["output"])
				if len(output) != 2 || output[0].(map[string]any)["type"] != "message" {
					t.Fatal(output)
				}
				callID = stringValue(output[1].(map[string]any)["call_id"])
			}
			decoded, err := transform.Decode(callID)
			if err != nil || decoded != "toolu_1" {
				t.Fatalf("bad tool id %q: %v", decoded, err)
			}
		})
	}
}

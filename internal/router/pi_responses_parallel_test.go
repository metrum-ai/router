// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestPiResponsesParallelToolHistory(t *testing.T) {
	input := `[{"role":"user","content":"hi"},{"type":"function_call","call_id":"call_1","name":"read","arguments":"{\"path\":\"one\"}"},{"type":"function_call","call_id":"call_2","name":"read","arguments":"{\"path\":\"two\"}"},{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"first"},{"type":"input_text","text":" output"}]},{"type":"function_call_output","call_id":"call_2","output":"second output"}]`
	req, err := decodeRequest("openai-responses", []byte(piResponsePrefix+`"input":`+input+`}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	chat := Target{ResponsesToChat: ResponsesToChatBridge{Enabled: true, Text: true, FunctionTools: true, Streaming: true}, ToolSupport: ToolSupport{OpenAIChat: []string{"tools"}}}
	for _, dialect := range []string{"chat", "anthropic"} {
		t.Run(dialect, func(t *testing.T) {
			var raw []byte
			var err error
			if dialect == "chat" {
				raw, err = encodeResponsesToChatBridge("up", req, chat)
			} else {
				raw, err = encodeOpenAIToolsToAnthropic("up", "openai-responses", req, anthropicToolTarget())
			}
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err = json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			messages := valueAsSlice(body["messages"])
			if dialect == "chat" {
				if len(messages) != 4 {
					t.Fatalf("bad message count: %#v", messages)
				}
				assistant := messages[1].(map[string]any)
				calls := valueAsSlice(assistant["tool_calls"])
				first := messages[2].(map[string]any)
				second := messages[3].(map[string]any)
				if assistant["role"] != "assistant" || len(calls) != 2 || calls[0].(map[string]any)["id"] != "call_1" || calls[1].(map[string]any)["id"] != "call_2" || first["tool_call_id"] != "call_1" || first["content"] != "first output" || second["tool_call_id"] != "call_2" || second["content"] != "second output" {
					t.Fatalf("bad parallel Chat messages: %#v", messages)
				}
			} else {
				if len(messages) != 3 {
					t.Fatalf("bad message count: %#v", messages)
				}
				assistant := messages[1].(map[string]any)
				user := messages[2].(map[string]any)
				calls := valueAsSlice(assistant["content"])
				results := valueAsSlice(user["content"])
				if assistant["role"] != "assistant" || user["role"] != "user" || len(calls) != 2 || len(results) != 2 || calls[0].(map[string]any)["id"] != "call_1" || calls[1].(map[string]any)["id"] != "call_2" || results[0].(map[string]any)["tool_use_id"] != "call_1" || results[1].(map[string]any)["tool_use_id"] != "call_2" {
					t.Fatalf("bad parallel Anthropic messages: %#v", messages)
				}
				parts := valueAsSlice(results[0].(map[string]any)["content"])
				if len(parts) != 1 || parts[0].(map[string]any)["text"] != "first output" {
					t.Fatalf("bad joined result %#v", results)
				}
			}
		})
	}
	// Function history is its own explicitly opted-in shape, even without
	// new tool definitions or a text-only bridge flag.
	req.Tools = nil
	chat.ResponsesToChat.Text = false
	if reason := responsesToChatBridgeFilterReason(chat, req, "openai-responses", "openai-chat"); reason != "" {
		t.Fatalf("history only rejected: %s", reason)
	}
}

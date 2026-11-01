// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

func TestChatAnthropic627ParallelResultShapes(t *testing.T) {
	for _, content := range []any{"E2E-TOOL-RESULT-7842\n", []any{map[string]any{"type": "text", "text": "E2E-TOOL-"}, map[string]any{"type": "text", "text": "RESULT-7842\n"}}} {
		input := map[string]any{"model": "m", "messages": []any{
			map[string]any{"role": "user", "content": "read"},
			map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "call_a", "type": "function", "function": map[string]any{"name": "read", "arguments": `{"path":"quote.txt"}`}}, map[string]any{"id": "call_b", "type": "function", "function": map[string]any{"name": "read", "arguments": `{"path":"other.txt"}`}}}},
			map[string]any{"role": "tool", "tool_call_id": "call_a", "content": content}, map[string]any{"role": "tool", "tool_call_id": "call_b", "content": "second"},
		}}
		raw, _ := json.Marshal(input)
		req, err := decodeRequest("openai-chat", raw, http.Header{})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := encodeOpenAIToolsToAnthropic("m", "openai-chat", req, anthropicToolTarget())
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err = json.Unmarshal(encoded, &body); err != nil {
			t.Fatal(err)
		}
		messages := valueAsSlice(body["messages"])
		if len(messages) != 3 {
			t.Fatalf("wrong grouping: %#v", messages)
		}
		assistant := messages[1].(map[string]any)
		user := messages[2].(map[string]any)
		uses := valueAsSlice(assistant["content"])
		results := valueAsSlice(user["content"])
		if len(uses) != 2 || len(results) != 2 {
			t.Fatalf("parallel blocks lost: uses=%#v results=%#v", uses, results)
		}
		for i, id := range []string{"call_a", "call_b"} {
			use := uses[i].(map[string]any)
			result := results[i].(map[string]any)
			if use["type"] != "tool_use" || use["id"] != id || result["type"] != "tool_result" || result["tool_use_id"] != id {
				t.Fatalf("parallel order/identity lost: %#v %#v", uses, results)
			}
		}
		args := uses[0].(map[string]any)["input"].(map[string]any)
		if args["path"] != "quote.txt" {
			t.Fatalf("tool input lost: %#v", args)
		}
		blocks := valueAsSlice(results[0].(map[string]any)["content"])
		var text string
		for _, block := range blocks {
			text += stringValue(block.(map[string]any)["text"])
		}
		if text != "E2E-TOOL-RESULT-7842\n" {
			t.Fatalf("text changed %q", text)
		}
	}
}

func TestAnthropic627NativeToolResultPreservation(t *testing.T) {
	for _, content := range []any{"E2E-TOOL-RESULT-7842\n", []any{map[string]any{"type": "text", "text": "E2E-TOOL-RESULT-7842\n"}}} {
		blocks := []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": content, "is_error": false, "cache_control": map[string]any{"type": "ephemeral"}}, map[string]any{"type": "tool_result", "tool_use_id": "toolu_2", "content": "second", "is_error": true}}
		input := map[string]any{"model": "m", "messages": []any{map[string]any{"role": "user", "content": blocks}}, "max_tokens": 32}
		raw, _ := json.Marshal(input)
		req, err := decodeRequest("anthropic", raw, http.Header{})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := encodeToolPassthrough("anthropic", "m", req, anthropicToolTarget())
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err = json.Unmarshal(encoded, &body); err != nil {
			t.Fatal(err)
		}
		got := valueAsSlice(body["messages"])[0].(map[string]any)["content"]
		if !reflect.DeepEqual(got, blocks) {
			t.Fatalf("native result shape changed: %#v want %#v", got, blocks)
		}
	}
}

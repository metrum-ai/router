// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"testing"
)

// Mixed-media results retain their ordered parts rather than taking the text-only path.
func TestToolResultMixedMedia672(t *testing.T) {
	for _, tc := range []struct{ caller, out, body string }{
		{"anthropic", "openai-chat", `{"model":"m","max_tokens":100,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"c","content":[{"type":"text","text":"a"},{"type":"image","source":{"type":"url","url":"https://example.invalid/img"}},{"type":"text","text":"b"}]}]}]}`},
		{"openai-chat", "openai-responses", `{"model":"m","messages":[{"role":"tool","tool_call_id":"c","content":[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"https://example.invalid/img"}},{"type":"text","text":"b"}]}]}`},
	} {
		t.Run(tc.caller+"-to-"+tc.out, func(t *testing.T) {
			req, err := decodeRequest(tc.caller, []byte(tc.body), nil)
			if err != nil {
				t.Fatal(err)
			}
			target := anthropicToolTarget()
			target.InputModalities = []string{"text", "image"}
			target.Bridges.ChatToResponses = DialectBridgeSupport{Enabled: true, Tools: true, Images: true}
			var raw []byte
			if tc.caller == "anthropic" {
				raw, err = encodeAnthropicToolsBridge("m", req, target, tc.out)
			} else {
				raw, err = encodeChatToResponsesBridge("m", req, target, "")
			}
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			var content []any
			if tc.out == "openai-chat" {
				content = valueAsSlice(valueAsSlice(body["messages"])[0].(map[string]any)["content"])
			} else {
				content = valueAsSlice(valueAsSlice(body["input"])[0].(map[string]any)["output"])
			}
			if len(content) != 3 {
				t.Fatalf("parts: %s", raw)
			}
			a, img, b := content[0].(map[string]any), content[1].(map[string]any), content[2].(map[string]any)
			if a["text"] != "a" || b["text"] != "b" || (img["type"] != "image_url" && img["type"] != "input_image") {
				t.Fatalf("parts changed: %s", raw)
			}
		})
	}
}

func TestToolResultText672(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   any
		text string
		ok   bool
	}{
		{"string", " a\nb ", " a\nb ", true},
		{"ordered bytes", []any{map[string]any{"type": "text", "text": "α "}, map[string]any{"type": "text", "text": ""}, map[string]any{"type": "text", "text": "\n β"}}, "α \n β", true},
		{"empty", []any{}, "", true},
		{"input text", []any{map[string]any{"type": "input_text", "text": "a"}, map[string]any{"type": "input_text", "text": "b"}}, "ab", true},
		{"mixed media", []any{map[string]any{"type": "text", "text": "a"}, map[string]any{"type": "image_url", "image_url": "https://example.invalid/a"}}, "", false},
		{"unknown", []any{map[string]any{"type": "unknown", "text": "a"}}, "", false},
		{"nested", []any{map[string]any{"type": "text", "text": "a", "content": "b"}}, "", false},
		{"malformed", []any{map[string]any{"type": "text", "text": 7}}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := toolResultText(tc.in)
			if got != tc.text || ok != tc.ok {
				t.Fatalf("got %q,%v want %q,%v", got, ok, tc.text, tc.ok)
			}
		})
	}
	// Ordinary message flattening deliberately retains its previous formatting.
	if got := contentToText([]any{map[string]any{"type": "text", "text": "a"}, map[string]any{"type": "text", "text": "b"}}); got != "a\nb" {
		t.Fatalf("ordinary content formatting changed: %q", got)
	}
}

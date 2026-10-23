// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCaptureChatContinuationPrivacy(t *testing.T) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(`{"api_key":"vendor-extension-secret","messages":[{"role":"user","content":"lookup ticket","vendor_secret":"extension-secret"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"ticket\":1}","secret":"fn-extension-secret"},"secret":"call-extension-secret"}]},{"role":"tool","tool_call_id":"call_1","content":"open"}]}`), &raw); err != nil {
		t.Fatal(err)
	}
	req := &IRRequest{Raw: raw}
	for _, enabled := range []bool{false, true} {
		payload := contentCaptureRequestPayload(req, ContentCaptureConfig{CaptureToolCalls: enabled})
		b, _ := json.Marshal(payload)
		if strings.Contains(string(b), "extension-secret") {
			t.Fatalf("raw extension leaked: %s", b)
		}
		chat, exists := payload["chat_messages"]
		if exists != enabled {
			t.Fatalf("tool capture enabled=%v canonical present=%v", enabled, exists)
		}
		if enabled {
			rows := chat.([]any)
			if len(rows) != 3 || rows[2].(map[string]any)["tool_call_id"] != "call_1" {
				t.Fatalf("lost continuation: %s", b)
			}
			calls := rows[1].(map[string]any)["tool_calls"].([]any)
			if calls[0].(map[string]any)["function"].(map[string]any)["arguments"] != `{"ticket":1}` {
				t.Fatalf("arguments not canonical: %s", b)
			}
		}
	}
}

func TestCaptureChatUnsupportedFailsClosed(t *testing.T) {
	for _, input := range []string{
		`[{"role":"user","content":[{"type":"image_url","image_url":{"url":"private-image"}}]}]`,
		`[{"role":"tool","content":"unpaired"}]`,
		`[{"role":"assistant","tool_calls":[{"id":"x","type":"function","function":{"name":"x","arguments":{}}}]}]`,
		`[{"role":"unknown","content":"bad"}]`,
	} {
		var messages []any
		if err := json.Unmarshal([]byte(input), &messages); err != nil {
			t.Fatal(err)
		}
		if captureChatMessages(messages) != nil {
			t.Fatalf("accepted unsupported transcript: %s", input)
		}
	}
}

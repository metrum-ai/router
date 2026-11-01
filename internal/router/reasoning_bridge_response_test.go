// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBridgeReasoningResponseVisibleTextOnly(t *testing.T) {
	for _, tc := range []struct {
		name, upstream, caller, raw, want string
	}{
		{"anthropic-to-responses", "anthropic", "openai-responses", `{"content":[{"type":"thinking","thinking":"visible thought","signature":"secret_signature"},{"type":"redacted_thinking","data":"secret_cipher"}]}`, `"type":"reasoning"`},
		{"responses-to-anthropic", "openai-responses", "anthropic", `{"output":[{"type":"reasoning","encrypted_content":"secret_cipher","summary":[{"type":"summary_text","text":"visible thought"}]}]}`, `"type":"thinking"`},
		{"anthropic-to-chat", "anthropic", "openai-chat", `{"content":[{"type":"thinking","thinking":"visible thought","signature":"secret_signature"}]}`, `"reasoning_content":"visible thought"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &IRResponse{ID: "r1", Model: "m", Text: "answer"}
			attachBridgeReasoning(resp, []byte(tc.raw), tc.upstream, tc.caller)
			if !resp.RawResponse {
				t.Fatal("reasoning response not attached")
			}
			result, err := json.Marshal(resp.Raw)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(result), tc.want) || !strings.Contains(string(result), "visible thought") || !strings.Contains(string(result), "answer") || strings.Contains(string(result), "secret_signature") || strings.Contains(string(result), "secret_cipher") {
				t.Fatalf("unsafe or incomplete bridge response: %s", result)
			}
		})
	}
}

func TestBridgeReasoningResponseDoesNotInventText(t *testing.T) {
	resp := &IRResponse{Text: "answer"}
	attachBridgeReasoning(resp, []byte(`{"output":[{"type":"reasoning","encrypted_content":"secret"}]}`), "openai-responses", "anthropic")
	if resp.RawResponse {
		t.Fatal("encrypted-only reasoning must not be exposed or invented")
	}
}

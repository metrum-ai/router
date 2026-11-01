// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestForeignReasoningHistorySanitizedWithoutChangingNative(t *testing.T) {
	for _, tc := range []struct{ caller, out, raw string }{
		{"anthropic", "openai-responses", `{"model":"m","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"secret_thought","signature":"secret_signature"},{"type":"redacted_thinking","data":"secret_cipher"},{"type":"text","text":"visible"}]}]}`},
		{"openai-responses", "anthropic", `{"model":"m","input":[{"type":"reasoning","encrypted_content":"secret_cipher","summary":[{"type":"summary_text","text":"secret_thought"}]},{"type":"message","role":"user","content":"visible"}]}`},
		{"openai-chat", "anthropic", `{"model":"m","messages":[{"role":"assistant","content":"visible","reasoning_content":"secret_thought","signature":"secret_signature"}]}`},
	} {
		t.Run(tc.caller, func(t *testing.T) {
			req, err := decodeRequest(tc.caller, []byte(tc.raw), http.Header{})
			if err != nil {
				t.Fatal(err)
			}
			if !reasoningHistoryPresent(req) {
				t.Fatal("history not detected for shadow suppression")
			}
			clean, err := foreignReasoningHistory(req, tc.caller, tc.out)
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(clean)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "secret_") || !strings.Contains(string(body), "visible") {
				t.Fatalf("unsafe history %s", body)
			}
			original, _ := json.Marshal(req.Raw)
			if !strings.Contains(string(original), "secret_") {
				t.Fatal("original input mutated")
			}
			native, err := foreignReasoningHistory(req, tc.caller, tc.caller)
			if err != nil || native != req {
				t.Fatal("native path changed")
			}
		})
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
)

// The default is a sanitized Pi-shaped request. Set the capture path to test
// the exact local request without committing its prompt or cache key. Only the
// model alias and (for unary) stream mode are changed.
func TestPiCapturedDeveloper642UnaryStreaming(t *testing.T) {
	raw := []byte(`{"model":"claude-reason","input":[{"role":"developer","content":"synthetic Pi system prompt"},{"role":"user","content":[{"type":"input_text","text":"write and read a file"}]}],"prompt_cache_key":"synthetic","include":["reasoning.encrypted_content"],"reasoning":{"effort":"low","summary":"auto"},"store":false,"max_output_tokens":2048,"stream":true,"tools":[{"type":"function","name":"write","parameters":{"type":"object"}},{"type":"function","name":"read","parameters":{"type":"object"}}]}`)
	if path := os.Getenv("ROUTER_TEST_PI_642_CAPTURE"); path != "" {
		var err error
		raw, err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
			var original map[string]any
			if err := json.Unmarshal(raw, &original); err != nil {
				t.Fatal(err)
			}
			input := valueAsSlice(original["input"])
			if len(input) == 0 {
				t.Fatal("missing input")
			}
			first, ok := input[0].(map[string]any)
			if !ok || first["role"] != "developer" {
				t.Fatal("missing leading developer")
			}
			expected, ok := first["content"].(string)
			if !ok {
				t.Fatal("captured developer is not text")
			}
			if inc := valueAsSlice(original["include"]); len(inc) != 1 || inc[0] != "reasoning.encrypted_content" {
				t.Fatal("capture lacks expected include")
			}
			var mu sync.Mutex
			hits := 0
			var seen map[string]any
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				hits++
				if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
					t.Error(err)
				}
				if !streaming {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"id":"msg_642","type":"message","role":"assistant","content":[{"type":"thinking","thinking":"visible thought","signature":"private_signature"},{"type":"text","text":"capture-ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":2}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_642\",\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"visible thought\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"capture-ok\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			}))
			defer up.Close()
			target := anthropicToolTarget()
			target.DefaultThinking = map[string]any{"type": "adaptive"}
			target.Reasoning = ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlTokenBudget}
			svc := nativeStreamTestService(t, up.URL, "anthropic", []Target{target})
			original["model"] = "native-stream"
			original["stream"] = streaming
			body, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			mu.Lock()
			defer mu.Unlock()
			if w.Code != 200 || hits != 1 || seen["system"] != expected {
				t.Fatalf("status=%d upstream hits=%d system matches=%t", w.Code, hits, seen["system"] == expected)
			}
			if _, ok := seen["include"]; ok {
				t.Fatal("include forwarded")
			}
			if v, _ := seen["thinking"].(map[string]any); v["type"] != "adaptive" {
				t.Fatal("adaptive thinking missing")
			}
			result := w.Body.Bytes()
			if !bytes.Contains(result, []byte("capture-ok")) || !bytes.Contains(result, []byte("visible thought")) || bytes.Contains(result, []byte("private_signature")) || bytes.Contains(result, []byte("encrypted_content")) {
				t.Fatalf("missing output or opaque data leaked; status=%d", w.Code)
			}
		})
	}
}

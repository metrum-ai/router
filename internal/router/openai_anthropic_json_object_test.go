// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIJSONObjectAnthropicValidation(t *testing.T) {
	for _, dialect := range []string{"openai-chat", "openai-responses"} {
		for _, stream := range []bool{false, true} {
			for _, valid := range []bool{false, true} {
				t.Run(dialect+"/stream="+map[bool]string{true: "yes", false: "no"}[stream]+"/valid="+map[bool]string{true: "yes", false: "no"}[valid], func(t *testing.T) {
					calls := 0
					up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls++
						var body map[string]any
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						if body["stream"] != false || !strings.Contains(stringValue(body["system"]), "valid JSON object") {
							t.Errorf("request missing instruction or not buffered: %#v", body)
						}
						text := "```json\n{\"ok\":true}\n```"
						if valid {
							text = `{"ok":true}`
						}
						writeJSON(w, 200, map[string]any{"id": "msg_up", "type": "message", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}}, "stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 10, "output_tokens": 4}})
					}))
					defer up.Close()
					target := anthropicToolTarget()
					target.StructuredOutput = true
					svc := nativeStreamTestService(t, up.URL, "anthropic", []Target{target})
					path := "/v1/chat/completions"
					payload := map[string]any{"model": "native-stream", "stream": stream, "messages": []any{map[string]any{"role": "user", "content": "answer"}}, "response_format": map[string]any{"type": "json_object"}}
					if dialect == "openai-responses" {
						path = "/v1/responses"
						payload = map[string]any{"model": "native-stream", "stream": stream, "input": "answer", "text": map[string]any{"format": map[string]any{"type": "json_object"}}}
					}
					encoded, _ := json.Marshal(payload)
					r := httptest.NewRequest("POST", path, strings.NewReader(string(encoded)))
					r.Header.Set("Authorization", "Bearer "+testToken)
					w := httptest.NewRecorder()
					svc.Handler().ServeHTTP(w, r)
					if calls != 1 {
						t.Fatalf("upstream calls=%d status=%d body=%s", calls, w.Code, w.Body.String())
					}
					if valid && (w.Code != 200 || !strings.Contains(w.Body.String(), `ok`)) {
						t.Fatalf("valid response: %d %s", w.Code, w.Body.String())
					}
					if !valid && (w.Code != 502 || strings.Contains(w.Body.String(), "```") || strings.Contains(w.Body.String(), "data:")) {
						t.Fatalf("invalid response exposed: %d %s", w.Code, w.Body.String())
					}
					if valid && stream && !strings.Contains(w.Body.String(), "data:") {
						t.Fatalf("missing synthesized stream: %s", w.Body.String())
					}
					svc.quota.mu.Lock()
					charged := svc.quota.state.Callers["alice"].LifetimeTokens
					svc.quota.mu.Unlock()
					if charged != 14 {
						t.Fatalf("upstream usage was not settled: got %d want 14", charged)
					}
				})
			}
		}
	}
}

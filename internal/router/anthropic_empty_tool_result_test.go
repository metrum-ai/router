// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// Exercise the request through localhost, including a file-backed tool result.
// The fake upstream checks the serialized input; it does not model live API validation.
func TestAnthropicBridgeEmptyToolResultLocalhost(t *testing.T) {
	for _, dialect := range []string{"openai-chat", "openai-responses"} {
		t.Run(dialect, func(t *testing.T) {
			var hits atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				messages := valueAsSlice(body["messages"])
				if len(messages) != 3 {
					t.Errorf("unexpected messages: %#v", messages)
					return
				}
				results := valueAsSlice(messages[2].(map[string]any)["content"])
				if len(results) != 3 {
					t.Errorf("unexpected results: %#v", results)
					return
				}
				wantIDs := []string{"call_c", "call_a", "call_b"}
				for i, value := range results {
					result := value.(map[string]any)
					if result["type"] != "tool_result" || result["tool_use_id"] != wantIDs[i] {
						t.Errorf("result %d identity: %#v", i, result)
					}
					if i < 2 {
						var content strings.Builder
						for _, part := range valueAsSlice(result["content"]) {
							content.WriteString(stringValue(part.(map[string]any)["text"]))
						}
						if want := []string{"error: missing", "alpha beta"}[i]; content.String() != want {
							t.Errorf("result %d text: got %q want %q", i, content.String(), want)
						}
					}
				}
				empty := results[2].(map[string]any)
				if value, ok := empty["content"].(string); !ok || value != "" {
					t.Errorf("empty result must be explicit empty string, got %#v", empty["content"])
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"msg_end","type":"message","role":"assistant","content":[{"type":"text","text":"continued"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}`)
			}))
			defer up.Close()
			svc := nativeStreamTestService(t, up.URL, "anthropic", []Target{anthropicToolTarget()})
			router := httptest.NewServer(svc.Handler())
			defer router.Close()
			path := filepath.Join(t.TempDir(), "empty-result.txt")
			if err := os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
			toolOutput, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			calls := []any{}
			for _, id := range []string{"a", "b", "c"} {
				calls = append(calls, map[string]any{"id": "call_" + id, "type": "function", "function": map[string]any{"name": "read", "arguments": `{"path":"` + id + `"}`}})
			}
			var endpoint string
			var body map[string]any
			if dialect == "openai-chat" {
				endpoint = "/v1/chat/completions"
				body = map[string]any{"model": "native-stream", "messages": []any{
					map[string]any{"role": "user", "content": "read files"},
					map[string]any{"role": "assistant", "tool_calls": calls},
					map[string]any{"role": "tool", "tool_call_id": "call_c", "content": "error: missing"},
					map[string]any{"role": "tool", "tool_call_id": "call_a", "content": "alpha beta"},
					map[string]any{"role": "tool", "tool_call_id": "call_b", "content": string(toolOutput)},
				}, "tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "read", "parameters": map[string]any{"type": "object"}}}}}
			} else {
				endpoint = "/v1/responses"
				body = map[string]any{"model": "native-stream", "input": []any{
					map[string]any{"role": "user", "content": "read files"},
					map[string]any{"type": "function_call", "call_id": "call_a", "name": "read", "arguments": `{"path":"a"}`},
					map[string]any{"type": "function_call", "call_id": "call_b", "name": "read", "arguments": `{"path":"b"}`},
					map[string]any{"type": "function_call", "call_id": "call_c", "name": "read", "arguments": `{"path":"c"}`},
					map[string]any{"type": "function_call_output", "call_id": "call_c", "output": "error: missing"},
					map[string]any{"type": "function_call_output", "call_id": "call_a", "output": []any{map[string]any{"type": "input_text", "text": "alpha"}, map[string]any{"type": "input_text", "text": " beta"}}},
					map[string]any{"type": "function_call_output", "call_id": "call_b", "output": string(toolOutput)},
				}, "tools": []any{map[string]any{"type": "function", "name": "read", "parameters": map[string]any{"type": "object"}}}}
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			r, err := http.NewRequest(http.MethodPost, router.URL+endpoint, strings.NewReader(string(raw)))
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("Authorization", "Bearer "+testToken)
			r.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK || hits.Load() != 1 {
				t.Fatalf("status=%d hits=%d", resp.StatusCode, hits.Load())
			}
		})
	}
}

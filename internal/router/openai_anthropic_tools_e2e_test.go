// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOpenAIAnthropicToolsUnaryContinuation(t *testing.T) {
	for _, dialect := range []string{"openai-chat", "openai-responses"} {
		t.Run(dialect, func(t *testing.T) {
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if r.URL.Path != "/v1/messages" {
					t.Errorf("upstream path %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				if calls.Add(1) == 1 {
					if len(valueAsSlice(body["tools"])) != 1 || body["tool_choice"].(map[string]any)["type"] != "any" {
						t.Errorf("tools not translated: %#v", body)
					}
					_, _ = fmt.Fprint(w, `{"id":"msg_up","type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"weather","input":{"city":"Paris"}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":4}}`)
				} else {
					messages := valueAsSlice(body["messages"])
					last := messages[len(messages)-1].(map[string]any)
					block := valueAsSlice(last["content"])[0].(map[string]any)
					if block["type"] != "tool_result" || block["tool_use_id"] != "toolu_1" {
						t.Errorf("continuation did not decode id: %#v", body)
					}
					_, _ = fmt.Fprint(w, `{"id":"msg_next","type":"message","role":"assistant","content":[{"type":"text","text":"sunny"}],"stop_reason":"end_turn","usage":{"input_tokens":15,"output_tokens":2}}`)
				}
			}))
			defer up.Close()
			target := anthropicToolTarget()
			svc := nativeStreamTestService(t, up.URL, "anthropic", []Target{target}, func(cfg *Config) { cfg.Server.Identifiers = identifierTestConfig() })
			path, first := "/v1/chat/completions", `{"model":"native-stream","messages":[{"role":"user","content":"weather"}],"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object"}}}],"tool_choice":"required"}`
			if dialect == "openai-responses" {
				path, first = "/v1/responses", `{"model":"native-stream","input":"weather","tools":[{"type":"function","name":"weather","parameters":{"type":"object"}}],"tool_choice":"required"}`
			}
			do := func(body string) *httptest.ResponseRecorder {
				req := httptest.NewRequest("POST", path, strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+testToken)
				w := httptest.NewRecorder()
				svc.Handler().ServeHTTP(w, req)
				return w
			}
			w := do(first)
			if w.Code != 200 {
				t.Fatalf("first: %d %s", w.Code, w.Body.String())
			}
			var result map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			var callID string
			if dialect == "openai-chat" {
				choice := valueAsSlice(result["choices"])[0].(map[string]any)
				msg := choice["message"].(map[string]any)
				callID = stringValue(valueAsSlice(msg["tool_calls"])[0].(map[string]any)["id"])
			} else {
				callID = stringValue(valueAsSlice(result["output"])[0].(map[string]any)["call_id"])
			}
			if callID == "" || callID == "toolu_1" {
				t.Fatalf("tool id not rewritten: %q", callID)
			}
			var continuation map[string]any
			if dialect == "openai-chat" {
				continuation = map[string]any{"model": "native-stream", "messages": []any{map[string]any{"role": "user", "content": "weather"}, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": callID, "type": "function", "function": map[string]any{"name": "weather", "arguments": `{"city":"Paris"}`}}}}, map[string]any{"role": "tool", "tool_call_id": callID, "content": "sunny"}}}
			} else {
				continuation = map[string]any{"model": "native-stream", "input": []any{map[string]any{"role": "user", "content": "weather"}, map[string]any{"type": "function_call", "call_id": callID, "name": "weather", "arguments": `{"city":"Paris"}`}, map[string]any{"type": "function_call_output", "call_id": callID, "output": "sunny"}}}
			}
			body, _ := json.Marshal(continuation)
			w = do(string(body))
			if w.Code != 200 || !strings.Contains(w.Body.String(), "sunny") || calls.Load() != 2 {
				t.Fatalf("continuation: %d %s calls=%d", w.Code, w.Body.String(), calls.Load())
			}
		})
	}
}

func TestOpenAIAnthropicToolsStream(t *testing.T) {
	fixture, err := os.ReadFile("../../testdata/sse/synthetic/anthropic-bridges/tools.sse")
	if err != nil {
		t.Fatal(err)
	}
	for _, dialect := range []string{"openai-chat", "openai-responses"} {
		t.Run(dialect, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body["stream"] != true || len(valueAsSlice(body["tools"])) != 1 {
					t.Error("missing translated stream/tool")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write(fixture)
			}))
			defer up.Close()
			svc := nativeStreamTestService(t, up.URL, "anthropic", []Target{anthropicToolTarget()})
			path, body := "/v1/chat/completions", `{"model":"native-stream","stream":true,"messages":[{"role":"user","content":"weather"}],"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object"}}}]}`
			if dialect == "openai-responses" {
				path, body = "/v1/responses", `{"model":"native-stream","stream":true,"input":"weather","tools":[{"type":"function","name":"weather","parameters":{"type":"object"}}]}`
			}
			r := httptest.NewRequest("POST", path, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "weather") {
				t.Fatalf("stream: %d %s", w.Code, w.Body.String())
			}
			if dialect == "openai-chat" && !strings.Contains(w.Body.String(), `"tool_calls"`) {
				t.Fatalf("missing chat tools: %s", w.Body.String())
			}
			if dialect == "openai-responses" && !strings.Contains(w.Body.String(), "response.function_call_arguments.delta") {
				t.Fatalf("missing responses tool delta: %s", w.Body.String())
			}
		})
	}
}

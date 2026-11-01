// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPiParallelResponsesStreamingHTTP(t *testing.T) {
	for _, dialect := range []string{"openai-chat", "anthropic"} {
		t.Run(dialect, func(t *testing.T) {
			var hits atomic.Int32
			var received map[string]any
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if dialect == "openai-chat" {
					fmt.Fprint(w, "data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"continued\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					return
				}
				fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"continued\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			}))
			defer up.Close()
			target := anthropicToolTarget()
			if dialect == "openai-chat" {
				target.ResponsesToChat = ResponsesToChatBridge{Enabled: true, Text: true, FunctionTools: true, ToolChoice: true, Streaming: true, ValidationStatus: "passed"}
			}
			svc := nativeStreamTestService(t, up.URL, dialect, []Target{target})
			input := `[{"role":"user","content":"hi"},{"type":"function_call","call_id":"c1","name":"read","arguments":"{}"},{"type":"function_call","call_id":"c2","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":[{"type":"input_text","text":"one"},{"type":"input_text","text":" two"}]},{"type":"function_call_output","call_id":"c2","output":"three"}]`
			post := func(input string) *httptest.ResponseRecorder {
				body := strings.Replace(piResponsePrefix, `"model":"m"`, `"model":"native-stream"`, 1) + `"input":` + input + `}`
				r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+testToken)
				w := httptest.NewRecorder()
				svc.Handler().ServeHTTP(w, r)
				return w
			}
			w := post(input)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "continued") || hits.Load() != 1 {
				t.Fatalf("status=%d body=%s hits=%d", w.Code, w.Body.String(), hits.Load())
			}
			messages := valueAsSlice(received["messages"])
			if dialect == "openai-chat" {
				if len(messages) != 4 {
					t.Fatalf("messages %#v", messages)
				}
				assistant := messages[1].(map[string]any)
				calls := valueAsSlice(assistant["tool_calls"])
				if len(calls) != 2 || calls[0].(map[string]any)["id"] != "c1" || calls[1].(map[string]any)["id"] != "c2" || messages[2].(map[string]any)["content"] != "one two" || messages[3].(map[string]any)["content"] != "three" {
					t.Fatalf("bad parallel chat: %#v", messages)
				}
			} else {
				if len(messages) != 3 {
					t.Fatalf("messages %#v", messages)
				}
				calls := valueAsSlice(messages[1].(map[string]any)["content"])
				results := valueAsSlice(messages[2].(map[string]any)["content"])
				if len(calls) != 2 || len(results) != 2 || calls[0].(map[string]any)["id"] != "c1" || calls[1].(map[string]any)["id"] != "c2" || results[0].(map[string]any)["tool_use_id"] != "c1" || results[1].(map[string]any)["tool_use_id"] != "c2" {
					t.Fatalf("bad parallel anthropic: %#v", messages)
				}
			}
			bad := `[{"role":"user","content":"hi"},{"type":"function_call_output","call_id":"c1","output":[{"type":"input_image","image_url":"https://example.invalid/x"}]}]`
			w = post(bad)
			if w.Code != 400 || hits.Load() != 1 || !strings.Contains(w.Body.String(), "function_call_output.output") {
				t.Fatalf("non-text output not clearly rejected: %d %s hits=%d", w.Code, w.Body.String(), hits.Load())
			}
			unknown := `[{"role":"user","content":[{"type":"future_part","text":"must not be lost"}]}]`
			w = post(unknown)
			if w.Code != 400 || hits.Load() != 1 {
				t.Fatalf("unknown content not rejected: %d %s hits=%d", w.Code, w.Body.String(), hits.Load())
			}
			if dialect == "openai-chat" {
				w = post(`[{"role":"bogus","content":"do not silently change my role"}]`)
				if w.Code != 400 || hits.Load() != 1 {
					t.Fatalf("unknown role not rejected: %d %s hits=%d", w.Code, w.Body.String(), hits.Load())
				}
			}
		})
	}
}

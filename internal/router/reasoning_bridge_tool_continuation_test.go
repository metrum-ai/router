// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bufio"
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

// Exercise opt-in reasoning, foreign history sanitization and a real file
// tool continuation on the Anthropic caller to Responses target bridge.
func TestReasoningMessagesToolStreamContinuation(t *testing.T) {
	for _, dialect := range []string{"openai-responses"} {
		for _, array := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/array=%v", dialect, array), func(t *testing.T) {
				var hits atomic.Int32
				var continuation map[string]any
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var payload map[string]any
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
						return
					}
					first := hits.Add(1) == 1
					if !first {
						continuation = payload
						b, _ := json.Marshal(payload)
						if !strings.Contains(string(b), "LOCAL-READ-NOT-EMPTY-619") {
							http.Error(w, "missing file data", 400)
							return
						}
					}
					w.Header().Set("Content-Type", "text/event-stream")
					if dialect == "openai-chat" {
						if first {
							fmt.Fprint(w, "data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\\\"file.txt\\\"}\"}}]},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
						} else {
							fmt.Fprint(w, "data: {\"id\":\"chat_2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"file had data\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chat_2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
						}
						return
					}
					if first {
						fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\nevent: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"type\":\"reasoning\",\"id\":\"rs_1\",\"encrypted_content\":\"PRIVATE_ENCRYPTED\"}}\n\nevent: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"output_index\":1,\"delta\":\"visible thought\"}\n\nevent: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"type\":\"reasoning\"}}\n\nevent: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"read\",\"arguments\":\"\"}}\n\nevent: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"item_id\":\"fc_1\",\"delta\":\"{\\\"path\\\":\\\"file.txt\\\"}\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"usage\":{\"input_tokens\":2,\"output_tokens\":2}}}\n\n")
					} else {
						fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_2\"}}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"file had data\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_2\",\"usage\":{\"input_tokens\":2,\"output_tokens\":2}}}\n\n")
					}
				}))
				defer up.Close()
				target := anthropicToolTarget()
				target.Reasoning = ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum, SupportsSummaries: true}
				svc := nativeStreamTestService(t, up.URL, dialect, []Target{target})
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("LOCAL-READ-NOT-EMPTY-619"), 0600); err != nil {
					t.Fatal(err)
				}
				post := func(messages []any) string {
					body, _ := json.Marshal(map[string]any{"model": "native-stream", "stream": true, "max_tokens": 8192, "thinking": map[string]any{"type": "enabled", "budget_tokens": 4096}, "tools": []any{map[string]any{"name": "read", "input_schema": map[string]any{"type": "object"}}}, "messages": messages})
					req := httptest.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(string(body)))
					req.Header.Set("Authorization", "Bearer "+testToken)
					rec := httptest.NewRecorder()
					svc.Handler().ServeHTTP(rec, req)
					if rec.Code != 200 {
						t.Fatalf("status %d: %s", rec.Code, rec.Body)
					}
					return rec.Body.String()
				}
				messages := []any{map[string]any{"role": "user", "content": "Read file.txt"}}
				first := post(messages)
				if !strings.Contains(first, "visible thought") || strings.Contains(first, "PRIVATE_ENCRYPTED") {
					t.Fatalf("missing visible thought or leaked ciphertext: %s", first)
				}
				var id, name, args string
				scan := bufio.NewScanner(strings.NewReader(first))
				for scan.Scan() {
					line := scan.Text()
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					var event map[string]any
					if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil {
						continue
					}
					block, _ := event["content_block"].(map[string]any)
					if stringValue(block["type"]) == "tool_use" {
						id = stringValue(block["id"])
						name = stringValue(block["name"])
					}
					delta, _ := event["delta"].(map[string]any)
					args += stringValue(delta["partial_json"])
				}
				var parsed struct {
					Path string `json:"path"`
				}
				if name != "read" || id == "" || json.Unmarshal([]byte(args), &parsed) != nil || parsed.Path != "file.txt" {
					t.Fatalf("bad returned tool call id=%q name=%q args=%q body=%s", id, name, args, first)
				}
				data, err := os.ReadFile(filepath.Join(dir, parsed.Path))
				if err != nil {
					t.Fatal(err)
				}
				var output any = string(data)
				if array {
					output = []any{map[string]any{"type": "text", "text": string(data)}}
				}
				messages = append(messages, map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "thinking", "thinking": "foreign history", "signature": "PRIVATE_SIGNATURE"}, map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{"path": parsed.Path}}}}, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": output}}})
				next := post(messages)
				if !strings.Contains(next, "file had data") || hits.Load() != 2 {
					t.Fatalf("bad continuation %s", next)
				}
				encoded, _ := json.Marshal(continuation)
				if strings.Contains(string(encoded), "PRIVATE_SIGNATURE") || strings.Contains(string(encoded), "foreign history") || strings.Contains(string(encoded), "PRIVATE_ENCRYPTED") {
					t.Fatalf("foreign reasoning leaked upstream: %s", encoded)
				}
				if dialect == "openai-chat" {
					ms := valueAsSlice(continuation["messages"])
					last := ms[len(ms)-1].(map[string]any)
					if last["role"] != "tool" || last["tool_call_id"] != id || last["content"] != string(data) {
						t.Fatalf("lost chat result %#v", ms)
					}
				} else {
					input := valueAsSlice(continuation["input"])
					last := input[len(input)-1].(map[string]any)
					if last["type"] != "function_call_output" || last["call_id"] != id || last["output"] != string(data) {
						t.Fatalf("lost responses result %#v", input)
					}
				}
			})
		}
	}
}

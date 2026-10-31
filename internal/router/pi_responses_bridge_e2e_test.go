// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPiResponsesToolStreamingLocalhostContinuation(t *testing.T) {
	for _, dialect := range []string{"openai-chat", "anthropic"} {
		t.Run(dialect, func(t *testing.T) {
			var count atomic.Int32
			requests := make(chan map[string]any, 2)
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				requests <- payload
				w.Header().Set("Content-Type", "text/event-stream")
				first := count.Add(1) == 1
				if dialect == "openai-chat" {
					if first {
						fmt.Fprint(w, "data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\\\"quote.txt\\\"}\"}}]},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
					} else {
						fmt.Fprint(w, "data: {\"id\":\"chat_2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"continued\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chat_2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					}
					return
				}
				fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}\n\n")
				if first {
					fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"read\",\"input\":{}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"quote.txt\\\"}\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":2}}\n\n")
				} else {
					fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"continued\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n")
				}
				fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			}))
			defer up.Close()
			target := anthropicToolTarget()
			if dialect == "openai-chat" {
				target.ResponsesToChat = ResponsesToChatBridge{Enabled: true, Text: true, FunctionTools: true, ToolChoice: true, Streaming: true, ValidationStatus: "passed"}
			}
			svc := nativeStreamTestService(t, up.URL, dialect, []Target{target})
			router := httptest.NewServer(svc.Handler())
			defer router.Close()
			tool := map[string]any{"type": "function", "name": "read", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}}}
			base := map[string]any{"model": "native-stream", "tools": []any{tool}, "max_output_tokens": 512, "stream": true, "store": false, "prompt_cache_key": "synthetic"}
			input := []any{map[string]any{"role": "system", "content": "You are a coding assistant."}, map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Read quote.txt"}}}}
			post := func(input []any) string {
				base["input"] = input
				payload, _ := json.Marshal(base)
				req, _ := http.NewRequest(http.MethodPost, router.URL+"/v1/responses", bytes.NewReader(payload))
				req.Header.Set("Authorization", "Bearer "+testToken)
				req.Header.Set("Content-Type", "application/json")
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				raw, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != 200 {
					t.Fatalf("router status %d: %s", resp.StatusCode, raw)
				}
				return string(raw)
			}
			first := post(input)
			var call map[string]any
			scan := bufio.NewScanner(strings.NewReader(first))
			for scan.Scan() {
				data, ok := strings.CutPrefix(scan.Text(), "data: ")
				if !ok {
					continue
				}
				var event map[string]any
				if json.Unmarshal([]byte(data), &event) != nil {
					continue
				}
				item, _ := event["item"].(map[string]any)
				if event["type"] == "response.output_item.done" && item["type"] == "function_call" {
					call = item
				}
			}
			if call == nil || call["name"] != "read" || !strings.Contains(stringValue(call["arguments"]), "quote.txt") {
				t.Fatalf("missing tool call: %s", first)
			}
			path := filepath.Join(t.TempDir(), "quote.txt")
			if err := os.WriteFile(path, []byte("E2E-TOOL-RESULT-7842"), 0600); err != nil {
				t.Fatal(err)
			}
			effect, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			input = append(input, map[string]any{"type": "function_call", "call_id": call["call_id"], "name": call["name"], "arguments": call["arguments"]}, map[string]any{"type": "function_call_output", "call_id": call["call_id"], "output": string(effect)})
			second := post(input)
			if !strings.Contains(second, "continued") || !strings.Contains(second, "response.completed") || count.Load() != 2 {
				t.Fatalf("bad continuation: %s calls=%d", second, count.Load())
			}
			initial, continuation := <-requests, <-requests
			initialJSON, _ := json.Marshal(initial)
			continuationJSON, _ := json.Marshal(continuation)
			if !strings.Contains(string(initialJSON), "You are a coding assistant.") || !strings.Contains(string(continuationJSON), "E2E-TOOL-RESULT-7842") || !strings.Contains(string(continuationJSON), "call_1") {
				t.Fatalf("lost system or tool output: initial=%s next=%s", initialJSON, continuationJSON)
			}
		})
	}
}

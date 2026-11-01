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

func TestReasoningResponsesToolStreamingLocalhostContinuation(t *testing.T) {
	for _, dialect := range []string{"anthropic"} {
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
				first := count.Add(1) == 1
				if !first {
					body, _ := json.Marshal(payload)
					if !strings.Contains(string(body), "E2E-TOOL-RESULT-7842") {
						http.Error(w, "missing expected tool output", http.StatusBadRequest)
						return
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
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
					fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"visible thought\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"PRIVATE_SIGNATURE\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n")
					fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"read\",\"input\":{}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"quote.txt\\\"}\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":2}}\n\n")
				} else {
					fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"continued\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n")
				}
				fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			}))
			defer up.Close()
			target := anthropicToolTarget()
			target.DefaultThinking = map[string]any{"type": "adaptive"}
			target.Reasoning = ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlTokenBudget}
			if dialect == "openai-chat" {
				target.ResponsesToChat = ResponsesToChatBridge{Enabled: true, Text: true, FunctionTools: true, ToolChoice: true, Streaming: true, ValidationStatus: "passed"}
			}
			svc := nativeStreamTestService(t, up.URL, dialect, []Target{target})
			router := httptest.NewServer(svc.Handler())
			defer router.Close()
			tool := map[string]any{"type": "function", "name": "read", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}}}
			base := map[string]any{"model": "native-stream", "tools": []any{tool}, "max_output_tokens": 2048, "reasoning": map[string]any{"effort": "low"}, "stream": true, "store": false, "prompt_cache_key": "synthetic"}
			input := []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Read quote.txt"}}}}
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
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "quote.txt"), []byte("E2E-TOOL-RESULT-7842"), 0600); err != nil {
				t.Fatal(err)
			}
			first := post(input)
			if !strings.Contains(first, "visible thought") || strings.Contains(first, "PRIVATE_SIGNATURE") {
				t.Fatalf("reasoning or signature failure: %s", first)
			}
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
			var args struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal([]byte(stringValue(call["arguments"])), &args); err != nil || args.Path != "quote.txt" {
				t.Fatalf("unexpected returned read arguments: %v %#v", err, args)
			}
			effect, err := os.ReadFile(filepath.Join(dir, args.Path))
			if err != nil {
				t.Fatal(err)
			}
			input = append(input, map[string]any{"type": "reasoning", "encrypted_content": "PRIVATE_CIPHER", "summary": []any{map[string]any{"type": "summary_text", "text": "foreign reasoning"}}}, map[string]any{"type": "function_call", "call_id": call["call_id"], "name": call["name"], "arguments": call["arguments"]}, map[string]any{"type": "function_call_output", "call_id": call["call_id"], "output": string(effect)})
			second := post(input)
			if !strings.Contains(second, "continued") || !strings.Contains(second, "response.completed") || count.Load() != 2 {
				t.Fatalf("bad continuation: %s calls=%d", second, count.Load())
			}
			_, continuation := <-requests, <-requests
			continuationJSON, _ := json.Marshal(continuation)
			if strings.Contains(string(continuationJSON), "PRIVATE_CIPHER") || strings.Contains(string(continuationJSON), "foreign reasoning") {
				t.Fatalf("foreign reasoning leaked: %s", continuationJSON)
			}
			messages := valueAsSlice(continuation["messages"])
			if len(messages) < 3 {
				t.Fatalf("missing continuation messages: %#v", continuation)
			}
			assistant, _ := messages[len(messages)-2].(map[string]any)
			result, _ := messages[len(messages)-1].(map[string]any)
			if dialect == "openai-chat" {
				calls := valueAsSlice(assistant["tool_calls"])
				if assistant["role"] != "assistant" || len(calls) != 1 || calls[0].(map[string]any)["id"] != "call_1" || result["role"] != "tool" || result["tool_call_id"] != "call_1" || result["content"] != "E2E-TOOL-RESULT-7842" {
					t.Fatalf("bad Chat continuation: %#v", messages)
				}
			} else {
				uses, results := valueAsSlice(assistant["content"]), valueAsSlice(result["content"])
				if assistant["role"] != "assistant" || result["role"] != "user" || len(uses) != 1 || len(results) != 1 {
					t.Fatalf("bad Anthropic continuation: %#v", messages)
				}
				use := uses[0].(map[string]any)
				toolResult := results[0].(map[string]any)
				encoded, _ := json.Marshal(toolResult)
				if use["type"] != "tool_use" || use["id"] != "call_1" || toolResult["type"] != "tool_result" || toolResult["tool_use_id"] != "call_1" || !strings.Contains(string(encoded), "E2E-TOOL-RESULT-7842") {
					t.Fatalf("bad Anthropic tool IDs/result: %#v", messages)
				}
			}
		})
	}
}

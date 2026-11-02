// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// Three-call synthetic native SSE controls. The fixture is not a live capture.
func TestParallelNativeStreams(t *testing.T) {
	for _, tc := range []struct{ dialect, path, fixture string }{
		{"openai-chat", "/v1/chat/completions", "chat-three.sse"},
		{"openai-responses", "/v1/responses", "responses-three.sse"},
		{"anthropic", "/v1/messages", "anthropic-three.sse"},
	} {
		t.Run(tc.dialect, func(t *testing.T) {
			fixture, err := os.ReadFile(filepath.Join("../../testdata/sse/parallel", tc.fixture))
			if err != nil {
				t.Fatal(err)
			}
			var hits atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if body["stream"] != true {
					t.Errorf("upstream stream flag: %#v", body["stream"])
				}
				w.Header().Set("Content-Type", "text/event-stream")
				for start := 0; start < len(fixture); {
					end := start + 7
					if end > len(fixture) {
						end = len(fixture)
					}
					if _, err := w.Write(fixture[start:end]); err != nil {
						return
					}
					w.(http.Flusher).Flush()
					start = end
				}
			}))
			defer up.Close()
			svc := nativeStreamTestService(t, up.URL, tc.dialect, []Target{anthropicToolTarget()})
			router := httptest.NewServer(svc.Handler())
			defer router.Close()
			body := map[string]any{"model": "native-stream", "stream": true}
			switch tc.dialect {
			case "openai-chat":
				body["messages"] = []any{map[string]any{"role": "user", "content": "read files"}}
				body["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "read", "parameters": map[string]any{"type": "object"}}}}
				body["stream_options"] = map[string]any{"include_usage": true}
			case "openai-responses":
				body["input"] = "read files"
				body["store"] = false
				body["tools"] = []any{map[string]any{"type": "function", "name": "read", "parameters": map[string]any{"type": "object"}}}
			case "anthropic":
				body["messages"] = []any{map[string]any{"role": "user", "content": "read files"}}
				body["max_tokens"] = 256
				body["tools"] = []any{map[string]any{"name": "read", "input_schema": map[string]any{"type": "object"}}}
			}
			raw, _ := json.Marshal(body)
			req, _ := http.NewRequest(http.MethodPost, router.URL+tc.path, bytes.NewReader(raw))
			req.Header.Set("Authorization", "Bearer "+testToken)
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			output, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != 200 || hits.Load() != 1 {
				t.Fatalf("status=%d hits=%d output=%s", resp.StatusCode, hits.Load(), output)
			}
			ids, args, items, names := map[int]string{}, map[int]string{}, map[int]string{}, map[int]string{}
			var order []int
			stop, usage := false, false
			scanner := bufio.NewScanner(bytes.NewReader(output))
			for scanner.Scan() {
				data, ok := strings.CutPrefix(scanner.Text(), "data:")
				if !ok {
					continue
				}
				data = strings.TrimSpace(data)
				if data == "[DONE]" {
					continue
				}
				var p map[string]any
				if err := json.Unmarshal([]byte(data), &p); err != nil {
					t.Fatalf("bad SSE: %s: %v", data, err)
				}
				switch tc.dialect {
				case "openai-chat":
					if p["usage"] != nil {
						usage = true
					}
					for _, c := range valueAsSlice(p["choices"]) {
						choice := c.(map[string]any)
						if choice["finish_reason"] == "tool_calls" {
							stop = true
						}
						delta, _ := choice["delta"].(map[string]any)
						for _, value := range valueAsSlice(delta["tool_calls"]) {
							call := value.(map[string]any)
							index := int(call["index"].(float64))
							if id, ok := call["id"].(string); ok {
								ids[index] = id
								order = append(order, index)
							}
							if f, ok := call["function"].(map[string]any); ok {
								names[index] += stringValue(f["name"])
								args[index] += stringValue(f["arguments"])
							}
						}
					}
				case "openai-responses":
					typ := stringValue(p["type"])
					if typ == "response.completed" {
						stop = true
						response, _ := p["response"].(map[string]any)
						usage = response["usage"] != nil
					}
					if typ == "response.output_item.added" {
						item, _ := p["item"].(map[string]any)
						if item["type"] == "function_call" {
							index := int(p["output_index"].(float64))
							ids[index] = stringValue(item["call_id"])
							names[index] = stringValue(item["name"])
							items[index] = stringValue(item["id"])
							order = append(order, index)
						}
					}
					if typ == "response.function_call_arguments.delta" {
						index := int(p["output_index"].(float64))
						if items[index] == "" || p["item_id"] != items[index] {
							t.Fatalf("item index mismatch: %#v", p)
						}
						args[index] += stringValue(p["delta"])
					}
				case "anthropic":
					typ := stringValue(p["type"])
					if typ == "message_delta" {
						delta, _ := p["delta"].(map[string]any)
						stop = delta["stop_reason"] == "tool_use"
						usage = p["usage"] != nil
					}
					if typ == "content_block_start" {
						block, _ := p["content_block"].(map[string]any)
						if block["type"] == "tool_use" {
							index := int(p["index"].(float64))
							ids[index] = stringValue(block["id"])
							names[index] = stringValue(block["name"])
							order = append(order, index)
						}
					}
					if typ == "content_block_delta" {
						delta, _ := p["delta"].(map[string]any)
						if delta["type"] == "input_json_delta" {
							index := int(p["index"].(float64))
							if ids[index] == "" {
								t.Fatalf("delta before tool start: %#v", p)
							}
							args[index] += stringValue(delta["partial_json"])
						}
					}
				}
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
			if !stop || !usage || len(ids) != 3 || len(order) != 3 {
				t.Fatalf("missing tool/finish/usage: ids=%v order=%v stop=%v usage=%v output=%s", ids, order, stop, usage, output)
			}
			for i, index := range order {
				var input map[string]any
				if ids[index] != []string{"call_a", "call_b", "call_c"}[i] || names[index] != "read" || json.Unmarshal([]byte(args[index]), &input) != nil || input["path"] != []string{"a", "b", "c"}[i] {
					t.Fatalf("call %d: id=%q name=%q args=%q", i, ids[index], names[index], args[index])
				}
			}
		})
	}
}

// Copyright (c) 2026 Metrum AI. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package router

import (
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

func TestSSELineLimitConfig(t *testing.T) {
	for _, n := range []int{0, 64 << 10, 8 << 20, 32 << 20, -1, (64 << 10) - 1, (32 << 20) + 1} {
		cfg := testConfig(t, "http://localhost:9999", "key", t.TempDir())
		cfg.Server.Streaming.MaxSSELineBytes = n
		cfg.setDefaults()
		err := cfg.Validate()
		valid := n == 0 || n >= 64<<10 && n <= 32<<20
		if (err == nil) != valid {
			t.Fatalf("limit %d: %v", n, err)
		}
		if n == 0 && cfg.Server.Streaming.MaxSSELineBytes != 8<<20 {
			t.Fatal("wrong default")
		}
	}
}

// Real sockets exercise native and translated streams, a repeated completed
// payload, file creation, and the client's subsequent tool-result continuation.
func TestLargeSSEToolContinuationLocalhost(t *testing.T) {
	for _, surface := range []string{"openai-chat", "openai-responses", "chat-bridge"} {
		dialect := surface
		if surface == "chat-bridge" {
			dialect = "openai-responses"
		}
		t.Run(surface, func(t *testing.T) {
			content := strings.Repeat("x", 1<<20)
			args, _ := json.Marshal(map[string]string{"path": "proof.txt", "content": content})
			var continued atomic.Bool
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				if surface == "chat-bridge" {
					fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_large\"}}\n\n")
				}
				if strings.Contains(string(raw), "file created") {
					continued.Store(true)
					if dialect == "openai-chat" {
						fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					} else {
						fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_next\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n")
					}
					return
				}
				var payload any
				if dialect == "openai-chat" {
					payload = map[string]any{"id": "chat_large", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_large", "type": "function", "function": map[string]any{"name": "write_file", "arguments": string(args)}}}}, "finish_reason": "tool_calls"}}}
				} else {
					payload = map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_large", "status": "completed", "output": []any{map[string]any{"type": "function_call", "id": "fc_large", "call_id": "call_large", "name": "write_file", "arguments": string(args)}}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 20}}}
				}
				if surface == "chat-bridge" {
					item := payload.(map[string]any)["response"].(map[string]any)["output"].([]any)[0]
					event, _ := json.Marshal(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item})
					fmt.Fprintf(w, "data: %s\n\n", event)
				}
				data, _ := json.Marshal(payload)
				fmt.Fprintf(w, "data: %s\n\n", data)
				if dialect == "openai-chat" {
					fmt.Fprint(w, "data: [DONE]\n\n")
				}
			}))
			defer up.Close()
			cfg := testConfig(t, up.URL, "key", t.TempDir())
			cfg.Provider = map[string]ProviderConfig{"up": {BaseURL: up.URL, Dialect: dialect}}
			cfg.Models = map[string]ModelGroup{"large": {Strategy: "static", Targets: []Target{{Provider: "up", Model: "large", ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}, OpenAIResponses: []string{"function"}}}}}}
			if surface == "chat-bridge" {
				g := cfg.Models["large"]
				g.Targets[0].Bridges.ChatToResponses = DialectBridgeSupport{Enabled: true, Tools: true, ToolChoice: true}
				cfg.Models["large"] = g
			}
			cfg.Callers[0].Allow = []string{"large"}
			cfg.Server.DefaultModelGroup = "large"
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			router := httptest.NewServer(svc.Handler())
			defer router.Close()
			path, body := "/v1/chat/completions", `{"model":"large","stream":true,"messages":[{"role":"user","content":"write"}]}`
			if surface == "openai-responses" {
				path, body = "/v1/responses", `{"model":"large","stream":true,"input":"write"}`
			}
			post := func(body string) string {
				req, _ := http.NewRequest("POST", router.URL+path, strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+testToken)
				req.Header.Set("Content-Type", "application/json")
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				raw, _ := io.ReadAll(resp.Body)
				if resp.StatusCode != 200 {
					t.Fatalf("status %d: %.200s", resp.StatusCode, raw)
				}
				return string(raw)
			}
			raw := post(body)
			if !strings.Contains(raw, string(args)[100:1000]) || !strings.Contains(raw, "call_large") {
				t.Fatal("large tool call lost")
			}
			// Execute the returned tool arguments, not an independent canned write.
			var returned map[string]string
			for _, line := range strings.Split(raw, "\n") {
				if !strings.HasPrefix(line, "data:") {
					continue
				}
				var payload map[string]any
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data:")), &payload) != nil {
					continue
				}
				if surface != "openai-responses" {
					choices, ok := payload["choices"].([]any)
					if !ok || len(choices) == 0 {
						continue
					}
					delta, _ := choices[0].(map[string]any)["delta"].(map[string]any)
					if delta["tool_calls"] == nil {
						continue
					}
					returnedArgs := payload["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["arguments"].(string)
					_ = json.Unmarshal([]byte(returnedArgs), &returned)
				} else if response, ok := payload["response"].(map[string]any); ok {
					output := response["output"].([]any)
					if len(output) > 0 {
						_ = json.Unmarshal([]byte(output[0].(map[string]any)["arguments"].(string)), &returned)
					}
				}
			}
			if returned["content"] != content {
				t.Fatal("tool arguments changed")
			}
			proof := filepath.Join(t.TempDir(), "proof.txt")
			if err := os.WriteFile(proof, []byte(returned["content"]), 0600); err != nil {
				t.Fatal(err)
			}
			if surface != "openai-responses" {
				post(`{"model":"large","stream":true,"messages":[{"role":"assistant","tool_calls":[{"id":"call_large","type":"function","function":{"name":"write_file","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_large","content":"file created"}]}`)
			} else {
				post(`{"model":"large","stream":true,"input":[{"type":"function_call_output","call_id":"call_large","output":"file created"}]}`)
			}
			if !continued.Load() {
				t.Fatal("tool continuation missing")
			}
			t.Log("1 MiB streaming tool arguments, file write and tool-result continuation passed")
		})
	}
}

func TestSSELineLimitFallbackAndDiscardedUsage(t *testing.T) {
	for _, surface := range []string{"openai-chat", "openai-responses", "chat-bridge"} {
		dialect := surface
		if surface == "chat-bridge" {
			dialect = "openai-responses"
		}
		for _, mode := range []string{"fallback", "exhausted", "committed"} {
			t.Run(surface+"/"+mode, func(t *testing.T) {
				var backupCalls atomic.Int64
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					w.Header().Set("Content-Type", "text/event-stream")
					if surface == "chat-bridge" {
						fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_first\",\"usage\":{\"input_tokens\":4,\"output_tokens\":3,\"total_tokens\":7}}}\n\n")
					}
					if body["model"] == "backup" {
						backupCalls.Add(1)
						if dialect == "openai-chat" {
							fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"backup-ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
						} else {
							fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_backup\",\"status\":\"completed\",\"output\":[]}}\n\n")
						}
						return
					}
					if dialect == "openai-chat" {
						fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":3,\"total_tokens\":7}}\n\n")
					}
					if mode == "committed" && surface == "chat-bridge" {
						fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"first\"}\n\n")
					}
					if mode == "committed" && surface != "chat-bridge" {
						fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_first\"},\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
					}
					fmt.Fprint(w, "data: "+strings.Repeat("x", 65<<10)+"\n\n")
				}))
				defer up.Close()
				dir := t.TempDir()
				cfg := testConfig(t, up.URL, "key", dir)
				cfg.Server.Streaming.MaxSSELineBytes = 64 << 10
				cfg.Provider = map[string]ProviderConfig{"up": {BaseURL: up.URL, Dialect: dialect}}
				targets := []Target{{Provider: "up", Model: "primary"}}
				if mode != "exhausted" {
					targets = append(targets, Target{Provider: "up", Model: "backup"})
				}
				if surface == "chat-bridge" {
					for i := range targets {
						targets[i].Bridges.ChatToResponses = DialectBridgeSupport{Enabled: true, Tools: true, ToolChoice: true}
					}
				}
				cfg.Models = map[string]ModelGroup{"large": {Strategy: "static", Targets: targets}}
				cfg.Callers[0].Allow = []string{"large"}
				cfg.Server.DefaultModelGroup = "large"
				svc, err := New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				path, body := "/v1/chat/completions", `{"model":"large","stream":true,"messages":[{"role":"user","content":"hi"}]}`
				if surface == "openai-responses" {
					path, body = "/v1/responses", `{"model":"large","stream":true,"input":"hi"}`
				}
				req := httptest.NewRequest("POST", path, strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+testToken)
				rr := httptest.NewRecorder()
				svc.Handler().ServeHTTP(rr, req)
				svc.Close()
				wantCalls := int64(0)
				if mode == "fallback" {
					wantCalls = 1
				}
				if backupCalls.Load() != wantCalls {
					t.Fatalf("backup calls %d want %d", backupCalls.Load(), wantCalls)
				}
				if mode == "exhausted" {
					if rr.Code != 502 || !strings.Contains(rr.Body.String(), "upstream_frame_too_large") {
						t.Fatalf("%d %.300s", rr.Code, rr.Body.String())
					}
				}
				if mode == "fallback" && rr.Code != 200 {
					t.Fatalf("status %d", rr.Code)
				}
				if surface != "openai-responses" && mode != "committed" {
					raw, err := os.ReadFile(filepath.Join(dir, "requests.jsonl"))
					if err != nil {
						t.Fatal(err)
					}
					var record logRecord
					if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &record); err != nil {
						t.Fatal(err)
					}
					if mode == "fallback" {
						wantSource := "estimated"
						if surface == "chat-bridge" {
							wantSource = "reported"
						}
						if record.Usage.TotalTokens <= 7 || record.UsageSource != wantSource {
							t.Fatalf("serving estimate or discarded usage lost: %+v", record.Usage)
						}
					} else if record.Usage.InputTokens != 4 || record.Usage.OutputTokens != 3 || record.Usage.TotalTokens != 7 {
						t.Fatalf("reported upstream usage lost: %+v", record.Usage)
					}
					if (mode == "exhausted" && record.UsageSource != "discarded") || !strings.Contains(string(raw), "upstream-usage-without-downstream-output") {
						t.Fatalf("discarded usage not marked: %.1000s", raw)
					}
				}
			})
		}
	}
}

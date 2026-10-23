// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Opt-in because CI does not install Claude Code. No provider credentials or
// paid inference: Claude's only upstream is this localhost fixture router.
func TestClaudeCodeCacheLocalhostContinuation(t *testing.T) {
	if os.Getenv("USAGE_A_CLAUDE_E2E") != "1" {
		t.Skip("USAGE_A_CLAUDE_E2E=1 and claude required")
	}
	for _, dialect := range []string{"anthropic", "openai-chat"} {
		t.Run(dialect, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "created.txt")
			var sawResult atomic.Bool
			up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Logf("stub received %s %s", r.Method, r.URL.Path)
				body, _ := io.ReadAll(r.Body)
				continuation := strings.Contains(string(body), `"tool_result"`) || strings.Contains(string(body), `"role":"tool"`)
				if continuation {
					sawResult.Store(true)
					var payload map[string]any
					_ = json.Unmarshal(body, &payload)
					messages, _ := payload["messages"].([]any)
					if len(messages) > 0 {
						b, _ := json.Marshal(messages[len(messages)-1])
						t.Logf("fixture tool result: %s", b)
					}
				}
				var message any
				if continuation {
					message = map[string]any{"type": "text", "text": "CACHE_CONTINUATION_OK"}
				} else {
					message = map[string]any{"type": "tool_use", "id": "tool_cache_write", "name": "Write", "input": map[string]any{"file_path": file, "content": "cache continuation\n"}}
				}
				stop := "tool_use"
				if continuation {
					stop = "end_turn"
				}
				var raw map[string]any
				_ = json.Unmarshal(body, &raw)
				if raw["stream"] != true && dialect == "anthropic" {
					w.Header().Set("Content-Type", "application/json")
					b, _ := json.Marshal(map[string]any{"id": "msg_cache", "type": "message", "role": "assistant", "model": "bridge", "content": []any{message}, "stop_reason": stop, "usage": map[string]any{"input_tokens": 25, "output_tokens": 5, "cache_read_input_tokens": 2000, "cache_creation_input_tokens": 300}})
					w.Write(b)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				emit := func(p any) { b, _ := json.Marshal(p); fmt.Fprintf(w, "data: %s\n\n", b) }
				if dialect == "anthropic" {
					emit(map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_cache", "type": "message", "role": "assistant", "model": "bridge", "content": []any{}, "stop_reason": nil, "usage": map[string]any{"input_tokens": 25, "output_tokens": 0, "cache_read_input_tokens": 2000, "cache_creation_input_tokens": 300}}})
					emit(map[string]any{"type": "content_block_start", "index": 0, "content_block": message})
					emit(map[string]any{"type": "content_block_stop", "index": 0})
					emit(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 5}})
					emit(map[string]any{"type": "message_stop"})
				} else {
					delta := map[string]any{"role": "assistant", "content": "CACHE_CONTINUATION_OK"}
					finish := "stop"
					if !continuation {
						args, _ := json.Marshal(map[string]any{"file_path": file, "content": "cache continuation\n"})
						delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "tool_cache_write", "type": "function", "function": map[string]any{"name": "Write", "arguments": string(args)}}}}
						finish = "tool_calls"
					}
					emit(map[string]any{"id": "chat_cache", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": 2325, "completion_tokens": 5, "total_tokens": 2330, "prompt_tokens_details": map[string]any{"cached_tokens": 2000, "cache_write_tokens": 300}}})
					fmt.Fprint(w, "data: [DONE]\n\n")
				}
			}))
			var err error
			up.Listener, err = net.Listen("tcp", "127.0.0.1:18477")
			if err != nil {
				t.Fatal(err)
			}
			up.Start()
			defer up.Close()
			target := anthropicToolTarget()
			cached, write := 1.0, 12.5
			target.InputPricePerMillionUSD = 10
			target.OutputPricePerMillionUSD = 20
			target.CachedInputPricePerMillionUSD = &cached
			target.CacheWritePricePerMillionUSD = &write
			svc := nativeStreamTestService(t, up.URL, dialect, []Target{target}, func(c *Config) {
				c.Server.Cache.Enabled = false
				c.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
			})
			listener, err := net.Listen("tcp", "127.0.0.1:18476")
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Logf("router received %s %s", r.Method, r.URL.Path)
				svc.Handler().ServeHTTP(w, r)
			})}
			go server.Serve(listener)
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "claude", "--debug-file", filepath.Join(dir, "claude-debug.log"), "--setting-sources", "", "--dangerously-skip-permissions", "--no-session-persistence", "--tools", "Write", "--effort", "low", "--model", "native-stream", "-p", "Create created.txt using Write, then confirm the tool result.")
			cmd.Dir = dir
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "ANTHROPIC_BASE_URL=http://127.0.0.1:18476/anthropic", "ANTHROPIC_API_KEY=" + testToken, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_TELEMETRY=1", "DISABLE_ERROR_REPORTING=1", "MAX_THINKING_TOKENS=0", "CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING=1"}
			out, err := cmd.CombinedOutput()
			if err != nil {
				debug, _ := os.ReadFile(filepath.Join(dir, "claude-debug.log"))
				t.Logf("fixture-only Claude debug: %s", debug)
				t.Fatalf("Claude: %v %s", err, out)
			}
			if !sawResult.Load() || !strings.Contains(string(out), "CACHE_CONTINUATION_OK") {
				t.Fatalf("continuation missing: %s", out)
			}
			got, err := os.ReadFile(file)
			if err != nil || string(got) != "cache continuation\n" {
				t.Fatalf("file %q %v", got, err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				var rows []usageRecord
				_ = svc.usage.db.Find(&rows).Error
				if len(rows) >= 2 {
					for _, row := range rows {
						if row.InputTokens != 2325 || row.CacheWriteInputTokens == nil || *row.CacheWriteInputTokens != 300 || row.TotalCostUSD != 0.0061 {
							t.Fatalf("stored=%+v", row)
						}
						t.Logf("Claude Code %s stored input=%d cached=2000 write=300 output=%d total_cost=%g", dialect, row.InputTokens, row.OutputTokens, row.TotalCostUSD)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("missing rows")
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Logf("Claude Code %s: actual Write file + tool result continuation PASS", dialect)
		})
	}
}

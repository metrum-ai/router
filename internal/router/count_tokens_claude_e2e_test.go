//go:build count_tokens_claude_e2e

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

// Optional real coding-agent check. Upstream is deterministic and credential-free;
// Claude Code itself executes Write and manages context compaction.
func TestCountTokensClaudeCodeCompactionE2E(t *testing.T) {
	cli, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	created := filepath.Join(dir, "created-by-claude.txt")
	var counts, compactions, messages atomic.Int64
	var issuedTool, continued atomic.Bool
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if r.URL.Path == "/v1/messages/count_tokens" {
			counts.Add(1)
			writeJSON(w, 200, map[string]any{"input_tokens": 12345})
			return
		}
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		n := messages.Add(1)
		raw, _ := json.Marshal(body["messages"])
		if strings.Contains(string(raw), `"tool_result"`) {
			continued.Store(true)
		}
		compact := false
		if list, ok := body["messages"].([]any); ok && len(list) > 0 {
			last, _ := json.Marshal(list[len(list)-1])
			text := strings.ToLower(string(last))
			compact = issuedTool.Load() && (strings.Contains(text, "summary of the conversation") || strings.Contains(text, "summarize the conversation") || strings.Contains(text, "detailed summary"))
		}
		if compact {
			compactions.Add(1)
		}
		tools, _ := json.Marshal(body["tools"])
		tool := !compact && strings.Contains(string(tools), `"name":"Write"`) && issuedTool.CompareAndSwap(false, true)
		t.Logf("Claude upstream request %d compact=%v tool=%v hasWrite=%v", n, compact, tool, strings.Contains(string(tools), `"name":"Write"`))
		input := 100
		content := []any{map[string]any{"type": "text", "text": "Done. File created; context summary is complete."}}
		stop := "end_turn"
		if tool {
			input = 190000
			stop = "tool_use"
			content = []any{map[string]any{"type": "tool_use", "id": "toolu_count_e2e", "name": "Write", "input": map[string]any{"file_path": created, "content": "created by real Claude Code tool execution\n"}}}
		}
		id := fmt.Sprintf("msg_count_%d", n)
		if body["stream"] != true {
			writeJSON(w, 200, map[string]any{"id": id, "type": "message", "role": "assistant", "model": "claude-sonnet-4-5", "content": content, "stop_reason": stop, "stop_sequence": nil, "usage": map[string]any{"input_tokens": input, "output_tokens": 100}})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		event := func(name string, data any) {
			encoded, _ := json.Marshal(data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, encoded)
		}
		event("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": id, "type": "message", "role": "assistant", "model": "claude-sonnet-4-5", "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": input, "output_tokens": 0}}})
		if tool {
			event("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "toolu_count_e2e", "name": "Write", "input": map[string]any{}}})
			args, _ := json.Marshal(map[string]any{"file_path": created, "content": "created by real Claude Code tool execution\n"})
			event("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(args)}})
		} else {
			event("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
			event("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "Done. File created; context summary is complete."}})
		}
		event("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		event("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 100}})
		event("message_stop", map[string]any{"type": "message_stop"})
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:18447")
	if err != nil {
		t.Fatal(err)
	}
	upstream.Listener.Close()
	upstream.Listener = listener
	upstream.Start()
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "mock-provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.Identifiers = identifierTestConfig()
	provider := cfg.Provider["mock"]
	provider.Dialect = "anthropic"
	provider.BaseURL = upstream.URL
	cfg.Provider["mock"] = provider
	group := cfg.Models["default"]
	group.Targets[0].Model = "claude-sonnet-4-5"
	group.Targets[0].ToolSupport = ToolSupport{AnthropicMessages: []string{"client_tools"}}
	group.Targets[0].Reasoning = ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlTokenBudget, MinBudgetTokens: 1, MaxBudgetTokens: 100000}
	honors := true
	group.Targets[0].HonorsMaxTokens = &honors
	group.Targets[0].ContextTokens = 200000
	group.Targets[0].AllowedBetas = []string{"context-management-2025-06-27", "interleaved-thinking-2025-05-14"}
	cfg.Models["claude-sonnet-4-5"] = group
	cfg.Callers[0].Allow = []string{"claude-sonnet-4-5"}
	cfg.Callers[0].Rate.TPM = 10000000
	cfg.Callers[0].Quota.Day.Tokens = 10000000
	cfg.Callers[0].Quota.Month.Tokens = 10000000
	cfg.Callers[0].Key.LifetimeTokens = 10000000
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	router := httptest.NewUnstartedServer(svc.Handler())
	listener, err = net.Listen("tcp", "127.0.0.1:18448")
	if err != nil {
		t.Fatal(err)
	}
	router.Listener.Close()
	router.Listener = listener
	router.Start()
	defer router.Close()
	// A pre-compaction count must equal the direct upstream count and settle no usage.
	countBody := `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"compaction prefix"}]}`
	req, _ := http.NewRequest("POST", router.URL+"/v1/messages/count_tokens", strings.NewReader(countBody))
	req.Header.Set("X-API-Key", testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(data), "12345") {
		t.Fatalf("count status=%d body=%s", resp.StatusCode, data)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	debugFile := os.Getenv("COUNT_TOKENS_CLAUDE_DEBUG")
	if debugFile == "" {
		debugFile = filepath.Join(dir, "claude-debug.log")
	}
	cmd := exec.CommandContext(ctx, cli, "--debug-file", debugFile, "--setting-sources", "", "--print", "--dangerously-skip-permissions", "--model", "claude-sonnet-4-5", "--autocompact", "100k", "--allowedTools", "Write", "--output-format", "json", "Create the requested file using the Write tool, then confirm completion. Use the file path supplied by the tool request.")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "USER=agent", "LANG=C.UTF-8", "CLAUDE_CONFIG_DIR=" + filepath.Join(dir, "claude-config"), "ANTHROPIC_BASE_URL=" + router.URL, "ANTHROPIC_AUTH_TOKEN=" + testToken, "ANTHROPIC_API_KEY=" + testToken, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "ENABLE_TOOL_SEARCH=false", "DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_MAX_OUTPUT_TOKENS=1024"}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Claude Code failed: %v messages=%d compactions=%d counts=%d output=%s", err, messages.Load(), compactions.Load(), counts.Load(), output)
	}
	createdContent, fileErr := os.ReadFile(created)
	if fileErr != nil || string(createdContent) != "created by real Claude Code tool execution\n" || !continued.Load() {
		t.Fatalf("tool continuation failed: file=%v continued=%v output=%s", fileErr, continued.Load(), output)
	}
	if compactions.Load() == 0 {
		compactCmd := exec.CommandContext(ctx, cli, "--debug-file", debugFile, "--setting-sources", "", "--continue", "--print", "--dangerously-skip-permissions", "--model", "claude-sonnet-4-5", "--autocompact", "100k", "--output-format", "json", "/compact")
		compactCmd.Dir = dir
		compactCmd.Env = cmd.Env
		compactOutput, compactErr := compactCmd.CombinedOutput()
		if compactErr != nil || compactions.Load() == 0 {
			t.Fatalf("Claude Code compact failed: err=%v messages=%d output=%s", compactErr, messages.Load(), compactOutput)
		}
	}
	flushUsageForTest(t, svc)
	var rows []usageRecord
	if err := svc.usage.db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	var inferenceTokens int64
	for _, row := range rows {
		if row.RequestKind == "count_tokens" {
			if row.TotalTokens != 0 || row.TotalCostUSD != 0 || row.UsageSource != "none" {
				t.Fatalf("count billed: %+v", row)
			}
		} else {
			inferenceTokens += int64(row.TotalTokens)
		}
	}
	svc.quota.mu.Lock()
	settled := svc.quota.state.Callers["alice"].LifetimeTokens
	svc.quota.mu.Unlock()
	if settled != inferenceTokens {
		t.Fatalf("ledger=%d inference tokens=%d", settled, inferenceTokens)
	}
	t.Logf("real Claude Code wrote file, continued with tool result, and compacted: messages=%d compactions=%d count_calls=%d; count events unbilled, ledger equals inference-only usage", messages.Load(), compactions.Load(), counts.Load())
}

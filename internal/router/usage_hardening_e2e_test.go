// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real TCP router and fixture upstream: thinking must not break a committed
// Chat stream, including a tool invocation and subsequent tool-result turn.
func TestUsageHardeningLocalhostContinuation(t *testing.T) {
	turns := 0
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		turns++
		if body["stream"] != true {
			writeJSON(w, 200, map[string]any{"id": "unary", "content": []any{map[string]any{"type": "text", "text": "ok"}}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 5}})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(s string) { fmt.Fprintf(w, "data: %s\n\n", s); w.(http.Flusher).Flush() }
		emit(`{"type":"message_start","message":{"id":"msg_fixture","usage":{"input_tokens":10,"output_tokens":0}}}`)
		emit(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)
		emit(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"private"}}`)
		emit(`{"type":"content_block_stop","index":0}`)
		if turns == 3 {
			emit(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tool_fixture","name":"write","input":{}}}`)
			emit(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"created.txt\"}"}}`)
		} else {
			raw, _ := json.Marshal(body)
			if turns > 3 && (!strings.Contains(string(raw), "tool_result") || !strings.Contains(string(raw), "written")) {
				t.Errorf("missing tool result: %s", raw)
			}
			emit(`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":"done"}}`)
		}
		emit(`{"type":"content_block_stop","index":1}`)
		stop := "end_turn"
		if turns == 3 {
			stop = "tool_use"
		}
		emit(fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":5}}`, stop))
		emit(`{"type":"message_stop"}`)
	}))
	var err error
	upstream.Listener, err = net.Listen("tcp", "127.0.0.1:18471")
	if err != nil {
		t.Fatal(err)
	}
	upstream.Start()
	defer upstream.Close()
	target := Target{RequestShapeSupport: anthropicBridgeTarget().RequestShapeSupport, Provider: "native", Model: "claude-fixture", DefaultOpenAIChatThinking: map[string]any{"type": "enabled", "budget_tokens": 1024}, ToolSupport: anthropicToolTarget().ToolSupport, InputPricePerMillionUSD: 10, OutputPricePerMillionUSD: 20}
	svc := nativeStreamTestService(t, upstream.URL, "anthropic", []Target{target}, func(c *Config) {
		c.Server.Cache.Enabled = false
		c.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	})
	listener, err := net.Listen("tcp", "127.0.0.1:18470")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: svc.Handler()}
	go server.Serve(listener)
	defer server.Close()
	post := func(endpoint, body string) string {
		req, _ := http.NewRequest("POST", "http://127.0.0.1:18470"+endpoint, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("status=%d %s", resp.StatusCode, raw)
		}
		return string(raw)
	}
	post("/v1/chat/completions", `{"model":"native-stream","messages":[{"role":"user","content":"hello"}]}`)
	chat := post("/v1/chat/completions", `{"model":"native-stream","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	if !strings.Contains(chat, "[DONE]") || !strings.Contains(chat, "done") || strings.Contains(chat, "private") {
		t.Fatalf("thinking bridge failed or leaked reasoning: %s", chat)
	}
	first := post("/anthropic/v1/messages", `{"model":"native-stream","stream":true,"max_tokens":2048,"tools":[{"name":"write","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"create file"}]}`)
	if !strings.Contains(first, "tool_fixture") || !strings.Contains(first, "message_stop") {
		t.Fatalf("tool stream: %s", first)
	}
	path := filepath.Join(t.TempDir(), "created.txt")
	if err := os.WriteFile(path, []byte("written"), 0600); err != nil {
		t.Fatal(err)
	}
	final := post("/anthropic/v1/messages", `{"model":"native-stream","stream":true,"max_tokens":2048,"messages":[{"role":"user","content":"create file"},{"role":"assistant","content":[{"type":"tool_use","id":"tool_fixture","name":"write","input":{"path":"created.txt"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_fixture","content":"written"}]}]}`)
	if !strings.Contains(final, "done") || !strings.Contains(final, "message_stop") {
		t.Fatalf("continuation: %s", final)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var rows []usageRecord
		if err := svc.usage.db.Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) == 4 {
			for _, row := range rows {
				t.Logf("stored stream=%v input=%d output=%d total=%d cost=%g", row.Stream, row.InputTokens, row.OutputTokens, row.TotalTokens, row.TotalCostUSD)
				if row.InputTokens != 10 || row.OutputTokens != 5 || row.TotalTokens != 15 || row.TotalCostUSD != 0.0002 {
					t.Fatalf("row=%+v", row)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stored rows=%d", len(rows))
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Log("localhost:18470 unary + thinking stream + file write + tool result continuation completed; upstream :18471 stopped on cleanup")
}

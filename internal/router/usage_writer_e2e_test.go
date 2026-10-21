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
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUsageWriterLocalhostToolContinuation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		messages := body["messages"].([]any)
		final := messages[len(messages)-1].(map[string]any)["role"] == "tool"
		w.Header().Set("Content-Type", "text/event-stream")
		delta := `{"role":"assistant","tool_calls":[{"index":0,"id":"call_write","type":"function","function":{"name":"write_file","arguments":"{\"content\":\"usage-e2e\"}"}}]}`
		finish := "tool_calls"
		if final {
			delta = `{"role":"assistant","content":"file created"}`
			finish = "stop"
		}
		fmt.Fprintf(w, "data: {\"id\":\"chatcmpl_local\",\"object\":\"chat.completion.chunk\",\"model\":\"tool-model\",\"choices\":[{\"index\":0,\"delta\":%s,\"finish_reason\":null}]}\n\n", delta)
		fmt.Fprintf(w, "data: {\"id\":\"chatcmpl_local\",\"object\":\"chat.completion.chunk\",\"model\":\"tool-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":4,\"total_tokens\":14}}\n\ndata: [DONE]\n\n", finish)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.Cache.Enabled = false
	cfg.Server.ContentCapture = ContentCaptureConfig{Enabled: true, RetentionDays: 7, CaptureRequest: true, CaptureResponse: true, CaptureHeadersAllowlist: []string{"Content-Type"}, Encryption: testContentCaptureEncryption(t)}
	cfg.Callers[0].Rate.Concurrent = 32
	cfg.Provider["openai_chat"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["tool-e2e"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "openai_chat", Model: "tool-model", ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}}}}
	cfg.Callers[0].Allow = []string{"tool-e2e"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:18109")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: svc.Handler()}
	go server.Serve(listener)
	defer server.Close()
	request := func(messages []any) (string, error) {
		payload, _ := json.Marshal(map[string]any{"model": "tool-e2e", "stream": true, "messages": messages, "tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "write_file", "parameters": map[string]any{"type": "object", "properties": map[string]any{"content": map[string]any{"type": "string"}}}}}}})
		req, _ := http.NewRequest("POST", "http://127.0.0.1:18109/v1/chat/completions", strings.NewReader(string(payload)))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			return "", fmt.Errorf("status %d: %s", resp.StatusCode, b)
		}
		return string(b), err
	}
	user := map[string]any{"role": "user", "content": "create a file"}
	sqlDB, _ := svc.usage.db.DB()
	lockConn, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lockConn.Close()
	if _, err = lockConn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer lockConn.ExecContext(context.Background(), "ROLLBACK")
	started := time.Now()
	body, err := request([]any{user})
	latency := time.Since(started)
	_, _ = lockConn.ExecContext(context.Background(), "ROLLBACK")
	t.Logf("localhost streaming response while usage DB write lock held: %s", latency)
	if latency > time.Second {
		t.Fatalf("SQLite lock delayed response: %s", latency)
	}
	if err != nil {
		t.Fatal(err)
	}
	var call map[string]any
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil {
			continue
		}
		choices, _ := event["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		delta, _ := choices[0].(map[string]any)["delta"].(map[string]any)
		calls, _ := delta["tool_calls"].([]any)
		if len(calls) > 0 {
			call = calls[0].(map[string]any)
		}
	}
	if call == nil {
		t.Fatalf("no tool call: %s", body)
	}
	function := call["function"].(map[string]any)
	var args struct {
		Content string `json:"content"`
	}
	if err = json.Unmarshal([]byte(function["arguments"].(string)), &args); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "created.txt")
	if err = os.WriteFile(path, []byte(args.Content), 0600); err != nil {
		t.Fatal(err)
	}
	delete(call, "index")
	body, err = request([]any{user, map[string]any{"role": "assistant", "tool_calls": []any{call}}, map[string]any{"role": "tool", "tool_call_id": call["id"], "content": "created"}})
	if err != nil || !strings.Contains(body, "file created") {
		t.Fatalf("continuation: %v %s", err, body)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := request([]any{user}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err = svc.usage.writer.close(); err != nil {
		t.Fatal(err)
	}
	if got := usageCount(t, svc.usage); got != 18 {
		t.Fatalf("rows=%d want 18", got)
	}
	var captures int64
	if err := svc.usage.db.Model(&contentCaptureRecord{}).Where("encrypted = ?", true).Count(&captures).Error; err != nil {
		t.Fatal(err)
	}
	if captures < 18 {
		t.Fatalf("encrypted captures=%d want at least 18", captures)
	}
	t.Log("localhost :18109: tool created file, tool-result continuation succeeded, 16 concurrent streams completed; 18 usage rows and encrypted content captures persisted")
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
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

func TestCacheBillingLocalhostE2E(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input map[string]any
		_ = json.NewDecoder(r.Body).Decode(&input)
		if _, ok := input["tools"]; ok {
			writeJSON(w, 200, map[string]any{"id": "tool", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "call_w5", "type": "function", "function": map[string]any{"name": "write_file", "arguments": "{}"}}}}, "finish_reason": "tool_calls"}}})
			return
		}
		writeJSON(w, 200, map[string]any{"id": "mock", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "answer"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150}})
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "mock-key", dir)
	cfg.Callers[0].Project = "local"
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	t.Setenv("W5_ADMIN_HASH", mustBcryptHash(t, "w5-password"))
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{Enabled: true, AllowInsecureHTTP: true, Users: []AdminBasicAuthUser{{Username: "admin", PasswordHashEnv: "W5_ADMIN_HASH", Subject: "basic:admin", Domain: "local/test"}}}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, basic:admin, local/test, admin:reports, read|export|drilldown"}}
	cfg.Server.AdminReports = AdminReportsConfig{Enabled: true, DefaultSince: "24h", MaxRange: "31d", MaxRows: 100}
	group := cfg.Models["default"]
	group.Targets[0].ToolSupport = ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}
	group.Targets[0].InputPricePerMillionUSD = 10
	group.Targets[0].OutputPricePerMillionUSD = 20
	cfg.Models["default"] = group
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:18155")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: svc.Handler()}
	go server.Serve(listener)
	defer server.Close()
	client := &http.Client{}
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest("POST", "http://127.0.0.1:18155/v1/chat/completions", strings.NewReader(`{"model":"default","temperature":0,"messages":[{"role":"user","content":"same"}]}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("status %d: %s", resp.StatusCode, body)
		}
	}
	var row usageRecord
	// Usage persistence may be asynchronous (OPS-09); the HTTP response must not
	// have to wait for the writer. Observe its eventual result without flushing
	// or otherwise coupling this request-path proof to a synchronous writer.
	deadline := time.Now().Add(5 * time.Second)
	for {
		result := svc.usage.db.Where("cache = ?", "hit").Limit(1).Find(&row)
		if result.Error != nil {
			t.Fatal(result.Error)
		}
		if result.RowsAffected > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for cache-hit usage row")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if row.CostBilledUSD != 0 || row.CostListUSD <= 0 {
		t.Fatalf("cache row list=%g billed=%g", row.CostListUSD, row.CostBilledUSD)
	}
	req, _ := http.NewRequest("GET", "http://127.0.0.1:18155/admin/reports/api/overview?since=24h", nil)
	req.SetBasicAuth("admin", "w5-password")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var report struct {
		Summary adminReportSummary `json:"summary"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || report.Summary.CostBilledUSD != row.CostListUSD || report.Summary.CacheSavingsUSD != row.CostListUSD {
		t.Fatalf("report status=%d summary=%+v", resp.StatusCode, report.Summary)
	}
	// Exercise a client tool-result continuation over the same localhost router.
	post := func(body string) map[string]any {
		req, _ := http.NewRequest("POST", "http://127.0.0.1:18155/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var result map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || resp.StatusCode != 200 {
			t.Fatalf("tool request status=%d err=%v body=%#v", resp.StatusCode, err, result)
		}
		return result
	}
	result := post(`{"model":"default","messages":[{"role":"user","content":"create a file"}],"tools":[{"type":"function","function":{"name":"write_file","parameters":{"type":"object"}}}]}`)
	message := result["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	calls, ok := message["tool_calls"].([]any)
	if !ok || len(calls) != 1 {
		t.Fatalf("missing tool call: %#v", result)
	}
	if err := os.WriteFile(filepath.Join(dir, "created.txt"), []byte("tool ran"), 0600); err != nil {
		t.Fatal(err)
	}
	continuation, _ := json.Marshal(map[string]any{"model": "default", "messages": []any{map[string]any{"role": "user", "content": "create a file"}, message, map[string]any{"role": "tool", "tool_call_id": calls[0].(map[string]any)["id"], "content": "file created"}}})
	continued := post(string(continuation))
	if continued["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"] != "answer" {
		t.Fatalf("continuation: %#v", continued)
	}
	t.Log("tool-using client created created.txt and continued with a tool result")
	t.Logf("localhost:18155 cache hit cost_list_usd=%g cost_billed_usd=%g; API billed=%g savings=%g", row.CostListUSD, row.CostBilledUSD, report.Summary.CostBilledUSD, report.Summary.CacheSavingsUSD)
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A real TCP client uses a streaming tool call, runs the tool, then sends its
// result back through a localhost router built from this working tree.
func TestUsageProvenanceLocalhostE2E(t *testing.T) {
	upstreamListener, err := net.Listen("tcp", "127.0.0.1:18438")
	if err != nil {
		t.Fatal(err)
	}
	upstream := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		options, _ := body["stream_options"].(map[string]any)
		messages := body["messages"].([]any)
		last := messages[len(messages)-1].(map[string]any)
		w.Header().Set("Content-Type", "text/event-stream")
		if last["role"] != "tool" {
			fmt.Fprint(w, "data: {\"id\":\"tool-stream\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_write\",\"type\":\"function\",\"function\":{\"name\":\"write_file\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
		} else {
			fmt.Fprintf(w, "data: {\"id\":\"continued\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\n", strings.Repeat("a", 160))
		}
		if options["include_usage"] == true {
			fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":40,\"total_tokens\":50}}\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	})}
	go upstream.Serve(upstreamListener)
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:18438", "mock-key", dir)
	cfg.Callers[0].Project = "local"
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	t.Setenv("USAGE_B_ADMIN_HASH", mustBcryptHash(t, "usage-b-password"))
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{Enabled: true, AllowInsecureHTTP: true, Users: []AdminBasicAuthUser{{Username: "admin", PasswordHashEnv: "USAGE_B_ADMIN_HASH", Subject: "basic:admin", Domain: "local/test"}}}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, basic:admin, local/test, admin:reports, read|export|drilldown"}}
	cfg.Server.AdminReports = AdminReportsConfig{Enabled: true, DefaultSince: "24h", MaxRange: "31d", MaxRows: 100}
	group := cfg.Models["default"]
	group.Targets[0].ToolSupport = ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}
	cfg.Models["default"] = group
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:18437")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: svc.Handler()}
	go server.Serve(listener)
	defer server.Close()
	post := func(body string) string {
		req, _ := http.NewRequest("POST", "http://127.0.0.1:18437/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("status=%d body=%s", resp.StatusCode, data)
		}
		return string(data)
	}
	first := post(`{"model":"default","stream":true,"max_tokens":32000,"messages":[{"role":"user","content":"create a file"}],"tools":[{"type":"function","function":{"name":"write_file","parameters":{"type":"object"}}}]}`)
	var callID string
	for _, line := range strings.Split(first, "\n") {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						ID string `json:"id"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			t.Fatal(err)
		}
		for _, choice := range chunk.Choices {
			for _, call := range choice.Delta.ToolCalls {
				if call.ID != "" {
					callID = call.ID
				}
			}
		}
	}
	if callID == "" {
		t.Fatalf("no tool call: %s", first)
	}
	if err := os.WriteFile(filepath.Join(dir, "created.txt"), []byte("tool executed"), 0600); err != nil {
		t.Fatal(err)
	}
	messages := []any{map[string]any{"role": "user", "content": "create a file"}, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": callID, "type": "function", "function": map[string]any{"name": "write_file", "arguments": "{}"}}}}, map[string]any{"role": "tool", "tool_call_id": callID, "content": "file created"}}
	for _, estimated := range []bool{false, true} {
		body := map[string]any{"model": "default", "stream": true, "max_tokens": 32000, "messages": messages}
		if estimated {
			body["stream_options"] = map[string]any{"include_usage": false}
		}
		data, _ := json.Marshal(body)
		result := post(string(data))
		if !strings.Contains(result, strings.Repeat("a", 160)) {
			t.Fatalf("continuation failed: %s", result)
		}
	}
	flushUsageForTest(t, svc)
	var rows []usageRecord
	if err := svc.usage.db.Order("ts").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows=%d", len(rows))
	}
	settledTotal := 0
	for i, row := range rows {
		settledTotal += row.TotalTokens
		want := "reported"
		if i == 2 {
			want = "estimated"
		}
		if row.UsageSource != want || row.OutputTokens != 40 {
			t.Fatalf("row=%+v", row)
		}
		t.Logf("stored source=%s input=%d output=%d (max_tokens=32000)", row.UsageSource, row.InputTokens, row.OutputTokens)
	}
	svc.quota.mu.Lock()
	lifetime := svc.quota.state.Callers["alice"].LifetimeTokens
	svc.quota.mu.Unlock()
	if lifetime != int64(settledTotal) {
		t.Fatalf("quota settled %d; stored total %d", lifetime, settledTotal)
	}
	for _, endpoint := range []string{"overview", "summary", "savings"} {
		req, _ := http.NewRequest("GET", "http://127.0.0.1:18437/admin/reports/api/"+endpoint+"?since=24h", nil)
		req.SetBasicAuth("admin", "usage-b-password")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var report struct {
			Summary adminReportSummary `json:"summary"`
		}
		err = json.NewDecoder(resp.Body).Decode(&report)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 || report.Summary.EstimatedOutputTokens != 40 || report.Summary.EstimatedRequests != 1 {
			t.Fatalf("endpoint=%s status=%d err=%v summary=%+v", endpoint, resp.StatusCode, err, report.Summary)
		}
		t.Logf("API %s estimatedOutputTokens=%d estimatedTokenShare=%.2f", endpoint, report.Summary.EstimatedOutputTokens, report.Summary.EstimatedTokenShare)
	}
	req, _ := http.NewRequest("GET", "http://127.0.0.1:18437/admin/reports/api/requests?since=24h&usage_source=estimated", nil)
	req.SetBasicAuth("admin", "usage-b-password")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var filtered struct {
		Rows []adminReportRequest `json:"requests"`
	}
	err = json.NewDecoder(resp.Body).Decode(&filtered)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || len(filtered.Rows) != 1 || filtered.Rows[0].UsageSource != "estimated" {
		t.Fatalf("filtered=%+v err=%v status=%d", filtered, err, resp.StatusCode)
	}
	t.Log("localhost:18437 streaming tool call created created.txt; tool-result continuation succeeded with reported and estimated usage")
}

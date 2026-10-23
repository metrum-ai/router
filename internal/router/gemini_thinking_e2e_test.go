// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
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

func TestGeminiThinkingLocalhostE2E(t *testing.T) {
	fixture, err := os.ReadFile("../../testdata/gemini/thinking-response.json")
	if err != nil {
		t.Fatal(err)
	}
	up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	up.Listener, err = net.Listen("tcp", "127.0.0.1:18473")
	if err != nil {
		t.Fatal(err)
	}
	up.Start()
	defer up.Close()
	svc := nativeStreamTestService(t, up.URL, "gemini-generate-content", []Target{{ActivationEvidence: ActivationEvidence{ExactModel: "gemini-thinking", Dialect: "gemini-generate-content", DirectTextPassed: true, RouterTextPassed: true, ValidatedAt: "2026-10-23"}, InputModalities: []string{"text"}, OutputModalities: []string{"text"}, Provider: "native", Model: "gemini-thinking", InputPricePerMillionUSD: 10, OutputPricePerMillionUSD: 20}}, func(c *Config) {
		c.Callers[0].Project = "local"
		c.Callers[0].Environment = "test"
		c.Server.Cache.Enabled = false
		c.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
		t.Setenv("GEMINI_TEST_ADMIN_HASH", mustBcryptHash(t, "fixture-password"))
		c.Server.AdminAuth.Basic = AdminBasicAuthConfig{Enabled: true, AllowInsecureHTTP: true, Users: []AdminBasicAuthUser{{Username: "admin", PasswordHashEnv: "GEMINI_TEST_ADMIN_HASH", Subject: "basic:admin", Domain: "local/test"}}}
		c.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, basic:admin, local/test, admin:reports, read|export|drilldown"}}
		c.Server.AdminReports = AdminReportsConfig{Enabled: true, DefaultSince: "24h", MaxRange: "31d", MaxRows: 100}
	})
	listener, err := net.Listen("tcp", "127.0.0.1:18472")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: svc.Handler()}
	go server.Serve(listener)
	defer server.Close()
	client := &http.Client{Timeout: 15 * time.Second}
	for _, stream := range []bool{false, true} {
		req, _ := http.NewRequest("POST", "http://127.0.0.1:18472/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"native-stream","stream":%v,"messages":[{"role":"user","content":"hi"}]}`, stream)))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(raw), `"reasoning_tokens":500`) || !strings.Contains(string(raw), `"completion_tokens":520`) {
			t.Fatalf("stream=%v status=%d body=%s", stream, resp.StatusCode, raw)
		}
	}
	var rows []usageRecord
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := svc.usage.db.Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("missing usage rows")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, row := range rows {
		t.Logf("stored stream=%v input=%d output=%d reasoning=%v total=%d output_cost=%g total_cost=%g", row.Stream, row.InputTokens, row.OutputTokens, row.ReasoningTokens, row.TotalTokens, row.OutputCostUSD, row.TotalCostUSD)
		if row.InputTokens != 100 || row.OutputTokens != 520 || row.TotalTokens != 620 || row.ReasoningTokens == nil || *row.ReasoningTokens != 500 || row.OutputCostUSD != 0.0104 || row.TotalCostUSD != 0.0114 {
			t.Fatalf("stored usage mismatch: %+v", row)
		}
		req, _ := http.NewRequest("GET", "http://127.0.0.1:18472/admin/reports/api/request/"+row.RequestID, nil)
		req.SetBasicAuth("admin", "fixture-password")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(raw), `"reasoningTokens":500`) {
			t.Fatalf("detail status=%d %s", resp.StatusCode, raw)
		}
	}
	// Current usage export is /v1/usage (the issue's /admin/api/usage is not a registered route).
	req, _ := http.NewRequest("GET", "http://127.0.0.1:18472/v1/usage", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(raw), `"tokens":1240`) {
		t.Fatalf("usage export %d %s", resp.StatusCode, raw)
	}
	t.Logf("localhost :18472 unary/synthesized stream, detail and /v1/usage export PASS: %s", raw)
}

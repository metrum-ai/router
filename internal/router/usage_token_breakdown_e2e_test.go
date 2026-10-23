// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// End-to-end ingestion uses the merged provider parsers, not prefilled SQL rows.
// Only timestamps are shifted to a completed UTC day for finalization/retention.
func TestTokenBreakdownLocalhostE2E(t *testing.T) {
	var chatCalls atomic.Int32
	upstreamListener, err := net.Listen("tcp", "127.0.0.1:18454")
	if err != nil {
		t.Fatal(err)
	}
	upstream := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mock-key" && r.Header.Get("X-API-Key") != "mock-key" {
			t.Error("missing upstream credentials")
		}
		switch r.URL.Path {
		case "/v1/chat/completions":
			n := chatCalls.Add(1)
			usage := map[string]any{"prompt_tokens": 100, "completion_tokens": 40, "total_tokens": 140}
			if n <= 2 {
				reasoning, cached := 20, 60
				if n == 2 {
					reasoning, cached = 0, 0
				}
				usage["completion_tokens_details"] = map[string]int{"reasoning_tokens": reasoning}
				usage["prompt_tokens_details"] = map[string]int{"cached_tokens": cached}
			}
			writeJSON(w, 200, map[string]any{"id": "chat-fixture", "object": "chat.completion", "model": "mock-reasoner", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "reasoned answer"}, "finish_reason": "stop"}}, "usage": usage})
		case "/v1/messages":
			writeJSON(w, 200, map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "model": "mock-cache", "content": []any{map[string]string{"type": "text", "text": "cached answer"}}, "stop_reason": "end_turn", "usage": map[string]int{"input_tokens": 20, "cache_read_input_tokens": 60, "cache_creation_input_tokens": 20, "output_tokens": 40}})
		default:
			t.Errorf("unexpected upstream path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})}
	go upstream.Serve(upstreamListener)
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:18454", "mock-key", dir)
	configureDocsSmokeCapacity(cfg)
	cfg.Server.Cache.Enabled = false
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Callers[0].Project = "local"
	cfg.Callers[0].MetricsAdmin = true
	cfg.Callers[0].Allow = []string{"reasoning", "cache"}
	t.Setenv("USAGE_B_339_ADMIN_HASH", mustBcryptHash(t, "usage-b-password"))
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{Enabled: true, AllowInsecureHTTP: true, Users: []AdminBasicAuthUser{{Username: "admin", PasswordHashEnv: "USAGE_B_339_ADMIN_HASH", Subject: "basic:admin", Domain: "local/test"}}}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, basic:admin, local/test, admin:reports, read|export|drilldown"}}
	cfg.Server.AdminReports = AdminReportsConfig{Enabled: true, DefaultSince: "24h", MaxRange: "31d", MaxRows: 100}
	cfg.Provider["anthropic"] = ProviderConfig{BaseURL: "http://127.0.0.1:18454", Dialect: "anthropic", APIKey: "mock-key"}
	cachedPrice, writePrice := 2.0, 12.5
	cfg.Models["reasoning"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "mock-reasoner", InputPricePerMillionUSD: 10, OutputPricePerMillionUSD: 20, CachedInputPricePerMillionUSD: &cachedPrice, Reasoning: ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum}}}}
	cfg.Models["cache"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "anthropic", Model: "mock-cache", InputPricePerMillionUSD: 10, OutputPricePerMillionUSD: 20, CachedInputPricePerMillionUSD: &cachedPrice, CacheWritePricePerMillionUSD: &writePrice}}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:18455")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: svc.Handler()}
	go server.Serve(listener)
	defer server.Close()
	post := func(path string, body map[string]any) {
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", "http://127.0.0.1:18455"+path, strings.NewReader(string(raw)))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("%s status=%d body=%s", path, resp.StatusCode, data)
		}
	}
	prefix := strings.Repeat("stable cached prefix ", 100)
	for i := 0; i < 3; i++ {
		post("/v1/chat/completions", map[string]any{"model": "reasoning", "reasoning_effort": "medium", "messages": []any{map[string]string{"role": "user", "content": prefix + "reason about the next step"}}})
	}
	post("/v1/messages", map[string]any{"model": "cache", "max_tokens": 256, "messages": []any{map[string]string{"role": "user", "content": prefix + "reuse this prefix"}}})
	flushUsageForTest(t, svc)
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	if err := svc.usage.db.Model(&usageRecord{}).Where("total_tokens > 0").Update("ts", day.Add(time.Hour).Format(time.RFC3339Nano)).Error; err != nil {
		t.Fatal(err)
	}
	query := "?from=" + url.QueryEscape(day.Format(time.RFC3339)) + "&to=" + url.QueryEscape(day.Add(24*time.Hour).Format(time.RFC3339))
	get := func(path string, dest any) {
		req, _ := http.NewRequest("GET", "http://127.0.0.1:18455/admin/reports/api/"+path, nil)
		req.SetBasicAuth("admin", "usage-b-password")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("%s status=%d body=%s", path, resp.StatusCode, data)
		}
		if err := json.Unmarshal(data, dest); err != nil {
			t.Fatal(err)
		}
	}
	markdown, err := GenerateUsageMarkdown(UsageReportOptions{Driver: "sqlite", DBPath: cfg.Server.UsageDB.Path, MigrationPolicy: usageDBMigrationPolicyAutoSafe, From: day, To: day.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(markdown, "Cache Write Tokens") || !strings.Contains(markdown, "| reasoning | 20 | 2 | 60 | 2 | 0 | 0 | 3 | 3 | 2 |") {
		t.Fatalf("CLI coverage columns or sums missing: %s", markdown)
	}
	var raw adminReportResponse
	get("summary"+query, &raw)
	if raw.Summary.Requests != 4 || raw.Summary.InputTokens != 400 || raw.Summary.ReasoningTokens != 20 || raw.Summary.ReasoningReportedRequests != 2 || raw.Summary.CachedInputTokens != 120 || raw.Summary.CachedInputReportedRequests != 3 || raw.Summary.CacheWriteInputTokens != 20 || raw.Summary.CacheWriteReportedRequests != 1 {
		t.Fatalf("parsed usage totals %+v", raw.Summary)
	}
	if raw.Summary.ReasoningAttemptCount != 4 || raw.Summary.ReasoningSuccessfulAttemptCount != 4 || raw.Summary.ReasoningReportedAttemptCount != 2 {
		t.Fatalf("attempt counters %+v", raw.Summary.UsageTokenBreakdown)
	}
	for filter, want := range map[string]int64{"reasoning_present=true": 3, "reasoning_present=false": 1, "cache_present=true": 3, "cache_present=false": 1} {
		var filtered adminReportResponse
		get("summary"+query+"&"+filter, &filtered)
		if filtered.Summary.Requests != want {
			t.Fatalf("%s requests=%d want=%d", filter, filtered.Summary.Requests, want)
		}
	}
	metricsReq, _ := http.NewRequest("GET", "http://127.0.0.1:18455/metrics", nil)
	metricsReq.Header.Set("Authorization", "Bearer "+testToken)
	metricsResp, err := http.DefaultClient.Do(metricsReq)
	if err != nil {
		t.Fatal(err)
	}
	metricsBody, _ := io.ReadAll(metricsResp.Body)
	metricsResp.Body.Close()
	if metricsResp.StatusCode != 200 {
		t.Fatalf("metrics status=%d", metricsResp.StatusCode)
	}
	sumMetric := func(name string) float64 {
		total := 0.0
		for _, line := range strings.Split(string(metricsBody), "\n") {
			if strings.HasPrefix(line, name+"{") {
				fields := strings.Fields(line)
				value, err := strconv.ParseFloat(fields[len(fields)-1], 64)
				if err != nil {
					t.Fatal(err)
				}
				total += value
			}
		}
		return total
	}
	for name, want := range map[string]float64{"metrum_ai_router_reasoning_tokens_total": 20, "metrum_ai_router_cached_input_tokens_total": 120, "metrum_ai_router_cache_write_input_tokens_total": 20, "metrum_ai_router_cost_usd_total": raw.Summary.TotalCostUSD} {
		if got := sumMetric(name); math.Abs(got-want) > 1e-9 {
			t.Fatalf("metrics %s=%g want=%g", name, got, want)
		}
	}
	if _, err := svc.usage.generateUsageRollup(UsageRollupOptions{RollupType: "daily", From: day, To: day.Add(24 * time.Hour), Finalize: true}); err != nil {
		t.Fatal(err)
	}
	var rolled adminReportResponse
	get("summary"+query+"&history_source=rollup", &rolled)
	assertTokenBreakdownClose(t, raw.Summary.UsageTokenBreakdown, rolled.Summary.UsageTokenBreakdown)
	for _, endpoint := range []string{"requested-models", "provider-model-mix", "usage-by-caller", "savings-by-project"} {
		var before, after adminScalarReportResponse
		get(endpoint+query, &before)
		get(endpoint+query+"&history_source=rollup", &after)
		assertTokenBreakdownClose(t, before.Summary.UsageTokenBreakdown, after.Summary.UsageTokenBreakdown)
		if before.Summary.Requests != after.Summary.Requests {
			t.Fatalf("%s request totals differ", endpoint)
		}
	}
	// Simulate raw-row retention only after the immutable daily rollup exists.
	if err := svc.usage.db.Where("ts >= ? AND ts < ?", day.Format(time.RFC3339), day.Add(24*time.Hour).Format(time.RFC3339)).Delete(&usageRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	var retained adminReportResponse
	get("summary"+query+"&history_source=rollup", &retained)
	assertTokenBreakdownClose(t, raw.Summary.UsageTokenBreakdown, retained.Summary.UsageTokenBreakdown)
	if retained.Summary.Requests != 4 || retained.Summary.HistorySource != "finalized_daily_rollups" {
		t.Fatalf("retained summary %+v", retained.Summary)
	}
	t.Logf("localhost raw/rollup/retained parity: input=400 output=160 reasoning=20 reported=2/4 cached=120 reported=3/4 writes=20 reported=1/4; billed cost=%g; /metrics matches DB", retained.Summary.TotalCostUSD)
	if hold, _ := strconv.Atoi(os.Getenv("USAGE_B_339_E2E_HOLD_SECONDS")); hold > 0 && hold <= 300 {
		fmt.Println("USAGE_B_339_BROWSER_READY http://127.0.0.1:18455/admin/reports/?tab=overview&history_source=rollup&since=48h")
		time.Sleep(time.Duration(hold) * time.Second)
	}
}

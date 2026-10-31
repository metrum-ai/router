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
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCountTokensProxyUnbilled(t *testing.T) {
	var expected atomic.Value
	var calls atomic.Int64
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		data, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/messages/count_tokens" || string(data) != expected.Load() {
			t.Errorf("upstream path=%s body=%s want=%s", r.URL.Path, data, expected.Load())
		}
		if r.Header.Get("X-API-Key") != "provider-key" || r.Header.Get("Authorization") != "" || r.Header.Get("Anthropic-Version") != "2023-06-01" {
			t.Error("provider authentication mismatch")
		}
		w.Header().Set("Content-Type", "application/json")
		if call == 3 {
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"type":"rate_limit_error","message":"private provider detail"}}`)
			return
		}
		fmt.Fprint(w, "{\n  \"input_tokens\": 12345, \"exact\": true\n}")
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:18445")
	if err != nil {
		t.Fatal(err)
	}
	upstream.Listener.Close()
	upstream.Listener = listener
	upstream.Start()
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	configureDocsSmokeCapacity(cfg)
	cfg.Server.Identifiers = identifierTestConfig()
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Callers[0].Project = "local"
	t.Setenv("COUNT_ADMIN_HASH", mustBcryptHash(t, "count-password"))
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{Enabled: true, AllowInsecureHTTP: true, Users: []AdminBasicAuthUser{{Username: "admin", PasswordHashEnv: "COUNT_ADMIN_HASH", Subject: "basic:admin", Domain: "local/test"}}}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, basic:admin, local/test, admin:reports, read|export|drilldown"}}
	cfg.Server.AdminReports = AdminReportsConfig{Enabled: true, DefaultSince: "24h", MaxRange: "31d", MaxRows: 100}
	provider := cfg.Provider["mock"]
	provider.Dialect = "anthropic"
	cfg.Provider["mock"] = provider
	group := cfg.Models["default"]
	group.Targets[0].InputPricePerMillionUSD = 10
	group.Targets[0].OutputPricePerMillionUSD = 20
	cfg.Models["default"] = group
	// ADMISSION-LIMIT TEST EXCEPTION: counting must not reserve against tiny
	// token budgets. All unrelated limits retain the high smoke profile. A large
	// compaction payload that would fail inference admission.
	cfg.Callers[0].Rate.TPM = 1
	cfg.Callers[0].Quota.Day.Tokens = 1
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	router := httptest.NewUnstartedServer(svc.Handler())
	routerListener, err := net.Listen("tcp", "127.0.0.1:18446")
	if err != nil {
		t.Fatal(err)
	}
	router.Listener.Close()
	router.Listener = routerListener
	router.Start()
	defer router.Close()
	getReport := func(endpoint string, out any) {
		req, _ := http.NewRequest("GET", router.URL+"/admin/reports/api/"+endpoint+"?since=24h", nil)
		req.SetBasicAuth("admin", "count-password")
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("report %s status=%d", endpoint, response.StatusCode)
		}
		if err := json.NewDecoder(response.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	var before struct {
		Summary adminReportSummary `json:"summary"`
	}
	getReport("overview", &before)
	encoded := svc.identifiers.Encode("upstream-tool-id")
	body := fmt.Sprintf(`{ "model":"default", "messages":[{"role":"assistant","content":[{"type":"tool_use","id":"%s","name":"shell","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"%s","content":"ok"},{"type":"text","text":"%s"}]}], "provider_extension": {"preserve":true}, "system":[{"type":"text","text":"cache me","cache_control":{"type":"ephemeral"}}] }`, encoded, encoded, encoded)
	want := strings.Replace(body, `"model":"default"`, `"model":"mock-model"`, 1)
	want = strings.Replace(want, `"id":"`+encoded+`"`, `"id":"upstream-tool-id"`, 1)
	want = strings.Replace(want, `"tool_use_id":"`+encoded+`"`, `"tool_use_id":"upstream-tool-id"`, 1)
	expected.Store(want)
	for i, path := range []string{"/v1/messages/count_tokens", "/anthropic/v1/messages/count_tokens", "/v1/messages/count_tokens"} {
		req, _ := http.NewRequest("POST", router.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if i == 2 {
			if response.StatusCode != 429 || response.Header.Get("Retry-After") != "5" || response.Header.Get("Request-Id") == "" || !strings.Contains(string(data), `"type":"rate_limit_error"`) || strings.Contains(string(data), "private provider detail") {
				t.Fatalf("sanitized error status=%d body=%s", response.StatusCode, data)
			}
			continue
		}
		if response.StatusCode != 200 || string(data) != "{\n  \"input_tokens\": 12345, \"exact\": true\n}" || response.Header.Get("X-Metrum-Usage-Source") != "reported" {
			t.Fatalf("status=%d body=%s", response.StatusCode, data)
		}
	}
	unauth := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(body))
	unauthRec := httptest.NewRecorder()
	svc.Handler().ServeHTTP(unauthRec, unauth)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized count status=%d", unauthRec.Code)
	}
	if stats := svc.observations.stats(dynamicObservationKey("default", "mock", group.Targets[0].Model), time.Hour); stats.Count != 0 {
		t.Fatalf("count calls polluted dynamic inference observations: %+v", stats)
	}
	if calls.Load() != 3 {
		t.Fatalf("upstream calls=%d", calls.Load())
	}
	svc.quota.mu.Lock()
	state := svc.quota.state.Callers["alice"]
	caller := svc.quota.callers["alice"]
	if state.LifetimeTokens != 0 || state.DayTokens != 0 || state.DayRequests != 0 || caller.inFlightReservedTokens != 0 || caller.inFlight != 0 {
		t.Errorf("count changed ledger/state: %+v reserved=%d inFlight=%d", state, caller.inFlightReservedTokens, caller.inFlight)
	}
	svc.quota.mu.Unlock()
	flushUsageForTest(t, svc)
	var records []usageRecord
	if err := svc.usage.db.Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 {
		t.Fatalf("records=%d", len(records))
	}
	for _, row := range records {
		if row.RequestKind != "count_tokens" || row.UsageSource != "none" || row.InputTokens != 0 || row.TotalTokens != 0 || row.TotalCostUSD != 0 || row.CostBilledUSD != 0 || row.UpstreamTotalTPS != nil {
			t.Fatalf("billed event: %+v", row)
		}
	}
	opts := UsageReportOptions{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)}
	rows, err := svc.usage.rows(opts)
	if err != nil || len(rows) != 0 {
		t.Fatalf("aggregate rows=%d err=%v", len(rows), err)
	}
	opts.IncludeRequestEvents = true
	rows, err = svc.usage.rows(opts)
	if err != nil || len(rows) != 4 || adminRequestFromRow(rows[0]).RequestKind != "count_tokens" {
		t.Fatalf("request events missing: rows=%d err=%v", len(rows), err)
	}
	if len(svc.metrics.series) != 0 {
		t.Fatal("count polluted inference metrics")
	}
	var after struct {
		Summary adminReportSummary `json:"summary"`
	}
	getReport("overview", &after)
	if after.Summary.Requests != before.Summary.Requests || after.Summary.TotalTokens != before.Summary.TotalTokens || after.Summary.TotalCostUSD != before.Summary.TotalCostUSD {
		t.Fatalf("count changed overview: before=%+v after=%+v", before, after)
	}
	var savings struct {
		Summary adminSavingsRow `json:"summary"`
	}
	getReport("savings", &savings)
	if savings.Summary.Requests != 0 || savings.Summary.ActualCostUSD != 0 {
		t.Fatalf("count changed savings: %+v", savings)
	}
	var events struct {
		Requests []adminReportRequest `json:"requests"`
	}
	getReport("requests", &events)
	if len(events.Requests) != 3 || events.Requests[0].RequestKind != "count_tokens" {
		t.Fatalf("events not visible: %+v", events)
	}
	direct, _ := http.NewRequest("POST", upstream.URL+"/v1/messages/count_tokens", strings.NewReader(want))
	direct.Header.Set("X-API-Key", "provider-key")
	direct.Header.Set("Anthropic-Version", "2023-06-01")
	response, err := http.DefaultClient.Do(direct)
	if err != nil {
		t.Fatal(err)
	}
	directBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(directBody) != "{\n  \"input_tokens\": 12345, \"exact\": true\n}" {
		t.Fatalf("direct count mismatch: %s", directBody)
	}
	t.Log("localhost:18446 native count=12345 matches direct upstream; three count events leave token ledger, overview, cost, savings and inference metrics unchanged; request list retains count_tokens/none rows")
}

func TestCountTokensProviderCredentials(t *testing.T) {
	for _, scheme := range []string{"x-api-key", "bearer"} {
		t.Run(scheme, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				header, value := "X-API-Key", "provider-key"
				if scheme == "bearer" {
					header, value = "Authorization", "Bearer provider-key"
				}
				if r.Header.Get(header) != value {
					t.Errorf("wrong provider authentication for %s", scheme)
				}
				if r.Header.Get("Anthropic-Beta") != "allowed-beta" {
					t.Error("count beta allowlist not applied")
				}
				writeJSON(w, 200, map[string]int{"input_tokens": 42})
			}))
			defer upstream.Close()
			cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
			configureDocsSmokeCapacity(cfg)
			provider := cfg.Provider["mock"]
			provider.Dialect = "anthropic"
			provider.AuthScheme = scheme
			cfg.Provider["mock"] = provider
			group := cfg.Models["default"]
			group.Targets[0].AllowedBetas = []string{"allowed-beta"}
			cfg.Models["default"] = group
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			req := httptest.NewRequest("POST", "/v1/messages/count_tokens", strings.NewReader(`{"model":"default","messages":[]}`))
			req.Header.Set("Authorization", "Bearer "+testToken)
			req.Header.Set("Anthropic-Beta", "blocked-beta,allowed-beta")
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != 200 {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestCountTokensStorageAndRollupExclusion(t *testing.T) {
	store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "counts.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := verifyUsageRequestKindMigration(store.db); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	legacy := usageRow{TS: now, RequestID: "inference", InputTokens: 10, TotalTokens: 10, TotalCostUSD: 1}
	if err := store.db.Create(recordFromRow(legacy)).Error; err != nil {
		t.Fatal(err)
	}
	cacheRead, cacheWrite, reasoning := 2, 3, 4
	count := usageRow{TS: now, RequestID: "count", RequestKind: "count_tokens", InputTokens: 999, TotalTokens: 999, TotalCostUSD: 300, CostListUSD: 300, CachedInputTokens: &cacheRead, CacheWriteInputTokens: &cacheWrite, ReasoningTokens: &reasoning}
	event := recordFromRow(count)
	if event.InputTokens != 0 || event.CachedInputTokens != nil || event.CacheWriteInputTokens != nil || event.ReasoningTokens != nil || event.CostBilledUSD != 0 || event.CostListUSD != 0 || event.UsageSource != "none" {
		t.Fatalf("count event billed: %+v", event)
	}
	if err := store.db.Create(event).Error; err != nil {
		t.Fatal(err)
	}
	if err := applyUsageRequestKindMigration(store.db); err != nil {
		t.Fatal(err)
	}
	var historic usageRecord
	if err := store.db.Where("request_id = ?", "inference").First(&historic).Error; err != nil || historic.RequestKind != "" || historic.InputTokens != 10 || historic.TotalCostUSD != 1 {
		t.Fatalf("changed unknown historic usage: %+v err=%v", historic, err)
	}
	opts := UsageReportOptions{From: now.Add(-time.Hour), To: now.Add(time.Hour)}
	overview, err := store.adminOverviewSQL(opts, adminSavingsBaselineDTO{}, 100)
	if err != nil || overview.Total.Calls != 1 || overview.Total.InputTokens != 10 || overview.Total.TotalCostUSD != 1 {
		t.Fatalf("overview=%+v err=%v", overview.Total, err)
	}
	savings, _, _, _, _, _, err := store.adminSavingsAggsSQL(opts, adminSavingsBaselineDTO{}, 100)
	if err != nil || savings.Requests != 1 || savings.ActualCostUSD != 1 {
		t.Fatalf("savings=%+v err=%v", savings, err)
	}
	daily := dailyRollupRecords([]usageRow{legacy, count}, "finalized", "2026-10-23", "2026-10-24", usageRollupBaseline{})
	if len(daily) != 1 || daily[0].SourceRequestCount != 1 || daily[0].InputTokens != 10 {
		t.Fatalf("rollup includes count: %+v", daily)
	}
}

func TestCountTokensEndpointPathAndBody(t *testing.T) {
	for base, want := range map[string]string{"https://example.test": "https://example.test/v1/messages/count_tokens", "https://example.test/v1": "https://example.test/v1/messages/count_tokens", "https://example.test/proxy/v1/messages": "https://example.test/proxy/v1/messages/count_tokens"} {
		got, err := countTokensEndpoint(base)
		if err != nil || got != want {
			t.Fatalf("endpoint=%s err=%v want=%s", got, err, want)
		}
	}
	body, err := countTokensModelBody([]byte(`{"messages":[],"extra":true}`), "actual")
	if err != nil {
		t.Fatal(err)
	}
	missing, err := countTokensModelBody([]byte("{ \"messages\":[] }\n"), "actual")
	if err != nil || string(missing) != "{ \"messages\":[] ,\"model\":\"actual\"}\n" {
		t.Fatalf("default model changed unrelated bytes: %q err=%v", missing, err)
	}
	if _, err := countTokensModelBody([]byte("null"), "actual"); err == nil {
		t.Fatal("accepted a non-object count request")
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil || decoded["model"] != "actual" || decoded["extra"] != true {
		t.Fatalf("body=%s err=%v", body, err)
	}
}

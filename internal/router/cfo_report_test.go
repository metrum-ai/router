// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCFOReportReconcilesAdminOverview(t *testing.T) {
	store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	for i, ts := range []time.Time{start, start.Add(time.Hour), end} {
		rec := pricedTokenBreakdownRecord(string(rune('a'+i)), "miss", ts)
		rec.CallerProject = []string{"finance", "engineering", "excluded"}[i]
		rec.CallerUser = []string{"alice", "bob", "excluded"}[i]
		rec.TokenID = []string{"key-a", "key-b", "key-c"}[i]
		if err := store.db.Create(recordFromRow(rowFromRecord(rec))).Error; err != nil {
			t.Fatal(err)
		}
	}
	baseline := adminSavingsBaselineDTO{BaselineID: "fixture", BaselineInputPricePerMillionUSD: 20, BaselineOutputPricePerMillionUSD: 30}
	opts := UsageReportOptions{From: start, To: end}
	filters := adminReportFilters{From: start, To: end, UsageReportOptions: opts, Limit: 50}
	service := &Service{usage: store, cfg: &Config{Server: ServerConfig{AdminReports: AdminReportsConfig{CFOTeams: map[string]string{"finance": "shared", "engineering": "shared"}}}}}
	report, err := service.buildCFOReport(filters, baseline)
	if err != nil {
		t.Fatal(err)
	}
	overview, err := store.adminOverviewSQL(opts, baseline, 50)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Requests != 2 || report.Summary.Requests != overview.Total.Calls || math.Abs(report.Summary.TotalCostUSD-overview.Total.TotalCostUSD) > 1e-9 {
		t.Fatalf("CFO totals %+v do not reconcile with overview %+v", report.Summary, overview.Total)
	}
	wantBaseline := (200.0*20 + 80.0*30) / 1_000_000
	if report.Summary.BaselineCostUSD == nil || math.Abs(*report.Summary.BaselineCostUSD-wantBaseline) > 1e-9 {
		t.Fatalf("baseline got %v want %g", report.Summary.BaselineCostUSD, wantBaseline)
	}
	if report.Summary.SavingsUSD == nil || math.Abs(*report.Summary.SavingsUSD-(wantBaseline-report.Summary.TotalCostUSD)) > 1e-9 {
		t.Fatalf("incorrect savings: %+v", report.Summary)
	}
	if report.TopUsers.UserCount != 2 || report.TopUsers.TopUserCount != 1 || report.TopUsers.TopTokens != 140 || report.TopUsers.TokenSharePct != 50 {
		t.Fatalf("incorrect consumer concentration: %+v", report.TopUsers)
	}
	if len(report.ByProject.Rows) != 2 || len(report.ByKey.Rows) != 2 || len(report.ByModelGroup.Rows) != 1 {
		t.Fatalf("unexpected dimensions: project=%d key=%d group=%d", len(report.ByProject.Rows), len(report.ByKey.Rows), len(report.ByModelGroup.Rows))
	}
	if len(report.ByTeam) != 2 || report.ByTeam[0].Team != "shared" || report.ByTeam[0].Requests != 2 || report.ByTeam[1].Requests != 0 || !report.ByTeam[1].Unassigned || math.Abs(report.ByTeam[0].CostUSD-report.Summary.TotalCostUSD) > 1e-9 {
		t.Fatalf("team allocation does not reconcile: %+v", report.ByTeam)
	}
	service.cfg.Server.AdminReports.CFOTeams["engineering"] = "private-team"
	filters.CallerProject = "finance"
	scoped, err := service.buildCFOReport(filters, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if scoped.Summary.Requests != 1 || len(scoped.ByTeam) != 2 || scoped.ByTeam[0].Team != "shared" || scoped.ByTeam[0].Requests != 1 {
		t.Fatalf("team scope leaked or failed: %+v", scoped.ByTeam)
	}
}

func TestCFOReportCaseStudyExample(t *testing.T) {
	raw, err := os.ReadFile("../../examples/case-studies/support-agent/evidence/usage.json")
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		Arms map[string]struct {
			Usage []struct {
				Input  int    `json:"input_tokens"`
				Output int    `json:"output_tokens"`
				Cost   string `json:"cost_billed_usd"`
			} `json:"usage"`
		} `json:"arms"`
	}
	if err := json.Unmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	routed := evidence.Arms["routed"].Usage
	if len(routed) < 2 {
		t.Fatal("missing routed evidence")
	}
	item := routed[1]
	cost, err := strconv.ParseFloat(item.Cost, 64)
	if err != nil {
		t.Fatal(err)
	}
	if item.Input != 678 || item.Output != 47 || math.Abs(cost-0.0002598) > 1e-12 {
		t.Fatalf("case study evidence changed: %+v", item)
	}
	store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	rec := pricedTokenBreakdownRecord("case-study", "miss", now)
	rec.Usage.InputTokens, rec.Usage.OutputTokens, rec.Usage.TotalTokens = item.Input, item.Output, item.Input+item.Output
	row := recordFromRow(rowFromRecord(rec))
	row.CostBilledUSD, row.TotalCostUSD = cost, cost
	if err := store.db.Create(row).Error; err != nil {
		t.Fatal(err)
	}
	opts := UsageReportOptions{From: now.Add(-time.Minute), To: now.Add(time.Minute)}
	baseline := adminSavingsBaselineDTO{BaselineID: "fixture", BaselineInputPricePerMillionUSD: 2, BaselineOutputPricePerMillionUSD: 10}
	report, err := (&Service{usage: store, cfg: &Config{}}).buildCFOReport(adminReportFilters{From: opts.From, To: opts.To, UsageReportOptions: opts, Limit: 10}, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.BaselineCostUSD == nil || report.Summary.SavingsUSD == nil || math.Abs(*report.Summary.BaselineCostUSD-0.001826) > 1e-12 || math.Abs(*report.Summary.SavingsUSD-0.0015662) > 1e-12 {
		t.Fatalf("case study calculation: %+v", report.Summary)
	}
}

func TestCFOTopNUsesRecordedBilledCost(t *testing.T) {
	store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	for i, project := range []string{"finance", "engineering"} {
		rec := pricedTokenBreakdownRecord(string(rune('a'+i)), "miss", now)
		rec.CallerProject = project
		row := recordFromRow(rowFromRecord(rec))
		row.CostBilledUSD = []float64{10, 1}[i]
		row.TotalCostUSD = []float64{1, 10}[i]
		if err := store.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	opts := UsageReportOptions{From: now.Add(-time.Minute), To: now.Add(time.Minute)}
	report, err := (&Service{usage: store, cfg: &Config{}}).buildCFOReport(adminReportFilters{From: opts.From, To: opts.To, UsageReportOptions: opts, Limit: 1}, adminSavingsBaselineDTO{BaselineID: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.ByProject.Rows) != 1 || report.ByProject.Rows[0].Key != "finance" || !report.ByProject.Pagination.HasMore || report.Summary.TotalCostUSD != 11 {
		t.Fatalf("incorrect cost top-N or totals: %+v", report.ByProject)
	}
	if report.Summary.SavingsPct != nil || report.ByProject.Rows[0].SavingsPct != nil {
		t.Fatal("zero baseline must not report a savings percentage")
	}
}

func TestCFOReportAuthorizationAndScope(t *testing.T) {
	hash := mustBcryptHash(t, "test-password")
	t.Setenv("CFO_ADMIN_HASH", hash)
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{Enabled: true, AllowInsecureHTTP: true, Users: []AdminBasicAuthUser{
		{Username: "admin", PasswordHashEnv: "CFO_ADMIN_HASH", Subject: "basic:admin", Domain: "finance/test"},
		{Username: "reader", PasswordHashEnv: "CFO_ADMIN_HASH", Subject: "basic:reader", Domain: "finance/test"},
	}}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{
		"p, basic:admin, finance/test, admin:reports, read|export",
		"p, basic:reader, finance/test, admin:reports, read",
	}}
	cfg.Server.AdminReports = AdminReportsConfig{Enabled: true, DefaultSince: "24h", MaxRange: "31d", MaxRows: 100, CFOTeams: map[string]string{"finance": "finance-team", "engineering": "private-team"}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	secret := "rtr_metrum_alice_finance_test_public_fake-secret"
	svc.quota = &quotaStore{callers: map[string]*callerRuntime{"legacy": {cfg: CallerConfig{ID: "legacy", TokenID: secret, Project: "finance", Environment: "test"}}}, state: persistentState{Callers: map[string]*callerState{}}}
	for i, project := range []string{"finance", "engineering"} {
		rec := pricedTokenBreakdownRecord(string(rune('a'+i)), "miss", time.Now().UTC().Add(-time.Minute))
		rec.CallerProject, rec.CallerEnvironment = project, "test"
		rec.TokenID = "=key"
		if err := svc.usage.db.Create(recordFromRow(rowFromRecord(rec))).Error; err != nil {
			t.Fatal(err)
		}
	}
	request := func(path, user string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if user != "" {
			r.SetBasicAuth(user, "test-password")
		}
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		return w
	}
	if got := request("/admin/reports/api/cfo", ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", got.Code)
	}
	for _, path := range []string{"/admin/reports/cfo/export.csv", "/admin/reports/cfo/export.md"} {
		if got := request(path, "reader"); got.Code != http.StatusForbidden {
			t.Fatalf("reader export %s status=%d body=%s", path, got.Code, got.Body.String())
		}
	}
	got := request("/admin/reports/api/cfo?since=1h", "reader")
	if got.Code != http.StatusOK {
		t.Fatalf("report status=%d body=%s", got.Code, got.Body.String())
	}
	var report cfoReport
	if err := json.Unmarshal(got.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Summary.Requests != 1 || len(report.ByTeam) != 2 || report.ByTeam[0].Team != "finance-team" || strings.Contains(got.Body.String(), "private-team") || strings.Contains(got.Body.String(), "provider-key") {
		t.Fatalf("bad scoped report: %s", got.Body.String())
	}
	if strings.Contains(got.Body.String(), secret) || strings.Contains(got.Body.String(), publicTokenID(secret)) || strings.Contains(got.Body.String(), `"tokenId"`) || !strings.Contains(got.Body.String(), "key-1") {
		t.Fatal("token ID leaked or opaque key missing from JSON")
	}
	if prefix := request("/admin/reports/api/cfo?token_id_prefix=%25", "admin"); prefix.Code != http.StatusBadRequest {
		t.Fatalf("wildcard prefix status=%d", prefix.Code)
	}
	if !strings.Contains(got.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("missing no-store: %v", got.Header())
	}
	if disabled := request("/admin/reports/cfo/export.md?since=1h", "admin"); disabled.Code != http.StatusNotFound {
		t.Fatalf("disabled markdown status=%d", disabled.Code)
	}
	svc.cfg.Server.AdminReports.ExportMarkdown = true
	if enabled := request("/admin/reports/cfo/export.md?since=1h", "admin"); enabled.Code != http.StatusOK || strings.Contains(enabled.Body.String(), secret) || strings.Contains(enabled.Body.String(), publicTokenID(secret)) || !strings.Contains(enabled.Body.String(), "key-1") {
		t.Fatalf("enabled markdown status=%d", enabled.Code)
	}
	exported := request("/admin/reports/cfo/export.csv?since=1h", "admin")
	if exported.Code != http.StatusOK || !strings.Contains(exported.Body.String(), "key-1") || strings.Contains(exported.Body.String(), "private-team") || strings.Contains(exported.Body.String(), secret) || strings.Contains(exported.Body.String(), publicTokenID(secret)) {
		t.Fatalf("bad csv status=%d body=%s", exported.Code, exported.Body.String())
	}
	rows, err := csv.NewReader(strings.NewReader(exported.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows[1:] {
		if row[0] == "live_token_budget" && row[17] != "" {
			t.Fatal("quota was linked to a token ID")
		}
	}
	legacySecret := "legacy-raw-bearer-token-for-test-only"
	old := pricedTokenBreakdownRecord("old", "miss", time.Now().UTC().Add(-time.Minute))
	old.CallerProject, old.CallerEnvironment, old.TokenID = "finance", "test", legacySecret
	if err := svc.usage.db.Create(recordFromRow(rowFromRecord(old))).Error; err != nil {
		t.Fatal(err)
	}
	svc.quota.mu.Lock()
	// Historical finance usage may belong to a caller now in another domain.
	svc.quota.callers["unsafe"] = &callerRuntime{cfg: CallerConfig{ID: "unsafe", TokenID: legacySecret, TokenSHA256: legacyCallerTokenSHA256Hex(legacySecret), Project: "engineering", Environment: "test"}}
	svc.quota.mu.Unlock()
	for _, phase := range []string{"moved", "changed", "removed"} {
		for _, path := range []string{"/admin/reports/api/cfo", "/admin/reports/cfo/export.csv", "/admin/reports/cfo/export.md"} {
			result := request(path, "admin")
			if result.Code != http.StatusOK || strings.Contains(result.Body.String(), legacySecret) || !strings.Contains(result.Body.String(), "key-1") {
				t.Fatalf("%s legacy response %s: status %d", phase, path, result.Code)
			}
		}
		svc.quota.mu.Lock()
		if phase == "moved" {
			svc.quota.callers["unsafe"].cfg.TokenID = "safe-label"
		} else {
			delete(svc.quota.callers, "unsafe")
		}
		svc.quota.mu.Unlock()
	}
	invalid := request("/admin/reports/api/cfo?baseline=custom&baseline_input_price_per_million_usd=NaN&baseline_output_price_per_million_usd=1", "admin")
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid baseline status=%d", invalid.Code)
	}
	// Exercise the authenticated route through a real loopback HTTP server.
	server := httptest.NewServer(svc.Handler())
	defer server.Close()
	live, err := http.NewRequest(http.MethodGet, server.URL+"/admin/reports/api/cfo?since=1h", nil)
	if err != nil {
		t.Fatal(err)
	}
	live.SetBasicAuth("admin", "test-password")
	resp, err := server.Client().Do(live)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || strings.Contains(string(body), "private-team") {
		t.Fatalf("loopback CFO status=%d body=%s", resp.StatusCode, body)
	}
}

func TestCFOUnknownProjectMapping(t *testing.T) {
	store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	for i, project := range []string{"", "unknown"} {
		rec := pricedTokenBreakdownRecord(string(rune('a'+i)), "miss", now)
		row := recordFromRow(rowFromRecord(rec))
		row.CallerProject = project
		if err := store.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	opts := UsageReportOptions{From: now.Add(-time.Minute), To: now.Add(time.Minute)}
	svc := &Service{usage: store, cfg: &Config{Server: ServerConfig{AdminReports: AdminReportsConfig{CFOTeams: map[string]string{"unknown": "mapped"}}}}}
	report, err := svc.buildCFOReport(adminReportFilters{From: opts.From, To: opts.To, UsageReportOptions: opts, Limit: 10}, adminSavingsBaselineDTO{BaselineID: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.ByTeam) != 2 || report.ByTeam[0].Requests != 2 || report.ByTeam[1].Requests != 0 || report.ByProject.Rows[0].Requests != 2 {
		t.Fatalf("unknown project mismatch: %+v %+v", report.ByTeam, report.ByProject.Rows)
	}
}

func TestCFOTeamConfigValidation(t *testing.T) {
	valid := AdminReportsConfig{CFOTeams: map[string]string{"project": "team"}}
	if err := validateAdminReports(valid, AdminAuthConfig{}, UsageDBConfig{}); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []map[string]string{{"": "team"}, {"project": " team"}, {"project": "bad\nname"}, {"Finance Team": "team"}, {"finance-team": "a", "Finance Team": "b"}} {
		if err := validateAdminReports(AdminReportsConfig{CFOTeams: invalid}, AdminAuthConfig{}, UsageDBConfig{}); err == nil {
			t.Fatalf("accepted invalid mapping %#v", invalid)
		}
	}
}

func TestCFOMarkdownEscapesLabels(t *testing.T) {
	report := cfoReport{ByTeam: []cfoTeam{{Team: "<script>|[link](javascript:alert(1))"}}}
	result := cfoMarkdown(report)
	if strings.Contains(result, "<script>") || !strings.Contains(result, `\|`) || !strings.Contains(result, `\[link\]`) {
		t.Fatalf("unsafe markdown: %s", result)
	}
}

func TestCFONegativeSavingsAcrossFormats(t *testing.T) {
	negative := -1.25
	report := cfoReport{Summary: adminReportSummary{SavingsUSD: &negative, BaselineCostUSD: &negative}, ByTeam: []cfoTeam{{SavingsUSD: negative}}, ByKey: adminScalarReportResponse{Rows: []adminScalarReportRow{{SavingsUSD: &negative}}}, Trend: []adminReportSeries{{SavingsUSD: negative}}}
	encoded, err := json.Marshal(report)
	if err != nil || !strings.Contains(string(encoded), "-1.25") {
		t.Fatalf("JSON loss missing: %s %v", encoded, err)
	}
	data, err := cfoCSV(report)
	if err != nil || !strings.Contains(string(data), ",-1.25,") {
		t.Fatalf("CSV loss missing: %s %v", data, err)
	}
	if got := cfoMarkdown(report); strings.Count(got, "-1.250000") < 4 {
		t.Fatalf("Markdown losses missing: %s", got)
	}
}

func TestCFOCSVNeutralizesFormulas(t *testing.T) {
	negative := -1.25
	report := cfoReport{ByKey: adminScalarReportResponse{Rows: []adminScalarReportRow{{Key: "=not-a-formula", SavingsUSD: &negative}}}}
	data, err := cfoCSV(report)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 3 || rows[2][0] != "key" || rows[2][1] != "'=not-a-formula" || rows[2][7] != "-1.25" {
		t.Fatalf("unsafe CSV: %#v", rows)
	}
}

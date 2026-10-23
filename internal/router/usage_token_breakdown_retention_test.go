// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func assertTokenBreakdownClose(t *testing.T, want, got UsageTokenBreakdown) {
	t.Helper()
	values := func(b UsageTokenBreakdown) map[string]float64 {
		raw, _ := json.Marshal(b)
		out := map[string]float64{}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	a, b := values(want), values(got)
	for key, value := range a {
		if math.Abs(value-b[key]) > 1e-9 {
			t.Errorf("%s: want %g got %g", key, value, b[key])
		}
	}
}
func pricedTokenBreakdownRecord(id, cache string, ts time.Time) logRecord {
	read, write, reasoning := 60, 20, 20
	readPrice, writePrice := 2.0, 12.5
	rec := logRecord{RequestID: id, TS: ts.Format(time.RFC3339Nano), CallerID: "alice", CallerProject: "public", RequestedModel: "reasoning", ResolvedGroup: "reasoning", TargetProvider: "mock", TargetModel: "upstream", UsageSource: "reported", Status: 200, Cache: cache, ReasoningAttemptCount: 1, ReasoningSuccessfulAttemptCount: 1, ReasoningReportedAttemptCount: 1, Usage: Usage{InputTokens: 100, OutputTokens: 40, TotalTokens: 140, CachedInputTokens: &read, CacheWriteInputTokens: &write, ReasoningTokens: &reasoning}, InputPricePerMillionUSD: 10, OutputPricePerMillionUSD: 20, CachedInputPricePerMillionUSD: &readPrice, CacheWritePricePerMillionUSD: &writePrice}
	rec.ReasoningCoverageMeasured = true
	rec.ReasoningTokens = &reasoning
	populateCosts(&rec)
	return rec
}
func TestTokenBreakdownRollupRetentionParity(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	verifyTokenBreakdownRetentionParity(t, store)
}

func TestTokenBreakdownPostgresParity(t *testing.T) {
	dsn := os.Getenv("USAGE_B_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("USAGE_B_POSTGRES_TEST_DSN required")
	}
	if !strings.Contains(dsn, "usage_b_provenance_339") {
		t.Fatal("fresh disposable usage_b_provenance_339 database required")
	}
	runner, closeDB, err := UsageMigrationRunner(UsageDBConfig{Driver: "postgres", DSN: dsn, MigrationPolicy: usageDBMigrationPolicyAutoSafe})
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	if err := runner.ApplyPending("usage-b-339"); err != nil {
		t.Fatal(err)
	}
	if err := runner.ApplyMaintenancePendingWithEvidence("usage-b-339", "disposable-empty-test-database"); err != nil {
		t.Fatal(err)
	}
	if err := runner.ApplyPending("usage-b-339"); err != nil {
		t.Fatal(err)
	}
	if err := verifyUsageTokenBreakdownMigration(runner.db); err != nil {
		t.Fatal(err)
	}
	verifyTokenBreakdownRetentionParity(t, &usageStore{db: runner.db})
}

func verifyTokenBreakdownRetentionParity(t *testing.T, store *usageStore) {
	t.Helper()
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	for i, cache := range []string{"miss", "hit"} {
		rec := pricedTokenBreakdownRecord(string(rune('a'+i)), cache, day.Add(time.Hour))
		if err := store.db.Create(recordFromRow(rowFromRecord(rec))).Error; err != nil {
			t.Fatal(err)
		}
	}
	baseline := adminSavingsBaselineDTO{BaselineID: "test", BaselineInputPricePerMillionUSD: 20, BaselineOutputPricePerMillionUSD: 30}
	opts := UsageReportOptions{From: day, To: day.Add(24 * time.Hour)}
	raw, err := store.adminOverviewSQL(opts, baseline, 50)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Total.Calls != 2 || raw.Total.CacheWriteInputTokens != 40 || raw.Total.CacheWriteReportedRequests != 2 || raw.Total.ReasoningAttemptCount != 2 {
		t.Fatalf("raw %+v", raw.Total)
	}
	costs := raw.Total.UsageCostComponents
	if math.Abs(costs.UncachedInputCostUSD+costs.CachedInputCostUSD+costs.CacheWriteCostUSD+costs.VisibleOutputCostUSD+costs.ReasoningOutputCostUSD-raw.Total.TotalCostUSD) > 1e-9 {
		t.Fatalf("cost partitions double count: %+v total=%g", costs, raw.Total.TotalCostUSD)
	}
	split := savingsAttribution(raw.Total.BaselineCostUSD, raw.Total.RoutingListCostUSD, raw.Total.ProviderListCostUSD, raw.Total.TotalCostUSD)
	if math.Abs(split.RoutingSavingsUSD+split.PromptCacheSavingsUSD+split.ResponseCacheSavingsUSD-(raw.Total.BaselineCostUSD-raw.Total.TotalCostUSD)) > 1e-9 {
		t.Fatal("savings do not reconcile")
	}
	if _, err := store.generateUsageRollup(UsageRollupOptions{RollupType: "daily", From: opts.From, To: opts.To, Finalize: true}); err != nil {
		t.Fatal(err)
	}
	opts.RollupHistory = true
	rolled, err := store.adminOverviewSQL(opts, baseline, 50)
	if err != nil {
		t.Fatal(err)
	}
	assertTokenBreakdownClose(t, raw.Total.UsageTokenBreakdown, rolled.Total.UsageTokenBreakdown)
	if rolled.Total.Calls != raw.Total.Calls || math.Abs(rolled.Total.TotalCostUSD-raw.Total.TotalCostUSD) > 1e-9 {
		t.Fatalf("rollup totals %+v raw %+v", rolled.Total, raw.Total)
	}
	for _, endpoint := range []string{"/api/requested-models", "/api/provider-model-mix", "/api/usage-by-caller", "/api/savings-by-project"} {
		spec, ok := adminScalarEndpointSpecs(endpoint)
		if !ok {
			t.Fatal(endpoint)
		}
		rows, total, _, _, err := store.adminScalarAggsSQL(opts, spec, baseline, "requests", 50)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Fatalf("%s rows=%d", endpoint, len(rows))
		}
		assertTokenBreakdownClose(t, raw.Total.UsageTokenBreakdown, total.UsageTokenBreakdown)
	}
	if err := store.db.Where("1 = 1").Delete(&usageRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	retained, err := store.adminOverviewSQL(opts, baseline, 50)
	if err != nil {
		t.Fatal(err)
	}
	assertTokenBreakdownClose(t, raw.Total.UsageTokenBreakdown, retained.Total.UsageTokenBreakdown)
	opts.CallerProject = "private"
	private, err := store.adminOverviewSQL(opts, baseline, 50)
	if err != nil || private.Total.Calls != 0 {
		t.Fatalf("rollup project scope leaked: %+v err=%v", private.Total, err)
	}
}
func TestTokenBreakdownWriteOnlyAndReportedZero(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	zero := 0
	for _, row := range []usageRow{{TS: now, RequestID: "missing", InputTokens: 10}, {TS: now, RequestID: "zero-write", InputTokens: 10, CacheWriteInputTokens: &zero}} {
		if err := store.db.Create(recordFromRow(row)).Error; err != nil {
			t.Fatal(err)
		}
	}
	present := true
	rows, err := store.rows(UsageReportOptions{From: now.Add(-time.Hour), To: now.Add(time.Hour), CachePresent: &present})
	if err != nil || len(rows) != 1 {
		t.Fatalf("write-only evidence filtered out: rows=%d err=%v", len(rows), err)
	}
	var result tokenScalarAggRecord
	if err := store.db.Model(&usageRecord{}).Select(adminScalarAggSQLSelectExpr(adminSavingsBaselineDTO{})).Scan(&result).Error; err != nil {
		t.Fatal(err)
	}
	if result.CacheWriteInputTokens != 0 || result.CacheWriteReportedRequests != 1 || result.CachedInputReportedRequests != 0 {
		t.Fatalf("NULL/zero coverage %+v", result)
	}
}

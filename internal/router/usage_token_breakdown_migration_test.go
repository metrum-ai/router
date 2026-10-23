// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"path/filepath"
	"testing"
	"time"
)

func TestTokenBreakdownMigrationPreservesUnknownHistoricalCoverage(t *testing.T) {
	cfg := uncachedSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	store, err := OpenUsageStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	rec := pricedTokenBreakdownRecord("history", "miss", day.Add(time.Hour))
	if err := store.db.Create(recordFromRow(rowFromRecord(rec))).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.generateUsageRollup(UsageRollupOptions{RollupType: "daily", From: day, To: day.Add(24 * time.Hour), Finalize: true}); err != nil {
		t.Fatal(err)
	}
	// Restore the physical shape immediately before this migration. The old
	// finalized rollup retains its ordinary totals but has no new evidence.
	for table, columns := range usageTokenBreakdownColumns {
		for column := range columns {
			if err := store.db.Exec("ALTER TABLE " + table + " DROP COLUMN " + column).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := store.db.Where("scope = ? AND migration_id = ?", usageMigrationScope, 2026102303).Delete(&migrationAttemptRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Exec("DELETE FROM schema_migration_ledger WHERE scope = ? AND migration_id = ?", usageMigrationScope, 2026102303).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	runner, closeDB, err := UsageMigrationRunner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	if err := runner.ApplyPending("coverage-replay"); err != nil {
		t.Fatal(err)
	}
	if err := runner.ApplyPending("coverage-replay"); err != nil {
		t.Fatal(err)
	}
	var old usageRollupDailyRecord
	if err := runner.db.First(&old).Error; err != nil {
		t.Fatal(err)
	}
	if old.SourceRequestCount != 1 || old.ReasoningReportedRequests != 0 || old.CachedInputReportedRequests != 0 || old.CacheWriteReportedRequests != 0 || old.CostComponentReportedRequests != 0 {
		t.Fatalf("invented historical evidence: %+v", old)
	}
	report, err := (&usageStore{db: runner.db}).adminOverviewSQL(UsageReportOptions{From: day, To: day.Add(24 * time.Hour), RollupHistory: true}, adminSavingsBaselineDTO{}, 50)
	if err != nil {
		t.Fatal(err)
	}
	total := aggFromTokenScalarAggRecord(report.Total)
	summary := adminSummaryFromAgg(&total)
	if summary.SavingsAttributionComplete || summary.ResponseCacheSavingsUSD != 0 || summary.TotalCostUSD <= 0 {
		t.Fatalf("invented historical attribution: %+v", summary)
	}
}

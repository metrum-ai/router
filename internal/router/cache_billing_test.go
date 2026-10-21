// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"gorm.io/gorm"
	"path/filepath"
	"testing"
)

func TestCacheBillingMigrationBackfill(t *testing.T) {
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "cost.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	for _, apply := range []func(*gorm.DB) error{applyUsageExplicitBaseline, applyUsageReasoningTelemetryMigration, applyUsageContentCaptureEncryptionMigration, applyUsageTargetRegionDiagnosticsMigration, applyUsageCachedInputPricingMigration} {
		if err := apply(db); err != nil {
			t.Fatal(err)
		}
	}
	for _, state := range []string{"hit", "miss", "unknown"} {
		if err := db.Omit("CostListUSD", "CostBilledUSD").Create(&usageRecord{RequestID: state, Cache: state, TotalCostUSD: 2}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := applyUsageCacheBillingMigration(db); err != nil {
		t.Fatal(err)
	}
	var rows []usageRecord
	if err := db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		want := 2.0
		if row.Cache == "hit" {
			want = 0
		}
		if row.CostListUSD != 2 || row.CostBilledUSD != want || row.TotalCostUSD != want {
			t.Fatalf("bad backfill: %+v", row)
		}
	}
}

func TestCacheBillingRecordCosts(t *testing.T) {
	for _, state := range []string{"hit", "miss", "bypass", ""} {
		row := recordFromRow(usageRow{Cache: state, TotalCostUSD: 1.25})
		want := 1.25
		if state == "hit" {
			want = 0
		}
		if row.CostListUSD != 1.25 || row.CostBilledUSD != want || row.TotalCostUSD != want {
			t.Fatalf("cache=%q list=%g billed=%g total=%g", state, row.CostListUSD, row.CostBilledUSD, row.TotalCostUSD)
		}
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"path/filepath"
	"testing"
)

func TestCacheWriteMigrationReplay(t *testing.T) {
	cfg := uncachedSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	store, err := OpenUsageStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"cache_write_input_tokens", "cache_write_price_per_million_usd"} {
		if err := store.db.Exec("ALTER TABLE request_usage DROP COLUMN " + field).Error; err != nil {
			t.Fatal(err)
		}
	}
	// A replay must also clear dependent migrations, not leave applied children
	// referencing a missing parent migration.
	if err := store.db.Where("scope = ? AND migration_id >= ?", usageMigrationScope, 2026102301).Delete(&migrationAttemptRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Exec("DELETE FROM schema_migration_ledger WHERE scope = ? AND migration_id >= ?", usageMigrationScope, 2026102301).Error; err != nil {
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
	if err := runner.ApplyPending("cache-write-replay"); err != nil {
		t.Fatal(err)
	}
	if err := runner.ApplyPending("cache-write-replay"); err != nil {
		t.Fatal(err)
	}
	if err := verifyUsageCacheWriteMigration(runner.db); err != nil {
		t.Fatal(err)
	}
	n := 300
	price := 12.5
	record := recordFromRow(rowFromRecord(logRecord{RequestID: "cache-write", Usage: Usage{InputTokens: 325, CacheWriteInputTokens: &n}, CacheWritePricePerMillionUSD: &price}))
	if err := runner.db.Create(record).Error; err != nil {
		t.Fatal(err)
	}
	var got usageRecord
	if err := runner.db.First(&got, "request_id = ?", "cache-write").Error; err != nil {
		t.Fatal(err)
	}
	if got.CacheWriteInputTokens == nil || *got.CacheWriteInputTokens != 300 || got.CacheWritePricePerMillionUSD == nil || *got.CacheWritePricePerMillionUSD != 12.5 {
		t.Fatalf("roundtrip=%+v", got)
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"path/filepath"
	"testing"
)

func TestUsageRecomputedInputSchema20SQLiteUpgradeFrom19(t *testing.T) {
	cfg := uncachedSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	db, err := openUsageDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	definitionsThrough19 := make([]MigrationDefinition, 0, len(usageMigrationDefinitions)-1)
	for _, definition := range usageMigrationDefinitions {
		if definition.SchemaVersion <= 19 {
			definitionsThrough19 = append(definitionsThrough19, definition)
		}
	}
	old, err := NewMigrationRunnerWithDataJobs(db, usageMigrationScope, MigrationCompatibility{MinSchema: 0, MaxSchema: 19, MinData: 0, MaxData: 1}, definitionsThrough19, usageDataJobDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.ApplyPending("recomputed-input-schema-20-test"); err != nil {
		t.Fatal(err)
	}
	before, err := old.Status()
	if err != nil || before.SchemaVersion != 19 || !before.Compatible {
		t.Fatalf("schema 19 fixture: %+v err=%v", before, err)
	}
	cached := 40
	legacy := usageRecord{RequestID: "schema-19-row", TS: "2026-11-16T00:00:00Z", InputTokens: 100, CachedInputTokens: &cached}
	if err := db.Omit("RecomputedInputTokens").Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	current, err := NewMigrationRunnerWithDataJobs(db, usageMigrationScope, usageMigrationCompatibility, usageMigrationDefinitions, usageDataJobDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.ApplyPending("recomputed-input-schema-20-test"); err != nil {
		t.Fatal(err)
	}
	after, err := current.Status()
	if err != nil || after.SchemaVersion != 20 || !after.Compatible {
		t.Fatalf("schema 20 upgrade: %+v err=%v", after, err)
	}
	var got usageRecord
	if err := db.First(&got, "request_id = ?", legacy.RequestID).Error; err != nil {
		t.Fatal(err)
	}
	if got.RecomputedInputTokens == nil || *got.RecomputedInputTokens != 60 {
		t.Fatalf("recomputed input tokens=%v, want 60", got.RecomputedInputTokens)
	}
}

func TestUsageRecomputedInputMigrationBackfillsKnownRowsOnly(t *testing.T) {
	store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cached := 40
	for _, row := range []usageRecord{
		{RequestID: "known-cache", TS: "2026-11-16T00:00:00Z", InputTokens: 100, CachedInputTokens: &cached},
		{RequestID: "unknown-cache", TS: "2026-11-16T00:00:01Z", InputTokens: 100},
		{RequestID: "negative-input", TS: "2026-11-16T00:00:02Z", InputTokens: -1, CachedInputTokens: &cached},
	} {
		if err := store.db.Omit("RecomputedInputTokens").Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := store.db.Model(&usageRecord{}).Where("request_id = ?", "known-cache").Update("recomputed_input_tokens", nil).Error; err != nil {
		t.Fatal(err)
	}
	if err := applyUsageRecomputedInputMigration(store.db); err != nil {
		t.Fatal(err)
	}
	var known, unknown, negative usageRecord
	if err := store.db.Where("request_id = ?", "known-cache").First(&known).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Where("request_id = ?", "unknown-cache").First(&unknown).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Where("request_id = ?", "negative-input").First(&negative).Error; err != nil {
		t.Fatal(err)
	}
	if known.RecomputedInputTokens == nil || *known.RecomputedInputTokens != 60 {
		t.Fatalf("known recomputed tokens=%v", known.RecomputedInputTokens)
	}
	if unknown.RecomputedInputTokens != nil {
		t.Fatalf("missing cache evidence was backfilled as known: %d", *unknown.RecomputedInputTokens)
	}
	if negative.RecomputedInputTokens != nil {
		t.Fatalf("negative input was backfilled as known: %d", *negative.RecomputedInputTokens)
	}
}

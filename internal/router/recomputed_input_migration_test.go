// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"path/filepath"
	"testing"
)

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
	var known, unknown usageRecord
	if err := store.db.Where("request_id = ?", "known-cache").First(&known).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Where("request_id = ?", "unknown-cache").First(&unknown).Error; err != nil {
		t.Fatal(err)
	}
	if known.RecomputedInputTokens == nil || *known.RecomputedInputTokens != 60 {
		t.Fatalf("known recomputed tokens=%v", known.RecomputedInputTokens)
	}
	if unknown.RecomputedInputTokens != nil {
		t.Fatalf("missing cache evidence was backfilled as known: %d", *unknown.RecomputedInputTokens)
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"path/filepath"
	"testing"
	"time"
)

func TestUsageEndpointSQLiteMigrationAndRoundTrip(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.db.Exec("ALTER TABLE request_usage DROP COLUMN endpoint").Error; err != nil {
		t.Fatal(err)
	}
	if err := verifyUsageTokenBreakdownMigration(store.db); err != nil {
		t.Fatalf("previous schema must remain valid: %v", err)
	}
	if err := applyUsageEndpointMigration(store.db); err != nil {
		t.Fatal(err)
	}
	if err := applyUsageEndpointMigration(store.db); err != nil {
		t.Fatalf("migration must be idempotent: %v", err)
	}
	row := recordFromRow(usageRow{RequestID: "endpoint-test", Endpoint: "embeddings", TS: time.Now().UTC()})
	if err := store.db.Create(row).Error; err != nil {
		t.Fatal(err)
	}
	var saved usageRecord
	if err := store.db.First(&saved, "request_id = ?", row.RequestID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Endpoint != "embeddings" {
		t.Fatalf("lost endpoint: %q", saved.Endpoint)
	}
}

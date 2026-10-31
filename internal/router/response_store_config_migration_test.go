// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"gorm.io/gorm"
	"testing"
	"time"
)

func exerciseResponseStoreConfig(t *testing.T, db *gorm.DB) {
	t.Helper()
	t.Setenv("MOCK_API_KEY", "test-only-key")
	if err := db.Exec("ALTER TABLE router_config_server DROP COLUMN responses_store_retention").Error; err != nil {
		t.Fatal(err)
	}
	if err := applyConfigResponsesStoreMigration(db); err != nil {
		t.Fatal(err)
	}
	if err := applyConfigResponsesStoreMigration(db); err != nil {
		t.Fatal(err)
	}
	cfg := minimalProjectionConfig()
	cfg.Server.ResponsesStore.Retention = "5d"
	cfg.setDefaults()
	cfg.Server.Identifiers = IdentifierConfig{Mode: "passthrough"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := ProjectConfigToRelational(db, "set-1", cfg); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`UPDATE router_config_sets SET status='active', validation_status='valid' WHERE id=?`, "set-1").Error; err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadActiveConfigFromDB(db, "staging", IdentifierConfig{Mode: "passthrough"})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Server.ResponsesStore.Retention != "5d" {
		t.Fatalf("lost retention: %q", loaded.Server.ResponsesStore.Retention)
	}
}

func TestResponseStoreConfigSQLiteMigration(t *testing.T) {
	r, closeDB := newRelationalProjectionTestDB(t, "set-1")
	defer closeDB()
	exerciseResponseStoreConfig(t, r.db)
}

func TestResponseStoreConfigPostgresMigration(t *testing.T) {
	db, cleanup, _ := configAdminPostgresTestDB(t)
	defer cleanup()
	if err := db.Exec(`INSERT INTO router_config_sets (id, runtime_scope, name, status, validation_status, created_at) VALUES (?, ?, ?, ?, ?, ?)`, "set-1", "staging", "set-1", "draft", "valid", time.Now().UTC().Format(time.RFC3339Nano)).Error; err != nil {
		t.Fatal(err)
	}
	exerciseResponseStoreConfig(t, db)
}

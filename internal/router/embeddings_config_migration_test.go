// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"reflect"
	"testing"
	"time"
)

func TestEmbeddingConfigSQLiteMigrationAndRoundTrip(t *testing.T) {
	t.Setenv("MOCK_API_KEY", "test-only-key")
	r, closeDB := newRelationalProjectionTestDB(t, "set-1")
	defer closeDB()
	cfg := minimalProjectionConfig()
	g := cfg.Models["default"]
	g.Strategy = "failover"
	g.Targets[0].OutputModalities = []string{"embeddings"}
	g.Targets[0].EmbeddingSpace = "shared-space"
	g.Targets[0].EmbeddingDimensions = []int{256, 1024}
	cfg.Models["default"] = g
	cfg.setDefaults()
	cfg.Server.Identifiers = IdentifierConfig{Mode: "passthrough"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := ProjectConfigToRelational(r.db, "set-1", cfg); err != nil {
		t.Fatal(err)
	}
	if err := r.db.Exec(`UPDATE router_config_sets SET status = 'active', validation_status = 'valid' WHERE id = ?`, "set-1").Error; err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadActiveConfigFromDB(r.db, "staging", IdentifierConfig{Mode: "passthrough"})
	if err != nil {
		t.Fatal(err)
	}
	target := loaded.Models["default"].Targets[0]
	if target.EmbeddingSpace != "shared-space" || !reflect.DeepEqual(target.EmbeddingDimensions, []int{256, 1024}) || !isEmbeddingTarget(target) {
		t.Fatalf("lost embedding target configuration: %+v", target)
	}
	if err := r.db.Exec(`UPDATE router_config_model_group_targets SET embedding_dimensions = 'not-json' WHERE config_set_id = ?`, "set-1").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := LoadActiveConfigFromDB(r.db, "staging", IdentifierConfig{Mode: "passthrough"}); err == nil {
		t.Fatal("malformed dimensions must fail closed")
	}
}

func TestEmbeddingConfigPostgresMigrationAndRoundTrip(t *testing.T) {
	t.Setenv("MOCK_API_KEY", "test-only-key")
	db, cleanup, _ := configAdminPostgresTestDB(t)
	defer cleanup()
	if err := db.Exec(`INSERT INTO router_config_sets (id, runtime_scope, name, status, validation_status, created_at) VALUES (?, ?, ?, ?, ?, ?)`, "set-1", "staging", "set-1", "draft", "valid", time.Now().UTC().Format(time.RFC3339Nano)).Error; err != nil {
		t.Fatal(err)
	}
	cfg := minimalProjectionConfig()
	g := cfg.Models["default"]
	g.Strategy = "weighted"
	g.Targets[0].OutputModalities = []string{"embeddings"}
	g.Targets[0].EmbeddingSpace = "shared-space"
	g.Targets[0].EmbeddingDimensions = []int{256, 1024}
	cfg.Models["default"] = g
	cfg.setDefaults()
	cfg.Server.Identifiers = IdentifierConfig{Mode: "passthrough"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := ProjectConfigToRelational(db, "set-1", cfg); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`UPDATE router_config_sets SET status = 'active' WHERE id = ?`, "set-1").Error; err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadActiveConfigFromDB(db, "staging", IdentifierConfig{Mode: "passthrough"})
	if err != nil {
		t.Fatal(err)
	}
	target := loaded.Models["default"].Targets[0]
	if target.EmbeddingSpace != "shared-space" || !reflect.DeepEqual(target.EmbeddingDimensions, []int{256, 1024}) || !isEmbeddingTarget(target) {
		t.Fatalf("lost embedding target configuration: %+v", target)
	}
	if err := applyConfigEmbeddingMigration(db); err != nil {
		t.Fatal(err)
	}
}

func TestEmbeddingConfigSQLiteUpgradeIsIdempotent(t *testing.T) {
	r, closeDB := newRelationalProjectionTestDB(t, "set-1")
	defer closeDB()
	if err := ProjectConfigToRelational(r.db, "set-1", minimalProjectionConfig()); err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"embedding_space", "embedding_dimensions"} {
		if err := r.db.Exec("ALTER TABLE router_config_model_group_targets DROP COLUMN " + col).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := applyConfigEmbeddingMigration(r.db); err != nil {
		t.Fatal(err)
	}
	if err := applyConfigEmbeddingMigration(r.db); err != nil {
		t.Fatal(err)
	}
	var target modelGroupTargetRow
	if err := r.db.First(&target, "config_set_id = ?", "set-1").Error; err != nil {
		t.Fatal(err)
	}
	if target.EmbeddingSpace != "" || target.EmbeddingDimensions != "[]" {
		t.Fatalf("unsafe defaults: %+v", target)
	}
}

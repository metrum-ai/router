// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestV5AdminConfigCleanupMigrationRemovesRemovedKeys(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	prior, err := NewMigrationRunner(r.db, configControlPlaneScope, MigrationCompatibility{MinSchema: 0, MaxSchema: 12, MinData: 0, MaxData: 0}, configControlPlaneMigrationDefinitions[:len(configControlPlaneMigrationDefinitions)-1])
	if err != nil {
		t.Fatal(err)
	}
	applyConfigControlPlaneMigrationsForTest(t, prior)

	legacy := "admin:\n  enabled: true\n  legacy_routes_on_serving_port: false\n  legacy_sunset: 2027-03-01T00:00:00Z\nserver:\n  listen: ':8080'\n"
	digest := sha256.Sum256([]byte(legacy))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := r.db.Exec(`INSERT INTO router_config_sets (id, runtime_scope, name, status, validation_status, created_at) VALUES (?, ?, ?, ?, ?, ?)`, "admin-cleanup", "default", "legacy", "active", "valid", now).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.db.Exec(`INSERT INTO router_config_documents (config_set_id, canonical_yaml, content_sha256, updated_at) VALUES (?, ?, ?, ?)`, "admin-cleanup", legacy, hex.EncodeToString(digest[:]), time.Now().UTC().Format(time.RFC3339Nano)).Error; err != nil {
		t.Fatal(err)
	}
	next, err := NewMigrationRunner(r.db, configControlPlaneScope, configControlPlaneCompatibility, configControlPlaneMigrationDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	if err := next.ApplyMaintenancePendingWithEvidence("test-v5-cleanup", "test-backup-ref"); err != nil {
		t.Fatalf("apply v5 cleanup migration: %v", err)
	}
	if _, err := next.Verify(); err != nil {
		t.Fatalf("verify v5 cleanup: %v", err)
	}
	var cleaned, storedDigest string
	if err := r.db.Raw(`SELECT canonical_yaml, content_sha256 FROM router_config_documents WHERE config_set_id = ?`, "admin-cleanup").Row().Scan(&cleaned, &storedDigest); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cleaned, "legacy_routes_on_serving_port") || strings.Contains(cleaned, "legacy_sunset") {
		t.Fatalf("removed settings remain in stored config: %s", cleaned)
	}
	if !strings.Contains(cleaned, "listen: ':8080'") || !strings.Contains(cleaned, "enabled: true") {
		t.Fatalf("cleanup changed unrelated stored settings: %s", cleaned)
	}
	updatedAtBefore := ""
	if err := r.db.Raw(`SELECT updated_at FROM router_config_documents WHERE config_set_id = ?`, "admin-cleanup").Row().Scan(&updatedAtBefore); err != nil {
		t.Fatal(err)
	}
	if err := applyV5AdminConfigCleanup(r.db); err != nil {
		t.Fatalf("repeat cleanup: %v", err)
	}
	var cleanedAgain, updatedAtAfter string
	if err := r.db.Raw(`SELECT canonical_yaml, updated_at FROM router_config_documents WHERE config_set_id = ?`, "admin-cleanup").Row().Scan(&cleanedAgain, &updatedAtAfter); err != nil {
		t.Fatal(err)
	}
	if cleanedAgain != cleaned || updatedAtAfter != updatedAtBefore {
		t.Fatalf("repeat cleanup was not idempotent: yaml_equal=%t updated_at_equal=%t", cleanedAgain == cleaned, updatedAtAfter == updatedAtBefore)
	}
	newDigest := sha256.Sum256([]byte(cleaned))
	if storedDigest != hex.EncodeToString(newDigest[:]) {
		t.Fatal("stored config content hash was not refreshed")
	}
}

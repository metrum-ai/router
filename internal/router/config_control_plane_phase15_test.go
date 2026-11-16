// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"path/filepath"
	"testing"
)

func TestCallerTokenRevocationMigrationBackfillsLegacyHashes(t *testing.T) {
	runner, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	previousCompatibility := configControlPlaneCompatibility
	previousCompatibility.MaxSchema = 14
	previousDefinitions := configControlPlaneMigrationDefinitions[:len(configControlPlaneMigrationDefinitions)-1]
	previousRunner, err := NewMigrationRunner(runner.db, configControlPlaneScope, previousCompatibility, previousDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	applyConfigControlPlaneMigrationsForTest(t, previousRunner)
	if err := runner.db.Exec(`INSERT INTO router_config_sets (id, runtime_scope, name, status, validation_status, created_at) VALUES (?, ?, ?, ?, ?, ?)`, "legacy-set", "test", "legacy", "active", "valid", "2026-01-01T00:00:00Z").Error; err != nil {
		t.Fatal(err)
	}
	const legacyVerifier = "pbkdf2-sha3-512:i=20000:c2FsdA:a2V5"
	if err := runner.db.Exec(`INSERT INTO router_config_callers (config_set_id, caller_id, token_sha256, revoked_token_hash) VALUES (?, ?, ?, ?)`, "legacy-set", "legacy-caller", legacyVerifier, legacyVerifier).Error; err != nil {
		t.Fatal(err)
	}
	currentRunner, err := NewMigrationRunner(runner.db, configControlPlaneScope, configControlPlaneCompatibility, configControlPlaneMigrationDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	if err := currentRunner.ApplyPending("test-phase15"); err != nil {
		t.Fatalf("apply sticky-revocation migration: %v", err)
	}
	revoked, err := loadRevokedCallerTokenHashes(runner.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := revoked[legacyVerifier]; !ok {
		t.Fatal("migration did not preserve a legacy revoked verifier")
	}
}

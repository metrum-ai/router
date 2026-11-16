// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"

	"gorm.io/gorm"
)

const callerTokenMetadataMigrationID = 2026111601

func callerTokenMetadataMigrationDefinition() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{
		ID: callerTokenMetadataMigrationID, Scope: configControlPlaneScope,
		Name: "store caller token metadata and verifier revocations", Release: "2026.11",
		Checksum:      MigrationChecksum("router-config.phase13.caller-token-metadata.v1"),
		SchemaVersion: 13, Transactional: true, MaintenanceMode: "online", RollbackClass: "restore-required",
		HandlerKey:       "router-config.phase13.apply.v1@applyCallerTokenMetadataMigration",
		PostconditionKey: "router-config.phase13.schema.v1@verifyCallerTokenMetadataMigration",
		Dependencies:     []int{configControlPlanePhase11MigrationID}, ExecutionMode: "transactional",
		LockClass: "online", TimeoutClass: "bounded", Apply: applyCallerTokenMetadataMigration,
		Verify: verifyCallerTokenMetadataMigration,
	})
}

func applyCallerTokenMetadataMigration(tx *gorm.DB) error {
	for name, ddl := range map[string]string{
		"token_format_version": "INTEGER NOT NULL DEFAULT 0",
		"token_entropy_bits":   "INTEGER NOT NULL DEFAULT 0",
		"revoked_token_hash":   "TEXT NOT NULL DEFAULT ''",
	} {
		if tx.Migrator().HasColumn("router_config_callers", name) {
			continue
		}
		if err := tx.Exec(fmt.Sprintf("ALTER TABLE router_config_callers ADD COLUMN %s %s", name, ddl)).Error; err != nil {
			return fmt.Errorf("add caller token column %s: %w", name, err)
		}
	}
	return nil
}

func verifyCallerTokenMetadataMigration(tx *gorm.DB) error {
	for _, name := range []string{"token_format_version", "token_entropy_bits", "revoked_token_hash"} {
		if !tx.Migrator().HasColumn("router_config_callers", name) {
			return fmt.Errorf("required caller token column %s is missing", name)
		}
	}
	return nil
}

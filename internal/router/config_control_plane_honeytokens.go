// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"

	"gorm.io/gorm"
)

const configControlPlaneHoneytokenMigrationID = 2026111611

func configControlPlaneHoneytokenMigrationDefinition() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{
		ID:               configControlPlaneHoneytokenMigrationID,
		Scope:            configControlPlaneScope,
		Name:             "persist honeytoken caller metadata",
		Release:          "2026.11",
		Checksum:         MigrationChecksum("router-config.phase18.honeytoken-callers.v1"),
		SchemaVersion:    18,
		Transactional:    true,
		MaintenanceMode:  "online",
		RollbackClass:    "restore-required",
		HandlerKey:       "router-config.phase18.apply.v1@applyConfigControlPlaneHoneytokens",
		PostconditionKey: "router-config.phase18.schema.v1@verifyConfigControlPlaneHoneytokens",
		Dependencies:     []int{adminClientMigrationID},
		ExecutionMode:    "transactional",
		LockClass:        "online",
		TimeoutClass:     "bounded",
		Apply:            applyConfigControlPlaneHoneytokens,
		Verify:           verifyConfigControlPlaneHoneytokens,
	})
}

func applyConfigControlPlaneHoneytokens(tx *gorm.DB) error {
	for _, column := range []string{"HoneytokenLabel", "HoneytokenPlantedAt"} {
		if tx.Migrator().HasColumn(&callerRow{}, column) {
			continue
		}
		if err := tx.Migrator().AddColumn(&callerRow{}, column); err != nil {
			return fmt.Errorf("add caller honeytoken metadata: %w", err)
		}
	}
	return nil
}

func verifyConfigControlPlaneHoneytokens(tx *gorm.DB) error {
	for _, column := range []string{"honeytoken_label", "honeytoken_planted_at"} {
		if !tx.Migrator().HasColumn("router_config_callers", column) {
			return fmt.Errorf("required control-plane column router_config_callers.%s is missing", column)
		}
	}
	return nil
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"gorm.io/gorm"
)

func configEmbeddingMigration() MigrationDefinition {
	return MigrationDefinition{ID: 2026103002, Scope: configControlPlaneScope, Name: "preserve embedding target configuration", Release: "2026.10", Checksum: MigrationChecksum("router-config.embedding-targets.v1"), SchemaVersion: 10, Transactional: true, MaintenanceMode: "online", RollbackClass: "restore-required", HandlerKey: "router-config.embedding-targets.apply.v1@applyConfigEmbeddingMigration", PostconditionKey: "router-config.embedding-targets.schema.v1@verifyConfigEmbeddingMigration", Dependencies: []int{configControlPlanePhase9MigrationID}, ExecutionMode: "transactional", LockClass: "online", TimeoutClass: "bounded", Apply: applyConfigEmbeddingMigration, Verify: verifyConfigEmbeddingMigration}
}

func applyConfigEmbeddingMigration(db *gorm.DB) error {
	for _, column := range []string{"EmbeddingSpace", "EmbeddingDimensions"} {
		if !db.Migrator().HasColumn(&modelGroupTargetRow{}, column) {
			if err := db.Migrator().AddColumn(&modelGroupTargetRow{}, column); err != nil {
				return err
			}
		}
	}
	if db.Dialector.Name() == "postgres" {
		for _, statement := range []string{
			"COMMENT ON COLUMN router_config_model_group_targets.embedding_space IS 'Embedding compatibility space; empty for non-embedding targets.'",
			"COMMENT ON COLUMN router_config_model_group_targets.embedding_dimensions IS 'JSON array of explicitly allowed embedding dimensions.'",
		} {
			if err := db.Exec(statement).Error; err != nil {
				return err
			}
		}
	}
	return verifyConfigEmbeddingMigration(db)
}

func verifyConfigEmbeddingMigration(db *gorm.DB) error {
	if err := verifyConfigControlPlanePhase9(db); err != nil {
		return err
	}
	for _, column := range []string{"EmbeddingSpace", "EmbeddingDimensions"} {
		if !db.Migrator().HasColumn(&modelGroupTargetRow{}, column) {
			return fmt.Errorf("missing embedding target %s column", column)
		}
	}
	return nil
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"errors"
	"gorm.io/gorm"
)

func configResponsesStoreMigration() MigrationDefinition {
	return MigrationDefinition{ID: 2026103004, Scope: configControlPlaneScope, Name: "retain stored response retention configuration", Release: "2026.10", Checksum: MigrationChecksum("router-config.response-store-retention.v1"), SchemaVersion: 11, Transactional: true, MaintenanceMode: "online", RollbackClass: "restore-required", HandlerKey: "router-config.response-store.apply.v1@applyConfigResponsesStoreMigration", PostconditionKey: "router-config.response-store.schema.v1@verifyConfigResponsesStoreMigration", Dependencies: []int{2026103002}, ExecutionMode: "transactional", LockClass: "online", TimeoutClass: "bounded", Apply: applyConfigResponsesStoreMigration, Verify: verifyConfigResponsesStoreMigration}
}

func applyConfigResponsesStoreMigration(db *gorm.DB) error {
	if !db.Migrator().HasColumn(&serverConfigRow{}, "ResponsesStoreRetention") {
		if err := db.Migrator().AddColumn(&serverConfigRow{}, "ResponsesStoreRetention"); err != nil {
			return err
		}
	}
	if db.Dialector.Name() == "postgres" {
		if err := db.Exec("COMMENT ON COLUMN router_config_server.responses_store_retention IS 'Stored response affinity retention duration.'").Error; err != nil {
			return err
		}
	}
	return verifyConfigResponsesStoreMigration(db)
}

func verifyConfigResponsesStoreMigration(db *gorm.DB) error {
	if err := verifyConfigEmbeddingMigration(db); err != nil {
		return err
	}
	if !db.Migrator().HasColumn(&serverConfigRow{}, "ResponsesStoreRetention") {
		return errors.New("missing stored response retention configuration column")
	}
	return nil
}

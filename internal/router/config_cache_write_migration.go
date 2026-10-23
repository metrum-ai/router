// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"gorm.io/gorm"
)

func configCacheWriteMigration() MigrationDefinition {
	return MigrationDefinition{ID: 2026102301, Scope: configControlPlaneScope, Name: "retain cache write catalog pricing", Release: "2026.10", Checksum: MigrationChecksum("router-config.cache-write-price.v1"), SchemaVersion: 7, Transactional: true, MaintenanceMode: "online", RollbackClass: "restore-required", HandlerKey: "router-config.cache-write.apply.v1@applyConfigCacheWriteMigration", PostconditionKey: "router-config.cache-write.schema.v1@verifyConfigCacheWriteMigration", Dependencies: []int{configControlPlanePhase6MigrationID}, ExecutionMode: "transactional", LockClass: "online", TimeoutClass: "bounded", Apply: applyConfigCacheWriteMigration, Verify: verifyConfigCacheWriteMigration}
}
func applyConfigCacheWriteMigration(db *gorm.DB) error {
	if !db.Migrator().HasColumn(&providerModelRow{}, "CacheWritePricePerMillionUSD") {
		if err := db.Migrator().AddColumn(&providerModelRow{}, "CacheWritePricePerMillionUSD"); err != nil {
			return err
		}
	}
	if db.Dialector.Name() == "postgres" {
		if err := db.Exec("COMMENT ON COLUMN router_config_provider_models.cache_write_price_per_million_usd IS 'Optional cache-write input price per million tokens; NULL means unknown.'").Error; err != nil {
			return err
		}
	}
	return verifyConfigCacheWriteMigration(db)
}
func verifyConfigCacheWriteMigration(db *gorm.DB) error {
	if err := verifyConfigControlPlanePhase6(db); err != nil {
		return err
	}
	if !db.Migrator().HasColumn(&providerModelRow{}, "CacheWritePricePerMillionUSD") {
		return fmt.Errorf("missing cache write catalog pricing column")
	}
	return nil
}

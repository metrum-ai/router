// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"errors"
	"gorm.io/gorm"
)

const usageMissionAttributionMigrationID = 2026111607

var usageMissionAttributionColumns = map[string]map[string]struct{}{
	"request_usage": {"task_id": {}, "human_owner": {}},
}

func applyUsageMissionAttributionMigration(db *gorm.DB) error {
	if db == nil {
		return errors.New("mission attribution migration requires database")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if !tx.Migrator().HasColumn(&usageRecord{}, "TaskID") {
			if err := tx.Migrator().AddColumn(&usageRecord{}, "TaskID"); err != nil {
				return err
			}
		}
		if !tx.Migrator().HasColumn(&usageRecord{}, "HumanOwner") {
			if err := tx.Migrator().AddColumn(&usageRecord{}, "HumanOwner"); err != nil {
				return err
			}
		}
		if !tx.Migrator().HasIndex(&usageRecord{}, "idx_request_usage_task") {
			if err := tx.Migrator().CreateIndex(&usageRecord{}, "TaskID"); err != nil {
				return err
			}
		}
		if !tx.Migrator().HasIndex(&usageRecord{}, "idx_request_usage_owner") {
			if err := tx.Migrator().CreateIndex(&usageRecord{}, "HumanOwner"); err != nil {
				return err
			}
		}
		return verifyUsageMissionAttributionMigration(tx)
	})
}

func verifyUsageMissionAttributionMigration(db *gorm.DB) error {
	if db == nil {
		return errors.New("mission attribution migration requires database")
	}
	for _, column := range []string{"TaskID", "HumanOwner"} {
		if !db.Migrator().HasColumn(&usageRecord{}, column) {
			return errors.New("mission attribution column missing")
		}
	}
	return nil
}

func UsageMissionAttributionMigrationDefinition() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{ID: usageMissionAttributionMigrationID, Scope: usageMigrationScope, Name: "add mission task and owner attribution", Release: "2026.11", Checksum: MigrationChecksum("usage.mission-attribution.v1"), SchemaVersion: 19, DataVersion: 0, Transactional: true, MaintenanceMode: "online", RollbackClass: "restore-required", HandlerKey: "usage.mission-attribution.apply.v1@applyUsageMissionAttributionMigration", PostconditionKey: "usage.mission-attribution.schema.v1@verifyUsageMissionAttributionMigration", Dependencies: []int{usageTraceCorrelationMigrationID}, ExecutionMode: "transactional", LockClass: "online", TimeoutClass: "bounded", Apply: applyUsageMissionAttributionMigration, Verify: verifyUsageMissionAttributionMigration})
}

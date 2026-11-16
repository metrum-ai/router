// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"

	"gorm.io/gorm"
)

const offlineOperationMarkerMigrationID = 2026111627

func offlineOperationMarkerControlMigrationDefinition() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{
		ID: offlineOperationMarkerMigrationID, Scope: configControlPlaneScope,
		Name: "offline operation audit markers", Release: "2026.11",
		Checksum: MigrationChecksum("router-config.offline-operation-markers.v2"), SchemaVersion: 19,
		Transactional: true, MaintenanceMode: "online", RollbackClass: "restore-required",
		HandlerKey:       "router-config.offline-markers.apply.v1@applyOfflineOperationMarkerMigration",
		PostconditionKey: "router-config.offline-markers.verify.v1@verifyOfflineOperationMarkerMigration",
		Dependencies:     []int{2026111611}, ExecutionMode: "transactional", LockClass: "online", TimeoutClass: "bounded",
		Apply: applyOfflineOperationMarkerMigration, Verify: verifyOfflineOperationMarkerMigration,
	})
}

func offlineOperationMarkerUsageMigrationDefinition() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{
		ID: offlineOperationMarkerMigrationID, Scope: usageMigrationScope,
		Name: "offline operation audit markers", Release: "2026.11",
		Checksum: MigrationChecksum("usage.offline-operation-markers.v2"), SchemaVersion: 23,
		Transactional: true, MaintenanceMode: "online", RollbackClass: "restore-required",
		HandlerKey:       "usage.offline-markers.apply.v1@applyOfflineOperationMarkerMigration",
		PostconditionKey: "usage.offline-markers.verify.v1@verifyOfflineOperationMarkerMigration",
		Dependencies:     []int{usageHierarchyAuditMigrationID}, ExecutionMode: "transactional", LockClass: "online", TimeoutClass: "bounded",
		Apply: applyOfflineOperationMarkerMigration, Verify: verifyOfflineOperationMarkerMigration,
	})
}

func applyOfflineOperationMarkerMigration(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("offline operation marker migration requires a database")
	}
	if !db.Migrator().HasTable(&offlineMaintenanceRecord{}) {
		if err := db.Migrator().CreateTable(&offlineMaintenanceRecord{}); err != nil {
			return err
		}
	}
	return verifyOfflineOperationMarkerMigration(db)
}

func verifyOfflineOperationMarkerMigration(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(&offlineMaintenanceRecord{}) {
		return fmt.Errorf("offline operation marker table is missing")
	}
	for _, column := range []string{"id", "kind", "command", "revision", "actor", "runtime_scope", "at"} {
		if !db.Migrator().HasColumn(&offlineMaintenanceRecord{}, column) {
			return fmt.Errorf("offline operation marker column %s is missing", column)
		}
	}
	return nil
}

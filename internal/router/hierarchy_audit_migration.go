// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"

	"gorm.io/gorm"
)

const usageHierarchyAuditMigrationID = 2026111626
const usageHierarchyAuditIndexName = "idx_request_usage_hierarchy_audit"

var usageHierarchyAuditColumns = map[string]map[string]struct{}{
	"request_usage": {
		"organization_id": {},
		"team_id":         {},
	},
}

func HierarchyAuditMigrationDefinition() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{
		ID: usageHierarchyAuditMigrationID, Scope: usageMigrationScope,
		Name: "add hierarchy audit attribution", Release: "2026.11",
		Checksum: MigrationChecksum("usage.hierarchy-audit.v1"), SchemaVersion: 22,
		DataVersion: 0, Transactional: true, MaintenanceMode: "online", RollbackClass: "restore-required",
		HandlerKey:       "usage.hierarchy-audit.apply.v1@applyHierarchyAuditMigration",
		PostconditionKey: "usage.hierarchy-audit.schema.v1@verifyHierarchyAuditMigration",
		Dependencies:     []int{toolRoutingUsageMigrationID}, ExecutionMode: "transactional",
		LockClass: "online", TimeoutClass: "bounded",
		Apply: applyHierarchyAuditMigration, Verify: verifyHierarchyAuditMigration,
	})
}

func applyHierarchyAuditMigration(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("hierarchy audit migration requires database")
	}
	for _, field := range []string{"OrganizationID", "TeamID"} {
		if !db.Migrator().HasColumn(&usageRecord{}, field) {
			if err := db.Migrator().AddColumn(&usageRecord{}, field); err != nil {
				return fmt.Errorf("add hierarchy audit column %s: %w", field, err)
			}
		}
	}
	if err := db.Exec("CREATE INDEX IF NOT EXISTS " + usageHierarchyAuditIndexName + " ON request_usage(organization_id, team_id, span_id)").Error; err != nil {
		return fmt.Errorf("create hierarchy audit index: %w", err)
	}
	return verifyHierarchyAuditMigration(db)
}

func verifyHierarchyAuditMigration(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("hierarchy audit migration requires database")
	}
	for _, field := range []string{"OrganizationID", "TeamID"} {
		if !db.Migrator().HasColumn(&usageRecord{}, field) {
			return fmt.Errorf("hierarchy audit column %s is missing", field)
		}
	}
	indexes, err := db.Migrator().GetIndexes(&usageRecord{})
	if err != nil {
		return fmt.Errorf("inspect hierarchy audit index: %w", err)
	}
	for _, index := range indexes {
		if index.Name() == usageHierarchyAuditIndexName {
			columns := index.Columns()
			if len(columns) == 3 && columns[0] == "organization_id" && columns[1] == "team_id" && columns[2] == "span_id" {
				return nil
			}
			return fmt.Errorf("hierarchy audit index has unexpected columns")
		}
	}
	return fmt.Errorf("hierarchy audit index is missing")
}

func revertHierarchyAuditMigration(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("hierarchy audit migration requires database")
	}
	if err := db.Exec("DROP INDEX IF EXISTS " + usageHierarchyAuditIndexName).Error; err != nil {
		return fmt.Errorf("drop hierarchy audit index: %w", err)
	}
	for _, field := range []string{"TeamID", "OrganizationID"} {
		if db.Migrator().HasColumn(&usageRecord{}, field) {
			var err error
			if db.Dialector.Name() == "sqlite" {
				name := "team_id"
				if field == "OrganizationID" {
					name = "organization_id"
				}
				err = db.Exec("ALTER TABLE request_usage DROP COLUMN " + name).Error
			} else {
				err = db.Migrator().DropColumn(&usageRecord{}, field)
			}
			if err != nil {
				return fmt.Errorf("drop hierarchy audit column %s: %w", field, err)
			}
		}
	}
	return verifyTraceCorrelationMigration(db)
}

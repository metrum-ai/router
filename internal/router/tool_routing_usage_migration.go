// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"

	"gorm.io/gorm"
)

const toolRoutingUsageMigrationID = 2026111623

var usageToolRoutingColumns = map[string]map[string]struct{}{
	"request_usage": {
		"tool_call_count":          {},
		"tool_first_failure_class": {},
		"tool_retries":             {},
		"tool_escalations":         {},
		"tool_reliability_bucket":  {},
		"tool_reliability_source":  {},
	},
}

func ToolRoutingUsageMigrationDefinition() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{
		ID: toolRoutingUsageMigrationID, Scope: usageMigrationScope,
		Name: "add tool routing usage fields", Release: "2026.11",
		Checksum: MigrationChecksum("usage.tool-routing-fields.v1"), SchemaVersion: 21,
		DataVersion: 0, Transactional: true, MaintenanceMode: "online", RollbackClass: "restore-required",
		HandlerKey:       "usage.tool-routing.apply.v1@applyToolRoutingUsageMigration",
		PostconditionKey: "usage.tool-routing.schema.v1@verifyToolRoutingUsageMigration",
		Dependencies:     []int{usageRecomputedInputMigrationID}, ExecutionMode: "transactional", LockClass: "online", TimeoutClass: "bounded",
		Apply: applyToolRoutingUsageMigration, Verify: verifyToolRoutingUsageMigration,
	})
}

func applyToolRoutingUsageMigration(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("tool routing usage migration requires database")
	}
	for _, field := range []string{"ToolCallCount", "ToolFirstFailureClass", "ToolRetries", "ToolEscalations", "ToolReliabilityBucket", "ToolReliabilitySource"} {
		if !db.Migrator().HasColumn(&usageRecord{}, field) {
			if err := db.Migrator().AddColumn(&usageRecord{}, field); err != nil {
				return fmt.Errorf("add tool routing usage column %s: %w", field, err)
			}
		}
	}
	return verifyToolRoutingUsageMigration(db)
}

func verifyToolRoutingUsageMigration(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("tool routing usage migration requires database")
	}
	for _, field := range []string{"ToolCallCount", "ToolFirstFailureClass", "ToolRetries", "ToolEscalations", "ToolReliabilityBucket", "ToolReliabilitySource"} {
		if !db.Migrator().HasColumn(&usageRecord{}, field) {
			return fmt.Errorf("tool routing usage column %s is missing", field)
		}
	}
	return nil
}

func revertToolRoutingUsageMigration(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("tool routing usage migration requires database")
	}
	columns := []struct{ field, name string }{
		{"ToolReliabilitySource", "tool_reliability_source"},
		{"ToolReliabilityBucket", "tool_reliability_bucket"},
		{"ToolEscalations", "tool_escalations"},
		{"ToolRetries", "tool_retries"},
		{"ToolFirstFailureClass", "tool_first_failure_class"},
		{"ToolCallCount", "tool_call_count"},
	}
	for _, column := range columns {
		if !db.Migrator().HasColumn(&usageRecord{}, column.field) {
			continue
		}
		var err error
		if db.Dialector.Name() == "sqlite" {
			err = db.Exec("ALTER TABLE request_usage DROP COLUMN " + column.name).Error
		} else {
			err = db.Migrator().DropColumn(&usageRecord{}, column.field)
		}
		if err != nil {
			return fmt.Errorf("drop tool routing usage column %s: %w", column.name, err)
		}
	}
	return verifyUsageEndpointMigration(db)
}

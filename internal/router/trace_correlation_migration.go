// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"

	"gorm.io/gorm"
)

const usageTraceCorrelationMigrationID = 2026111603
const usageTraceCorrelationIndexName = "idx_request_usage_trace"

var usageTraceCorrelationColumns = map[string]map[string]struct{}{
	"request_usage": {
		"trace_id":       {},
		"span_id":        {},
		"parent_span_id": {},
	},
}

func TraceCorrelationMigrationDefinition() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{
		ID:               usageTraceCorrelationMigrationID,
		Scope:            usageMigrationScope,
		Name:             "add request trace correlation metadata",
		Release:          "2026.11",
		Checksum:         MigrationChecksum("usage.trace-correlation.v1"),
		SchemaVersion:    18,
		DataVersion:      0,
		Transactional:    true,
		MaintenanceMode:  "online",
		RollbackClass:    "restore-required",
		HandlerKey:       "usage.trace-correlation.apply.v1@applyTraceCorrelationMigration",
		PostconditionKey: "usage.trace-correlation.schema.v1@verifyTraceCorrelationMigration",
		Dependencies:     []int{policyRuntimeMigrationID},
		ExecutionMode:    "transactional",
		LockClass:        "online",
		TimeoutClass:     "bounded",
		Apply:            applyTraceCorrelationMigration,
		Verify:           verifyTraceCorrelationMigration,
	})
}

func applyTraceCorrelationMigration(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("trace correlation migration requires database")
	}
	for _, field := range []string{"TraceID", "SpanID", "ParentSpanID"} {
		if !db.Migrator().HasColumn(&usageRecord{}, field) {
			if err := db.Migrator().AddColumn(&usageRecord{}, field); err != nil {
				return fmt.Errorf("add trace correlation column %s: %w", field, err)
			}
		}
	}
	if err := db.Exec("CREATE INDEX IF NOT EXISTS " + usageTraceCorrelationIndexName + " ON request_usage(trace_id)").Error; err != nil {
		return fmt.Errorf("create trace correlation index: %w", err)
	}
	return verifyTraceCorrelationMigration(db)
}

func verifyTraceCorrelationMigration(db *gorm.DB) error {
	if err := ensureUsageRelationalSchema(db); err != nil {
		return err
	}
	indexes, err := db.Migrator().GetIndexes(&usageRecord{})
	if err != nil {
		return fmt.Errorf("inspect trace correlation index: %w", err)
	}
	for _, index := range indexes {
		if index.Name() == usageTraceCorrelationIndexName {
			columns := index.Columns()
			if len(columns) == 1 && columns[0] == "trace_id" {
				return nil
			}
			return fmt.Errorf("trace correlation index has unexpected columns")
		}
	}
	return fmt.Errorf("trace correlation index is missing")
}

func revertTraceCorrelationMigration(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("trace correlation migration requires database")
	}
	if err := db.Exec("DROP INDEX IF EXISTS " + usageTraceCorrelationIndexName).Error; err != nil {
		return fmt.Errorf("drop trace correlation index: %w", err)
	}
	for _, column := range []struct{ field, name string }{{"ParentSpanID", "parent_span_id"}, {"SpanID", "span_id"}, {"TraceID", "trace_id"}} {
		if db.Migrator().HasColumn(&usageRecord{}, column.field) {
			var err error
			if db.Dialector.Name() == "sqlite" {
				err = db.Exec("ALTER TABLE request_usage DROP COLUMN " + column.name).Error
			} else {
				err = db.Migrator().DropColumn(&usageRecord{}, column.field)
			}
			if err != nil {
				return fmt.Errorf("drop trace correlation column %s: %w", column.field, err)
			}
		}
	}
	return verifyPolicyRuntimeMigration(db)
}

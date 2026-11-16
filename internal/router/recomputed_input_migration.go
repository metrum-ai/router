// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"

	"gorm.io/gorm"
)

func usageRecomputedInputMigrationDefinition() MigrationDefinition {
	return MigrationDefinition{
		ID: 2026111602, Scope: usageMigrationScope, Name: "record recomputed prompt tokens", Release: "2026.11",
		Checksum: MigrationChecksum("usage.recomputed-input.v1"), SchemaVersion: 17, DataVersion: 0,
		Transactional: true, MaintenanceMode: "online", RollbackClass: "restore-required",
		HandlerKey:       "usage.recomputed-input.apply.v1@applyUsageRecomputedInputMigration",
		PostconditionKey: "usage.recomputed-input.schema.v1@verifyUsageRecomputedInputMigration",
		Dependencies:     []int{automaticContainmentMigrationID}, ExecutionMode: "transactional", LockClass: "online", TimeoutClass: "bounded",
		Apply: applyUsageRecomputedInputMigration, Verify: verifyUsageRecomputedInputMigration,
	}
}

func applyUsageRecomputedInputMigration(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("recomputed-input migration requires database")
	}
	if !db.Migrator().HasColumn(&usageRecord{}, "RecomputedInputTokens") {
		if err := db.Migrator().AddColumn(&usageRecord{}, "RecomputedInputTokens"); err != nil {
			return fmt.Errorf("add recomputed input tokens: %w", err)
		}
	}
	for _, model := range []any{&usageRollupDailyRecord{}, &usageRollupHourlyRecord{}, &usageRollupMonthlyBillingRecord{}} {
		for _, field := range []string{"RecomputedInputTokens", "RecomputedInputReportedRequests"} {
			if !db.Migrator().HasColumn(model, field) {
				if err := db.Migrator().AddColumn(model, field); err != nil {
					return fmt.Errorf("add recomputed input rollup column %s: %w", field, err)
				}
			}
		}
	}
	if err := db.Exec(`UPDATE request_usage SET recomputed_input_tokens = CASE WHEN cached_input_tokens < 0 THEN input_tokens WHEN input_tokens >= cached_input_tokens THEN input_tokens - cached_input_tokens ELSE 0 END WHERE cached_input_tokens IS NOT NULL AND recomputed_input_tokens IS NULL`).Error; err != nil {
		return fmt.Errorf("backfill recomputed input tokens: %w", err)
	}
	return verifyUsageRecomputedInputMigration(db)
}

func verifyUsageRecomputedInputMigration(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasColumn(&usageRecord{}, "RecomputedInputTokens") {
		return fmt.Errorf("recomputed input token column is missing")
	}
	for _, model := range []any{&usageRollupDailyRecord{}, &usageRollupHourlyRecord{}, &usageRollupMonthlyBillingRecord{}} {
		for _, field := range []string{"RecomputedInputTokens", "RecomputedInputReportedRequests"} {
			if !db.Migrator().HasColumn(model, field) {
				return fmt.Errorf("recomputed input rollup column %s is missing", field)
			}
		}
	}
	return ensureUsageRelationalSchemaExcept(db, mergeUsageExcludedColumns())
}

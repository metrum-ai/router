// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
)

func clearTraceCorrelationMigrationLedger(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Where("scope = ? AND migration_id = ?", usageMigrationScope, usageTraceCorrelationMigrationID).Delete(&migrationAttemptRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("scope = ? AND migration_id = ?", usageMigrationScope, usageTraceCorrelationMigrationID).Delete(&migrationLedgerRecord{}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestTraceCorrelationSchema18SQLiteUpgradeDownAndRowRoundTrip(t *testing.T) {
	cfg := uncachedSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	db, err := openUsageDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	definitionsThrough17 := make([]MigrationDefinition, 0, len(usageMigrationDefinitions)-2)
	for _, definition := range usageMigrationDefinitions {
		if definition.SchemaVersion <= 17 {
			definitionsThrough17 = append(definitionsThrough17, definition)
		}
	}
	runner, err := NewMigrationRunnerWithDataJobs(db, usageMigrationScope, MigrationCompatibility{MinSchema: 0, MaxSchema: 17, MinData: 0, MaxData: 1}, definitionsThrough17, usageDataJobDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.ApplyPending("trace-correlation-schema-test"); err != nil {
		t.Fatal(err)
	}
	legacyRow := recordFromRow(rowFromRecord(logRecord{RequestID: "legacy-trace-row", TS: time.Now().UTC().Format(time.RFC3339)}))
	if err := db.Omit("TraceID", "SpanID", "ParentSpanID", "TaskID", "HumanOwner", "RecomputedInputTokens", "OrganizationID", "TeamID", "ToolCallCount", "ToolFirstFailureClass", "ToolRetries", "ToolEscalations", "ToolReliabilityBucket", "ToolReliabilitySource").Create(legacyRow).Error; err != nil {
		t.Fatal(err)
	}
	definitionsThrough18 := make([]MigrationDefinition, 0, len(usageMigrationDefinitions)-1)
	for _, definition := range usageMigrationDefinitions {
		if definition.SchemaVersion <= 18 {
			definitionsThrough18 = append(definitionsThrough18, definition)
		}
	}
	runner, err = NewMigrationRunnerWithDataJobs(runner.db, usageMigrationScope, MigrationCompatibility{MinSchema: 0, MaxSchema: 18, MinData: 0, MaxData: 1}, definitionsThrough18, usageDataJobDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	before, err := runner.Status()
	if err != nil || before.SchemaVersion != 17 {
		t.Fatalf("before: %+v err=%v", before, err)
	}
	if err := runner.ApplyPending("trace-correlation-schema-test"); err != nil {
		t.Fatal(err)
	}
	if err := verifyTraceCorrelationMigration(runner.db); err != nil {
		t.Fatal(err)
	}
	status, err := runner.Status()
	if err != nil || status.SchemaVersion != 18 || !status.Compatible {
		t.Fatalf("after: %+v err=%v", status, err)
	}
	var legacy usageRecord
	if err := runner.db.First(&legacy, "request_id = ?", "legacy-trace-row").Error; err != nil || legacy.TraceID != "" || legacy.SpanID != "" || legacy.ParentSpanID != "" {
		t.Fatalf("legacy row=%+v err=%v", legacy, err)
	}
	row := recordFromRow(rowFromRecord(logRecord{RequestID: "traced-row", TS: time.Now().UTC().Format(time.RFC3339), TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", SpanID: "a1b2c3d4e5f60718", ParentSpanID: "00f067aa0ba902b7"}))
	if err := runner.db.Omit("TaskID", "HumanOwner", "RecomputedInputTokens", "OrganizationID", "TeamID", "ToolCallCount", "ToolFirstFailureClass", "ToolRetries", "ToolEscalations", "ToolReliabilityBucket", "ToolReliabilitySource").Create(row).Error; err != nil {
		t.Fatal(err)
	}
	var got usageRecord
	if err := runner.db.First(&got, "request_id = ?", "traced-row").Error; err != nil || got.TraceID != row.TraceID || got.SpanID != row.SpanID || got.ParentSpanID != row.ParentSpanID {
		t.Fatalf("trace row=%+v want=%+v err=%v", got, row, err)
	}

	previousDefinitions := make([]MigrationDefinition, 0, len(usageMigrationDefinitions)-2)
	for _, definition := range usageMigrationDefinitions {
		if definition.SchemaVersion <= 17 {
			previousDefinitions = append(previousDefinitions, definition)
		}
	}
	old, err := NewMigrationRunnerWithDataJobs(runner.db, usageMigrationScope, MigrationCompatibility{MinSchema: 0, MaxSchema: 17, MinData: 0, MaxData: 1}, previousDefinitions, usageDataJobDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	oldStatus, err := old.Status()
	if err != nil || oldStatus.Compatible || oldStatus.State != "incompatible" {
		t.Fatalf("schema 17 binary should reject schema 18: %+v err=%v", oldStatus, err)
	}
	if err := revertTraceCorrelationMigration(runner.db); err != nil {
		t.Fatal(err)
	}
	clearTraceCorrelationMigrationLedger(t, runner.db)
	afterDown, err := runner.Status()
	if err != nil || afterDown.SchemaVersion != 17 || !afterDown.Compatible {
		t.Fatalf("after down: %+v err=%v", afterDown, err)
	}
	oldStatus, err = old.Status()
	if err != nil || !oldStatus.Compatible || oldStatus.SchemaVersion != 17 {
		t.Fatalf("schema 17 binary should accept reversed database: %+v err=%v", oldStatus, err)
	}
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
}

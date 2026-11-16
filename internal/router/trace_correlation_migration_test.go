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

func clearHierarchyAuditMigrationLedger(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Where("scope = ? AND migration_id = ?", usageMigrationScope, usageHierarchyAuditMigrationID).Delete(&migrationAttemptRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("scope = ? AND migration_id = ?", usageMigrationScope, usageHierarchyAuditMigrationID).Delete(&migrationLedgerRecord{}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestTraceCorrelationAndHierarchyAuditSchema20SQLiteUpgradeDownAndRowRoundTrip(t *testing.T) {
	cfg := uncachedSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	store, err := OpenUsageStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(recordFromRow(rowFromRecord(logRecord{RequestID: "legacy-trace-row", TS: time.Now().UTC().Format(time.RFC3339)}))).Error; err != nil {
		t.Fatal(err)
	}
	if err := revertToolRoutingUsageMigration(store.db); err != nil {
		t.Fatal(err)
	}
	clearToolRoutingUsageLedger(t, store.db)
	if err := revertHierarchyAuditMigration(store.db); err != nil {
		t.Fatal(err)
	}
	clearHierarchyAuditMigrationLedger(t, store.db)
	if err := revertTraceCorrelationMigration(store.db); err != nil {
		t.Fatal(err)
	}
	clearTraceCorrelationMigrationLedger(t, store.db)
	if err := verifySecurityPostureReportMigration(store.db); err != nil {
		t.Fatalf("schema 17 postcondition: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	runner, closeDB, err := UsageMigrationRunner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
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
	if err != nil || status.SchemaVersion != 20 || !status.Compatible {
		t.Fatalf("after: %+v err=%v", status, err)
	}
	var legacy usageRecord
	if err := runner.db.First(&legacy, "request_id = ?", "legacy-trace-row").Error; err != nil || legacy.TraceID != "" || legacy.SpanID != "" || legacy.ParentSpanID != "" {
		t.Fatalf("legacy row=%+v err=%v", legacy, err)
	}
	row := recordFromRow(rowFromRecord(logRecord{RequestID: "traced-row", TS: time.Now().UTC().Format(time.RFC3339), TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", SpanID: "a1b2c3d4e5f60718", ParentSpanID: "00f067aa0ba902b7"}))
	if err := runner.db.Create(row).Error; err != nil {
		t.Fatal(err)
	}
	var got usageRecord
	if err := runner.db.First(&got, "request_id = ?", "traced-row").Error; err != nil || got.TraceID != row.TraceID || got.SpanID != row.SpanID || got.ParentSpanID != row.ParentSpanID {
		t.Fatalf("trace row=%+v want=%+v err=%v", got, row, err)
	}

	previousDefinitions := make([]MigrationDefinition, 0, len(usageMigrationDefinitions)-3)
	for _, definition := range usageMigrationDefinitions {
		if definition.ID != usageTraceCorrelationMigrationID && definition.ID != usageHierarchyAuditMigrationID && definition.ID != toolRoutingUsageMigrationID {
			previousDefinitions = append(previousDefinitions, definition)
		}
	}
	old, err := NewMigrationRunnerWithDataJobs(runner.db, usageMigrationScope, MigrationCompatibility{MinSchema: 0, MaxSchema: 17, MinData: 0, MaxData: 1}, previousDefinitions, usageDataJobDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	oldStatus, err := old.Status()
	if err != nil || oldStatus.Compatible || oldStatus.State != "incompatible" {
		t.Fatalf("schema 17 binary should reject schema 20: %+v err=%v", oldStatus, err)
	}
	if err := revertToolRoutingUsageMigration(runner.db); err != nil {
		t.Fatal(err)
	}
	clearToolRoutingUsageLedger(t, runner.db)
	if err := revertHierarchyAuditMigration(runner.db); err != nil {
		t.Fatal(err)
	}
	clearHierarchyAuditMigrationLedger(t, runner.db)
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
}

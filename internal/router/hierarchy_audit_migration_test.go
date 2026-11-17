// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
)

func hierarchyAuditMigrationTestRunner(cfg UsageDBConfig) (*migrationRunner, func() error, error) {
	db, err := openUsageDB(cfg)
	if err != nil {
		return nil, nil, err
	}
	definitions := make([]MigrationDefinition, 0, len(usageMigrationDefinitions)-2)
	for _, definition := range usageMigrationDefinitions {
		if definition.ID != offlineOperationMarkerMigrationID && definition.ID != adminAuditChainUsageMigrationID && definition.ID != adminCredentialAuditCounterMigrationID {
			definitions = append(definitions, definition)
		}
	}
	runner, err := NewMigrationRunnerWithDataJobs(db, usageMigrationScope, MigrationCompatibility{MinSchema: 0, MaxSchema: 22, MinData: 0, MaxData: 1}, definitions, usageDataJobDefinitions)
	if err != nil {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		return nil, nil, err
	}
	closeDB := func() error {
		sqlDB, err := db.DB()
		if err != nil {
			return err
		}
		return sqlDB.Close()
	}
	return runner, closeDB, nil
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

func clearAdminAuditChainMigrationLedger(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Where("scope = ? AND migration_id = ?", usageMigrationScope, adminAuditChainUsageMigrationID).Delete(&migrationAttemptRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("scope = ? AND migration_id = ?", usageMigrationScope, adminAuditChainUsageMigrationID).Delete(&migrationLedgerRecord{}).Error; err != nil {
		t.Fatal(err)
	}
}

func clearAdminCredentialAuditCounterMigrationLedger(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Where("scope = ? AND migration_id = ?", usageMigrationScope, adminCredentialAuditCounterMigrationID).Delete(&migrationAttemptRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("scope = ? AND migration_id = ?", usageMigrationScope, adminCredentialAuditCounterMigrationID).Delete(&migrationLedgerRecord{}).Error; err != nil {
		t.Fatal(err)
	}
}

func clearOfflineOperationMarkerUsageMigration(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Where("scope = ? AND migration_id = ?", usageMigrationScope, offlineOperationMarkerMigrationID).Delete(&migrationAttemptRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("scope = ? AND migration_id = ?", usageMigrationScope, offlineOperationMarkerMigrationID).Delete(&migrationLedgerRecord{}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestHierarchyAuditSchema22SQLiteUpgradeDownAndRowRoundTrip(t *testing.T) {
	cfg := uncachedSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	store, err := OpenUsageStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	legacy := recordFromRow(rowFromRecord(logRecord{RequestID: "legacy-hierarchy-row", TS: time.Now().UTC().Format(time.RFC3339)}))
	if err := store.db.Create(legacy).Error; err != nil {
		t.Fatal(err)
	}
	clearOfflineOperationMarkerUsageMigration(t, store.db)
	clearAdminAuditChainMigrationLedger(t, store.db)
	clearAdminCredentialAuditCounterMigrationLedger(t, store.db)
	if err := revertHierarchyAuditMigration(store.db); err != nil {
		t.Fatal(err)
	}
	clearHierarchyAuditMigrationLedger(t, store.db)
	clearAdminAuditChainMigrationLedger(t, store.db)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	runner, closeDB, err := hierarchyAuditMigrationTestRunner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	before, err := runner.Status()
	if err != nil || before.SchemaVersion != 21 || !before.Compatible {
		t.Fatalf("before hierarchy migration: %+v err=%v", before, err)
	}
	if err := runner.ApplyPending("hierarchy-schema-test"); err != nil {
		t.Fatal(err)
	}
	if err := verifyHierarchyAuditMigration(runner.db); err != nil {
		t.Fatal(err)
	}
	after, err := runner.Status()
	if err != nil || after.SchemaVersion != 22 || !after.Compatible {
		t.Fatalf("after hierarchy migration: %+v err=%v", after, err)
	}
	var oldRow usageRecord
	if err := runner.db.First(&oldRow, "request_id = ?", "legacy-hierarchy-row").Error; err != nil || oldRow.OrganizationID != "" || oldRow.TeamID != "" {
		t.Fatalf("legacy usage row=%+v err=%v", oldRow, err)
	}
	newRow := recordFromRow(rowFromRecord(logRecord{
		RequestID: "hierarchy-row", TS: time.Now().UTC().Format(time.RFC3339), OrganizationID: "org-a", TeamID: "team-a",
	}))
	if err := runner.db.Create(newRow).Error; err != nil {
		t.Fatal(err)
	}
	var roundTrip usageRecord
	if err := runner.db.First(&roundTrip, "request_id = ?", newRow.RequestID).Error; err != nil || roundTrip.OrganizationID != "org-a" || roundTrip.TeamID != "team-a" {
		t.Fatalf("hierarchy row=%+v err=%v", roundTrip, err)
	}

	priorDefinitions := make([]MigrationDefinition, 0, len(usageMigrationDefinitions)-2)
	for _, definition := range usageMigrationDefinitions {
		if definition.ID != offlineOperationMarkerMigrationID && definition.ID != adminAuditChainUsageMigrationID && definition.ID != adminCredentialAuditCounterMigrationID {
			priorDefinitions = append(priorDefinitions, definition)
		}
	}
	oldBinary, err := NewMigrationRunnerWithDataJobs(runner.db, usageMigrationScope, MigrationCompatibility{MinSchema: 0, MaxSchema: 21, MinData: 0, MaxData: 1}, priorDefinitions, usageDataJobDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	oldStatus, err := oldBinary.Status()
	if err != nil || oldStatus.Compatible || oldStatus.State != "incompatible" {
		t.Fatalf("schema 21 binary should reject schema 22: %+v err=%v", oldStatus, err)
	}
	if err := revertHierarchyAuditMigration(runner.db); err != nil {
		t.Fatal(err)
	}
	clearHierarchyAuditMigrationLedger(t, runner.db)
	clearAdminAuditChainMigrationLedger(t, runner.db)
	afterDown, err := runner.Status()
	if err != nil || afterDown.SchemaVersion != 21 || !afterDown.Compatible {
		t.Fatalf("after down: %+v err=%v", afterDown, err)
	}
}

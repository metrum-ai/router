// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
)

func clearToolRoutingUsageLedger(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Where("scope = ? AND migration_id = ?", usageMigrationScope, toolRoutingUsageMigrationID).Delete(&migrationAttemptRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("scope = ? AND migration_id = ?", usageMigrationScope, toolRoutingUsageMigrationID).Delete(&migrationLedgerRecord{}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestToolRoutingUsageSummaryAndEvidence(t *testing.T) {
	attempts := []attemptLogRecord{
		{ToolValidation: &toolValidationLogRecord{Calls: 1, Invalid: 1, FirstFailure: toolFailureInvalidJSON, Action: "retried"}},
		{Selected: true, ToolValidation: &toolValidationLogRecord{Calls: 1, Valid: 1, Action: "observed"}},
	}
	summary := summarizeToolRoutingUsage(attempts)
	if summary.Calls != 2 || summary.FirstFailureClass != toolFailureInvalidJSON || summary.Retries != 1 || summary.Escalations != 0 || summary.ReliabilityBucket != "valid" || summary.Source != "live" {
		t.Fatalf("summary=%+v", summary)
	}
	row := usageRow{ToolCallCount: summary.Calls, ToolFirstFailureClass: summary.FirstFailureClass, ToolRetries: summary.Retries, ToolEscalations: summary.Escalations, ToolReliabilityBucket: summary.ReliabilityBucket, ToolReliabilitySource: summary.Source}
	evidence := adminEvidenceBundleFrom(row, adminReportRequest{}, nil, "complete", 100, nil, nil, nil, nil)
	if evidence.ToolRouting.Calls != 2 || evidence.ToolRouting.FirstFailureClass != toolFailureInvalidJSON || evidence.ToolRouting.Retries != 1 || evidence.ToolRouting.ReliabilityBucket != "valid" || evidence.ToolRouting.ReliabilitySource != "live" {
		t.Fatalf("evidence=%+v", evidence.ToolRouting)
	}
}

func TestToolRoutingUsageCountsFailedAndTimedOutRecoveryAttempts(t *testing.T) {
	for _, tc := range []struct {
		name       string
		errorClass string
		timedOut   bool
	}{
		{name: "failure", errorClass: "upstream_error"},
		{name: "timeout", errorClass: "timeout", timedOut: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// This matches recoverToolCall's callOne error record: no response
			// means no validation counts, while the recovery action is retained.
			attempts := []attemptLogRecord{
				{ToolValidation: &toolValidationLogRecord{Mode: toolRoutingModeEnforce, Calls: 1, Invalid: 1, Action: "delivered_invalid"}},
				{ErrorClass: tc.errorClass, TimedOut: tc.timedOut, ToolValidation: &toolValidationLogRecord{Mode: toolRoutingModeEnforce, Action: "retried"}},
			}
			summary := summarizeToolRoutingUsage(attempts)
			if summary.Retries != 1 || summary.Escalations != 0 || summary.Calls != 1 {
				t.Fatalf("recovery outcome missing from usage summary: %+v", summary)
			}
		})
	}
}

func TestToolRoutingUsageSchema21UpgradeDownAndRoundTrip(t *testing.T) {
	cfg := uncachedSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	db, err := openUsageDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	// This test isolates the schema-21 tool-routing migration. Later migrations
	// (including the append-only audit-chain migration) are tested separately.
	definitions := make([]MigrationDefinition, 0, len(usageMigrationDefinitions)-3)
	for _, definition := range usageMigrationDefinitions {
		if definition.ID != offlineOperationMarkerMigrationID && definition.ID != adminAuditChainUsageMigrationID && definition.ID != usageHierarchyAuditMigrationID {
			definitions = append(definitions, definition)
		}
	}
	runner, err := NewMigrationRunnerWithDataJobs(db, usageMigrationScope, usageMigrationCompatibility, definitions, usageDataJobDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.ApplyPending("tool-routing-usage-test"); err != nil {
		t.Fatal(err)
	}
	status, err := runner.Status()
	if err != nil || status.SchemaVersion != 21 || !status.Compatible {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if err := verifyToolRoutingUsageMigration(db); err != nil {
		t.Fatal(err)
	}
	when := time.Now().UTC()
	if err := db.Omit("OrganizationID", "TeamID").Create(recordFromRow(usageRow{RequestID: "legacy-tool-row", TS: when})).Error; err != nil {
		t.Fatal(err)
	}
	if err := revertToolRoutingUsageMigration(db); err != nil {
		t.Fatal(err)
	}
	clearToolRoutingUsageLedger(t, db)
	if err := verifyUsageRecomputedInputMigration(db); err != nil {
		t.Fatalf("schema 20 postcondition: %v", err)
	}
	status, err = runner.Status()
	if err != nil || status.SchemaVersion != 20 || !status.Compatible {
		t.Fatalf("down status=%+v err=%v", status, err)
	}
	if err := runner.ApplyPending("tool-routing-usage-test"); err != nil {
		t.Fatal(err)
	}
	if err := verifyToolRoutingUsageMigration(db); err != nil {
		t.Fatal(err)
	}
	status, err = runner.Status()
	if err != nil || status.SchemaVersion != 21 || !status.Compatible {
		t.Fatalf("upgraded status=%+v err=%v", status, err)
	}
	var legacy usageRecord
	if err := db.First(&legacy, "request_id = ?", "legacy-tool-row").Error; err != nil || legacy.ToolCallCount != 0 || legacy.ToolFirstFailureClass != "" || legacy.ToolRetries != 0 || legacy.ToolEscalations != 0 || legacy.ToolReliabilityBucket != "" || legacy.ToolReliabilitySource != "" {
		t.Fatalf("legacy usage row=%+v err=%v", legacy, err)
	}
	rec := logRecord{TS: when.Format(time.RFC3339Nano), RequestID: "tool-routing-row", AttemptsDetail: []attemptLogRecord{
		{ToolValidation: &toolValidationLogRecord{Calls: 2, Invalid: 1, FirstFailure: toolFailureMissingRequired, Action: "retried"}},
		{Selected: true, ToolValidation: &toolValidationLogRecord{Calls: 1, Valid: 1, Action: "escalated"}},
	}}
	row := rowFromRecord(rec)
	if row.ToolCallCount != 3 || row.ToolFirstFailureClass != toolFailureMissingRequired || row.ToolRetries != 1 || row.ToolEscalations != 1 || row.ToolReliabilityBucket != "valid" || row.ToolReliabilitySource != "live" {
		t.Fatalf("row=%+v", row)
	}
	if err := db.Omit("OrganizationID", "TeamID").Create(recordFromRow(row)).Error; err != nil {
		t.Fatal(err)
	}
	var persisted usageRecord
	if err := db.First(&persisted, "request_id = ?", row.RequestID).Error; err != nil {
		t.Fatal(err)
	}
	roundTrip, err := rowFromUsageRecord(persisted)
	if err != nil || roundTrip.ToolCallCount != 3 || roundTrip.ToolFirstFailureClass != toolFailureMissingRequired || roundTrip.ToolRetries != 1 || roundTrip.ToolEscalations != 1 || roundTrip.ToolReliabilityBucket != "valid" || roundTrip.ToolReliabilitySource != "live" {
		t.Fatalf("roundTrip=%+v err=%v", roundTrip, err)
	}
	priorDefinitions := make([]MigrationDefinition, 0, len(definitions)-1)
	for _, definition := range definitions {
		if definition.ID != toolRoutingUsageMigrationID {
			priorDefinitions = append(priorDefinitions, definition)
		}
	}
	old, err := NewMigrationRunnerWithDataJobs(db, usageMigrationScope, MigrationCompatibility{MinSchema: 0, MaxSchema: 20, MinData: 0, MaxData: 1}, priorDefinitions, usageDataJobDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	oldStatus, err := old.Status()
	if err != nil || oldStatus.Compatible || oldStatus.State != "incompatible" {
		t.Fatalf("schema 20 binary should reject schema 21: %+v err=%v", oldStatus, err)
	}
	if err := revertToolRoutingUsageMigration(db); err != nil {
		t.Fatal(err)
	}
	clearToolRoutingUsageLedger(t, db)
	status, err = runner.Status()
	if err != nil || status.SchemaVersion != 20 || !status.Compatible {
		t.Fatalf("down status=%+v err=%v", status, err)
	}
	oldStatus, err = old.Status()
	if err != nil || !oldStatus.Compatible || oldStatus.SchemaVersion != 20 {
		t.Fatalf("schema 20 binary should accept reversed schema: %+v err=%v", oldStatus, err)
	}
	for _, field := range []string{"ToolCallCount", "ToolFirstFailureClass", "ToolRetries", "ToolEscalations", "ToolReliabilityBucket", "ToolReliabilitySource"} {
		if db.Migrator().HasColumn(&usageRecord{}, field) {
			t.Fatalf("column %s remains after down migration", field)
		}
	}
}

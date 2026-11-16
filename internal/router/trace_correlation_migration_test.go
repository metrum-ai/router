// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"net/http/httptest"
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

func TestTraceCorrelationAndHierarchyAuditSchema18SQLiteUpgradeDownAndRowRoundTrip(t *testing.T) {
	cfg := uncachedSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	store, err := OpenUsageStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(recordFromRow(rowFromRecord(logRecord{RequestID: "legacy-trace-row", TS: time.Now().UTC().Format(time.RFC3339)}))).Error; err != nil {
		t.Fatal(err)
	}
	if err := revertHierarchyAuditMigration(store.db); err != nil {
		t.Fatal(err)
	}
	clearHierarchyAuditMigrationLedger(t, store.db)
	if err := revertTraceCorrelationMigration(store.db); err != nil {
		t.Fatal(err)
	}
	clearTraceCorrelationMigrationLedger(t, store.db)
	if err := verifyPolicyRuntimeMigration(store.db); err != nil {
		t.Fatalf("schema 16 postcondition: %v", err)
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
	if err != nil || before.SchemaVersion != 16 {
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
	if err := runner.db.Create(row).Error; err != nil {
		t.Fatal(err)
	}
	var got usageRecord
	if err := runner.db.First(&got, "request_id = ?", "traced-row").Error; err != nil || got.TraceID != row.TraceID || got.SpanID != row.SpanID || got.ParentSpanID != row.ParentSpanID {
		t.Fatalf("trace row=%+v want=%+v err=%v", got, row, err)
	}

	old, err := NewMigrationRunnerWithDataJobs(runner.db, usageMigrationScope, MigrationCompatibility{MinSchema: 0, MaxSchema: 16, MinData: 0, MaxData: 1}, usageMigrationDefinitions[:len(usageMigrationDefinitions)-1], usageDataJobDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	oldStatus, err := old.Status()
	if err != nil || oldStatus.Compatible || oldStatus.State != "incompatible" {
		t.Fatalf("schema 16 binary should reject schema 18: %+v err=%v", oldStatus, err)
	}
	if err := revertHierarchyAuditMigration(runner.db); err != nil {
		t.Fatal(err)
	}
	clearHierarchyAuditMigrationLedger(t, runner.db)
	if err := revertTraceCorrelationMigration(runner.db); err != nil {
		t.Fatal(err)
	}
	clearTraceCorrelationMigrationLedger(t, runner.db)
	afterDown, err := runner.Status()
	if err != nil || afterDown.SchemaVersion != 16 || !afterDown.Compatible {
		t.Fatalf("after down: %+v err=%v", afterDown, err)
	}
	oldStatus, err = old.Status()
	if err != nil || !oldStatus.Compatible || oldStatus.SchemaVersion != 16 {
		t.Fatalf("schema 16 binary should accept reversed database: %+v err=%v", oldStatus, err)
	}
}

func TestHierarchyAuditUsageFiltersAndRoundTrip(t *testing.T) {
	store, err := OpenUsageStore(uncachedSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "hierarchy.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	rows := []usageRow{
		{RequestID: "hierarchy-a", TS: now, TraceID: "trace-a", SpanID: "hop-a", ParentSpanID: "parent-a", OrganizationID: "org-a", TeamID: "team-a", CallerID: "caller-a", Status: 200},
		{RequestID: "hierarchy-b", TS: now, TraceID: "trace-b", SpanID: "hop-b", ParentSpanID: "parent-b", OrganizationID: "org-b", TeamID: "team-b", CallerID: "caller-b", Status: 200},
	}
	for _, row := range rows {
		if err := store.db.Create(recordFromRow(row)).Error; err != nil {
			t.Fatal(err)
		}
	}
	for name, filter := range map[string]UsageReportOptions{
		"organization": {OrganizationID: "org-a"},
		"team":         {TeamID: "team-a"},
		"hop":          {HopID: "hop-a"},
	} {
		filter.From, filter.To = now.Add(-time.Minute), now.Add(time.Minute)
		got, err := store.rows(filter)
		if err != nil || len(got) != 1 || got[0].RequestID != "hierarchy-a" {
			t.Fatalf("%s filter rows=%+v err=%v", name, got, err)
		}
	}
	request := adminRequestFromRow(rows[0])
	if request.TraceID != "trace-a" || request.HopID != "hop-a" || request.ParentHopID != "parent-a" || request.OrganizationID != "org-a" || request.TeamID != "team-a" {
		t.Fatalf("hierarchy audit export lost linked hop metadata: %+v", request)
	}
	svc := &Service{cfg: &Config{Server: ServerConfig{AdminReports: AdminReportsConfig{DefaultSince: "24h", MaxRange: "720h", MaxRows: 100}}}}
	filterRequest := httptest.NewRequest(http.MethodGet, "/admin/reports/api/requests?organization_id=org-a&team_id=team-a&hop_id=hop-a", nil)
	filterResponse := httptest.NewRecorder()
	filters, ok := svc.parseAdminReportFilters(filterResponse, filterRequest, false, adminAuthSubject{}, true)
	if !ok || filters.OrganizationID != "org-a" || filters.TeamID != "team-a" || filters.HopID != "hop-a" {
		t.Fatalf("hierarchy report filters=%+v status=%d body=%s", filters, filterResponse.Code, filterResponse.Body.String())
	}
	if err := validateRollupReportOptions(UsageReportOptions{From: now.UTC().Truncate(24 * time.Hour), To: now.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour), OrganizationID: "org-a"}); err == nil {
		t.Fatal("row-level hierarchy filters must not be accepted for aggregated rollup history")
	}
}

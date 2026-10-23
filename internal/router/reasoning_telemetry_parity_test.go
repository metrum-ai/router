// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReasoningTelemetrySQLiteParity(t *testing.T) {
	runReasoningTelemetryParity(t, UsageDBConfig{
		Driver: "sqlite",
		Path:   filepath.Join(t.TempDir(), "reasoning-telemetry.sqlite"),
	})
}

// TestReasoningTelemetryPostgresParity is intentionally opt-in because its DSN
// must identify a disposable database. scripts/test_reasoning_telemetry_postgres.sh
// creates that database and sets the two required test-only environment values.
func TestReasoningTelemetryPostgresParity(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("SMART_ROUTER_POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("SMART_ROUTER_POSTGRES_TEST_DSN is required for disposable PostgreSQL parity coverage")
	}
	if os.Getenv("SMART_ROUTER_POSTGRES_TEST_ALLOW") != "issue-571" || !strings.Contains(strings.ToLower(dsn), "smart_router_issue_571") {
		t.Fatal("PostgreSQL parity coverage requires the explicit issue-571 disposable database guard")
	}
	runReasoningTelemetryParity(t, UsageDBConfig{Driver: "postgres", DSN: dsn})
}

func runReasoningTelemetryParity(t *testing.T, cfg UsageDBConfig) {
	t.Helper()
	legacyCfg := cfg
	legacyCfg.MigrationPolicy = usageDBMigrationPolicyAutoSafe
	legacy, err := OpenUsageStore(legacyCfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"ALTER TABLE request_usage DROP COLUMN reasoning_tokens",
		"ALTER TABLE request_usage DROP COLUMN reasoning_attempt_count",
		"ALTER TABLE request_usage DROP COLUMN reasoning_successful_attempt_count",
		"ALTER TABLE request_usage DROP COLUMN reasoning_reported_attempt_count",
		"ALTER TABLE request_attempts DROP COLUMN reasoning_tokens",
	} {
		if err := legacy.db.Exec(statement).Error; err != nil {
			_ = legacy.Close()
			t.Fatal(err)
		}
	}
	jobID := dataJobID(usageMigrationScope, "historical-usage-validation-v1")
	if err := legacy.db.Where("job_id = ?", jobID).Delete(&migrationDataJobCheckpointRecord{}).Error; err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.db.Where("job_id = ?", jobID).Delete(&migrationDataJobRecord{}).Error; err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.db.Where("scope = ?", usageMigrationScope).Delete(&migrationAttemptRecord{}).Error; err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.db.Exec("DELETE FROM schema_migration_ledger WHERE scope = ?", usageMigrationScope).Error; err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	runner, closeDB, err := UsageMigrationRunner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.ApplyPending("reasoning-telemetry-parity"); err != nil {
		_ = closeDB()
		t.Fatal(err)
	}
	if _, err := runner.RunDataJob(context.Background(), "historical-usage-validation-v1", "reasoning-telemetry-parity", DataJobCheckpoint{Ordinal: 0}); err != nil {
		_ = closeDB()
		t.Fatal(err)
	}
	if status, err := runner.Verify(); err != nil || status.State != "current" || status.SchemaVersion != usageMigrationCompatibility.MaxSchema {
		_ = closeDB()
		t.Fatalf("migration status=%+v err=%v", status, err)
	}
	if err := closeDB(); err != nil {
		t.Fatal(err)
	}

	validatedCfg := cfg
	validatedCfg.MigrationPolicy = usageDBMigrationPolicyValidate
	store, err := OpenUsageStore(validatedCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := verifyUsageReasoningTelemetryMigration(store.db); err != nil {
		t.Fatalf("reasoning migration postcondition: %v", err)
	}

	ts := "2026-08-04T12:00:00.000Z"
	zero, positive := 0, 7
	requests := []usageRecord{
		{RequestID: "reasoning-absent", TS: ts, ResolvedGroup: "reasoning-parity", ReasoningAttemptCount: 1, ReasoningSuccessfulAttemptCount: 1},
		{RequestID: "reasoning-zero", TS: ts, ResolvedGroup: "reasoning-parity", ReasoningTokens: &zero, ReasoningAttemptCount: 1, ReasoningSuccessfulAttemptCount: 1, ReasoningReportedAttemptCount: 1},
		{RequestID: "reasoning-positive", TS: ts, ResolvedGroup: "reasoning-parity", ReasoningTokens: &positive, ReasoningAttemptCount: 1, ReasoningSuccessfulAttemptCount: 1, ReasoningReportedAttemptCount: 1},
	}
	if err := store.db.Create(&requests).Error; err != nil {
		t.Fatal(err)
	}
	attempts := []requestAttemptRecord{
		{RequestID: "reasoning-absent", AttemptIndex: 1, TS: ts, Provider: "parity", Model: "parity", Dialect: "openai-chat", EndpointHost: "parity"},
		{RequestID: "reasoning-zero", AttemptIndex: 1, TS: ts, Provider: "parity", Model: "parity", Dialect: "openai-chat", EndpointHost: "parity", ReasoningTokens: &zero},
		{RequestID: "reasoning-positive", AttemptIndex: 1, TS: ts, Provider: "parity", Model: "parity", Dialect: "openai-chat", EndpointHost: "parity", ReasoningTokens: &positive},
	}
	if err := store.db.Create(&attempts).Error; err != nil {
		t.Fatal(err)
	}

	var gotRequests []usageRecord
	if err := store.db.Where("resolved_group = ?", "reasoning-parity").Order("request_id").Find(&gotRequests).Error; err != nil {
		t.Fatal(err)
	}
	assertReasoningTokenStates(t, "request", []int{-1, 7, 0}, reasoningValuesFromUsage(gotRequests))
	var gotAttempts []requestAttemptRecord
	if err := store.db.Where("provider = ?", "parity").Order("request_id").Find(&gotAttempts).Error; err != nil {
		t.Fatal(err)
	}
	assertReasoningTokenStates(t, "attempt", []int{-1, 7, 0}, reasoningValuesFromAttempts(gotAttempts))

	from, err := time.Parse(time.RFC3339, "2026-08-04T11:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	to, err := time.Parse(time.RFC3339, "2026-08-04T13:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	opts := UsageReportOptions{Driver: cfg.Driver, DBPath: cfg.Path, DSN: cfg.DSN, MigrationPolicy: usageDBMigrationPolicyValidate, From: from, To: to, ResolvedGroup: "reasoning-parity"}
	rows, err := store.rowsWithoutBuckets(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("filtered request rows=%d, want 3", len(rows))
	}
	coverage, err := ExportReasoningCoverage(opts)
	if err != nil {
		t.Fatal(err)
	}
	if coverage.ReasoningTokens != 7 || coverage.ReasoningAttemptCount != 3 || coverage.ReasoningSuccessfulAttemptCount != 3 || coverage.ReasoningReportedAttemptCount != 2 || !coverage.CoverageComplete || len(coverage.Coverage) != 1 {
		t.Fatalf("reasoning aggregate=%+v", coverage)
	}
	// Gemini thinking uses the same nullable metric and persistence contract on
	// both database engines, with reasoning included in billable output.
	gemini := geminiUsageFromMap(map[string]any{"promptTokenCount": 100, "candidatesTokenCount": 20, "thoughtsTokenCount": 500, "totalTokenCount": 620})
	geminiRecord := recordFromRow(rowFromRecord(logRecord{TS: ts, RequestID: "gemini-thinking", Usage: gemini, AttemptsDetail: []attemptLogRecord{{Selected: true, ReasoningTokens: gemini.ReasoningTokens}}}))
	if err := store.db.Create(geminiRecord).Error; err != nil {
		t.Fatal(err)
	}
	var persisted usageRecord
	if err := store.db.Where("request_id = ?", "gemini-thinking").First(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	geminiRow, err := rowFromUsageRecord(persisted)
	if err != nil {
		t.Fatal(err)
	}
	// Request reasoning is the served metric, not a sum of failed attempts.
	// Populate the record as the request path does, using the selected attempt.
	if persisted.OutputTokens != 520 {
		t.Fatalf("Gemini output=%d", persisted.OutputTokens)
	}
	detail := adminRequestFromRow(geminiRow)
	if detail.ReasoningTokens == nil || *detail.ReasoningTokens != 500 {
		t.Fatalf("Gemini detail reasoning=%v", detail.ReasoningTokens)
	}
	entry := coverage.Coverage[0]
	if entry.Attempts != 3 || entry.ReportedAttempts != 2 || entry.ReasoningTokens != 7 {
		t.Fatalf("reasoning coverage row=%+v", entry)
	}
}

func reasoningValuesFromUsage(rows []usageRecord) []*int {
	values := make([]*int, len(rows))
	for i := range rows {
		values[i] = rows[i].ReasoningTokens
	}
	return values
}

func reasoningValuesFromAttempts(rows []requestAttemptRecord) []*int {
	values := make([]*int, len(rows))
	for i := range rows {
		values[i] = rows[i].ReasoningTokens
	}
	return values
}

// expected uses -1 for the absent state because reasoning-token counts cannot be negative.
func assertReasoningTokenStates(t *testing.T, recordType string, expected []int, actual []*int) {
	t.Helper()
	if len(actual) != len(expected) {
		t.Fatalf("%s row count=%d, want %d", recordType, len(actual), len(expected))
	}
	for i, want := range expected {
		if want < 0 {
			if actual[i] != nil {
				t.Fatalf("%s row %d reasoning_tokens=%d, want absent", recordType, i, *actual[i])
			}
			continue
		}
		if actual[i] == nil || *actual[i] != want {
			t.Fatalf("%s row %d reasoning_tokens=%v, want %d", recordType, i, actual[i], want)
		}
	}
}

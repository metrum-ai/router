// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminMigrationStatusProjectsBoundDataJobState(t *testing.T) {
	hash := mustBcryptHash(t, "admin-password")
	t.Setenv("SMART_ROUTER_MIGRATION_ADMIN_HASH", hash)
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Server.UsageDB = UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "usage.sqlite"), MigrationPolicy: usageDBMigrationPolicyAutoSafe}
	cfg.Server.AdminReports = AdminReportsConfig{Enabled: true}
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{Enabled: true, AllowInsecureHTTP: true, Users: []AdminBasicAuthUser{{Username: "admin", PasswordHashEnv: "SMART_ROUTER_MIGRATION_ADMIN_HASH", Subject: "basic:admin", Domain: "test/local"}}}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, basic:admin, test/local, admin:reports, read"}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	jobDefinition := usageDataJobDefinitions[0]
	jobID := dataJobID(usageMigrationScope, jobDefinition.Key)
	cases := []struct {
		name, jobState, effectiveState, validationState, summaryState string
	}{
		{name: "missing", effectiveState: "pending", validationState: "not-validated", summaryState: "pending"},
		{name: "running", jobState: migrationDataJobRunning, effectiveState: "in-progress", validationState: "in-progress", summaryState: "in-progress"},
		{name: "paused", jobState: migrationDataJobPaused, effectiveState: "pending", validationState: "not-validated", summaryState: "pending"},
		{name: "cancelled", jobState: migrationDataJobCancelled, effectiveState: "pending", validationState: "not-validated", summaryState: "pending"},
		{name: "failed", jobState: migrationDataJobFailed, effectiveState: "failed", validationState: "failed", summaryState: "failed"},
		{name: "validated", jobState: migrationDataJobValidated, effectiveState: "applied", validationState: "verified", summaryState: "current"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := svc.usage.db.Where("job_id = ?", jobID).Delete(&migrationDataJobCheckpointRecord{}).Error; err != nil {
				t.Fatal(err)
			}
			if err := svc.usage.db.Where("job_id = ?", jobID).Delete(&migrationDataJobRecord{}).Error; err != nil {
				t.Fatal(err)
			}
			if tc.jobState != "" {
				record := migrationDataJobRecord{JobID: jobID, Scope: usageMigrationScope, MigrationID: jobDefinition.MigrationID, DataVersion: jobDefinition.DataVersion, State: tc.jobState, ExecutionMode: jobDefinition.ExecutionMode, ValidationMode: jobDefinition.ValidationMode, RowsScanned: 20, RowsUpdated: 10, RowsSkipped: 8, RowsFailed: 2, StartedAt: "2026-08-05T12:00:00Z"}
				if tc.jobState == migrationDataJobFailed {
					record.SafeErrorClass = "data-job-failed"
				}
				if err := svc.usage.db.Create(&record).Error; err != nil {
					t.Fatal(err)
				}
			}

			response := adminMigrationStatusResponseForTest(t, svc, "/admin/reports/api/migrations")
			row := adminMigrationRowForTest(t, response.Rows, usageHistoricalValidationMigrationID)
			if row.State != tc.effectiveState || row.ValidationState != tc.validationState || row.DataJobState != tc.name {
				t.Fatalf("row=%+v, want state=%q validation=%q dataJobState=%q", row, tc.effectiveState, tc.validationState, tc.name)
			}
			if got, _ := response.Summary["state"].(string); got != tc.summaryState {
				t.Fatalf("summary=%v, want state=%q", response.Summary, tc.summaryState)
			}
			if tc.name == "missing" && response.Summary["missingDataJobs"] != float64(1) {
				t.Fatalf("missing data-job summary=%v", response.Summary)
			}
			filtered := adminMigrationStatusResponseForTest(t, svc, "/admin/reports/api/migrations?state="+tc.effectiveState)
			if len(filtered.Rows) == 0 || adminMigrationRowForTest(t, filtered.Rows, usageHistoricalValidationMigrationID).DataJobState != tc.name {
				t.Fatalf("effective-state filter did not retain %q data job: %+v", tc.name, filtered.Rows)
			}
		})
	}
}

func TestAdminMigrationStatusPreservesNonAppliedLedgerBeforeBoundJobProjection(t *testing.T) {
	hash := mustBcryptHash(t, "admin-password")
	t.Setenv("SMART_ROUTER_MIGRATION_ADMIN_HASH", hash)
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Server.UsageDB = UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "usage.sqlite"), MigrationPolicy: usageDBMigrationPolicyAutoSafe}
	cfg.Server.AdminReports = AdminReportsConfig{Enabled: true}
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{Enabled: true, AllowInsecureHTTP: true, Users: []AdminBasicAuthUser{{Username: "admin", PasswordHashEnv: "SMART_ROUTER_MIGRATION_ADMIN_HASH", Subject: "basic:admin", Domain: "test/local"}}}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, basic:admin, test/local, admin:reports, read"}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	definition := usageMigrationDefinitionByID(t, usageHistoricalValidationMigrationID)
	cases := []struct {
		name, ledgerState, jobState, effectiveState, validationState string
		hasJob                                                       bool
	}{
		{name: "failed-ledger-without-job", ledgerState: "failed", effectiveState: "failed", validationState: "failed"},
		{name: "running-ledger-without-job", ledgerState: "running", effectiveState: "running", validationState: "in-progress"},
		{name: "failed-ledger-with-validated-job", ledgerState: "failed", jobState: migrationDataJobValidated, effectiveState: "failed", validationState: "failed", hasJob: true},
		{name: "running-ledger-with-failed-job", ledgerState: "running", jobState: migrationDataJobFailed, effectiveState: "running", validationState: "in-progress", hasJob: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := MigrationStatus{Scope: usageMigrationScope, SchemaVersion: definition.SchemaVersion, DataVersion: definition.DataVersion, Compatible: true, Entries: []MigrationLedgerEntry{{MigrationID: definition.ID, State: tc.ledgerState, ErrorCode: "schema-" + tc.ledgerState, ErrorText: "schema-" + tc.ledgerState}}}
			if tc.hasJob {
				status.Jobs = []MigrationDataJobStatus{{MigrationID: definition.ID, State: tc.jobState, Present: true, RowsScanned: 20, RowsUpdated: 10, RowsSkipped: 8, RowsFailed: 2, Checkpoints: 2, ErrorClass: "data-job-" + tc.jobState}}
			}
			svc.migrationStatusFn = func() (MigrationStatus, error) { return status, nil }

			response := adminMigrationStatusResponseForTest(t, svc, "/admin/reports/api/migrations")
			row := adminMigrationRowForTest(t, response.Rows, definition.ID)
			if row.State != tc.effectiveState || row.ValidationState != tc.validationState || row.DataJobState != tc.jobState {
				t.Fatalf("row=%+v, want state=%q validation=%q dataJobState=%q", row, tc.effectiveState, tc.validationState, tc.jobState)
			}
			if row.ErrorClass != "schema-"+tc.ledgerState || row.ErrorMessage != "schema-"+tc.ledgerState {
				t.Fatalf("authoritative schema error was not retained: %+v", row)
			}
			if got, _ := response.Summary["state"].(string); got != tc.effectiveState {
				t.Fatalf("summary=%v, want state=%q", response.Summary, tc.effectiveState)
			}
			if tc.effectiveState == "failed" && response.Summary["failed"] != float64(1) {
				t.Fatalf("failed ledger was not counted in summary: %v", response.Summary)
			}
			if tc.effectiveState == "running" && response.Summary["inProgress"] != float64(1) {
				t.Fatalf("running ledger was not counted as in-progress in summary: %v", response.Summary)
			}
			filtered := adminMigrationStatusResponseForTest(t, svc, "/admin/reports/api/migrations?state="+tc.effectiveState)
			filteredRow := adminMigrationRowForTest(t, filtered.Rows, definition.ID)
			if filteredRow.State != tc.effectiveState || filteredRow.DataJobState != tc.jobState {
				t.Fatalf("effective-state filter lost authoritative ledger row: %+v", filteredRow)
			}
		})
	}
}

func usageMigrationDefinitionByID(t *testing.T, id int) MigrationDefinition {
	t.Helper()
	for _, definition := range usageMigrationDefinitions {
		if definition.ID == id {
			return definition
		}
	}
	t.Fatalf("usage migration %d not found", id)
	return MigrationDefinition{}
}

func adminMigrationStatusResponseForTest(t *testing.T, svc *Service, target string) adminMigrationStatusResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.SetBasicAuth("admin", "admin-password")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var response adminMigrationStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func adminMigrationRowForTest(t *testing.T, rows []adminMigrationStatusRow, migrationID int) adminMigrationStatusRow {
	t.Helper()
	for _, row := range rows {
		if row.MigrationID == migrationID {
			return row
		}
	}
	t.Fatalf("migration %d not found in %+v", migrationID, rows)
	return adminMigrationStatusRow{}
}

func TestMigrationMetricsAreAggregateAndSafe(t *testing.T) {
	metrics := newMetricsStore().Prometheus(newTrafficShapeManager(), MigrationStatus{
		Scope: "usage", SchemaVersion: 2, DataVersion: 1, Compatible: true,
		Pending: []MigrationDefinition{{ID: 999}},
		Jobs:    []MigrationDataJobStatus{{State: "running"}, {State: "running"}},
	})
	for _, want := range []string{
		`metrum_ai_router_migration_schema_version{scope="usage"} 2`,
		`metrum_ai_router_migration_data_version{scope="usage"} 1`,
		`metrum_ai_router_migration_compatible{scope="usage"} 1`,
		`metrum_ai_router_migration_pending{scope="usage"} 1`,
		`metrum_ai_router_migration_jobs{scope="usage",state="running"} 2`,
		`metrum_ai_router_migration_failures{scope="usage"} 0`,
		`metrum_ai_router_migration_progress_rows{scope="usage",outcome="scanned"} 0`,
		`metrum_ai_router_migration_in_progress_age_seconds{scope="usage"} 0`,
	} {
		if !strings.Contains(metrics, want) {
			t.Fatalf("migration metrics missing %q: %s", want, metrics)
		}
	}
	for _, forbidden := range []string{"caller_id", "token_id", "postgres://", "SELECT ", "secret"} {
		if strings.Contains(metrics, forbidden) {
			t.Fatalf("migration metrics leaked %q: %s", forbidden, metrics)
		}
	}
}

func TestMigrationMetricsExposeAllSafeDataJobStates(t *testing.T) {
	metrics := newMetricsStore().Prometheus(newTrafficShapeManager(), MigrationStatus{
		Scope: "usage",
		Jobs: []MigrationDataJobStatus{
			{State: migrationDataJobPending}, // includes a missing durable job.
			{State: migrationDataJobRunning},
			{State: migrationDataJobPaused},
			{State: migrationDataJobCancelled},
			{State: migrationDataJobFailed},
			{State: migrationDataJobValidated},
		},
	})
	for _, state := range []string{"pending", "running", "paused", "cancelled", "failed", "validated"} {
		want := `metrum_ai_router_migration_jobs{scope="usage",state="` + state + `"} 1`
		if !strings.Contains(metrics, want) {
			t.Fatalf("migration metrics missing %q: %s", want, metrics)
		}
	}
}

func TestMetricsRetainsAuthorizedGlobalFamiliesWhenMigrationStatusUnavailable(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Server.UsageDB = UsageDBConfig{Driver: "sqlite", Path: filepath.Join(dir, "usage.sqlite"), MigrationPolicy: usageDBMigrationPolicyAutoSafe}
	adminToken := "rtr_metrics_admin_unavailable"
	adminSum := sha256.Sum256([]byte(adminToken))
	cfg.Callers = append(cfg.Callers, CallerConfig{
		ID: "metrics-admin", User: "ops", Project: "observability", Environment: "test",
		TokenSHA256: hex.EncodeToString(adminSum[:]), TokenID: "rtr_metrics_admin_unavailable",
		Allow: []string{"default"}, MetricsAdmin: true, Rate: RateConfig{RPM: 100, TPM: 100000, Concurrent: 4},
	})
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	svc.metrics.Observe(logRecord{CallerID: "metrics-admin", CallerUser: "ops", CallerProject: "observability", CallerEnvironment: "test", TokenID: "rtr_metrics_admin_unavailable", ResolvedGroup: "default", TargetProvider: "mock", TargetModel: "mock-model", Status: http.StatusOK})
	lookupCalled := false
	svc.migrationStatusFn = func() (MigrationStatus, error) {
		lookupCalled = true
		return MigrationStatus{}, errors.New("postgres://private-host/usage: migration ledger read failed")
	}

	ordinary := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	ordinary.Header.Set("Authorization", "Bearer "+testToken)
	ordinaryRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(ordinaryRR, ordinary)
	if ordinaryRR.Code != http.StatusForbidden || !strings.Contains(ordinaryRR.Body.String(), "metrics-forbidden") {
		t.Fatalf("ordinary status=%d body=%s", ordinaryRR.Code, ordinaryRR.Body.String())
	}

	admin := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	admin.Header.Set("Authorization", "Bearer "+adminToken)
	adminRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(adminRR, admin)
	if adminRR.Code != http.StatusOK {
		t.Fatalf("admin status=%d body=%s", adminRR.Code, adminRR.Body.String())
	}
	body := adminRR.Body.String()
	if !lookupCalled {
		t.Fatal("migration status lookup hook was not called")
	}
	for _, want := range []string{
		`metrum_ai_router_migration_status_available{scope="usage"} 0`,
		"metrum_ai_router_requests_total",
		"metrum_ai_router_traffic_shape_queue_depth",
		"metrum_ai_router_build_info",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
	for _, removed := range []string{
		"metrum_ai_router_license_valid",
		"metrum_ai_router_license_seconds_until_expiry",
		"metrum_ai_router_license_grace_active",
		"metrum_ai_router_license_validation_failures_total",
	} {
		if strings.Contains(body, removed) {
			t.Fatalf("removed license metric %q still present:\n%s", removed, body)
		}
	}
	for _, forbidden := range []string{"postgres://", "private-host", "migration ledger read failed"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("metrics leaked %q: %s", forbidden, body)
		}
	}
	for _, unavailable := range []string{
		`metrum_ai_router_migration_schema_version{scope="usage"}`,
		`metrum_ai_router_migration_data_version{scope="usage"}`,
		`metrum_ai_router_migration_compatible{scope="usage"}`,
		`metrum_ai_router_migration_pending{scope="usage"}`,
	} {
		if strings.Contains(body, unavailable) {
			t.Fatalf("unavailable migration status emitted stale metric %q: %s", unavailable, body)
		}
	}
}

func TestAdminMigrationStatusIsReadOnlyAndRedacted(t *testing.T) {
	hash := mustBcryptHash(t, "admin-password")
	t.Setenv("SMART_ROUTER_MIGRATION_ADMIN_HASH", hash)
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Server.UsageDB = UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "usage.sqlite"), MigrationPolicy: usageDBMigrationPolicyAutoSafe}
	cfg.Server.AdminReports = AdminReportsConfig{Enabled: true}
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{Enabled: true, AllowInsecureHTTP: true, Users: []AdminBasicAuthUser{{Username: "admin", PasswordHashEnv: "SMART_ROUTER_MIGRATION_ADMIN_HASH", Subject: "basic:admin", Domain: "test/local"}}}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, basic:admin, test/local, admin:reports, read"}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	ordinary := httptest.NewRequest(http.MethodGet, "/admin/reports/api/migrations", nil)
	ordinary.Header.Set("Authorization", "Bearer "+testToken)
	ordinaryRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(ordinaryRR, ordinary)
	if ordinaryRR.Code != http.StatusForbidden || !strings.Contains(ordinaryRR.Body.String(), "reports-forbidden") {
		t.Fatalf("ordinary status=%d body=%s", ordinaryRR.Code, ordinaryRR.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/reports/api/migrations", nil)
	req.SetBasicAuth("admin", "admin-password")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin status=%d body=%s", rr.Code, rr.Body.String())
	}
	for _, want := range []string{"schemaVersion", "rollbackClass", "restore-required"} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Fatalf("missing %q: %s", want, rr.Body.String())
		}
	}
	for _, forbidden := range []string{"postgres://", "provider-key", testToken, "checksum", "handlerKey", "SELECT "} {
		if strings.Contains(rr.Body.String(), forbidden) {
			t.Fatalf("leaked %q: %s", forbidden, rr.Body.String())
		}
	}
}

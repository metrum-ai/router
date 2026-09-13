// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestUsageExplicitBootstrapPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("SMART_ROUTER_POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("SMART_ROUTER_POSTGRES_TEST_DSN is required for disposable PostgreSQL Stage 2 coverage")
	}
	if os.Getenv("SMART_ROUTER_POSTGRES_TEST_ALLOW") != "issue-507-stage2" || !strings.Contains(strings.ToLower(dsn), "smart_router_issue_507_stage2") {
		t.Fatal("PostgreSQL Stage 2 coverage requires the explicit disposable database guard")
	}
	store, err := OpenUsageStore(UsageDBConfig{Driver: "postgres", DSN: dsn, MigrationPolicy: usageDBMigrationPolicyAutoSafe})
	if err != nil {
		t.Fatalf("explicit PostgreSQL bootstrap: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	validated, err := OpenUsageStore(UsageDBConfig{Driver: "postgres", DSN: dsn, MigrationPolicy: usageDBMigrationPolicyDeploymentJob})
	if err != nil {
		t.Fatalf("PostgreSQL deployment-job validation: %v", err)
	}
	defer validated.Close()
	if err := ensureUsageRelationalSchema(validated.db); err != nil {
		t.Fatalf("PostgreSQL explicit schema contract: %v", err)
	}
}

func applyTestMigrationMarker(tx *gorm.DB) error {
	return tx.Exec("CREATE TABLE IF NOT EXISTS migration_marker (id INTEGER PRIMARY KEY)").Error
}

func verifyTestMigrationMarker(tx *gorm.DB) error {
	if !tx.Migrator().HasTable("migration_marker") {
		return errors.New("marker missing")
	}
	return nil
}

func applyTestMigrationFailure(*gorm.DB) error { return errors.New("deliberate migration failure") }

func applyTestMaintenanceAuditMarker(tx *gorm.DB) error {
	return tx.Exec("CREATE TABLE maintenance_audit_marker (id INTEGER PRIMARY KEY)").Error
}

func verifyTestMaintenanceAuditMarker(tx *gorm.DB) error {
	if !tx.Migrator().HasTable("maintenance_audit_marker") {
		return errors.New("maintenance audit marker missing")
	}
	return nil
}

func applyTestNonTransactionalAuditMarker(tx *gorm.DB) error {
	return tx.Exec("CREATE TABLE nontransactional_audit_marker (id INTEGER PRIMARY KEY)").Error
}

func verifyTestNonTransactionalAuditMarker(tx *gorm.DB) error {
	if !tx.Migrator().HasTable("nontransactional_audit_marker") {
		return errors.New("non-transactional audit marker missing")
	}
	return nil
}

func applyTestMigrationRequiresPostgresTimeouts(tx *gorm.DB) error {
	var lockTimeout, statementTimeout string
	if err := tx.Raw("SELECT current_setting('lock_timeout'), current_setting('statement_timeout')").Row().Scan(&lockTimeout, &statementTimeout); err != nil {
		return err
	}
	if lockTimeout != "5s" || statementTimeout != "30s" {
		return fmt.Errorf("unexpected migration timeouts")
	}
	return applyTestMigrationMarker(tx)
}

func testMigrationDefinition(id int, scope, name, checksum string, schemaVersion int, maintenanceMode, rollbackClass string) MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{
		ID: id, Scope: scope, Name: name, Release: "test", Checksum: checksum,
		SchemaVersion: schemaVersion, Transactional: true, MaintenanceMode: maintenanceMode, RollbackClass: rollbackClass,
		HandlerKey: "test.marker.apply.v1@applyTestMigrationMarker", PostconditionKey: "test.marker.schema.v1@verifyTestMigrationMarker",
		ExecutionMode: "transactional", LockClass: maintenanceMode, TimeoutClass: "bounded",
		Apply: applyTestMigrationMarker, Verify: verifyTestMigrationMarker,
	})
}

func TestMigrationRunnerAppliesAndVerifiesImmutableLedger(t *testing.T) {
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "migrations.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() }()
	d := testMigrationDefinition(1, "test", "add marker", MigrationChecksum("test", "1", "add marker"), 1, "online", "package-only")
	r, err := NewMigrationRunner(db, "test", MigrationCompatibility{MinSchema: 0, MaxSchema: 1, MinData: 0, MaxData: 0}, []MigrationDefinition{d})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ApplyPending("test-runner"); err != nil {
		t.Fatal(err)
	}
	status, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "current" || !status.Compatible || status.SchemaVersion != 1 || len(status.Entries) != 1 {
		t.Fatalf("unexpected status: %+v", status)
	}
	if err := r.ApplyPending("test-runner"); err != nil {
		t.Fatalf("idempotent apply: %v", err)
	}
}

func TestMigrationRunnerDurablyRecordsAtomicFailureWithFencing(t *testing.T) {
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "failed.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() }()
	d := testMigrationDefinition(1, "failure", "failing marker", MigrationChecksum("failure"), 1, "online", "restore-required")
	d.Apply = applyTestMigrationFailure
	d.HandlerKey = "test.failure.apply.v1@applyTestMigrationFailure"
	d = FinalizeMigrationDefinition(d)
	r, err := NewMigrationRunner(db, "failure", MigrationCompatibility{MinSchema: 0, MaxSchema: 1, MinData: 0, MaxData: 0}, []MigrationDefinition{d})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ApplyPending("failure-runner"); err == nil || !strings.Contains(err.Error(), "migration-apply-failed") {
		t.Fatalf("failed atomic migration must return a safe failure class, got %v", err)
	}
	var ledger migrationLedgerRecord
	if err := db.Where("scope = ? AND migration_id = ?", "failure", d.ID).First(&ledger).Error; err != nil {
		t.Fatalf("durable failed ledger: %v", err)
	}
	if ledger.State != "failed" || ledger.ErrorCode != "migration-apply-failed" {
		t.Fatalf("failed ledger = %+v", ledger)
	}
	var attempt migrationAttemptRecord
	if err := db.Where("scope = ? AND migration_id = ?", "failure", d.ID).First(&attempt).Error; err != nil {
		t.Fatalf("durable failure attempt: %v", err)
	}
	if attempt.State != "failed" || attempt.OwnerGeneration <= 0 || attempt.SafeErrorClass != "migration-apply-failed" {
		t.Fatalf("fenced failure attempt = %+v", attempt)
	}
}

func TestMigrationRunnerRequiresDedicatedNonTransactionalMaintenance(t *testing.T) {
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "nontransactional.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() }()
	d := testMigrationDefinition(1, "nontransactional", "concurrent-style marker", MigrationChecksum("nontransactional"), 1, "maintenance", "restore-required")
	d.Transactional, d.ExecutionMode = false, "non-transactional"
	d = FinalizeMigrationDefinition(d)
	r, err := NewMigrationRunner(db, "nontransactional", MigrationCompatibility{MinSchema: 0, MaxSchema: 1, MinData: 0, MaxData: 0}, []MigrationDefinition{d})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ApplyMaintenancePending("maintenance"); err == nil {
		t.Fatal("ordinary maintenance must reject non-transactional work")
	}
	if err := r.ApplyNonTransactionalMaintenancePendingWithEvidence("maintenance", "backup-approval-1"); err != nil {
		t.Fatalf("dedicated non-transactional maintenance: %v", err)
	}
	var attempt migrationAttemptRecord
	if err := db.Where("scope = ? AND migration_id = ? AND state = ?", "nontransactional", d.ID, "applied").First(&attempt).Error; err != nil {
		t.Fatalf("read audited maintenance attempt: %v", err)
	}
	if attempt.BackupEvidenceRef != "backup-approval-1" || attempt.OwnerGeneration <= 0 {
		t.Fatalf("maintenance backup evidence was not durably fenced: %+v", attempt)
	}
	status, err := r.Verify()
	if err != nil || status.State != "current" || !status.Compatible {
		t.Fatalf("non-transactional recovery state: status=%+v err=%v", status, err)
	}
}

func TestMaintenanceEvidenceAndAppliedStateAreAtomic(t *testing.T) {
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "maintenance-audit.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() }()
	d := testMigrationDefinition(1, "maintenance-audit", "audited marker", MigrationChecksum("maintenance-audit"), 1, "maintenance", "restore-required")
	d.Apply, d.Verify = applyTestMaintenanceAuditMarker, verifyTestMaintenanceAuditMarker
	d.HandlerKey, d.PostconditionKey = "test.maintenance-audit.apply.v1@applyTestMaintenanceAuditMarker", "test.maintenance-audit.verify.v1@verifyTestMaintenanceAuditMarker"
	d = FinalizeMigrationDefinition(d)
	r, err := NewMigrationRunner(db, "maintenance-audit", MigrationCompatibility{MinSchema: 0, MaxSchema: 1, MinData: 0, MaxData: 0}, []MigrationDefinition{d})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ensureLedger(); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER reject_applied_maintenance_attempt BEFORE INSERT ON schema_migration_attempts WHEN NEW.scope = 'maintenance-audit' AND NEW.state = 'applied' BEGIN SELECT RAISE(ABORT, 'simulated audit interruption'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.ApplyMaintenancePendingWithEvidence("maintenance-runner", "backup-approval-atomic"); err == nil {
		t.Fatal("audit interruption must fail maintenance migration")
	}
	var ledger migrationLedgerRecord
	if err := db.Where("scope = ? AND migration_id = ?", "maintenance-audit", d.ID).First(&ledger).Error; err != nil {
		t.Fatal(err)
	}
	if ledger.State != "failed" {
		t.Fatalf("interrupted maintenance ledger = %+v, want failed", ledger)
	}
	var appliedAttempts int64
	if err := db.Model(&migrationAttemptRecord{}).Where("scope = ? AND migration_id = ? AND state = ?", "maintenance-audit", d.ID, "applied").Count(&appliedAttempts).Error; err != nil {
		t.Fatal(err)
	}
	if appliedAttempts != 0 || db.Migrator().HasTable("maintenance_audit_marker") {
		t.Fatalf("atomic maintenance interruption left applied evidence=%d marker=%t", appliedAttempts, db.Migrator().HasTable("maintenance_audit_marker"))
	}
	var failedAttempt migrationAttemptRecord
	if err := db.Where("scope = ? AND migration_id = ? AND state = ?", "maintenance-audit", d.ID, "failed").First(&failedAttempt).Error; err != nil {
		t.Fatal(err)
	}
	if failedAttempt.BackupEvidenceRef != "backup-approval-atomic" || failedAttempt.OwnerGeneration <= 0 {
		t.Fatalf("fenced failed maintenance audit = %+v", failedAttempt)
	}
}

func TestMigrationOperationalPostgresAdvisoryLockAndTimeouts(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("SMART_ROUTER_POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("SMART_ROUTER_POSTGRES_TEST_DSN is required for disposable PostgreSQL operational coverage")
	}
	if os.Getenv("SMART_ROUTER_POSTGRES_TEST_ALLOW") != "issue-507-stage4" || !strings.Contains(strings.ToLower(dsn), "smart_router_issue_507_stage4") {
		t.Fatal("PostgreSQL operational coverage requires the explicit disposable database guard")
	}
	db, err := openUsageDB(UsageDBConfig{Driver: "postgres", DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() }()
	d := testMigrationDefinition(1, "postgres-operational", "timeout marker", MigrationChecksum("postgres-operational"), 1, "online", "package-only")
	d.Apply = applyTestMigrationRequiresPostgresTimeouts
	d.HandlerKey = "test.postgres-timeouts.apply.v1@applyTestMigrationRequiresPostgresTimeouts"
	d = FinalizeMigrationDefinition(d)
	r, err := NewMigrationRunner(db, "postgres-operational", MigrationCompatibility{MinSchema: 0, MaxSchema: 1, MinData: 0, MaxData: 0}, []MigrationDefinition{d})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ApplyPending("postgres-operational-runner"); err != nil {
		t.Fatalf("PostgreSQL timeout-bound migration: %v", err)
	}
	r2, err := NewMigrationRunner(db, "postgres-advisory", MigrationCompatibility{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r2.ensureLedger(); err != nil {
		t.Fatal(err)
	}
	if err := db.Connection(func(conn *gorm.DB) error {
		owner := *r2
		owner.db = conn
		if err := owner.acquireLock("first"); err != nil {
			return err
		}
		defer owner.releaseLock("first")
		contender, err := NewMigrationRunner(db, "postgres-advisory", MigrationCompatibility{}, nil)
		if err != nil {
			return err
		}
		if err := contender.acquireLock("second"); err == nil {
			return errors.New("second PostgreSQL connection acquired advisory migration lock")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Hold the fenced-generation row on a separate PostgreSQL session. The
	// contender must return the bounded, sanitized acquisition-timeout result
	// instead of waiting until the holder voluntarily releases its DML lock.
	const contentionScope = "postgres-ownership-timeout"
	if err := db.Exec("INSERT INTO schema_migration_lock_generations (scope, generation) VALUES (?, 1)", contentionScope).Error; err != nil {
		t.Fatal(err)
	}
	ready := make(chan error, 1)
	blockerDone := make(chan error, 1)
	release := make(chan struct{})
	defer func() {
		close(release)
		if err := <-blockerDone; err != nil {
			t.Errorf("contention blocker cleanup: %v", err)
		}
	}()
	go func() {
		blockerDone <- db.Connection(func(conn *gorm.DB) error {
			if err := conn.Exec("BEGIN").Error; err != nil {
				ready <- err
				return err
			}
			defer conn.Exec("ROLLBACK")
			if err := conn.Exec("SELECT generation FROM schema_migration_lock_generations WHERE scope = ? FOR UPDATE", contentionScope).Error; err != nil {
				ready <- err
				return err
			}
			ready <- nil
			<-release
			return nil
		})
	}()
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	contender, err := NewMigrationRunner(db, contentionScope, MigrationCompatibility{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = contender.ApplyPending("postgres-timeout-contender")
	elapsed := time.Since(started)
	if err == nil || err.Error() != "migration ownership acquisition timed out" {
		t.Fatalf("contended ownership acquisition = %v, want sanitized timeout", err)
	}
	if elapsed < migrationLockTimeout-500*time.Millisecond || elapsed > migrationLockTimeout+5*time.Second {
		t.Fatalf("contended ownership acquisition elapsed %s, want configured lock timeout near %s", elapsed, migrationLockTimeout)
	}
	observer, err := openUsageDB(UsageDBConfig{Driver: "postgres", DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := observer.DB(); _ = sqlDB.Close() }()
	var acquired bool
	if err := observer.Raw("SELECT pg_try_advisory_lock(hashtext(?))", contentionScope).Scan(&acquired).Error; err != nil || !acquired {
		t.Fatalf("contended ownership cleanup left advisory lock: acquired=%t err=%v", acquired, err)
	}
	if err := observer.Exec("SELECT pg_advisory_unlock(hashtext(?))", contentionScope).Error; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name             string
		nonTransactional bool
	}{
		{name: "postgres-maintenance-audit-transactional"},
		{name: "postgres-maintenance-audit-nontransactional", nonTransactional: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := db.Exec("DROP TABLE IF EXISTS maintenance_audit_marker").Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec("DROP TABLE IF EXISTS nontransactional_audit_marker").Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec(`CREATE OR REPLACE FUNCTION reject_applied_maintenance_attempt() RETURNS trigger AS $$ BEGIN IF NEW.scope = '` + tc.name + `' AND NEW.state = 'applied' THEN RAISE EXCEPTION 'simulated audit interruption'; END IF; RETURN NEW; END; $$ LANGUAGE plpgsql`).Error; err != nil {
				t.Fatal(err)
			}
			defer db.Exec("DROP FUNCTION IF EXISTS reject_applied_maintenance_attempt()")
			if err := db.Exec("DROP TRIGGER IF EXISTS reject_applied_maintenance_attempt ON schema_migration_attempts").Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec("CREATE TRIGGER reject_applied_maintenance_attempt BEFORE INSERT ON schema_migration_attempts FOR EACH ROW EXECUTE FUNCTION reject_applied_maintenance_attempt()").Error; err != nil {
				t.Fatal(err)
			}
			defer db.Exec("DROP TRIGGER IF EXISTS reject_applied_maintenance_attempt ON schema_migration_attempts")
			d := testMigrationDefinition(1, tc.name, "audited marker", MigrationChecksum(tc.name), 1, "maintenance", "restore-required")
			d.Apply, d.Verify = applyTestMaintenanceAuditMarker, verifyTestMaintenanceAuditMarker
			if tc.nonTransactional {
				d.Transactional, d.ExecutionMode = false, "non-transactional"
				d.Apply, d.Verify = applyTestNonTransactionalAuditMarker, verifyTestNonTransactionalAuditMarker
			}
			if tc.nonTransactional {
				d.HandlerKey, d.PostconditionKey = "test.nontransactional-audit.apply.v1@applyTestNonTransactionalAuditMarker", "test.nontransactional-audit.verify.v1@verifyTestNonTransactionalAuditMarker"
			} else {
				d.HandlerKey, d.PostconditionKey = "test.maintenance-audit.apply.v1@applyTestMaintenanceAuditMarker", "test.maintenance-audit.verify.v1@verifyTestMaintenanceAuditMarker"
			}
			d = FinalizeMigrationDefinition(d)
			r, err := NewMigrationRunner(db, tc.name, MigrationCompatibility{MinSchema: 0, MaxSchema: 1, MinData: 0, MaxData: 0}, []MigrationDefinition{d})
			if err != nil {
				t.Fatal(err)
			}
			var applyErr error
			if tc.nonTransactional {
				applyErr = r.ApplyNonTransactionalMaintenancePendingWithEvidence("postgres-maintenance-runner", "backup-approval-postgres")
			} else {
				applyErr = r.ApplyMaintenancePendingWithEvidence("postgres-maintenance-runner", "backup-approval-postgres")
			}
			if applyErr == nil {
				t.Fatal("simulated applied-audit interruption must fail closed")
			}
			var ledger migrationLedgerRecord
			if err := db.Where("scope = ? AND migration_id = ?", tc.name, d.ID).First(&ledger).Error; err != nil {
				t.Fatal(err)
			}
			if ledger.State != "failed" {
				t.Fatalf("interrupted %s ledger = %+v, want failed", tc.name, ledger)
			}
			var appliedAttempts int64
			if err := db.Model(&migrationAttemptRecord{}).Where("scope = ? AND migration_id = ? AND state = ?", tc.name, d.ID, "applied").Count(&appliedAttempts).Error; err != nil {
				t.Fatal(err)
			}
			if appliedAttempts != 0 {
				t.Fatalf("interrupted %s recorded applied attempt", tc.name)
			}
			if tc.nonTransactional && !db.Migrator().HasTable("nontransactional_audit_marker") {
				t.Fatal("non-transactional work should remain recoverably failed after final audit interruption")
			}
			if !tc.nonTransactional && db.Migrator().HasTable("maintenance_audit_marker") {
				t.Fatal("transactional audit interruption must roll back maintenance work")
			}
		})
	}
}

func TestMigrationDataJobResumesAtCheckpointsAndOnlyThenAdvancesDataVersion(t *testing.T) {
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "data-job.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() }()
	migration := testMigrationDefinition(1, "test", "data-job schema", MigrationChecksum("data-job schema"), 1, "online", "package-only")
	migration.DataVersion, migration.DataJobKey = 1, "normalise-marker-v1"
	migration = FinalizeMigrationDefinition(migration)
	calls := 0
	job := DataJobDefinition{Key: "normalise-marker-v1", Scope: "test", MigrationID: 1, DataVersion: 1, ExecutionMode: "batch", ValidationMode: "count", ThrottlePerMinute: 0, RestartSafe: true, HandlerKey: "test.normalise-marker.v1", RunCheckpoint: func(_ context.Context, tx *gorm.DB, cp DataJobCheckpoint) (DataJobCheckpointResult, error) {
		calls++
		if cp.Ordinal != calls-1 {
			return DataJobCheckpointResult{}, errors.New("checkpoint was not resumed deterministically")
		}
		if calls == 2 && cp.Cursor != "cursor-1" {
			return DataJobCheckpointResult{}, errors.New("checkpoint cursor was not restored deterministically")
		}
		return DataJobCheckpointResult{Cursor: fmt.Sprintf("cursor-%d", calls), RowsScanned: 10, RowsUpdated: 8, RowsSkipped: 2, Complete: calls == 2}, nil
	}, Validate: func(tx *gorm.DB) error { return nil }}
	r, err := NewMigrationRunnerWithDataJobs(db, "test", MigrationCompatibility{MinSchema: 0, MaxSchema: 1, MinData: 0, MaxData: 1}, []MigrationDefinition{migration}, []DataJobDefinition{job})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ApplyPending("test"); err != nil {
		t.Fatal(err)
	}
	status, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.DataVersion != 0 || status.State != "pending" || len(status.Jobs) != 1 || status.Jobs[0].State != migrationDataJobPending {
		t.Fatalf("unrun data job must not advance version: %+v", status)
	}
	if _, err := r.RunDataJob(context.Background(), job.Key, "test", DataJobCheckpoint{Ordinal: 0, Shard: "all", RangeStart: 1, RangeEnd: 10}); err != nil {
		t.Fatal(err)
	}
	status, err = r.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.DataVersion != 0 || status.State != "in-progress" || status.Jobs[0].RowsUpdated != 8 {
		t.Fatalf("partial job status: %+v", status)
	}
	var firstCheckpoint migrationDataJobCheckpointRecord
	if err := db.Where("job_id = ? AND checkpoint_ordinal = ?", dataJobID("test", job.Key), 0).First(&firstCheckpoint).Error; err != nil {
		t.Fatalf("read first checkpoint: %v", err)
	}
	// This models an operator retrying after an uncertain CLI result. The
	// ordinal is the idempotency key: it must not run the handler, overwrite the
	// persisted cursor, or add aggregate counters again.
	duplicate, err := r.RunDataJob(context.Background(), job.Key, "test", DataJobCheckpoint{Ordinal: 0, Shard: "conflicting", RangeStart: 99, RangeEnd: 100})
	if err != nil {
		t.Fatalf("duplicate checkpoint must return its durable result: %v", err)
	}
	if calls != 1 || duplicate.RowsScanned != 10 || duplicate.RowsUpdated != 8 || duplicate.RowsSkipped != 2 || duplicate.Checkpoints != 1 {
		t.Fatalf("duplicate checkpoint changed execution or aggregate accounting: calls=%d status=%+v", calls, duplicate)
	}
	var durableCheckpoint migrationDataJobCheckpointRecord
	if err := db.Where("job_id = ? AND checkpoint_ordinal = ?", dataJobID("test", job.Key), 0).First(&durableCheckpoint).Error; err != nil {
		t.Fatalf("read durable checkpoint: %v", err)
	}
	if durableCheckpoint != firstCheckpoint {
		t.Fatalf("duplicate checkpoint overwrote durable scalar progress: before=%+v after=%+v", firstCheckpoint, durableCheckpoint)
	}
	result, err := r.RunDataJob(context.Background(), job.Key, "test", DataJobCheckpoint{Ordinal: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != migrationDataJobValidated || result.RowsScanned != 20 || result.RowsUpdated != 16 {
		t.Fatalf("completed job: %+v", result)
	}
	status, err = r.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.DataVersion != 1 || status.State != "current" || !status.Compatible {
		t.Fatalf("validated data job must advance data version: %+v", status)
	}
}

// TestMigrationDataJobPostgresDuplicateOrdinalIdempotent exercises the
// composite checkpoint primary key and insert-only accounting against a fresh,
// explicitly guarded disposable PostgreSQL database.
func TestMigrationDataJobPostgresDuplicateOrdinalIdempotent(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("SMART_ROUTER_POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("SMART_ROUTER_POSTGRES_TEST_DSN is required for disposable PostgreSQL checkpoint coverage")
	}
	if os.Getenv("SMART_ROUTER_POSTGRES_TEST_ALLOW") != "issue-740" || !strings.Contains(strings.ToLower(dsn), "smart_router_issue_740") {
		t.Fatal("PostgreSQL checkpoint coverage requires the explicit disposable database guard")
	}
	db, err := openUsageDB(UsageDBConfig{Driver: "postgres", DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() }()
	migration := testMigrationDefinition(1, "postgres-checkpoint", "data-job schema", MigrationChecksum("postgres data-job schema"), 1, "online", "package-only")
	migration.DataVersion, migration.DataJobKey = 1, "postgres-checkpoint-v1"
	migration = FinalizeMigrationDefinition(migration)
	calls := 0
	job := DataJobDefinition{Key: "postgres-checkpoint-v1", Scope: "postgres-checkpoint", MigrationID: 1, DataVersion: 1, ExecutionMode: "batch", ValidationMode: "count", RestartSafe: true, HandlerKey: "test.postgres-checkpoint.v1", RunCheckpoint: func(context.Context, *gorm.DB, DataJobCheckpoint) (DataJobCheckpointResult, error) {
		calls++
		return DataJobCheckpointResult{Cursor: "durable-cursor", RowsScanned: 3, RowsUpdated: 2, RowsSkipped: 1}, nil
	}, Validate: func(*gorm.DB) error { return nil }}
	r, err := NewMigrationRunnerWithDataJobs(db, "postgres-checkpoint", MigrationCompatibility{MinSchema: 0, MaxSchema: 1, MinData: 0, MaxData: 1}, []MigrationDefinition{migration}, []DataJobDefinition{job})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ApplyPending("postgres-checkpoint-runner"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RunDataJob(context.Background(), job.Key, "postgres-checkpoint-runner", DataJobCheckpoint{Ordinal: 0}); err != nil {
		t.Fatal(err)
	}
	duplicate, err := r.RunDataJob(context.Background(), job.Key, "postgres-checkpoint-runner", DataJobCheckpoint{Ordinal: 0})
	if err != nil {
		t.Fatalf("PostgreSQL duplicate checkpoint must return its durable result: %v", err)
	}
	if calls != 1 || duplicate.RowsScanned != 3 || duplicate.RowsUpdated != 2 || duplicate.RowsSkipped != 1 || duplicate.Checkpoints != 1 {
		t.Fatalf("PostgreSQL duplicate checkpoint changed execution or aggregate accounting: calls=%d status=%+v", calls, duplicate)
	}
}

// TestMigrationDataJobPostgresOwnershipStaysOnAdvisorySession proves that a
// Stage 3 checkpoint's handler and durable state transitions execute on the
// physical PostgreSQL session that owns migration advisory lock.
func TestMigrationDataJobPostgresOwnershipStaysOnAdvisorySession(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("SMART_ROUTER_POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("SMART_ROUTER_POSTGRES_TEST_DSN is required for disposable PostgreSQL session ownership coverage")
	}
	if os.Getenv("SMART_ROUTER_POSTGRES_TEST_ALLOW") != "issue-746" || !strings.Contains(strings.ToLower(dsn), "smart_router_issue_746") {
		t.Fatal("PostgreSQL session ownership coverage requires the explicit disposable database guard")
	}
	db, err := openUsageDB(UsageDBConfig{Driver: "postgres", DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() }()
	observer, err := openUsageDB(UsageDBConfig{Driver: "postgres", DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := observer.DB(); _ = sqlDB.Close() }()

	newRunner := func(scope, key string, checkpoint func(context.Context, *gorm.DB, DataJobCheckpoint) (DataJobCheckpointResult, error)) *migrationRunner {
		migration := testMigrationDefinition(1, scope, "data-job schema", MigrationChecksum(scope), 1, "online", "package-only")
		migration.DataVersion, migration.DataJobKey = 1, key
		migration = FinalizeMigrationDefinition(migration)
		job := DataJobDefinition{Key: key, Scope: scope, MigrationID: 1, DataVersion: 1, ExecutionMode: "batch", ValidationMode: "count", RestartSafe: true, HandlerKey: "test.postgres-advisory-session.v1", RunCheckpoint: checkpoint, Validate: func(*gorm.DB) error { return nil }}
		runner, err := NewMigrationRunnerWithDataJobs(db, scope, MigrationCompatibility{MinSchema: 0, MaxSchema: 1, MinData: 0, MaxData: 1}, []MigrationDefinition{migration}, []DataJobDefinition{job})
		if err != nil {
			t.Fatal(err)
		}
		if err := runner.ApplyPending("postgres-session-runner"); err != nil {
			t.Fatal(err)
		}
		return runner
	}
	bootstrap, err := NewMigrationRunner(db, "postgres-data-job-session-bootstrap", MigrationCompatibility{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.ensureLedger(); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE OR REPLACE FUNCTION test_require_migration_advisory_lock() RETURNS trigger AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND pid = pg_backend_pid() AND granted) THEN
    RAISE EXCEPTION 'checkpoint mutation lacks advisory ownership';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER test_data_job_advisory_lock BEFORE INSERT OR UPDATE ON schema_data_jobs FOR EACH ROW EXECUTE FUNCTION test_require_migration_advisory_lock()`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER test_checkpoint_advisory_lock BEFORE INSERT OR UPDATE ON schema_data_job_checkpoints FOR EACH ROW EXECUTE FUNCTION test_require_migration_advisory_lock()`).Error; err != nil {
		t.Fatal(err)
	}
	assertReleased := func(scope string) {
		var acquired bool
		if err := observer.Raw("SELECT pg_try_advisory_lock(hashtext(?))", scope).Scan(&acquired).Error; err != nil || !acquired {
			t.Fatalf("independent connection could not acquire released advisory lock for %q: acquired=%t err=%v", scope, acquired, err)
		}
		if err := observer.Exec("SELECT pg_advisory_unlock(hashtext(?))", scope).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatal(err)
		}
	}

	const successScope = "postgres-data-job-session-success"
	success := newRunner(successScope, "success", func(_ context.Context, tx *gorm.DB, _ DataJobCheckpoint) (DataJobCheckpointResult, error) {
		var ownsAdvisoryLock bool
		if err := tx.Raw("SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND pid = pg_backend_pid() AND granted)").Scan(&ownsAdvisoryLock).Error; err != nil || !ownsAdvisoryLock {
			return DataJobCheckpointResult{}, errors.New("checkpoint handler lacks advisory ownership")
		}
		var contenderAcquired bool
		if err := observer.Raw("SELECT pg_try_advisory_lock(hashtext(?))", successScope).Scan(&contenderAcquired).Error; err != nil || contenderAcquired {
			if contenderAcquired {
				_ = observer.Exec("SELECT pg_advisory_unlock(hashtext(?))", successScope).Error
			}
			return DataJobCheckpointResult{}, errors.New("independent connection acquired active advisory lock")
		}
		return DataJobCheckpointResult{Cursor: "owned", RowsScanned: 1, Complete: true}, nil
	})
	if _, err := success.RunDataJob(context.Background(), "success", "postgres-session-runner", DataJobCheckpoint{Ordinal: 0}); err != nil {
		t.Fatal(err)
	}
	assertReleased(successScope)

	const cancelledScope = "postgres-data-job-session-cancelled"
	cancelled := newRunner(cancelledScope, "cancelled", func(context.Context, *gorm.DB, DataJobCheckpoint) (DataJobCheckpointResult, error) {
		return DataJobCheckpointResult{}, errors.New("cancelled handler must not run")
	})
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cancelled.RunDataJob(cancelledCtx, "cancelled", "postgres-session-runner", DataJobCheckpoint{Ordinal: 0}); err != nil {
		t.Fatal(err)
	}
	assertReleased(cancelledScope)

	const failedScope = "postgres-data-job-session-failed"
	failed := newRunner(failedScope, "failed", func(context.Context, *gorm.DB, DataJobCheckpoint) (DataJobCheckpointResult, error) {
		return DataJobCheckpointResult{}, errors.New("expected checkpoint failure")
	})
	if _, err := failed.RunDataJob(context.Background(), "failed", "postgres-session-runner", DataJobCheckpoint{Ordinal: 0}); err == nil {
		t.Fatal("failed checkpoint must return a safe error")
	}
	assertReleased(failedScope)
}

func TestMigrationDataJobThrottleRejectsImmediateNextCheckpoint(t *testing.T) {
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "data-job-throttle.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() }()
	migration := testMigrationDefinition(1, "test", "data-job schema", MigrationChecksum("data-job-throttle"), 1, "online", "package-only")
	migration.DataVersion, migration.DataJobKey = 1, "throttle-marker-v1"
	migration = FinalizeMigrationDefinition(migration)
	job := DataJobDefinition{Key: "throttle-marker-v1", Scope: "test", MigrationID: 1, DataVersion: 1, ExecutionMode: "batch", ValidationMode: "count", ThrottlePerMinute: 1, RestartSafe: true, HandlerKey: "test.throttle-marker.v1", RunCheckpoint: func(context.Context, *gorm.DB, DataJobCheckpoint) (DataJobCheckpointResult, error) {
		return DataJobCheckpointResult{Cursor: "next"}, nil
	}, Validate: func(*gorm.DB) error { return nil }}
	r, err := NewMigrationRunnerWithDataJobs(db, "test", MigrationCompatibility{MinSchema: 0, MaxSchema: 1, MinData: 0, MaxData: 1}, []MigrationDefinition{migration}, []DataJobDefinition{job})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ApplyPending("test"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RunDataJob(context.Background(), job.Key, "test", DataJobCheckpoint{Ordinal: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RunDataJob(context.Background(), job.Key, "test", DataJobCheckpoint{Ordinal: 1}); err == nil {
		t.Fatal("throttle must reject immediate next checkpoint")
	}
}

func TestMigrationDataJobCancellationAndRetryRemainCheckpointBounded(t *testing.T) {
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "data-job-cancel.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() }()
	migration := testMigrationDefinition(1, "test", "data-job schema", MigrationChecksum("data-job-cancel"), 1, "online", "package-only")
	migration.DataVersion, migration.DataJobKey = 1, "cancel-marker-v1"
	migration = FinalizeMigrationDefinition(migration)
	job := DataJobDefinition{Key: "cancel-marker-v1", Scope: "test", MigrationID: 1, DataVersion: 1, ExecutionMode: "batch", ValidationMode: "count", RestartSafe: true, HandlerKey: "test.cancel-marker.v1", RunCheckpoint: func(context.Context, *gorm.DB, DataJobCheckpoint) (DataJobCheckpointResult, error) {
		return DataJobCheckpointResult{RowsScanned: 1}, nil
	}, Validate: func(*gorm.DB) error { return nil }}
	r, err := NewMigrationRunnerWithDataJobs(db, "test", MigrationCompatibility{MinSchema: 0, MaxSchema: 1, MinData: 0, MaxData: 1}, []MigrationDefinition{migration}, []DataJobDefinition{job})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ApplyPending("test"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RunDataJob(context.Background(), job.Key, "test", DataJobCheckpoint{Ordinal: 0}); err != nil {
		t.Fatal(err)
	}
	if err := r.RequestDataJobCancellation(job.Key); err != nil {
		t.Fatal(err)
	}
	status, err := r.RunDataJob(context.Background(), job.Key, "test", DataJobCheckpoint{Ordinal: 1})
	if err != nil {
		t.Fatal(err)
	}
	if status.State != migrationDataJobCancelled {
		t.Fatalf("cancellation must happen at checkpoint: %+v", status)
	}
	if _, err := r.RunDataJob(context.Background(), job.Key, "test", DataJobCheckpoint{Ordinal: 1}); err == nil {
		t.Fatal("cancelled job ran without explicit retry")
	}
	if err := r.RetryDataJob(job.Key, "restore-drill-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RunDataJob(context.Background(), job.Key, "test", DataJobCheckpoint{Ordinal: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationRunnerUpgradesLegacyLedgerBeforeStatusAndApply(t *testing.T) {
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "legacy-ledger.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() }()
	// This is the exact pre-manifest-digest ledger shape. In particular, it
	// already has an applied row, proving Status upgrades the table before it
	// selects into the current ledger record and Apply preserves legacy rows.
	if err := db.Exec(`CREATE TABLE schema_migration_ledger (scope TEXT NOT NULL, migration_id BIGINT NOT NULL, checksum TEXT NOT NULL, state TEXT NOT NULL, started_at TEXT NOT NULL, completed_at TEXT NOT NULL DEFAULT '', runner TEXT NOT NULL DEFAULT '', duration_ms BIGINT NOT NULL DEFAULT 0, error_code TEXT NOT NULL DEFAULT '', error_text TEXT NOT NULL DEFAULT '', PRIMARY KEY (scope, migration_id))`).Error; err != nil {
		t.Fatal(err)
	}
	first := testMigrationDefinition(1, "test", "legacy baseline", MigrationChecksum("legacy baseline"), 1, "online", "package-only")
	second := testMigrationDefinition(2, "test", "new marker", MigrationChecksum("new marker"), 2, "online", "package-only")
	second.Dependencies = []int{first.ID}
	second = FinalizeMigrationDefinition(second)
	if err := db.Exec("INSERT INTO schema_migration_ledger (scope, migration_id, checksum, state, started_at) VALUES (?, ?, ?, ?, ?)", "test", first.ID, first.Checksum, "applied", "2026-07-01T00:00:00Z").Error; err != nil {
		t.Fatal(err)
	}
	r, err := NewMigrationRunner(db, "test", MigrationCompatibility{MinSchema: 0, MaxSchema: 2, MinData: 0, MaxData: 0}, []MigrationDefinition{first, second})
	if err != nil {
		t.Fatal(err)
	}
	status, err := r.Status()
	if err != nil {
		t.Fatalf("status must upgrade a legacy ledger before querying it: %v", err)
	}
	if status.SchemaVersion != 1 || len(status.Pending) != 1 || status.Pending[0].ID != second.ID {
		t.Fatalf("legacy ledger status = %+v", status)
	}
	if !db.Migrator().HasColumn(&migrationLedgerRecord{}, "manifest_digest") {
		t.Fatal("legacy ledger upgrade did not add manifest_digest")
	}
	if err := r.ApplyPending("legacy-upgrade-test"); err != nil {
		t.Fatalf("apply after legacy ledger upgrade: %v", err)
	}
	var records []migrationLedgerRecord
	if err := db.Where("scope = ?", "test").Order("migration_id ASC").Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].ManifestDigest != first.ManifestDigest || records[1].ManifestDigest != second.ManifestDigest {
		t.Fatalf("legacy/new manifest digest values = %+v", records)
	}
}

func TestMigrationRunnerFailsClosedWhenLegacyAppliedLedgerCannotBindManifestDigest(t *testing.T) {
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "legacy-unbound.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() }()
	if err := db.Exec(`CREATE TABLE schema_migration_ledger (scope TEXT NOT NULL, migration_id BIGINT NOT NULL, checksum TEXT NOT NULL, state TEXT NOT NULL, started_at TEXT NOT NULL, completed_at TEXT NOT NULL DEFAULT '', runner TEXT NOT NULL DEFAULT '', duration_ms BIGINT NOT NULL DEFAULT 0, error_code TEXT NOT NULL DEFAULT '', error_text TEXT NOT NULL DEFAULT '', PRIMARY KEY (scope, migration_id))`).Error; err != nil {
		t.Fatal(err)
	}
	d := testMigrationDefinition(1, "test", "baseline", MigrationChecksum("expected"), 1, "online", "package-only")
	if err := db.Exec("INSERT INTO schema_migration_ledger (scope, migration_id, checksum, state, started_at) VALUES (?, ?, ?, ?, ?)", "test", d.ID, MigrationChecksum("tampered"), "applied", "2026-07-01T00:00:00Z").Error; err != nil {
		t.Fatal(err)
	}
	r, err := NewMigrationRunner(db, "test", MigrationCompatibility{MinSchema: 0, MaxSchema: 1, MinData: 0, MaxData: 0}, []MigrationDefinition{d})
	if err != nil {
		t.Fatal(err)
	}
	status, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Compatible || status.State != "incompatible" {
		t.Fatalf("unbound legacy applied ledger must fail closed: %+v", status)
	}
	var rec migrationLedgerRecord
	if err := db.Where("scope = ? AND migration_id = ?", "test", d.ID).First(&rec).Error; err != nil {
		t.Fatal(err)
	}
	if rec.ManifestDigest != "" {
		t.Fatalf("unbound legacy row must not be assigned a manifest digest: %+v", rec)
	}
}

func TestMigrationRunnerStatusRejectsAppliedMigrationWithMissingOrUnappliedDependency(t *testing.T) {
	for _, dependencyState := range []string{"missing", "failed"} {
		t.Run(dependencyState, func(t *testing.T) {
			db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "dependency-ledger.sqlite")})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() }()
			first := testMigrationDefinition(1, "test", "first", MigrationChecksum("first"), 1, "online", "package-only")
			second := testMigrationDefinition(2, "test", "second", MigrationChecksum("second"), 2, "online", "package-only")
			second.Dependencies = []int{first.ID}
			second = FinalizeMigrationDefinition(second)
			r, err := NewMigrationRunner(db, "test", MigrationCompatibility{MinSchema: 0, MaxSchema: 2, MinData: 0, MaxData: 0}, []MigrationDefinition{first, second})
			if err != nil {
				t.Fatal(err)
			}
			if err := r.ensureLedger(); err != nil {
				t.Fatal(err)
			}
			if dependencyState == "failed" {
				if err := db.Create(&migrationLedgerRecord{Scope: "test", MigrationID: first.ID, Checksum: first.Checksum, ManifestDigest: first.ManifestDigest, State: "failed", StartedAt: "2026-07-01T00:00:00Z"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Create(&migrationLedgerRecord{Scope: "test", MigrationID: second.ID, Checksum: second.Checksum, ManifestDigest: second.ManifestDigest, State: "applied", StartedAt: "2026-07-01T00:00:00Z"}).Error; err != nil {
				t.Fatal(err)
			}
			status, err := r.Status()
			if err != nil {
				t.Fatal(err)
			}
			if status.Compatible || status.State != "incompatible" {
				t.Fatalf("applied migration with %s prerequisite must fail closed: %+v", dependencyState, status)
			}
		})
	}
}

func TestMigrationRunnerRequiresExplicitMaintenanceOperation(t *testing.T) {
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "migrations.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() }()
	online := testMigrationDefinition(1, "test", "online marker", MigrationChecksum("online marker"), 1, "online", "package-only")
	maintenance := testMigrationDefinition(2, "test", "maintenance marker", MigrationChecksum("maintenance marker"), 2, "maintenance", "restore-required")
	r, err := NewMigrationRunner(db, "test", MigrationCompatibility{MinSchema: 0, MaxSchema: 2, MinData: 0, MaxData: 0}, []MigrationDefinition{online, maintenance})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ApplyPending("test-online"); err == nil {
		t.Fatal("online runner must stop before a maintenance migration")
	}
	status, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.SchemaVersion != 1 || status.State != "pending" || len(status.Pending) != 1 || status.Pending[0].ID != maintenance.ID {
		t.Fatalf("online runner status = %+v, want only maintenance migration pending", status)
	}
	if err := r.ApplyMaintenancePending("test-maintenance"); err != nil {
		t.Fatalf("explicit maintenance operation: %v", err)
	}
	status, err = r.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if status.SchemaVersion != 2 || status.State != "current" || !status.Compatible {
		t.Fatalf("maintenance operation status = %+v", status)
	}
}

func TestMigrationRunnerFailsClosedForChangedChecksumAndFutureMigration(t *testing.T) {
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "migrations.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() }()
	d := testMigrationDefinition(1, "test", "marker", MigrationChecksum("one"), 1, "online", "package-only")
	r, err := NewMigrationRunner(db, "test", MigrationCompatibility{MinSchema: 0, MaxSchema: 1, MinData: 0, MaxData: 0}, []MigrationDefinition{d})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ApplyPending("runner"); err != nil {
		t.Fatal(err)
	}
	changed := d
	changed.Checksum = MigrationChecksum("changed")
	changedRunner, err := NewMigrationRunner(db, "test", MigrationCompatibility{MinSchema: 0, MaxSchema: 1, MinData: 0, MaxData: 0}, []MigrationDefinition{changed})
	if err != nil {
		t.Fatal(err)
	}
	status, err := changedRunner.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Compatible || status.State != "incompatible" {
		t.Fatalf("checksum mismatch must fail closed: %+v", status)
	}
	if err := db.Create(&migrationLedgerRecord{Scope: "test", MigrationID: 99, Checksum: MigrationChecksum("future"), State: "applied", StartedAt: "2026-01-01T00:00:00Z"}).Error; err != nil {
		t.Fatal(err)
	}
	status, err = r.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Compatible || status.State != "incompatible" {
		t.Fatalf("future migration must fail closed: %+v", status)
	}
}

func TestStrictManifestRejectsMetadataAndHandlerTampering(t *testing.T) {
	d := testMigrationDefinition(1, "usage", "strict marker", MigrationChecksum("strict"), 1, "online", "package-only")
	if _, err := NewMigrationRunner(&gorm.DB{}, "usage", MigrationCompatibility{}, []MigrationDefinition{d}); err != nil {
		t.Fatalf("strict manifest rejected: %v", err)
	}
	tampered := d
	tampered.HandlerKey = "usage.other.v1"
	if _, err := NewMigrationRunner(&gorm.DB{}, "usage", MigrationCompatibility{}, []MigrationDefinition{tampered}); err == nil {
		t.Fatal("handler-key tampering must fail closed")
	}
	tampered = d
	tampered.Name = "changed semantics"
	if _, err := NewMigrationRunner(&gorm.DB{}, "usage", MigrationCompatibility{}, []MigrationDefinition{tampered}); err == nil {
		t.Fatal("canonical metadata tampering must fail closed")
	}
	tampered = d
	tampered.Apply = verifyTestMigrationMarker
	if _, err := NewMigrationRunner(&gorm.DB{}, "usage", MigrationCompatibility{}, []MigrationDefinition{tampered}); err == nil {
		t.Fatal("apply handler pointer swap must fail closed")
	}
	tampered = d
	tampered.Verify = applyTestMigrationMarker
	if _, err := NewMigrationRunner(&gorm.DB{}, "usage", MigrationCompatibility{}, []MigrationDefinition{tampered}); err == nil {
		t.Fatal("verify handler pointer swap must fail closed")
	}
	tampered = d
	tampered.ManifestDigest = ""
	if _, err := NewMigrationRunner(&gorm.DB{}, "usage", MigrationCompatibility{}, []MigrationDefinition{tampered}); err == nil {
		t.Fatal("missing manifest digest must fail closed")
	}
}

func TestStrictManifestRejectsUnknownForwardAndDuplicateDependencies(t *testing.T) {
	// IDs need not be consecutive. The absent lower ID proves validation uses
	// the complete declared ID set rather than only numeric ordering.
	first := testMigrationDefinition(2, "usage", "first", MigrationChecksum("first"), 1, "online", "package-only")
	second := testMigrationDefinition(3, "usage", "second", MigrationChecksum("second"), 2, "online", "package-only")
	for name, dependencies := range map[string][]int{
		"unknown":          {99},
		"unknown_prior_id": {1},
		"forward":          {3},
		"duplicate":        {2, 2},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := second
			candidate.Dependencies = dependencies
			candidate = FinalizeMigrationDefinition(candidate)
			if _, err := NewMigrationRunner(&gorm.DB{}, "usage", MigrationCompatibility{}, []MigrationDefinition{first, candidate}); err == nil {
				t.Fatalf("manifest accepted %s dependency contract", name)
			}
		})
	}
}

func TestMigrationRunnerRequiresAppliedDependencies(t *testing.T) {
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "dependencies.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() }()
	first := testMigrationDefinition(1, "test", "first", MigrationChecksum("first"), 1, "maintenance", "restore-required")
	second := testMigrationDefinition(2, "test", "second", MigrationChecksum("second"), 2, "online", "package-only")
	second.Dependencies = []int{first.ID}
	second = FinalizeMigrationDefinition(second)
	r, err := NewMigrationRunner(db, "test", MigrationCompatibility{MinSchema: 0, MaxSchema: 2, MinData: 0, MaxData: 0}, []MigrationDefinition{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ApplyMaintenancePending("maintenance"); err == nil {
		t.Fatal("maintenance runner must not skip its pending online dependency chain")
	}
	status, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.SchemaVersion != 1 || len(status.Pending) != 1 || status.Pending[0].ID != second.ID {
		t.Fatalf("maintenance run must leave only the dependency-satisfied online migration pending: %+v", status)
	}
	if err := r.ApplyPending("online"); err != nil {
		t.Fatalf("online runner must apply only after its dependency: %v", err)
	}
}

func TestMigrationFrameworkBootstrapsNormalizedScalarContract(t *testing.T) {
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "contract.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() }()
	r, err := NewMigrationRunner(db, "test", MigrationCompatibility{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ensureLedger(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"schema_migration_ledger", "schema_migration_attempts", "schema_data_jobs", "schema_data_job_checkpoints"} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("missing normalized framework table %s", table)
		}
	}
	if !db.Migrator().HasColumn(&migrationLedgerRecord{}, "manifest_digest") {
		t.Fatal("ledger must bind manifest digest")
	}
	if err := db.Exec("INSERT INTO schema_migration_attempts (scope, migration_id, attempt, action, state, started_at) VALUES ('missing', 1, 1, 'apply', 'failed', 'now')").Error; err == nil {
		t.Fatal("migration attempts must be bound to an immutable ledger row")
	}
	if err := db.Exec("INSERT INTO schema_data_jobs (job_id, scope, migration_id, data_version, state, execution_mode, validation_mode, started_at) VALUES ('missing', 'missing', 1, 1, 'pending', 'batch', 'verify', 'now')").Error; err == nil {
		t.Fatal("data jobs must be bound to an immutable ledger row")
	}
}

func TestMigrationRunnerRejectsAnActiveScopeLease(t *testing.T) {
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "migrations.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() }()
	r, err := NewMigrationRunner(db, "test", MigrationCompatibility{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ensureLedger(); err != nil {
		t.Fatal(err)
	}
	if err := r.acquireLock("first"); err != nil {
		t.Fatal(err)
	}
	defer r.releaseLock("first")
	if err := r.acquireLock("second"); err == nil {
		t.Fatal("second runner acquired active scope lease")
	}
}

func TestUsageMigrationAdoptsVerifiedLegacyBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	store, err := OpenUsageStorePath(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	r, closeDB, err := UsageMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	status, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !status.Compatible || status.State != "pending" ||
		len(status.Pending) != len(usageMigrationDefinitions) ||
		len(status.Entries) != 0 || len(status.Jobs) != 0 {
		t.Fatalf("physical legacy schema must remain unapplied until the explicit migration operation: %+v", status)
	}
	if err := r.ApplyPending("test-runner"); err != nil {
		t.Fatal(err)
	}
	status, err = r.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "pending" || len(status.Pending) != 0 || len(status.Jobs) != 1 ||
		status.Jobs[0].Key != "historical-usage-validation-v1" ||
		status.Jobs[0].State != migrationDataJobPending || status.Jobs[0].Present {
		t.Fatalf("explicit apply must adopt the schema and synthesize the required pending data job: %+v", status)
	}
	if _, err := r.RunDataJob(context.Background(), "historical-usage-validation-v1", "test-runner", DataJobCheckpoint{Ordinal: 0}); err != nil {
		t.Fatal(err)
	}
	status, err = r.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if !status.Compatible || status.State != "current" || status.SchemaVersion != usageMigrationCompatibility.MaxSchema || status.DataVersion != usageMigrationCompatibility.MaxData || len(status.Entries) != len(usageMigrationDefinitions) {
		t.Fatalf("unexpected fully migrated usage status: %+v", status)
	}
}

func TestUsageMigrationBaselineBootstrapsEmptyDatabaseExplicitly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.sqlite")
	r, closeDB, err := UsageMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	if err := r.ApplyPending("test-runner"); err != nil {
		t.Fatalf("explicit baseline must initialize an empty database: %v", err)
	}
	if _, err := r.RunDataJob(context.Background(), "historical-usage-validation-v1", "test-runner", DataJobCheckpoint{Ordinal: 0}); err != nil {
		t.Fatal(err)
	}
	status, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !status.Compatible || status.State != "current" || status.SchemaVersion != usageMigrationCompatibility.MaxSchema || len(status.Entries) != len(usageMigrationDefinitions) {
		t.Fatalf("explicit baseline bootstrap status: %+v", status)
	}
}

func TestUsageHistoricalValidationRequiresMultipleCheckpointsBeforeDeploymentJobServing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "historical-validation.sqlite")
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	store := &usageStore{db: db}
	if err := applyUsageExplicitBaseline(store.db); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := applyUsageReasoningTelemetryMigration(store.db); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	for i := 0; i < 101; i++ {
		row := usageRecord{RequestID: fmt.Sprintf("historical-row-%03d", i), TS: "2026-08-05T00:00:00Z"}
		if err := store.db.Omit("TargetRegion", "CachedInputTokens", "CachedInputPricePerMillionUSD").Create(&row).Error; err != nil {
			_ = store.Close()
			t.Fatalf("create synthetic historical usage row %d: %v", i, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	runner, closeDB, err := UsageMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.ApplyPending("historical-validation-test"); err != nil {
		_ = closeDB()
		t.Fatal(err)
	}
	// Apply deliberately does not create or run its bound data job. Verify
	// synthesizes that post-apply/pre-resume pending state, which must block a
	// deployment-job router just like every other non-validated job state.
	status, err := runner.Verify()
	if err != nil {
		_ = closeDB()
		t.Fatal(err)
	}
	if status.State != "pending" || len(status.Jobs) != 1 || status.Jobs[0].State != migrationDataJobPending {
		_ = closeDB()
		t.Fatalf("applied historical validation must synthesize a pending bound job before resume: %+v", status)
	}
	if err := closeDB(); err != nil {
		t.Fatal(err)
	}
	if started, err := OpenUsageStore(UsageDBConfig{Driver: "sqlite", Path: path, MigrationPolicy: usageDBMigrationPolicyDeploymentJob}); err == nil {
		_ = started.Close()
		t.Fatal("deployment-job serving must reject a pending historical validation job")
	}

	runner, closeDB, err = UsageMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunDataJob(context.Background(), "historical-usage-validation-v1", "historical-validation-test", DataJobCheckpoint{Ordinal: 0}); err != nil {
		_ = closeDB()
		t.Fatal(err)
	}
	status, err = runner.Verify()
	if err != nil {
		_ = closeDB()
		t.Fatal(err)
	}
	if status.State != "in-progress" || status.DataVersion != 0 || len(status.Jobs) != 1 || status.Jobs[0].State != migrationDataJobRunning || status.Jobs[0].Checkpoints != 1 || status.Jobs[0].RowsScanned != 100 {
		_ = closeDB()
		t.Fatalf("101 rows must remain unready after ordinal 0: %+v", status)
	}
	// The checked-in job deliberately throttles operational checkpoints. Age the
	// synthetic durable timestamp so this deterministic test can exercise the
	// next permitted checkpoint without sleeping or weakening production limits.
	if err := runner.db.Model(&migrationDataJobCheckpointRecord{}).
		Where("job_id = ? AND checkpoint_ordinal = ?", dataJobID(usageMigrationScope, "historical-usage-validation-v1"), 0).
		Update("started_at", "2026-08-05T00:00:00Z").Error; err != nil {
		_ = closeDB()
		t.Fatal(err)
	}
	if err := closeDB(); err != nil {
		t.Fatal(err)
	}
	if blocked, err := OpenUsageStore(UsageDBConfig{Driver: "sqlite", Path: path, MigrationPolicy: usageDBMigrationPolicyDeploymentJob}); err == nil {
		_ = blocked.Close()
		t.Fatal("deployment-job serving must reject a running historical validation job")
	}

	runner, closeDB, err = UsageMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunDataJob(context.Background(), "historical-usage-validation-v1", "historical-validation-test", DataJobCheckpoint{Ordinal: 1}); err != nil {
		_ = closeDB()
		t.Fatal(err)
	}
	status, err = runner.Verify()
	if err != nil {
		_ = closeDB()
		t.Fatal(err)
	}
	if status.State != "current" || status.DataVersion != 1 || len(status.Jobs) != 1 || status.Jobs[0].State != migrationDataJobValidated || status.Jobs[0].Checkpoints != 2 || status.Jobs[0].RowsScanned != 101 {
		_ = closeDB()
		t.Fatalf("ordinal 1 must validate the remaining historical row: %+v", status)
	}
	if err := closeDB(); err != nil {
		t.Fatal(err)
	}
	ready, err := OpenUsageStore(UsageDBConfig{Driver: "sqlite", Path: path, MigrationPolicy: usageDBMigrationPolicyDeploymentJob})
	if err != nil {
		t.Fatalf("deployment-job serving must accept validated historical usage: %v", err)
	}
	if err := ready.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUsageMigrationServingCompatibilityRequiresValidatedBoundJobs(t *testing.T) {
	tests := []struct {
		name   string
		status MigrationStatus
		want   bool
	}{
		{name: "current without bound jobs", status: MigrationStatus{Compatible: true, State: "current"}, want: true},
		{name: "validated bound job", status: MigrationStatus{Compatible: true, State: "current", Jobs: []MigrationDataJobStatus{{State: migrationDataJobValidated}}}, want: true},
		{name: "synthesized pending bound job", status: MigrationStatus{Compatible: true, State: "current", Jobs: []MigrationDataJobStatus{{State: migrationDataJobPending}}}},
		{name: "running bound job", status: MigrationStatus{Compatible: true, State: "current", Jobs: []MigrationDataJobStatus{{State: migrationDataJobRunning}}}},
		{name: "paused bound job", status: MigrationStatus{Compatible: true, State: "current", Jobs: []MigrationDataJobStatus{{State: migrationDataJobPaused}}}},
		{name: "cancelled bound job", status: MigrationStatus{Compatible: true, State: "current", Jobs: []MigrationDataJobStatus{{State: migrationDataJobCancelled}}}},
		{name: "failed bound job", status: MigrationStatus{Compatible: true, State: "current", Jobs: []MigrationDataJobStatus{{State: migrationDataJobFailed}}}},
		{name: "unrecognized bound job", status: MigrationStatus{Compatible: true, State: "current", Jobs: []MigrationDataJobStatus{{State: "unrecognized"}}}},
		{name: "noncurrent ledger", status: MigrationStatus{Compatible: true, State: "pending", Jobs: []MigrationDataJobStatus{{State: migrationDataJobValidated}}}},
		{name: "incompatible ledger", status: MigrationStatus{Compatible: false, State: "current", Jobs: []MigrationDataJobStatus{{State: migrationDataJobValidated}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := UsageMigrationServingCompatible(tc.status); got != tc.want {
				t.Fatalf("UsageMigrationServingCompatible(%+v) = %t, want %t", tc.status, got, tc.want)
			}
		})
	}
}

func TestUsageMigrationBaselineKeepsReasoningColumnsForTheirOwnDefinition(t *testing.T) {
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "baseline-version.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() }()
	baseline, err := NewMigrationRunner(db, usageMigrationScope, MigrationCompatibility{MinSchema: 0, MaxSchema: 1, MinData: 0, MaxData: 0}, usageMigrationDefinitions[:1])
	if err != nil {
		t.Fatal(err)
	}
	if err := baseline.ApplyPending("baseline-version-test"); err != nil {
		t.Fatal(err)
	}
	for table, columns := range usageReasoningTelemetryColumns {
		for column := range columns {
			if db.Migrator().HasColumn(table, column) {
				t.Fatalf("baseline unexpectedly created v2 column %s.%s", table, column)
			}
		}
	}
	current, err := newUsageMigrationRunner(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.ApplyPending("baseline-version-test"); err != nil {
		t.Fatal(err)
	}
	for table, columns := range usageReasoningTelemetryColumns {
		for column := range columns {
			if !db.Migrator().HasColumn(table, column) {
				t.Fatalf("reasoning migration did not create v2 column %s.%s", table, column)
			}
		}
	}
}

func TestUsageStoreAutoSafeFreshSQLiteValidatesOnceAndRestartSkipsPreflight(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	store, err := OpenUsageStore(UsageDBConfig{Driver: "sqlite", Path: path, MigrationPolicy: usageDBMigrationPolicyAutoSafe})
	if err != nil {
		t.Fatalf("fresh SQLite auto-safe bootstrap: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	runner, closeDB, err := UsageMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	status, err := runner.Verify()
	if err != nil {
		_ = closeDB()
		t.Fatal(err)
	}
	if status.State != "current" || status.DataVersion != 1 || len(status.Jobs) != 1 ||
		status.Jobs[0].State != migrationDataJobValidated || status.Jobs[0].Checkpoints != 1 ||
		status.Jobs[0].RowsScanned != 0 || status.Jobs[0].RowsUpdated != 0 ||
		status.Jobs[0].RowsSkipped != 0 || status.Jobs[0].RowsFailed != 0 {
		_ = closeDB()
		t.Fatalf("fresh auto-safe must persist exactly one validated zero-row checkpoint: %+v", status)
	}
	before := status.Jobs[0]
	if err := runner.db.Create(&usageRecord{RequestID: "post-validation-row", TS: "2026-08-06T00:00:00Z"}).Error; err != nil {
		_ = closeDB()
		t.Fatal(err)
	}
	if err := closeDB(); err != nil {
		t.Fatal(err)
	}

	store, err = OpenUsageStore(UsageDBConfig{Driver: "sqlite", Path: path, MigrationPolicy: usageDBMigrationPolicyAutoSafe})
	if err != nil {
		t.Fatalf("validated restart must skip the zero-row preflight: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	runner, closeDB, err = UsageMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	status, err = runner.Verify()
	if err != nil {
		_ = closeDB()
		t.Fatal(err)
	}
	if len(status.Jobs) != 1 || status.Jobs[0] != before {
		_ = closeDB()
		t.Fatalf("validated restart changed checkpoint count or counters: before=%+v after=%+v", before, status.Jobs)
	}
	if err := closeDB(); err != nil {
		t.Fatal(err)
	}

	for _, policy := range []string{usageDBMigrationPolicyValidate, usageDBMigrationPolicyDeploymentJob} {
		store, err := OpenUsageStore(UsageDBConfig{Driver: "sqlite", Path: path, MigrationPolicy: policy})
		if err != nil {
			t.Fatalf("%s must accept the current verified ledger: %v", policy, err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}

	empty := filepath.Join(t.TempDir(), "empty.sqlite")
	for _, policy := range []string{usageDBMigrationPolicyValidate, usageDBMigrationPolicyDeploymentJob} {
		if store, err := OpenUsageStore(UsageDBConfig{Driver: "sqlite", Path: empty, MigrationPolicy: policy}); err == nil {
			_ = store.Close()
			t.Fatalf("%s must reject an empty database before the deployment job runs", policy)
		}
	}
}

func TestUsageStoreAutoSafeRejectsExistingSQLiteUsageRowsBeforeValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	store, err := OpenUsageStorePath(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&usageRecord{RequestID: "existing-row", TS: "2026-08-05T00:00:00Z"}).Error; err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenUsageStore(UsageDBConfig{Driver: "sqlite", Path: path, MigrationPolicy: usageDBMigrationPolicyAutoSafe}); err == nil {
		_ = store.Close()
		t.Fatal("auto-safe must reject existing usage rows before running validation")
	}

	runner, closeDB, err := UsageMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	status, err := runner.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "pending" || len(status.Jobs) != 1 ||
		status.Jobs[0].State != migrationDataJobPending || status.Jobs[0].Present ||
		status.Jobs[0].Checkpoints != 0 || UsageMigrationServingCompatible(status) {
		t.Fatalf("preflight rejection must leave the required job fresh and non-serving: %+v", status)
	}
}

func TestUsageStoreAutoSafeFailsClosedForDurableNonValidatedJobStates(t *testing.T) {
	for _, state := range []string{
		migrationDataJobPending,
		migrationDataJobRunning,
		migrationDataJobPaused,
		migrationDataJobCancelled,
		migrationDataJobFailed,
		"unknown",
	} {
		t.Run(state, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "usage.sqlite")
			runner, closeDB, err := UsageMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: path})
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.ApplyPending("state-test"); err != nil {
				_ = closeDB()
				t.Fatal(err)
			}
			record := migrationDataJobRecord{
				JobID: dataJobID(usageMigrationScope, "historical-usage-validation-v1"),
				Scope: usageMigrationScope, MigrationID: usageHistoricalValidationMigrationID,
				DataVersion: 1, State: state, ExecutionMode: "batch",
				ValidationMode: "row-addressability", ThrottlePerMinute: 60,
				StartedAt: "2026-08-06T00:00:00Z",
			}
			if err := runner.db.Create(&record).Error; err != nil {
				_ = closeDB()
				t.Fatal(err)
			}
			if err := closeDB(); err != nil {
				t.Fatal(err)
			}

			if store, err := OpenUsageStore(UsageDBConfig{Driver: "sqlite", Path: path, MigrationPolicy: usageDBMigrationPolicyAutoSafe}); err == nil {
				_ = store.Close()
				t.Fatalf("auto-safe served with durable %q data-job state", state)
			}
			runner, closeDB, err = UsageMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: path})
			if err != nil {
				t.Fatal(err)
			}
			defer closeDB()
			var checkpoints int64
			if err := runner.db.Model(&migrationDataJobCheckpointRecord{}).Count(&checkpoints).Error; err != nil {
				t.Fatal(err)
			}
			if checkpoints != 0 {
				t.Fatalf("durable %q state created %d auto-safe checkpoints", state, checkpoints)
			}
		})
	}
}

func TestAutoSafeZeroRowCheckpointFailureFailsClosed(t *testing.T) {
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "auto-safe-failure.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	definition := FinalizeMigrationDefinition(MigrationDefinition{
		ID: 1, Scope: "auto-safe-failure", Name: "test auto-safe failure", Release: "test",
		Checksum: strings.Repeat("a", 64), SchemaVersion: 1, DataVersion: 1,
		Transactional: true, MaintenanceMode: "online", RollbackClass: "restore-required",
		HandlerKey: "test.apply@applyTestMigrationMarker", PostconditionKey: "test.verify@verifyTestMigrationMarker",
		ExecutionMode: "transactional", LockClass: "online", TimeoutClass: "bounded",
		DataJobKey: "failure-job", Apply: applyTestMigrationMarker, Verify: verifyTestMigrationMarker,
	})
	runner, err := NewMigrationRunnerWithDataJobs(db, "auto-safe-failure", MigrationCompatibility{MinSchema: 0, MaxSchema: 1, MinData: 0, MaxData: 1}, []MigrationDefinition{definition}, []DataJobDefinition{{
		Key: "failure-job", Scope: "auto-safe-failure", MigrationID: 1, DataVersion: 1,
		ExecutionMode: "batch", ValidationMode: "test", AutoSafeZeroRow: true,
		RestartSafe: true, HandlerKey: "test.failure-job.v1",
		RunCheckpoint: func(context.Context, *gorm.DB, DataJobCheckpoint) (DataJobCheckpointResult, error) {
			return DataJobCheckpointResult{}, errors.New("deliberate checkpoint failure")
		},
		Validate: verifyTestMigrationMarker,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.ApplyPending("test"); err != nil {
		t.Fatal(err)
	}
	if err := runner.RunAutoSafeZeroRowDataJob(context.Background(), "router-startup", func(*gorm.DB) error { return nil }); err == nil {
		t.Fatal("checkpoint failure must fail auto-safe startup")
	}
	status, err := runner.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "failed" || len(status.Jobs) != 1 ||
		status.Jobs[0].State != migrationDataJobFailed || status.Jobs[0].Checkpoints != 0 ||
		UsageMigrationServingCompatible(status) {
		t.Fatalf("checkpoint failure must remain failed and non-serving: %+v", status)
	}
}

func TestAutoSafeZeroRowNonZeroCountersRollBackValidation(t *testing.T) {
	runner := newAtomicAutoSafeTestRunner(t, filepath.Join(t.TempDir(), "non-zero.sqlite"), func(context.Context, *gorm.DB, DataJobCheckpoint) (DataJobCheckpointResult, error) {
		return DataJobCheckpointResult{RowsScanned: 1, Complete: true}, nil
	})
	if err := runner.RunAutoSafeZeroRowDataJob(context.Background(), "router-startup", requireEmptyUsageStore); err == nil {
		t.Fatal("non-zero auto-safe checkpoint counters must reject validation")
	}
	assertAtomicAutoSafeFailure(t, runner)
	if err := runner.RunAutoSafeZeroRowDataJob(context.Background(), "router-restart", requireEmptyUsageStore); err == nil {
		t.Fatal("failed non-zero auto-safe checkpoint must not become restart-eligible")
	}
}

func TestAutoSafeZeroRowConcurrentInsertBeforeDurableTransitionRollsBackValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent-insert.sqlite")
	checkpointComplete := make(chan struct{})
	persistCheckpoint := make(chan struct{})
	runner := newAtomicAutoSafeTestRunner(t, path, func(context.Context, *gorm.DB, DataJobCheckpoint) (DataJobCheckpointResult, error) {
		close(checkpointComplete)
		<-persistCheckpoint
		return DataJobCheckpointResult{Complete: true}, nil
	})

	runErr := make(chan error, 1)
	go func() {
		runErr <- runner.RunAutoSafeZeroRowDataJob(context.Background(), "router-startup", requireEmptyUsageStore)
	}()
	select {
	case <-checkpointComplete:
	case <-time.After(2 * time.Second):
		t.Fatal("auto-safe checkpoint did not reach the pre-persistence boundary")
	}

	writer, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Exec("INSERT INTO request_usage (request_id) VALUES (?)", "racing-row").Error; err != nil {
		sqlDB, _ := writer.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
		t.Fatalf("insert between checkpoint work and validation transition: %v", err)
	}
	writerSQL, err := writer.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := writerSQL.Close(); err != nil {
		t.Fatal(err)
	}
	close(persistCheckpoint)
	select {
	case err := <-runErr:
		if err == nil {
			t.Fatal("concurrent usage insert must reject auto-safe validation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("auto-safe validation did not finish after the concurrent insert")
	}
	assertAtomicAutoSafeFailure(t, runner)
}

func newAtomicAutoSafeTestRunner(t *testing.T, path string, checkpoint func(context.Context, *gorm.DB, DataJobCheckpoint) (DataJobCheckpointResult, error)) *migrationRunner {
	t.Helper()
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	definition := FinalizeMigrationDefinition(MigrationDefinition{
		ID: 1, Scope: "auto-safe-atomic", Name: "test atomic auto-safe validation", Release: "test",
		Checksum: strings.Repeat("b", 64), SchemaVersion: 1, DataVersion: 1,
		Transactional: true, MaintenanceMode: "online", RollbackClass: "restore-required",
		HandlerKey: "test.atomic.apply.v1@applyAtomicAutoSafeTestSchema", PostconditionKey: "test.atomic.verify.v1@verifyAtomicAutoSafeTestSchema",
		ExecutionMode: "transactional", LockClass: "online", TimeoutClass: "bounded",
		DataJobKey: "atomic-job", Apply: applyAtomicAutoSafeTestSchema, Verify: verifyAtomicAutoSafeTestSchema,
	})
	runner, err := NewMigrationRunnerWithDataJobs(db, "auto-safe-atomic", MigrationCompatibility{MinSchema: 0, MaxSchema: 1, MinData: 0, MaxData: 1}, []MigrationDefinition{definition}, []DataJobDefinition{{
		Key: "atomic-job", Scope: "auto-safe-atomic", MigrationID: 1, DataVersion: 1,
		ExecutionMode: "batch", ValidationMode: "test", AutoSafeZeroRow: true,
		RestartSafe: true, HandlerKey: "test.atomic-job.v1",
		RunCheckpoint: checkpoint,
		Validate:      verifyAtomicAutoSafeTestSchema,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.ApplyPending("test"); err != nil {
		t.Fatal(err)
	}
	return runner
}

func applyAtomicAutoSafeTestSchema(tx *gorm.DB) error {
	return tx.Exec("CREATE TABLE request_usage (request_id TEXT NOT NULL PRIMARY KEY)").Error
}

func verifyAtomicAutoSafeTestSchema(tx *gorm.DB) error {
	if !tx.Migrator().HasTable("request_usage") {
		return errors.New("request_usage table is missing")
	}
	return nil
}

func assertAtomicAutoSafeFailure(t *testing.T, runner *migrationRunner) {
	t.Helper()
	status, err := runner.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "failed" || status.DataVersion != 0 || len(status.Jobs) != 1 ||
		status.Jobs[0].State != migrationDataJobFailed || status.Jobs[0].Checkpoints != 0 ||
		status.Jobs[0].RowsScanned != 0 || UsageMigrationServingCompatible(status) {
		t.Fatalf("rejected auto-safe validation must remain failed without durable counters or checkpoints: %+v", status)
	}
}

func TestUsageReasoningTelemetryMigrationAddsColumnsToAdoptedSchema(t *testing.T) {
	var definition *MigrationDefinition
	for i := range usageMigrationDefinitions {
		if usageMigrationDefinitions[i].ID == usageReasoningTelemetryMigrationID {
			definition = &usageMigrationDefinitions[i]
			break
		}
	}
	if definition == nil || definition.RollbackClass != "restore-required" {
		t.Fatalf("reasoning migration must require database restore for a binary downgrade: %#v", definition)
	}
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	legacy, err := OpenUsageStorePath(path)
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
			t.Fatal(err)
		}
	}
	// OpenUsageStorePath creates only the physical schema. The explicit
	// migration operation must adopt that legacy shape, add the missing
	// reasoning columns, and apply the later data-job ledger migration without
	// running its bound job.
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	runner, closeDB, err := UsageMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	if err := runner.ApplyPending("reasoning-adoption-test"); err != nil {
		t.Fatal(err)
	}
	if err := verifyUsageReasoningTelemetryMigration(runner.db); err != nil {
		t.Fatalf("reasoning migration postcondition: %v", err)
	}
	if err := verifyUsageContentCaptureEncryptionMigration(runner.db); err != nil {
		t.Fatalf("content-capture encryption migration postcondition: %v", err)
	}
	if err := verifyUsageTargetRegionDiagnosticsMigration(runner.db); err != nil {
		t.Fatalf("target-region diagnostics migration postcondition: %v", err)
	}
	if err := verifyUsageCachedInputPricingMigration(runner.db); err != nil {
		t.Fatalf("cached-input pricing migration postcondition: %v", err)
	}
	status, err := runner.Verify()
	if err != nil || !status.Compatible || status.SchemaVersion != 5 || status.DataVersion != 0 || status.State != "pending" || len(status.Jobs) != 1 || status.Jobs[0].Key != "historical-usage-validation-v1" || status.Jobs[0].State != migrationDataJobPending {
		t.Fatalf("schema migrations must reach v5 while the later non-serving data job remains pending: status=%+v err=%v", status, err)
	}
	previousBinary, err := NewMigrationRunner(runner.db, usageMigrationScope, MigrationCompatibility{MinSchema: 0, MaxSchema: 1, MinData: 0, MaxData: 0}, usageMigrationDefinitions[:1])
	if err != nil {
		t.Fatal(err)
	}
	previousStatus, err := previousBinary.Status()
	if err != nil || previousStatus.Compatible || previousStatus.State != "incompatible" {
		t.Fatalf("previous binary must reject the newer ledger and require restore before downgrade: status=%+v err=%v", previousStatus, err)
	}
}

func TestUsageTargetRegionMigrationOwnsColumnAfterV1Baseline(t *testing.T) {
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "target-region-ownership.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	if err := applyUsageExplicitBaseline(db); err != nil {
		t.Fatalf("apply v1 baseline: %v", err)
	}
	if db.Migrator().HasColumn(&usageRecord{}, "TargetRegion") {
		t.Fatal("schema v1 baseline must not create request_usage.target_region")
	}
	if err := applyUsageReasoningTelemetryMigration(db); err != nil {
		t.Fatalf("apply reasoning migration: %v", err)
	}
	if err := applyUsageContentCaptureEncryptionMigration(db); err != nil {
		t.Fatalf("apply content-capture migration: %v", err)
	}
	if err := applyUsageTargetRegionDiagnosticsMigration(db); err != nil {
		t.Fatalf("apply target-region migration: %v", err)
	}
	if !db.Migrator().HasColumn(&usageRecord{}, "TargetRegion") {
		t.Fatal("migration 2026090901 did not create request_usage.target_region")
	}
}

func TestUsageHistoricalValidationMigrationRequiresRestoreForPriorBinary(t *testing.T) {
	var definition *MigrationDefinition
	for i := range usageMigrationDefinitions {
		if usageMigrationDefinitions[i].ID == usageHistoricalValidationMigrationID {
			definition = &usageMigrationDefinitions[i]
			break
		}
	}
	if definition == nil || definition.RollbackClass != "restore-required" {
		t.Fatalf("historical validation migration must require database restore for a binary downgrade: %#v", definition)
	}

	path := filepath.Join(t.TempDir(), "usage.sqlite")
	stage2DB, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	stage2Runner, err := NewMigrationRunner(stage2DB, usageMigrationScope, MigrationCompatibility{MinSchema: 0, MaxSchema: 2, MinData: 0, MaxData: 0}, usageMigrationDefinitions[:2])
	if err != nil {
		t.Fatal(err)
	}
	if err := stage2Runner.ApplyPending("stage2-binary"); err != nil {
		t.Fatal(err)
	}
	stage2SQL, err := stage2DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := stage2SQL.Close(); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	stage3DB, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	mergedStage3Definitions := append([]MigrationDefinition(nil), usageMigrationDefinitions[:3]...)
	mergedStage3Definitions[2].RollbackClass = "package-only"
	mergedStage3Definitions[2].LegacyManifestDigests = nil
	mergedStage3Definitions[2] = FinalizeMigrationDefinition(mergedStage3Definitions[2])
	if len(definition.LegacyManifestDigests) != 1 || mergedStage3Definitions[2].ManifestDigest != definition.LegacyManifestDigests[0] {
		t.Fatalf("corrected Stage 3 definition must explicitly recognize the merged manifest digest: merged=%s legacy=%v", mergedStage3Definitions[2].ManifestDigest, definition.LegacyManifestDigests)
	}
	stage3Runner, err := NewMigrationRunnerWithDataJobs(stage3DB, usageMigrationScope, usageMigrationCompatibility, mergedStage3Definitions, usageDataJobDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	if err := stage3Runner.ApplyPending("merged-stage3-binary"); err != nil {
		t.Fatal(err)
	}
	correctedRunner, err := newUsageMigrationRunner(stage3DB)
	if err != nil {
		t.Fatal(err)
	}
	correctedStatus, err := correctedRunner.Status()
	if err != nil || !correctedStatus.Compatible || correctedStatus.State != "pending" || correctedStatus.SchemaVersion != 2 || correctedStatus.DataVersion != 0 || len(correctedStatus.Entries) != 3 || len(correctedStatus.Jobs) != 1 || correctedStatus.Jobs[0].State != migrationDataJobPending {
		t.Fatalf("corrected binary must accept the already-applied Stage 3 ledger without changing stored data: status=%+v err=%v", correctedStatus, err)
	}
	stage3SQL, err := stage3DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := stage3SQL.Close(); err != nil {
		t.Fatal(err)
	}

	priorDB, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	priorRunner, err := NewMigrationRunner(priorDB, usageMigrationScope, MigrationCompatibility{MinSchema: 0, MaxSchema: 2, MinData: 0, MaxData: 0}, usageMigrationDefinitions[:2])
	if err != nil {
		t.Fatal(err)
	}
	priorStatus, err := priorRunner.Status()
	if err != nil || priorStatus.Compatible || priorStatus.State != "incompatible" {
		t.Fatalf("prior binary must reject the Stage 3 ledger and require restore before downgrade: status=%+v err=%v", priorStatus, err)
	}
	priorSQL, err := priorDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := priorSQL.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, backup, 0o600); err != nil {
		t.Fatal(err)
	}
	restoredDB, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := restoredDB.DB(); _ = sqlDB.Close() }()
	restoredRunner, err := NewMigrationRunner(restoredDB, usageMigrationScope, MigrationCompatibility{MinSchema: 0, MaxSchema: 2, MinData: 0, MaxData: 0}, usageMigrationDefinitions[:2])
	if err != nil {
		t.Fatal(err)
	}
	restoredStatus, err := restoredRunner.Verify()
	if err != nil || !restoredStatus.Compatible || restoredStatus.State != "current" || restoredStatus.SchemaVersion != 2 || restoredStatus.DataVersion != 0 {
		t.Fatalf("restored Stage 2 snapshot must support the prior binary: status=%+v err=%v", restoredStatus, err)
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResponseAffinitySchema12SQLiteUpgrade(t *testing.T) {
	cfg := uncachedSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	store, err := OpenUsageStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&usageRecord{RequestID: "prior-event", Endpoint: "responses", TS: time.Now().UTC().Format(time.RFC3339Nano)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Migrator().DropTable(&responseAffinityRecord{}); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Where("scope = ? AND migration_id = ?", usageMigrationScope, 2026110201).Delete(&migrationAttemptRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Exec("DELETE FROM schema_migration_ledger WHERE scope = ? AND migration_id = ?", usageMigrationScope, 2026110201).Error; err != nil {
		t.Fatal(err)
	}
	if err := verifyUsageEndpointMigration(store.db); err != nil {
		t.Fatalf("version 11 postcondition: %v", err)
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
	if err != nil || before.SchemaVersion != 11 {
		t.Fatalf("before: %+v err=%v", before, err)
	}
	if err := runner.ApplyPending("affinity-schema-test"); err != nil {
		t.Fatal(err)
	}
	if err := runner.ApplyPending("affinity-schema-test"); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	status, err := runner.Status()
	if err != nil || status.SchemaVersion != 12 {
		t.Fatalf("after: %+v err=%v", status, err)
	}
	if err := verifyResponseAffinityMigration(runner.db); err != nil {
		t.Fatal(err)
	}
	var prior usageRecord
	if err := runner.db.First(&prior, "request_id = ?", "prior-event").Error; err != nil || prior.Endpoint != "responses" {
		t.Fatalf("previous row=%+v err=%v", prior, err)
	}
	old, err := NewMigrationRunnerWithDataJobs(runner.db, usageMigrationScope, MigrationCompatibility{MinSchema: 0, MaxSchema: 11, MinData: 0, MaxData: 1}, usageMigrationDefinitions[:len(usageMigrationDefinitions)-1], usageDataJobDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	oldStatus, err := old.Status()
	if err != nil || oldStatus.Compatible || oldStatus.State != "incompatible" {
		t.Fatalf("old binary should reject v12: %+v err=%v", oldStatus, err)
	}
}

func TestResponseAffinitySchema12PostgresUpgrade(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("USAGE_B_POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("USAGE_B_POSTGRES_TEST_DSN required")
	}
	if os.Getenv("USAGE_AFFINITY_DESTRUCTIVE_TEST") != "1" {
		t.Skip("USAGE_AFFINITY_DESTRUCTIVE_TEST=1 required for destructive disposable database test")
	}
	runner, closeDB, err := UsageMigrationRunner(UsageDBConfig{Driver: "postgres", DSN: dsn, MigrationPolicy: usageDBMigrationPolicyAutoSafe})
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	var database string
	if err := runner.db.Raw("SELECT current_database()").Scan(&database).Error; err != nil || database != "usage_b_provenance_339" {
		t.Fatalf("expected disposable database usage_b_provenance_339, got %q: %v", database, err)
	}
	if err := runner.ApplyPending("affinity-schema-test"); err != nil {
		t.Fatal(err)
	}
	if err := runner.ApplyMaintenancePendingWithEvidence("affinity-schema-test", "disposable-empty-test-database"); err != nil {
		t.Fatal(err)
	}
	if err := runner.ApplyPending("affinity-schema-test"); err != nil {
		t.Fatal(err)
	}
	if err := verifyResponseAffinityMigration(runner.db); err != nil {
		t.Fatalf("fresh schema: %v", err)
	}
	requestID := fmt.Sprintf("affinity-prior-%d", time.Now().UnixNano())
	if err := runner.db.Create(&usageRecord{RequestID: requestID, Endpoint: "responses", TS: time.Now().UTC().Format(time.RFC3339Nano)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := runner.db.Migrator().DropTable(&responseAffinityRecord{}); err != nil {
		t.Fatal(err)
	}
	if err := runner.db.Where("scope = ? AND migration_id = ?", usageMigrationScope, 2026110201).Delete(&migrationAttemptRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := runner.db.Exec("DELETE FROM schema_migration_ledger WHERE scope = ? AND migration_id = ?", usageMigrationScope, 2026110201).Error; err != nil {
		t.Fatal(err)
	}
	before, err := runner.Status()
	if err != nil || before.SchemaVersion != 11 {
		t.Fatalf("before: %+v err=%v", before, err)
	}
	if err := verifyUsageEndpointMigration(runner.db); err != nil {
		t.Fatalf("v11: %v", err)
	}
	if err := runner.ApplyPending("affinity-schema-test"); err != nil {
		t.Fatal(err)
	}
	if err := runner.ApplyPending("affinity-schema-test"); err != nil {
		t.Fatal(err)
	}
	if err := verifyResponseAffinityMigration(runner.db); err != nil {
		t.Fatal(err)
	}
	after, err := runner.Status()
	if err != nil || after.SchemaVersion != 12 {
		t.Fatalf("after: %+v err=%v", after, err)
	}
	var prior usageRecord
	if err := runner.db.First(&prior, "request_id = ?", requestID).Error; err != nil || prior.Endpoint != "responses" {
		t.Fatalf("previous row=%+v err=%v", prior, err)
	}
	now := time.Now().UTC()
	row := responseAffinityRecord{OwnerTokenFingerprint: "owner", Tenant: "tenant", Project: "project", ResponseLookupHash: "lookup", UpstreamIDCiphertext: "encrypted", UpstreamIDNonce: "nonce", UpstreamIDKeyID: "key-id", Provider: "provider", AccountRef: "account", Model: "model", ModelGroup: "group", Environment: "test", State: "active", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := runner.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	duplicate := row
	duplicate.ID = 0
	if err := runner.db.Create(&duplicate).Error; err == nil {
		t.Fatal("Postgres accepted duplicate scoped lookup")
	}
	other := row
	other.ID = 0
	other.Tenant = "other-tenant"
	if err := runner.db.Create(&other).Error; err != nil {
		t.Fatalf("different tenant: %v", err)
	}
}

func TestResponseAffinitySchema12ScopedUniqueIndex(t *testing.T) {
	store, err := OpenUsageStore(uncachedSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	row := responseAffinityRecord{OwnerTokenFingerprint: "owner-a", Tenant: "tenant-a", ResponseLookupHash: "lookup", UpstreamIDCiphertext: "encrypted", UpstreamIDNonce: "nonce", UpstreamIDKeyID: "key-id", Provider: "provider", AccountRef: "account", Model: "model", ModelGroup: "group", Project: "project", Environment: "test", State: "active", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := store.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	duplicate := row
	duplicate.ID = 0
	if err := store.db.Create(&duplicate).Error; err == nil {
		t.Fatal("duplicate owner and lookup accepted")
	}
	other := row
	other.ID = 0
	other.OwnerTokenFingerprint = "owner-b"
	if err := store.db.Create(&other).Error; err != nil {
		t.Fatalf("different owner: %v", err)
	}
	other = row
	other.ID = 0
	other.Tenant = "tenant-b"
	if err := store.db.Create(&other).Error; err != nil {
		t.Fatalf("different tenant: %v", err)
	}
	other = row
	other.ID = 0
	other.Project = "project-b"
	if err := store.db.Create(&other).Error; err != nil {
		t.Fatalf("different project: %v", err)
	}
}

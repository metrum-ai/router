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

	"gorm.io/gorm"
)

// testResponseAffinityRow returns a fully populated dormant row. The values are
// synthetic; no test here writes through a serving path because the schema-only
// change enables none.
func testResponseAffinityRow() responseAffinityRecord {
	now := formatUsageTime(time.Now().UTC())
	return responseAffinityRecord{
		OwnerTokenFingerprint: "owner-fingerprint",
		OwnerFingerprintKeyID: "owner-key-1",
		Tenant:                "tenant-a",
		Project:               "project-a",
		Environment:           "production",
		ResponseLookupHash:    "lookup-hash",
		LookupHashKeyID:       "lookup-key-1",
		UpstreamIDCiphertext:  "ciphertext",
		UpstreamIDNonce:       "nonce",
		UpstreamIDKeyID:       "upstream-key-1",
		Provider:              "openai",
		AccountRef:            "account-1",
		Model:                 "gpt-5-mini",
		ModelGroup:            "small",
		ProviderDialect:       "openai-responses",
		Endpoint:              "responses",
		State:                 "in_progress",
		Background:            true,
		UsageSettled:          false,
		SettledAt:             "",
		UpstreamDeleteState:   "not_attempted",
		TombstonedAt:          "",
		LastErrorClass:        "",
		CreatedAt:             now,
		UpdatedAt:             now,
		ExpiresAt:             formatUsageTime(time.Now().UTC().Add(30 * 24 * time.Hour)),
	}
}

// responseAffinityPreviousSchema rebuilds the pre-migration state for the upgrade test: the
// dormant table is dropped and the ledger rows at or after the affinity migration are cleared,
// so the upgrade must create the table again.
func responseAffinityPreviousSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	if db.Migrator().HasTable(&responseAffinityRecord{}) {
		if err := db.Migrator().DropTable(&responseAffinityRecord{}); err != nil {
			t.Fatal(err)
		}
	}
	clearAffinityLedger(t, db)
}

// clearAffinityLedger removes the ledger rows at or after the affinity migration without
// touching tables, which is what the disposable-database reverse step needs.
func clearAffinityLedger(t *testing.T, db *gorm.DB) {
	t.Helper()
	// Attempts reference the ledger row, so remove them first.
	// Clear the ledger rows at or after the affinity migration so the database reads as it
	// did immediately before it. Tables are left in place: dropping them would make the
	// revert assertion vacuous.
	if err := db.Where("scope = ? AND migration_id >= ?", usageMigrationScope, usageResponseAffinityMigrationID).Delete(&migrationAttemptRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("scope = ? AND migration_id >= ?", usageMigrationScope, usageResponseAffinityMigrationID).Delete(&migrationLedgerRecord{}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestResponseAffinitySchema12SQLiteUpgradeAndDown(t *testing.T) {
	cfg := uncachedSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	store, err := OpenUsageStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(recordFromRow(usageRow{RequestID: "prior-event", Endpoint: "responses", TS: time.Now().UTC()})).Error; err != nil {
		t.Fatal(err)
	}
	responseAffinityPreviousSchema(t, store.db)
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
	// 15 is the current usage head after the admin audit and policy-runtime
	// migrations (2026110702/0703/0704); this test pins the whole tail.
	if err != nil || status.SchemaVersion != 15 {
		t.Fatalf("after: %+v err=%v", status, err)
	}
	if err := verifyResponseAffinityMigration(runner.db); err != nil {
		t.Fatal(err)
	}
	var prior usageRecord
	if err := runner.db.First(&prior, "request_id = ?", "prior-event").Error; err != nil || prior.Endpoint != "responses" {
		t.Fatalf("previous row=%+v err=%v", prior, err)
	}
	row := testResponseAffinityRow()
	if err := runner.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}

	// Earlier binaries must fail closed on the future schema instead of
	// ignoring the table.
	old, err := NewMigrationRunnerWithDataJobs(runner.db, usageMigrationScope, MigrationCompatibility{MinSchema: 0, MaxSchema: 11, MinData: 0, MaxData: 1}, usageMigrationDefinitions[:len(usageMigrationDefinitions)-1], usageDataJobDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	oldStatus, err := old.Status()
	if err != nil || oldStatus.Compatible || oldStatus.State != "incompatible" {
		t.Fatalf("old binary should reject v12: %+v err=%v", oldStatus, err)
	}

	// Down: the disposable-database reverse step restores the v11 postcondition,
	// removes local affinity metadata and leaves a database the earlier manifest
	// accepts again.
	if err := revertResponseAffinityMigration(runner.db); err != nil {
		t.Fatal(err)
	}
	// A disposable database restored to the pre-affinity state also has the later ledger rows
	// removed. The table itself is not dropped here: the assertion below already proved the
	// reverse step removed it.
	clearAffinityLedger(t, runner.db)
	if runner.db.Migrator().HasTable(&responseAffinityRecord{}) {
		t.Fatal("reverse step left the response affinity table behind")
	}
	afterDown, err := runner.Status()
	if err != nil || afterDown.SchemaVersion != 11 {
		t.Fatalf("after down: %+v err=%v", afterDown, err)
	}
	oldStatus, err = old.Status()
	if err != nil || !oldStatus.Compatible || oldStatus.SchemaVersion != 11 {
		t.Fatalf("earlier manifest should accept the reversed schema: compatible=%v schema=%d err=%v", oldStatus.Compatible, oldStatus.SchemaVersion, err)
	}
	var stillThere usageRecord
	if err := runner.db.First(&stillThere, "request_id = ?", "prior-event").Error; err != nil || stillThere.Endpoint != "responses" {
		t.Fatalf("reverse step damaged prior usage rows: %+v err=%v", stillThere, err)
	}

	// Up again on the reversed database.
	if err := runner.ApplyPending("affinity-schema-test"); err != nil {
		t.Fatal(err)
	}
	if err := verifyResponseAffinityMigration(runner.db); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
}

func TestResponseAffinitySchema12FreshInstall(t *testing.T) {
	cfg := uncachedSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	runner, closeDB, err := UsageMigrationRunner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	if err := runner.ApplyPending("affinity-fresh-test"); err != nil {
		t.Fatal(err)
	}
	status, err := runner.Status()
	// Fresh installs reach the current usage head (15) including the admin audit
	// and policy-runtime migrations.
	if err != nil || status.SchemaVersion != 15 || !status.Compatible {
		t.Fatalf("fresh install: %+v err=%v", status, err)
	}
	if err := verifyResponseAffinityMigration(runner.db); err != nil {
		t.Fatal(err)
	}
	fresh := testResponseAffinityRow()
	if err := runner.db.Create(&fresh).Error; err != nil {
		t.Fatalf("fresh schema accepted the dormant contract: %v", err)
	}
}

func TestResponseAffinitySchema12ScopedUniqueIndex(t *testing.T) {
	store, err := OpenUsageStore(uncachedSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	row := testResponseAffinityRow()
	if err := store.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	duplicate := row
	duplicate.ID = 0
	if err := store.db.Create(&duplicate).Error; err == nil {
		t.Fatal("duplicate owner and lookup accepted")
	}
	for name, mutate := range map[string]func(*responseAffinityRecord){
		"other owner":       func(r *responseAffinityRecord) { r.OwnerTokenFingerprint = "owner-b" },
		"other tenant":      func(r *responseAffinityRecord) { r.Tenant = "tenant-b" },
		"other project":     func(r *responseAffinityRecord) { r.Project = "project-b" },
		"other environment": func(r *responseAffinityRecord) { r.Environment = "staging" },
		"other lookup":      func(r *responseAffinityRecord) { r.ResponseLookupHash = "lookup-b" },
	} {
		other := row
		other.ID = 0
		mutate(&other)
		if err := store.db.Create(&other).Error; err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// TestResponseAffinitySchema12PostgresUpgradeAndDown runs only against a
// disposable database named for this test. Set:
//
//	SMART_ROUTER_POSTGRES_TEST_DSN=postgres://.../smart_router_issue_665?sslmode=disable
//	SMART_ROUTER_POSTGRES_TEST_ALLOW=issue-665-affinity
func TestResponseAffinitySchema12PostgresUpgradeAndDown(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("SMART_ROUTER_POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("SMART_ROUTER_POSTGRES_TEST_DSN is required for disposable PostgreSQL coverage")
	}
	if os.Getenv("SMART_ROUTER_POSTGRES_TEST_ALLOW") != "issue-665-affinity" || !strings.Contains(strings.ToLower(dsn), "smart_router_issue_665") {
		t.Fatalf("refusing to run against a non-disposable database; set SMART_ROUTER_POSTGRES_TEST_ALLOW=issue-665-affinity and use smart_router_issue_665")
	}
	cfg := UsageDBConfig{Driver: "postgres", DSN: dsn, MigrationPolicy: usageDBMigrationPolicyAutoSafe}
	runner, closeDB, err := UsageMigrationRunner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	if err := runner.ApplyPending("affinity-pg-test"); err != nil {
		t.Fatal(err)
	}
	if err := verifyResponseAffinityMigration(runner.db); err != nil {
		t.Fatalf("fresh schema: %v", err)
	}
	requestID := fmt.Sprintf("affinity-prior-%d", time.Now().UnixNano())
	if err := runner.db.Create(recordFromRow(usageRow{RequestID: requestID, Endpoint: "responses", TS: time.Now().UTC()})).Error; err != nil {
		t.Fatal(err)
	}

	responseAffinityPreviousSchema(t, runner.db)
	before, err := runner.Status()
	if err != nil || before.SchemaVersion != 11 {
		t.Fatalf("before: %+v err=%v", before, err)
	}
	if err := verifyUsageEndpointMigration(runner.db); err != nil {
		t.Fatalf("version 11 postcondition: %v", err)
	}
	if err := runner.ApplyPending("affinity-pg-test"); err != nil {
		t.Fatal(err)
	}
	if err := runner.ApplyPending("affinity-pg-test"); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	after, err := runner.Status()
	// 15 is the current usage head after the admin audit and policy-runtime
	// migrations (2026110702/0703/0704).
	if err != nil || after.SchemaVersion != 15 {
		t.Fatalf("after: %+v err=%v", after, err)
	}
	if err := verifyResponseAffinityMigration(runner.db); err != nil {
		t.Fatal(err)
	}
	var prior usageRecord
	if err := runner.db.First(&prior, "request_id = ?", requestID).Error; err != nil || prior.Endpoint != "responses" {
		t.Fatalf("previous row=%+v err=%v", prior, err)
	}

	row := testResponseAffinityRow()
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

	if err := revertResponseAffinityMigration(runner.db); err != nil {
		t.Fatal(err)
	}
	// A disposable database restored to the pre-affinity state also has the later ledger rows
	// removed. The table itself is not dropped here: the assertion below already proved the
	// reverse step removed it.
	clearAffinityLedger(t, runner.db)
	if runner.db.Migrator().HasTable(&responseAffinityRecord{}) {
		t.Fatal("reverse step left the response affinity table behind")
	}
	afterDown, err := runner.Status()
	if err != nil || afterDown.SchemaVersion != 11 {
		t.Fatalf("after down: %+v err=%v", afterDown, err)
	}
	old, err := NewMigrationRunnerWithDataJobs(runner.db, usageMigrationScope, MigrationCompatibility{MinSchema: 0, MaxSchema: 11, MinData: 0, MaxData: 1}, usageMigrationDefinitions[:len(usageMigrationDefinitions)-1], usageDataJobDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	oldStatus, err := old.Status()
	if err != nil || !oldStatus.Compatible || oldStatus.SchemaVersion != 11 {
		t.Fatalf("earlier manifest should accept the reversed schema: compatible=%v schema=%d err=%v", oldStatus.Compatible, oldStatus.SchemaVersion, err)
	}
	if err := runner.ApplyPending("affinity-pg-test"); err != nil {
		t.Fatal(err)
	}
	if err := verifyResponseAffinityMigration(runner.db); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
}

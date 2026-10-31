// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
)

func exerciseResponseAffinity(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := verifyResponseAffinityMigration(db); err != nil {
		t.Fatal(err)
	}
	if err := applyResponseAffinityMigration(db); err != nil {
		t.Fatalf("idempotent migration: %v", err)
	}
	if err := db.Exec("ALTER TABLE response_affinity DROP COLUMN request_id").Error; err != nil {
		t.Fatal(err)
	}
	if err := verifyResponseAffinityMigration(db); err != nil {
		t.Fatalf("previous affinity schema must remain valid: %v", err)
	}
	if err := verifyResponseAffinityRequestMigration(db); err == nil {
		t.Fatal("missing retention link accepted")
	}
	if err := applyResponseAffinityRequestMigration(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&backgroundQuotaDaily{}, &backgroundQuotaSecond{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ALTER TABLE response_affinity DROP COLUMN billing_snapshot").Error; err != nil {
		t.Fatal(err)
	}
	if err := verifyResponseAffinityRequestMigration(db); err != nil {
		t.Fatalf("version 13 must remain valid: %v", err)
	}
	if err := verifyBackgroundSettlementMigration(db); err == nil {
		t.Fatal("missing settlement schema accepted")
	}
	if err := applyBackgroundSettlementMigration(db); err != nil {
		t.Fatal(err)
	}
	if err := applyBackgroundSettlementMigration(db); err != nil {
		t.Fatalf("settlement migration is not idempotent: %v", err)
	}
	now := time.Now().UTC()
	row := responseAffinityRecord{ResponseID: "visible-id", RequestID: "request-1", UpstreamResponseID: "upstream-id", TargetKey: "provider/account/model", ModelGroup: "group", CallerID: "owner", Project: "project", Tenant: "tenant", Status: "completed", CreatedAt: now.Add(-time.Hour).UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli()}
	if err := insertResponseAffinity(db, row); err != nil {
		t.Fatal(err)
	}
	collision := row
	collision.CallerID = "attacker"
	collision.UpstreamResponseID = "different"
	if err := insertResponseAffinity(db, collision); !errors.Is(err, errResponseAffinityCollision) {
		t.Fatalf("collision not rejected: %v", err)
	}
	for _, caller := range []string{"owner", "attacker", ""} {
		got, err := lookupOwnedResponseAffinity(db, row.ResponseID, caller, now)
		if err != nil || got != nil {
			t.Fatalf("collided response visible got=%v err=%v", got, err)
		}
	}
	row.ResponseID = "unique-id"
	if err := insertResponseAffinity(db, row); err != nil {
		t.Fatal(err)
	}
	got, err := lookupOwnedResponseAffinity(db, row.ResponseID, "owner", now)
	if err != nil || got == nil || got.UpstreamResponseID != "upstream-id" {
		t.Fatalf("owner lookup got=%v err=%v", got, err)
	}
	if changed, err := updateOwnedResponseAffinityStatus(db, row.ResponseID, "attacker", "deleted", now); err != nil || changed {
		t.Fatalf("nonowner changed row: %v %v", changed, err)
	}
	if changed, err := updateOwnedResponseAffinityStatus(db, row.ResponseID, "owner", "cancelled", now); err != nil || !changed {
		t.Fatalf("owner update: %v %v", changed, err)
	}
	got, err = lookupOwnedResponseAffinity(db, row.ResponseID, "owner", now)
	if err != nil || got == nil || got.Status != "cancelled" {
		t.Fatalf("status update failed: %v %v", got, err)
	}
	if changed, err := updateOwnedResponseAffinityStatus(db, row.ResponseID, "owner", "deleted", now); err != nil || !changed {
		t.Fatalf("delete: %v %v", changed, err)
	}
	got, err = lookupOwnedResponseAffinity(db, row.ResponseID, "owner", now)
	if err != nil || got != nil {
		t.Fatalf("deleted response visible: %v %v", got, err)
	}
	expired := row
	expired.ResponseID = "expired-id"
	expired.ExpiresAt = now.UnixMilli()
	if err := insertResponseAffinity(db, expired); err != nil {
		t.Fatal(err)
	}
	got, err = lookupOwnedResponseAffinity(db, expired.ResponseID, "owner", now)
	if err != nil || got != nil {
		t.Fatalf("expired response visible: %v %v", got, err)
	}
	if err := db.Migrator().DropIndex(&responseAffinityRecord{}, "idx_response_affinity_expiry"); err != nil {
		t.Fatal(err)
	}
	if err := verifyResponseAffinityMigration(db); err == nil {
		t.Fatal("missing index accepted")
	}
	if err := applyResponseAffinityMigration(db); err != nil {
		t.Fatal(err)
	}
	exerciseResponseAffinityRetention(t, db)
}

func exerciseResponseAffinityRetention(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Where("1 = 1").Delete(&responseAffinityRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond).Add(500 * time.Microsecond)
	created := now.Add(-10 * 24 * time.Hour)
	row := responseAffinityRecord{ResponseID: "held", RequestID: "request-held", UpstreamResponseID: "upstream", TargetKey: "target", ModelGroup: "group", CallerID: "owner", Status: "completed", CreatedAt: created.UnixMilli(), ExpiresAt: now.Add(-time.Hour).UnixMilli()}
	if err := insertResponseAffinity(db, row); err != nil {
		t.Fatal(err)
	}
	row.ResponseID = "free"
	row.RequestID = "request-free"
	if err := insertResponseAffinity(db, row); err != nil {
		t.Fatal(err)
	}
	row.ResponseID = "old"
	row.CreatedAt = now.Add(-40 * 24 * time.Hour).UnixMilli()
	row.ExpiresAt = now.Add(time.Hour).UnixMilli()
	if err := insertResponseAffinity(db, row); err != nil {
		t.Fatal(err)
	}
	row.ResponseID = "future"
	row.CreatedAt = now.Add(-time.Hour).UnixMilli()
	if err := insertResponseAffinity(db, row); err != nil {
		t.Fatal(err)
	}
	row.ResponseID = "unknown"
	row.CreatedAt = created.UnixMilli()
	row.ExpiresAt = now.Add(-time.Hour).UnixMilli()
	if err := insertResponseAffinity(db, row); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&responseAffinityRecord{}).Where("response_id = ?", "unknown").Update("created_end_ts", "").Error; err != nil {
		t.Fatal(err)
	}
	hold := legalHoldRecord{HoldID: "hold", DataClass: retentionDataClassContentCapture, RequestID: "request-held", Active: true, StartTS: formatUsageTime(created), CreatedAt: formatUsageTime(now)}
	if err := db.Create(&hold).Error; err != nil {
		t.Fatal(err)
	}
	class := RetentionClassConfig{DataClass: retentionDataClassUsageDetail, RetentionDays: 30, BatchSize: 1}
	cutoff := now.Add(-30 * 24 * time.Hour)
	dry, err := runResponseAffinityRetention(db, class, cutoff, true)
	if err != nil {
		t.Fatal(err)
	}
	if dry.CandidateRows != 4 || dry.HeldRows != 2 || dry.EligibleRows != 2 || dry.DeletedRows != 0 {
		t.Fatalf("dry run: %+v", dry)
	}
	for i := 0; i < 2; i++ {
		result, err := runResponseAffinityRetention(db, class, cutoff, false)
		if err != nil || result.DeletedRows != 1 {
			t.Fatalf("batch %d: %+v %v", i, result, err)
		}
	}
	result, err := runResponseAffinityRetention(db, class, cutoff, false)
	if err != nil || result.DeletedRows != 0 || result.HeldRows != 2 {
		t.Fatalf("hold protection: %+v %v", result, err)
	}
	if err := db.Model(&legalHoldRecord{}).Where("hold_id = ?", "hold").Update("active", false).Error; err != nil {
		t.Fatal(err)
	}
	result, err = runResponseAffinityRetention(db, class, cutoff, false)
	if err != nil || result.DeletedRows != 1 || result.HeldRows != 1 {
		t.Fatalf("released hold: %+v %v", result, err)
	}
}

func TestResponseStoreRetentionValidation(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  time.Duration
		bad   bool
	}{
		{"", 30 * 24 * time.Hour, false}, {"30d", 30 * 24 * time.Hour, false}, {"12h", 12 * time.Hour, false}, {"1ms", time.Millisecond, false},
		{"0", 0, true}, {"-1d", 0, true}, {"1ns", 0, true}, {"3661d", 0, true}, {"999999999999999999d", 0, true}, {"banana", 0, true},
	} {
		got, err := responseStoreRetention(ResponsesStoreConfig{Retention: tc.value})
		if (err != nil) != tc.bad || (!tc.bad && got != tc.want) {
			t.Fatalf("retention %q: %v %v", tc.value, got, err)
		}
	}
}

func TestResponseAffinityTargetIdentity(t *testing.T) {
	provider := ProviderConfig{BaseURL: "https://example.test/v1", KeyID: "account-a"}
	target := Target{Provider: "p", Model: "served-model", Weight: 1}
	key, err := responseAffinityTargetKey("p", provider, target)
	if err != nil || len(key) != 64 {
		t.Fatalf("target identity: %q %v", key, err)
	}
	target.Weight = 10
	if same, err := responseAffinityTargetKey("p", provider, target); err != nil || same != key {
		t.Fatal("weight must not affect target identity")
	}
	for _, altered := range []ProviderConfig{{BaseURL: "https://example.test/v1", KeyID: "account-b"}, {BaseURL: "https://other.test/v1", KeyID: "account-a"}} {
		if changed, err := responseAffinityTargetKey("p", altered, target); err != nil || changed == key {
			t.Fatal("changed account or endpoint was not isolated")
		}
	}
	provider.KeyID = ""
	if _, err := responseAffinityTargetKey("p", provider, target); err == nil {
		t.Fatal("missing key_id must fail closed")
	}
	provider.KeyID = "account-a"
	provider.BaseURL = "https://user:pass@example.test/v1"
	if _, err := responseAffinityTargetKey("p", provider, target); err == nil {
		t.Fatal("credential-bearing URL must not become a target identity")
	}
}

func TestResponseAffinitySQLiteMigrationAndOwnership(t *testing.T) {
	store, err := OpenUsageStore(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "usage.sqlite"), MigrationPolicy: usageDBMigrationPolicyAutoSafe})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	exerciseResponseAffinity(t, store.db)
}

func TestResponseAffinityPostgresMigrationAndOwnership(t *testing.T) {
	_, cleanup, schema := configAdminPostgresTestDB(t)
	defer cleanup()
	dsn := pgDSNWithSearchPath(os.Getenv("ROUTER_TEST_PG_DSN"), schema)
	r, closeDB, err := UsageMigrationRunner(UsageDBConfig{Driver: "postgres", DSN: dsn, MigrationPolicy: usageDBMigrationPolicyAutoSafe})
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	if err := r.ApplyPending("response-affinity-test"); err != nil {
		t.Fatal(err)
	}
	exerciseResponseAffinity(t, r.db)
}

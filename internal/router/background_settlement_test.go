// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
)

func exerciseBackgroundSettlement(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Now().UTC()
	snapshot, err := json.Marshal(backgroundBillingSnapshot{Provider: "mock", Model: "m", InputPrice: 1, OutputPrice: 2})
	if err != nil {
		t.Fatal(err)
	}
	row := responseAffinityRecord{ResponseID: "background-id", RequestID: "background-create", UpstreamResponseID: "provider-id", TargetKey: "key", ModelGroup: "group", CallerID: "owner", Project: "project", Background: true, Status: "in_progress", CreatedAt: now.Add(-time.Minute).UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli(), BillingSnapshot: string(snapshot)}
	if err := insertResponseAffinity(db, row); err != nil {
		t.Fatal(err)
	}
	usage := Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
	if changed, err := settleBackgroundResponse(context.Background(), db, row.ResponseID, "other", "completed", usage, now); err != nil || changed {
		t.Fatalf("non-owner settlement: %v %v", changed, err)
	}
	var success atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			changed, err := settleBackgroundResponse(context.Background(), db, row.ResponseID, "owner", "completed", usage, now)
			if err != nil {
				errs <- err
			}
			if changed {
				success.Add(1)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if !strings.Contains(strings.ToLower(err.Error()), "locked") && !strings.Contains(strings.ToLower(err.Error()), "busy") {
			t.Fatalf("unexpected concurrent failure: %v", err)
		}
	}
	if success.Load() != 1 {
		t.Fatalf("settled %d times", success.Load())
	}
	if changed, err := settleBackgroundResponse(context.Background(), db, row.ResponseID, "owner", "completed", usage, now); err != nil || changed {
		t.Fatalf("duplicate: %v %v", changed, err)
	}
	var daily backgroundQuotaDaily
	if err := db.Where("caller_id = ? AND day = ?", "owner", now.Format("2006-01-02")).Take(&daily).Error; err != nil || daily.Tokens != 15 {
		t.Fatalf("daily %+v %v", daily, err)
	}
	var second backgroundQuotaSecond
	if err := db.Where("caller_id = ? AND second = ?", "owner", now.Unix()).Take(&second).Error; err != nil || second.Tokens != 15 {
		t.Fatalf("second %+v %v", second, err)
	}
	var audit []usageRecord
	if err := db.Where("endpoint = ?", "responses.background_settlement").Find(&audit).Error; err != nil || len(audit) != 1 || audit[0].InputTokens != 10 || audit[0].OutputTokens != 5 || audit[0].TotalCostUSD < 0.00001999 || audit[0].TotalCostUSD > 0.00002001 {
		t.Fatalf("audit %+v %v", audit, err)
	}
	// A duplicate audit key forces a transaction failure. The settled flag and
	// aggregates must roll back, allowing a later retry to succeed.
	row.ResponseID = "rollback-id"
	row.UpstreamResponseID = "provider-id-2"
	if err := insertResponseAffinity(db, row); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(row.ResponseID + "\x00" + row.RequestID))
	conflict := *recordFromRow(usageRow{TS: now, RequestID: "background-" + hex.EncodeToString(sum[:]), CallerID: "owner", Cache: "bypass", Status: 200})
	if err := db.Create(&conflict).Error; err != nil {
		t.Fatal(err)
	}
	if changed, err := settleBackgroundResponse(context.Background(), db, row.ResponseID, "owner", "cancelled", usage, now); err == nil || changed {
		t.Fatalf("expected rollback: %v %v", changed, err)
	}
	var after responseAffinityRecord
	if err := db.Where("response_id = ?", row.ResponseID).Take(&after).Error; err != nil || after.UsageSettled {
		t.Fatalf("settled despite rollback: %+v %v", after, err)
	}
	if err := db.Where("request_id = ?", conflict.RequestID).Delete(&usageRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	if changed, err := settleBackgroundResponse(context.Background(), db, row.ResponseID, "owner", "cancelled", usage, now); err != nil || !changed {
		t.Fatalf("retry: %v %v", changed, err)
	}
	if err := db.Where("caller_id = ? AND day = ?", "owner", now.Format("2006-01-02")).Take(&daily).Error; err != nil || daily.Tokens != 30 {
		t.Fatalf("aggregate retry %+v %v", daily, err)
	}
	// A provider ID reused after physical retention purge belongs to a new
	// creation and must not collide with the old settlement audit key.
	if err := db.Where("response_id = ?", row.ResponseID).Delete(&responseAffinityRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	row.RequestID = "different-creation"
	if err := insertResponseAffinity(db, row); err != nil {
		t.Fatal(err)
	}
	if changed, err := settleBackgroundResponse(context.Background(), db, row.ResponseID, "owner", "completed", usage, now); err != nil || !changed {
		t.Fatalf("reused ID settlement: %v %v", changed, err)
	}
	if err := db.Where("caller_id = ? AND day = ?", "owner", now.Format("2006-01-02")).Take(&daily).Error; err != nil || daily.Tokens != 45 {
		t.Fatalf("reused ID aggregate %+v %v", daily, err)
	}
}

func TestBackgroundSettlementSQLite(t *testing.T) {
	store, err := OpenUsageStore(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "usage.sqlite"), MigrationPolicy: usageDBMigrationPolicyAutoSafe})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	exerciseBackgroundSettlement(t, store.db)
}

func TestBackgroundSettlementPostgres(t *testing.T) {
	_, cleanup, schema := configAdminPostgresTestDB(t)
	defer cleanup()
	dsn := pgDSNWithSearchPath(os.Getenv("ROUTER_TEST_PG_DSN"), schema)
	r, closeDB, err := UsageMigrationRunner(UsageDBConfig{Driver: "postgres", DSN: dsn, MigrationPolicy: usageDBMigrationPolicyAutoSafe})
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	if err := r.ApplyPending("background-settlement-test"); err != nil {
		t.Fatal(err)
	}
	exerciseBackgroundSettlement(t, r.db)
}

func TestBackgroundSettlementRejectsInvalidUsage(t *testing.T) {
	if _, err := settleBackgroundResponse(context.Background(), nil, "id", "owner", "completed", Usage{}, time.Now()); err == nil {
		t.Fatal("nil database accepted")
	}
	for _, value := range []Usage{{InputTokens: -1}, {OutputTokens: 2_000_000_000}, {InputTokens: 10, OutputTokens: 5, TotalTokens: 1}} {
		if _, err := settleBackgroundResponse(context.Background(), &gorm.DB{}, "id", "owner", "completed", value, time.Now()); err == nil {
			t.Fatal(fmt.Sprintf("invalid usage accepted: %+v", value))
		}
	}
}

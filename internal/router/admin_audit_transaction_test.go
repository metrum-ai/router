// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
)

type auditTestSQLiteError int

func (e auditTestSQLiteError) Error() string { return fmt.Sprintf("sqlite result %d", e) }
func (e auditTestSQLiteError) Code() int     { return int(e) }

func TestAuditTransactionRetriesOnlyRolledBackContention(t *testing.T) {
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "audit.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.Exec("CREATE TABLE mutations (id TEXT PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	// Check the actual settings on two independently leased connections.
	for range 2 {
		conn, err := pool.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		var timeout int
		if err := conn.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&timeout); err != nil || timeout <= 0 {
			t.Fatalf("SQLite busy_timeout=%d err=%v", timeout, err)
		}
	}
	for _, tc := range []struct {
		name                           string
		code, failures, attempts, rows int
	}{
		{"busy snapshot then success", 517, 1, 2, 1},
		{"locked until exhausted", 6, 5, 5, 0},
		{"other database failure", 19, 1, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempts := 0
			err := auditTransaction(db, func(tx *gorm.DB) error {
				attempts++
				if err := tx.Exec("INSERT INTO mutations (id) VALUES (?)", tc.name).Error; err != nil {
					return err
				}
				if attempts <= tc.failures {
					return fmt.Errorf("append audit: %w", auditTestSQLiteError(tc.code))
				}
				return nil
			})
			if attempts != tc.attempts || (tc.rows == 1 && err != nil) || (tc.rows == 0 && !errors.Is(err, auditTestSQLiteError(tc.code))) {
				t.Fatalf("attempts=%d err=%v", attempts, err)
			}
			var count int64
			if err := db.Table("mutations").Where("id = ?", tc.name).Count(&count).Error; err != nil || count != int64(tc.rows) {
				t.Fatalf("committed mutations=%d err=%v", count, err)
			}
		})
	}
}

func TestAuditTransactionWriterDeadline(t *testing.T) {
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "held.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := ensureAdminAuditChainAnchors(db); err != nil {
		t.Fatal(err)
	}
	holder := db.Begin()
	if holder.Error != nil {
		t.Fatal(holder.Error)
	}
	defer holder.Rollback()
	if err := holder.Exec("UPDATE router_admin_audit_chain_anchors SET head_hash=head_hash WHERE 0").Error; err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err = auditTransaction(db.WithContext(ctx), func(tx *gorm.DB) error { t.Error("write entered despite held reservation"); return nil })
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("writer deadline: elapsed=%v err=%v", time.Since(start), err)
	}
}

func TestRestoreFailureAuditAfterDeadline(t *testing.T) {
	db := adminUsageAuditTestStore(t).db
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	record := AdminAuditRecord{EventID: "expired-restore", Domain: AdminGlobalDomain, Object: "admin:restore", Action: "write"}
	err := restoreControlPlaneBackupWithAudit(ctx, configAdminTestDB(t), db, controlPlaneBackupBundle{}, &record)
	if err == nil {
		t.Fatal("invalid restore succeeded")
	}
	var count int64
	if err := db.Table(usageAdminAuditTable).Where("event_id = ? AND outcome = ? AND reason_code = ?", record.EventID, "failed", "invalid_backup").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("failure audit rows=%d err=%v", count, err)
	}
}

func TestAuditedMutationWaitsForSQLiteWriter(t *testing.T) {
	store := adminUsageAuditTestStore(t)
	holder := store.db.Begin()
	defer holder.Rollback()
	if err := holder.Exec("UPDATE router_admin_audit_chain_anchors SET head_hash=head_hash WHERE 0").Error; err != nil {
		t.Fatal(err)
	}
	released := make(chan error, 1)
	go func() { time.Sleep(2500 * time.Millisecond); released <- holder.Rollback().Error }()
	const id = "after-writer-release"
	err := store.CreateTrainingOutcomeAudited(contentCaptureRecord{RequestID: id}, AdminAuditRecord{EventID: id, Domain: AdminGlobalDomain, Object: "content:training", Action: "label"})
	if releaseErr := <-released; releaseErr != nil {
		t.Fatal(releaseErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := store.db.Table(usageAdminAuditTable).Where("event_id = ?", id).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("audit rows=%d err=%v", count, err)
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
//go:build authzpostgres

package router

import (
	"context"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"os"
	"testing"
	"time"
)

func TestPolicyPostgresNotificationCommitAndScope(t *testing.T) {
	dsn := os.Getenv("AUTHZ_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("AUTHZ_TEST_POSTGRES_DSN required for explicit postgres acceptance")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal("open disposable postgres failed")
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan struct{}, 16)
	stop, err := StartConfigRevisionNotifier(ctx, db, "admin-policy", func() {
		select {
		case events <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	// The existing API returns after connect, before its goroutine's LISTEN. Use
	// actual server activity as readiness, not a fixed delay or a lost probe.
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var count int64
		if err := db.Raw("SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND query = ? AND state = 'idle'", "LISTEN "+configControlPlaneNotifyChannel).Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("dedicated PostgreSQL LISTEN did not become ready")
		case <-tick.C:
		}
	}
	noEvent := func() {
		t.Helper()
		select {
		case <-events:
			t.Fatal("notification delivered outside matching committed transaction")
		case <-time.After(100 * time.Millisecond):
		}
	}
	event := func() {
		t.Helper()
		select {
		case <-events:
		case <-time.After(3 * time.Second):
			t.Fatal("committed matching notification was not delivered")
		}
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	queueConfigRevisionNotification(tx, "admin-policy")
	noEvent() // Listener is live, but transaction is uncommitted.
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	noEvent() // Rollback must discard the queued hint.
	if err := db.Transaction(func(tx *gorm.DB) error { queueConfigRevisionNotification(tx, "unrelated-policy-scope"); return nil }); err != nil {
		t.Fatal(err)
	}
	noEvent() // The shared channel must respect the policy scope.
	if err := db.Transaction(func(tx *gorm.DB) error { queueConfigRevisionNotification(tx, "admin-policy"); return nil }); err != nil {
		t.Fatal(err)
	}
	event()
	noEvent() // Exactly one matching committed hint, no leaked rollback.
}

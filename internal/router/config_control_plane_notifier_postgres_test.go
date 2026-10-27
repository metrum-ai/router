// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// PostgreSQL LISTEN/NOTIFY coverage for the config revision notifier (#411):
// activation notifies replicas in about one second, the notifier filters
// other scopes' payloads, and when the notifier connection drops the poller
// still converges the replica. Gated on ROUTER_TEST_PG_DSN (a disposable
// PostgreSQL); skipped by default. Each run uses a fresh schema and drops it
// afterwards.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
)

// configRevisionNotifierPostgresDB mirrors configAdminPostgresTestDB for the
// notifier tests.
func configRevisionNotifierPostgresDB(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	db, cleanup, _ := configAdminPostgresTestDB(t)
	return db, cleanup
}

func TestConfigRevisionNotifierReloadsOnActivate(t *testing.T) {
	db, cleanup := configRevisionNotifierPostgresDB(t)
	defer cleanup()

	configControlPlaneSeedSet(t, db, "set-A", "staging", "alpha", "active", "valid")

	var reloads atomic.Int32
	first := make(chan struct{}, 1)
	stop, err := StartConfigRevisionNotifier(context.Background(), db, "staging", func() {
		reloads.Add(1)
		select {
		case first <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatalf("notifier start: %v", err)
	}
	defer stop()

	// Give LISTEN time to register before the activate.
	time.Sleep(300 * time.Millisecond)

	configControlPlaneSeedSet(t, db, "set-B", "staging", "beta", "draft", "valid")
	start := time.Now()
	if err := ActivateConfigSet(db, "staging", "set-B", "tester"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first:
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("notifier reload took %v, want about 1s", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("notifier did not reload within 3s of activation")
	}
}

func TestConfigRevisionNotifierFiltersOtherScopes(t *testing.T) {
	db, cleanup := configRevisionNotifierPostgresDB(t)
	defer cleanup()

	configControlPlaneSeedSet(t, db, "set-A", "staging", "alpha", "active", "valid")
	configControlPlaneSeedSet(t, db, "set-P", "prod", "gamma", "active", "valid")

	var stagingReloads atomic.Int32
	stop, err := StartConfigRevisionNotifier(context.Background(), db, "staging", func() {
		stagingReloads.Add(1)
	})
	if err != nil {
		t.Fatalf("notifier start: %v", err)
	}
	defer stop()
	time.Sleep(300 * time.Millisecond)

	// A prod-scope activation must not reload the staging listener.
	configControlPlaneSeedSet(t, db, "set-P2", "prod", "delta", "draft", "valid")
	if err := ActivateConfigSet(db, "prod", "set-P2", "tester"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if got := stagingReloads.Load(); got != 0 {
		t.Fatalf("staging listener fired on a prod notification (%d times)", got)
	}

	// The staging scope still reloads on its own notification.
	configControlPlaneSeedSet(t, db, "set-B", "staging", "beta", "draft", "valid")
	if err := ActivateConfigSet(db, "staging", "set-B", "tester"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && stagingReloads.Load() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if stagingReloads.Load() == 0 {
		t.Fatal("staging listener did not reload on its own scope's notification")
	}
}

func TestConfigRevisionNotifierPollerFallbackAfterNotifierStop(t *testing.T) {
	db, cleanup := configRevisionNotifierPostgresDB(t)
	defer cleanup()

	configControlPlaneSeedSet(t, db, "set-A", "staging", "alpha", "active", "valid")

	var reloads atomic.Int32
	stopNotify, err := StartConfigRevisionNotifier(context.Background(), db, "staging", func() { reloads.Add(1) })
	if err != nil {
		t.Fatalf("notifier start: %v", err)
	}
	stopPoll, err := StartConfigRevisionPoller(context.Background(), db, "staging", 100*time.Millisecond, func() { reloads.Add(1) })
	if err != nil {
		t.Fatalf("poller start: %v", err)
	}
	defer stopPoll()
	time.Sleep(300 * time.Millisecond)

	// Drop the notifier connection: convergence must now come from the
	// poller alone (#411 acceptance).
	stopNotify()
	time.Sleep(200 * time.Millisecond)
	before := reloads.Load()

	configControlPlaneSeedSet(t, db, "set-B", "staging", "beta", "draft", "valid")
	if err := ActivateConfigSet(db, "staging", "set-B", "tester"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && reloads.Load() == before {
		time.Sleep(20 * time.Millisecond)
	}
	if reloads.Load() == before {
		t.Fatal("no reload after notifier stop: poller fallback did not converge the replica")
	}
}



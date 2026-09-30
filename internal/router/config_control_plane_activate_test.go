// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

// configControlPlaneSeedSet inserts a minimal config set for a given id and
// status. It returns when the row is persisted.
func configControlPlaneSeedSet(t *testing.T, db *gorm.DB, id, runtimeScope, name, status, validationStatus string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.Exec(
		`INSERT INTO router_config_sets (id, runtime_scope, name, status, validation_status, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, runtimeScope, name, status, validationStatus, now,
	).Error; err != nil {
		t.Fatal(err)
	}
}

func TestActivateConfigSetSwitchesActiveSetAndIncrementsRevision(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsForTest(t, r)

	configControlPlaneSeedSet(t, r.db, "set-A", "staging", "alpha", "active", "valid")
	configControlPlaneSeedSet(t, r.db, "set-B", "staging", "beta", "draft", "valid")

	if err := ActivateConfigSet(r.db, "staging", "set-A", "tester"); err != nil {
		t.Fatalf("initial activate set-A: %v", err)
	}
	if err := ActivateConfigSet(r.db, "staging", "set-B", "tester"); err != nil {
		t.Fatalf("activate set-B: %v", err)
	}

	var aStatus, bStatus string
	if err := r.db.Raw(`SELECT status FROM router_config_sets WHERE id = ?`, "set-A").Row().Scan(&aStatus); err != nil {
		t.Fatal(err)
	}
	if err := r.db.Raw(`SELECT status FROM router_config_sets WHERE id = ?`, "set-B").Row().Scan(&bStatus); err != nil {
		t.Fatal(err)
	}
	if aStatus != "superseded" {
		t.Fatalf("previous active set status = %q, want superseded", aStatus)
	}
	if bStatus != "active" {
		t.Fatalf("new active set status = %q, want active", bStatus)
	}

	var revision int64
	if err := r.db.Raw(`SELECT config_revision FROM router_config_runtime WHERE runtime_scope = ?`, "staging").Row().Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 2 {
		t.Fatalf("revision = %d, want 2", revision)
	}

	var activeSetID string
	if err := r.db.Raw(`SELECT active_set_id FROM router_config_runtime WHERE runtime_scope = ?`, "staging").Row().Scan(&activeSetID); err != nil {
		t.Fatal(err)
	}
	if activeSetID != "set-B" {
		t.Fatalf("active_set_id = %q, want set-B", activeSetID)
	}

	var action, actor string
	var eventRevision int64
	row := r.db.Raw(`SELECT action, actor, config_revision FROM router_config_change_events WHERE runtime_scope = ? ORDER BY at DESC LIMIT 1`, "staging").Row()
	if err := row.Scan(&action, &actor, &eventRevision); err != nil {
		t.Fatal(err)
	}
	if action != "activate" || actor != "tester" || eventRevision != revision {
		t.Fatalf("change event = action=%q actor=%q revision=%d, want activate/tester/%d", action, actor, eventRevision, revision)
	}
}

func TestActivateConfigSetRejectsNonValidTarget(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsForTest(t, r)

	configControlPlaneSeedSet(t, r.db, "set-A", "staging", "alpha", "active", "valid")
	configControlPlaneSeedSet(t, r.db, "set-invalid", "staging", "beta", "draft", "invalid")

	err = ActivateConfigSet(r.db, "staging", "set-invalid", "tester")
	if err == nil || !strings.Contains(err.Error(), "validation_status") {
		t.Fatalf("activate must reject non-valid set, got %v", err)
	}

	var status string
	if err := r.db.Raw(`SELECT status FROM router_config_sets WHERE id = ?`, "set-A").Row().Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Fatalf("set-A status = %q, want active after rejected activate", status)
	}
}

func TestRollbackConfigSetReactivatesPriorSetAndUsesRollbackAction(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsForTest(t, r)

	configControlPlaneSeedSet(t, r.db, "set-A", "staging", "alpha", "superseded", "valid")
	configControlPlaneSeedSet(t, r.db, "set-B", "staging", "beta", "active", "valid")

	if err := RollbackConfigSet(r.db, "staging", "set-A", "operator"); err != nil {
		t.Fatalf("rollback to set-A: %v", err)
	}

	var aStatus, bStatus string
	if err := r.db.Raw(`SELECT status FROM router_config_sets WHERE id = ?`, "set-A").Row().Scan(&aStatus); err != nil {
		t.Fatal(err)
	}
	if err := r.db.Raw(`SELECT status FROM router_config_sets WHERE id = ?`, "set-B").Row().Scan(&bStatus); err != nil {
		t.Fatal(err)
	}
	if aStatus != "active" {
		t.Fatalf("rollback target status = %q, want active", aStatus)
	}
	if bStatus != "superseded" {
		t.Fatalf("previously active status = %q, want superseded", bStatus)
	}

	var action string
	if err := r.db.Raw(`SELECT action FROM router_config_change_events WHERE runtime_scope = ? ORDER BY at DESC LIMIT 1`, "staging").Row().Scan(&action); err != nil {
		t.Fatal(err)
	}
	if action != "rollback" {
		t.Fatalf("rollback action recorded = %q, want rollback", action)
	}
}

func TestConfigSnapshotStoreAndGet(t *testing.T) {
	snap := NewConfigSnapshot()
	if got := snap.Get(); got != nil {
		t.Fatalf("initial Get = %v, want nil", got)
	}
	if got := snap.Load(); got != nil {
		t.Fatalf("initial Load = %v, want nil", got)
	}

	first := &Config{Server: ServerConfig{Listen: ":8080"}}
	snap.Store(first)
	if got := snap.Get(); got != first {
		t.Fatalf("Get after first Store = %p, want %p", got, first)
	}

	second := &Config{Server: ServerConfig{Listen: ":9090"}}
	snap.Store(second)
	if got := snap.Load(); got != second {
		t.Fatalf("Load after second Store = %p, want %p", got, second)
	}
	if snap.Get() == first {
		t.Fatal("snapshot retained stale pointer after Store")
	}

	snap.Store(nil)
	if got := snap.Get(); got != nil {
		t.Fatalf("Get after nil Store = %v, want nil", got)
	}
}

func TestConfigRevisionPollerCallsReloadOnRevisionChange(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsForTest(t, r)

	configControlPlaneSeedSet(t, r.db, "set-A", "staging", "alpha", "active", "valid")

	reloads := 0
	stop, err := StartConfigRevisionPoller(context.Background(), r.db, "staging", 10*time.Millisecond, func() {
		reloads++
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	time.Sleep(40 * time.Millisecond)

	configControlPlaneSeedSet(t, r.db, "set-B", "staging", "beta", "draft", "valid")
	if err := ActivateConfigSet(r.db, "staging", "set-B", "tester"); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if reloads > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if reloads == 0 {
		t.Fatal("poller did not call reload after revision changed")
	}
}

func TestStartConfigRevisionNotifierNoOpsOnSQLite(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()

	stop, err := StartConfigRevisionNotifier(context.Background(), r.db, "staging", func() {})
	if err != nil {
		t.Fatalf("notifier on sqlite must not error: %v", err)
	}
	if stop == nil {
		t.Fatal("notifier must return a non-nil stop function on sqlite")
	}
	stop()
}

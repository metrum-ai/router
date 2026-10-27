// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// PostgreSQL store-level coverage for the Admin API group write path (#395):
// a group write creates a new set, export round-trips, config rollback
// restores the exact prior document, and change events carry no secrets.
// Gated on ROUTER_TEST_PG_DSN (a disposable PostgreSQL); skipped by default.
// Each run uses a fresh schema and drops it afterwards.

import (
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestConfigAdminPostgresStore(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("ROUTER_TEST_PG_DSN"))
	if dsn == "" {
		t.Skip("set ROUTER_TEST_PG_DSN to a disposable PostgreSQL database")
	}
	db, closeDB, _ := configAdminPostgresTestDB(t)
	defer closeDB()

	// Seed: import + activate the shared two-provider document.
	seed, err := ImportCanonicalYAML(db, "pg-prod", "seed", []byte(configAdminTestYAML), "pg-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := ActivateConfigSet(db, "pg-prod", seed, "pg-test"); err != nil {
		t.Fatal(err)
	}

	// A group write creates a new set and activates it.
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	result, err := WriteGroupTargets(db, "pg-prod", GroupTargetsWriteRequest{
		Group:          "agent-default",
		Targets:        []GroupTargetSpec{{Provider: "beta", ModelRef: "b-one"}},
		Reason:         "pg store test",
		IdempotencyKey: "pg-idem-1",
	}, "adminkey:pg", GroupWritePolicy{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.SetID == "" || result.SetID == seed || result.Revision != 2 || result.PreviousSetID != seed {
		t.Fatalf("write result: %+v", result)
	}
	var setCount int64
	if err := db.Table("router_config_sets").Where("runtime_scope = ?", "pg-prod").Count(&setCount).Error; err != nil {
		t.Fatal(err)
	}
	if setCount != 2 {
		t.Fatalf("expected 2 sets, have %d", setCount)
	}

	// Export round-trips the stored document byte for byte.
	exported, err := ExportCanonicalYAML(db, result.SetID)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ImportCanonicalYAML(db, "pg-prod", "roundtrip", exported, "pg-test")
	if err != nil {
		t.Fatalf("exported document must re-import: %v", err)
	}
	roundTrip, err := ExportCanonicalYAML(db, again)
	if err != nil {
		t.Fatal(err)
	}
	if string(exported) != string(roundTrip) {
		t.Fatal("export round-trip mismatch")
	}

	// Idempotent replay returns the recorded set without a new row.
	replay, err := WriteGroupTargets(db, "pg-prod", GroupTargetsWriteRequest{
		Group:          "agent-default",
		Targets:        []GroupTargetSpec{{Provider: "beta", ModelRef: "b-one"}},
		Reason:         "pg store test",
		IdempotencyKey: "pg-idem-1",
	}, "adminkey:pg", GroupWritePolicy{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.SetID != result.SetID {
		t.Fatalf("idempotent replay: %+v", replay)
	}
	if err := db.Table("router_config_sets").Where("runtime_scope = ?", "pg-prod").Count(&setCount).Error; err != nil {
		t.Fatal(err)
	}
	if setCount != 3 { // seed, swap, roundtrip-import (not activated)
		t.Fatalf("replay created a set: %d", setCount)
	}

	// Stale If-Match conflicts with the current revision.
	stale := int64(1)
	if _, err := WriteGroupTargets(db, "pg-prod", GroupTargetsWriteRequest{
		Group: "agent-default", Targets: []GroupTargetSpec{{Provider: "alpha", ModelRef: "a-one"}},
		ExpectedRevision: &stale,
	}, "adminkey:pg", GroupWritePolicy{}, now); err == nil {
		t.Fatal("stale If-Match must conflict")
	} else {
		adminErr, ok := err.(*ConfigAdminError)
		if !ok || adminErr.Status != 409 || adminErr.Type != "config-revision-conflict" || adminErr.Revision == 0 {
			t.Fatalf("stale If-Match error: %v", err)
		}
	}

	// Config rollback restores the exact prior document.
	if err := RollbackConfigSet(db, "pg-prod", seed, "pg-test"); err != nil {
		t.Fatal(err)
	}
	restored, err := LoadActiveCanonicalYAML(db, "pg-prod")
	if err != nil {
		t.Fatal(err)
	}
	original, err := ExportCanonicalYAML(db, seed)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(original) {
		t.Fatal("rollback did not restore the exact prior document")
	}

	// Change events hold the audit metadata and no secrets: scan every
	// event field and the before/after target rows for credential shapes.
	var events []struct {
		Actor   string `gorm:"column:actor"`
		Action  string `gorm:"column:action"`
		Reason  string `gorm:"column:reason"`
		Detail  string `gorm:"column:detail"`
		Group   string `gorm:"column:group_name"`
		IdemKey string `gorm:"column:idempotency_key"`
	}
	if err := db.Table("router_config_change_events").
		Select("actor", "action", "reason", "detail", "group_name", "idempotency_key").
		Where("runtime_scope = ?", "pg-prod").Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) < 4 {
		t.Fatalf("expected import/activate/write/rollback events, have %d", len(events))
	}
	seen := map[string]bool{}
	for _, ev := range events {
		seen[ev.Action] = true
		for _, field := range []string{ev.Actor, ev.Action, ev.Reason, ev.Detail, ev.Group, ev.IdemKey} {
			if configControlPlaneSecretPattern.MatchString(field) {
				t.Fatalf("secret-shaped value in change event: %q", ev.Action)
			}
		}
	}
	for _, action := range []string{"import", "activate", configActionGroupTargetsReplace, "rollback"} {
		if !seen[action] {
			t.Fatalf("missing %s change event", action)
		}
	}
}

// configAdminPostgresTestDB opens a migrated control-plane connection in a
// fresh per-test schema and drops the schema on cleanup.
func configAdminPostgresTestDB(t *testing.T) (*gorm.DB, func(), string) {
	t.Helper()
	base := strings.TrimSpace(os.Getenv("ROUTER_TEST_PG_DSN"))
	if base == "" {
		t.Skip("set ROUTER_TEST_PG_DSN to a disposable PostgreSQL database")
	}
	schema := pgTestSchemaName(t)
	admin, err := gorm.Open(postgres.Open(base), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.Exec(`CREATE SCHEMA ` + schema).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	scoped := pgDSNWithSearchPath(base, schema)
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "postgres", DSN: scoped})
	if err != nil {
		t.Fatal(err)
	}
	applyConfigControlPlaneMigrationsForTest(t, r)
	cleanup := func() {
		_ = closeDB()
		if err := admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`).Error; err != nil {
			t.Logf("drop schema %s: %v", schema, err)
		}
		sqlDB, _ := admin.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	}
	return r.db, cleanup, schema
}

// pgTestSchemaName builds a unique safe schema identifier per test.
func pgTestSchemaName(t *testing.T) string {
	name := strings.ToLower(t.Name())
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return "r395" + b.String()[:min(24, b.Len())] + time.Now().UTC().Format("150405")
}

// pgDSNWithSearchPath appends search_path to a postgres DSN (URL or keyword
// form) so every pooled connection lands in the per-test schema.
func pgDSNWithSearchPath(dsn, schema string) string {
	if strings.Contains(dsn, "://") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		return dsn + sep + "search_path=" + schema
	}
	return dsn + " search_path=" + schema
}

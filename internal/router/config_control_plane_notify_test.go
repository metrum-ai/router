// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

// sqliteNotificationProbe observes requests inside the same SQLite
// transaction; a rollback also rolls back the probe row. It does not claim
// SQLite can deliver PostgreSQL notifications.
func sqliteNotificationProbe(t *testing.T, db *gorm.DB) *gorm.DB {
	t.Helper()
	if err := db.Exec("CREATE TABLE test_revision_notifications (scope TEXT NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	observer := func(tx *gorm.DB, scope string) {
		if err := tx.Exec("INSERT INTO test_revision_notifications (scope) VALUES (?)", scope).Error; err != nil {
			t.Errorf("record notification request: %v", err)
		}
	}
	return db.WithContext(context.WithValue(context.Background(), revisionNotificationObserverKey{}, observer))
}

func notificationCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var count int64
	if err := db.Table("test_revision_notifications").Where("scope = ?", "prod").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}

func TestConfigAdminNotificationsSQLite(t *testing.T) {
	db := sqliteNotificationProbe(t, configAdminTestDB(t))
	mux := configAdminTestMux(t, db, nil)
	admin := adminHeaders("adminkey:ops", "config:read", "config:write", "config:activate")
	body := map[string]any{"targets": []map[string]any{{"provider": "beta", "model_ref": "b-one"}}, "reason": "swap"}
	dry := map[string]any{"targets": body["targets"], "reason": "swap", "dry_run": true}
	rr := configAdminRequest(t, mux, "PUT", "/admin/config/groups/agent-default/targets", dry, admin)
	if rr.Code != http.StatusOK || notificationCount(t, db) != 0 {
		t.Fatalf("dry run status=%d or unexpectedly queued notification", rr.Code)
	}
	admin["Idempotency-Key"] = "notify-swap"
	rr = configAdminRequest(t, mux, "PUT", "/admin/config/groups/agent-default/targets", body, admin)
	if rr.Code != http.StatusOK || notificationCount(t, db) != 1 {
		t.Fatalf("group write status=%d or missing notification: %s", rr.Code, rr.Body.String())
	}
	var response struct {
		PreviousSetID string `json:"previous_set_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil || response.PreviousSetID == "" {
		t.Fatalf("missing previous set: %v", err)
	}
	rr = configAdminRequest(t, mux, "PUT", "/admin/config/groups/agent-default/targets", body, admin)
	if rr.Code != http.StatusOK || notificationCount(t, db) != 1 {
		t.Fatalf("idempotent replay status=%d or duplicate notification", rr.Code)
	}
	delete(admin, "Idempotency-Key")
	rr = configAdminRequest(t, mux, "POST", "/admin/config/groups/agent-default/rollback", map[string]any{"reason": "rollback"}, admin)
	if rr.Code != http.StatusOK || notificationCount(t, db) != 2 {
		t.Fatalf("group rollback status=%d or missing notification: %s", rr.Code, rr.Body.String())
	}
	rr = configAdminRequest(t, mux, "POST", "/admin/config/sets/"+response.PreviousSetID+"/activate", nil, admin)
	if rr.Code != http.StatusOK || notificationCount(t, db) != 3 {
		t.Fatalf("set activation status=%d or missing notification: %s", rr.Code, rr.Body.String())
	}
	rr = configAdminRequest(t, mux, "POST", "/admin/config/rollback", map[string]any{"set_id": response.PreviousSetID}, admin)
	if rr.Code != http.StatusOK || notificationCount(t, db) != 4 {
		t.Fatalf("set rollback status=%d or missing notification: %s", rr.Code, rr.Body.String())
	}
}

func TestConfigRevisionMutationsNotifySQLite(t *testing.T) {
	db := sqliteNotificationProbe(t, configAdminTestDB(t))
	state, _, err := loadConfigAdminRuntime(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if err := ActivateConfigSet(db, "prod", state.ActiveSetID, "tester"); err != nil {
		t.Fatal(err)
	}
	if err := RollbackConfigSet(db, "prod", state.ActiveSetID, "tester"); err != nil {
		t.Fatal(err)
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		_, err := bumpConfigRevision(tx, "prod", state.ActiveSetID, "caller_issue", "tester")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		_, err := bumpRevisionOnlyTx(tx, "prod")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := notificationCount(t, db); got != 4 {
		t.Fatalf("want four committed notification requests, got %d", got)
	}
}

// postgresNamedSQLite executes SQLite SQL while exercising the PostgreSQL
// notification branch. The raw callback below substitutes the unsupported
// function, while inspecting its bound parameters.
type postgresNamedSQLite struct{ gorm.Dialector }

func (postgresNamedSQLite) Name() string { return "postgres" }

func TestNotificationPostgresSQLWithSQLite(t *testing.T) {
	db := configAdminTestDB(t)
	original := db.Dialector
	db.Dialector = postgresNamedSQLite{original}
	t.Cleanup(func() { db.Dialector = original })
	var scopeValue any
	var channelValue any
	if err := db.Callback().Raw().Before("gorm:raw").Register("test:pg-notify", func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "pg_notify") {
			if len(tx.Statement.Vars) == 2 {
				channelValue, scopeValue = tx.Statement.Vars[0], tx.Statement.Vars[1]
			}
			tx.Statement.SQL.Reset()
			tx.Statement.SQL.WriteString("SELECT 1")
			tx.Statement.Vars = nil
		}
	}); err != nil {
		t.Fatal(err)
	}
	scope := "prod'; SELECT 1; --"
	if err := db.Transaction(func(tx *gorm.DB) error {
		queueConfigRevisionNotification(tx, scope)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if channelValue != configControlPlaneNotifyChannel || scopeValue != scope {
		t.Fatalf("pg_notify did not receive bound channel and scope")
	}
}

func TestNotificationFailureKeepsTransactionSQLite(t *testing.T) {
	db := configAdminTestDB(t)
	if err := db.Exec("CREATE TABLE notify_fallback_test (value INTEGER NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	original := db.Dialector
	db.Dialector = postgresNamedSQLite{original}
	t.Cleanup(func() { db.Dialector = original })
	if err := db.Callback().Raw().Before("gorm:raw").Register("test:pg-notify-fail", func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "pg_notify") {
			tx.Statement.SQL.Reset()
			tx.Statement.SQL.WriteString("SELECT nonexistent_notification_function()")
			tx.Statement.Vars = nil
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		queueConfigRevisionNotification(tx, "prod")
		return tx.Exec("INSERT INTO notify_fallback_test (value) VALUES (1)").Error
	}); err != nil {
		t.Fatalf("notification failure aborted mutation: %v", err)
	}
	var count int64
	if err := db.Table("notify_fallback_test").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("mutation not committed after notification failure: count=%d err=%v", count, err)
	}
}

func TestConfigNotificationRollbackSQLite(t *testing.T) {
	db := sqliteNotificationProbe(t, configAdminTestDB(t))
	var state configAdminRuntimeState
	var err error
	state, _, err = loadConfigAdminRuntime(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	abort := errors.New("abort transaction")
	err = db.Transaction(func(tx *gorm.DB) error {
		if _, err := activateConfigSetGuardedTx(tx, "prod", state.ActiveSetID, state.Revision, time.Now()); err != nil {
			return err
		}
		return abort
	})
	if !errors.Is(err, abort) || notificationCount(t, db) != 0 {
		t.Fatalf("rolled-back transaction emitted a committed probe: %v", err)
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Activate and rollback promote a validated config set for a runtime scope.
// Mutations update the active-set pointer, bump router_config_runtime
// config_revision, and append router_config_change_events using the phase-6
// schema. Tables are created by the phase-6 migration; ensure helpers only
// create the phase-6 DDL when tests have not yet registered that migration.

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

const configControlPlaneNotifyChannel = "router_config"

// ensureConfigControlPlaneRuntimeTables creates phase-6 runtime and change-
// event tables when missing. Idempotent on SQLite and PostgreSQL.
func ensureConfigControlPlaneRuntimeTables(tx *gorm.DB) error {
	for _, stmt := range configControlPlanePhase6DDL {
		if err := tx.Exec(stmt).Error; err != nil {
			return fmt.Errorf("ensure control-plane runtime tables: %w", err)
		}
	}
	return nil
}

// ActivateConfigSet promotes configSetID to the active set for runtimeScope.
// The previously active set (if any) is superseded. The target set must
// already have validation_status = 'valid'. A runtime revision is bumped
// and a change event is appended, all in one transaction. On PostgreSQL a
// NOTIFY is sent on the router_config channel after commit.
func ActivateConfigSet(db *gorm.DB, runtimeScope, configSetID, actor string) error {
	return activateConfigSetWithAction(db, runtimeScope, configSetID, actor, "activate")
}

// RollbackConfigSet re-activates a prior config set and records action
// "rollback" in the change-event log.
func RollbackConfigSet(db *gorm.DB, runtimeScope, configSetID, actor string) error {
	return activateConfigSetWithAction(db, runtimeScope, configSetID, actor, "rollback")
}

func activateConfigSetWithAction(db *gorm.DB, runtimeScope, configSetID, actor, action string) error {
	if db == nil {
		return errors.New("config control-plane database is required")
	}
	runtimeScope = strings.TrimSpace(runtimeScope)
	if runtimeScope == "" {
		return errors.New("config runtime scope is required")
	}
	configSetID = strings.TrimSpace(configSetID)
	if configSetID == "" {
		return errors.New("config set ID is required")
	}
	actor = strings.TrimSpace(actor)
	action = strings.TrimSpace(action)
	if action == "" {
		action = "activate"
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := ensureConfigControlPlaneRuntimeTables(tx); err != nil {
			return err
		}

		var target configSetRow
		if err := tx.Where("id = ? AND runtime_scope = ?", configSetID, runtimeScope).First(&target).Error; err != nil {
			return fmt.Errorf("load target config set %q: %w", configSetID, err)
		}
		if target.ValidationStatus != "valid" {
			return fmt.Errorf("config set %q has validation_status %q; only valid sets may be activated", configSetID, target.ValidationStatus)
		}

		if err := tx.Exec(
			`UPDATE router_config_sets SET status = 'superseded' WHERE runtime_scope = ? AND status = 'active' AND id != ?`,
			runtimeScope, configSetID,
		).Error; err != nil {
			return fmt.Errorf("supersede previous active set: %w", err)
		}

		if err := tx.Exec(
			`UPDATE router_config_sets SET status = 'active', activated_at = ? WHERE id = ?`,
			now, configSetID,
		).Error; err != nil {
			return fmt.Errorf("activate config set %q: %w", configSetID, err)
		}

		switch tx.Dialector.Name() {
		case "sqlite":
			if err := tx.Exec(
				`INSERT INTO router_config_runtime (runtime_scope, config_revision, active_set_id)
				 VALUES (?, 1, ?)
				 ON CONFLICT(runtime_scope) DO UPDATE SET config_revision = config_revision + 1, active_set_id = excluded.active_set_id`,
				runtimeScope, configSetID,
			).Error; err != nil {
				return fmt.Errorf("upsert runtime revision: %w", err)
			}
		case "postgres":
			if err := tx.Exec(
				`INSERT INTO router_config_runtime (runtime_scope, config_revision, active_set_id)
				 VALUES (?, 1, ?)
				 ON CONFLICT(runtime_scope) DO UPDATE SET config_revision = router_config_runtime.config_revision + 1, active_set_id = EXCLUDED.active_set_id`,
				runtimeScope, configSetID,
			).Error; err != nil {
				return fmt.Errorf("upsert runtime revision: %w", err)
			}
		default:
			return fmt.Errorf("unsupported control-plane database driver %q", tx.Dialector.Name())
		}

		var revision int64
		if err := tx.Raw(
			`SELECT config_revision FROM router_config_runtime WHERE runtime_scope = ?`,
			runtimeScope,
		).Row().Scan(&revision); err != nil {
			return fmt.Errorf("read runtime revision: %w", err)
		}

		eventID := newConfigChangeEventID()
		if err := tx.Exec(
			`INSERT INTO router_config_change_events (event_id, runtime_scope, config_set_id, action, actor, at, config_revision) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			eventID, runtimeScope, configSetID, action, actor, now, revision,
		).Error; err != nil {
			return fmt.Errorf("append change event: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	if db.Dialector.Name() == "postgres" {
		_ = db.Exec(fmt.Sprintf(`NOTIFY %s, '%s'`, configControlPlaneNotifyChannel, runtimeScope)).Error
	}
	return nil
}

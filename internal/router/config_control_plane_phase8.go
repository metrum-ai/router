// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Phase eight of the configuration control plane extends the append-only
// change-event audit log for the Admin API group write surface (#395) and adds
// the idempotency record those writes need:
//
//   - router_config_change_events gains group_name, prev_config_set_id,
//     reason, forced, idempotency_key, and request_hash columns, all with
//     empty/zero defaults so existing rows stay valid;
//   - router_config_change_event_targets holds the before/after ordered
//     target lists of a group change as one foreign-keyed row per target, so
//     no packed or JSON value appears in the audit log;
//   - a partial unique index makes an Idempotency-Key usable at most once per
//     runtime scope.
//
// The phase is online and transactional: it adds columns, one table, and
// indexes without rewriting existing rows. Apply is idempotent so the admin
// write path can also ensure the columns on databases migrated only through
// phase 6/7 (mirroring ensureConfigControlPlaneRuntimeTables).
//
// No raw tokens, provider credentials, or request content is ever stored in
// these columns: group names, set identifiers, ordered target identities,
// operator-supplied reason text (length-capped at the API), the forced flag,
// the idempotency key, and a SHA-256 request hash are all safe metadata.

import (
	"fmt"

	"gorm.io/gorm"
)

const configControlPlanePhase8MigrationID = 2026102701

// ConfigControlPlanePhase8MigrationDefinition returns the immutable migration
// definition for the phase-8 admin write audit schema.
func ConfigControlPlanePhase8MigrationDefinition() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{
		ID:               configControlPlanePhase8MigrationID,
		Scope:            configControlPlaneScope,
		Name:             "admin API group write audit metadata and idempotency",
		Release:          "2026.11",
		Checksum:         MigrationChecksum("router-config.phase8.admin-write-audit.v1"),
		SchemaVersion:    8,
		Transactional:    true,
		MaintenanceMode:  "online",
		RollbackClass:    "restore-required",
		HandlerKey:       "router-config.phase8.apply.v1@applyConfigControlPlanePhase8",
		PostconditionKey: "router-config.phase8.schema.v1@verifyConfigControlPlanePhase8",
		Dependencies:     []int{2026102301},
		ExecutionMode:    "transactional",
		LockClass:        "online",
		TimeoutClass:     "bounded",
		Apply:            applyConfigControlPlanePhase8,
		Verify:           verifyConfigControlPlanePhase8,
	})
}

var configControlPlanePhase8EventColumns = []string{
	"group_name",
	"prev_config_set_id",
	"reason",
	"forced",
	"idempotency_key",
	"request_hash",
	"detail",
}

var configControlPlanePhase8Tables = []string{
	"router_config_change_event_targets",
}

var configControlPlanePhase8RequiredColumns = map[string][]string{
	"router_config_change_events":        configControlPlanePhase8EventColumns,
	"router_config_change_event_targets": {"event_id", "phase", "sequence", "provider_name", "model_ref", "model"},
}

var configControlPlanePhase8EventTargetsDDL = `CREATE TABLE IF NOT EXISTS router_config_change_event_targets (event_id TEXT NOT NULL REFERENCES router_config_change_events(event_id), phase TEXT NOT NULL, sequence INTEGER NOT NULL, provider_name TEXT NOT NULL, model_ref TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '', PRIMARY KEY (event_id, phase, sequence))`

var configControlPlanePhase8Indexes = []string{
	`CREATE INDEX IF NOT EXISTS router_config_change_events_scope_group ON router_config_change_events (runtime_scope, group_name, at)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS router_config_change_events_idempotency ON router_config_change_events (runtime_scope, idempotency_key) WHERE idempotency_key != ''`,
}

// applyConfigControlPlanePhase8 adds the audit columns, target table, and
// indexes. Every statement is guarded so re-application is a no-op on both
// SQLite and PostgreSQL.
func applyConfigControlPlanePhase8(tx *gorm.DB) error {
	migrator := tx.Migrator()
	if !migrator.HasTable("router_config_change_events") {
		if err := ensureConfigControlPlaneRuntimeTables(tx); err != nil {
			return err
		}
	}
	for _, column := range configControlPlanePhase8EventColumns {
		if migrator.HasColumn("router_config_change_events", column) {
			continue
		}
		var stmt string
		if column == "forced" {
			// Portable zero-default integer flag on both drivers.
			stmt = `ALTER TABLE router_config_change_events ADD COLUMN forced INTEGER NOT NULL DEFAULT 0`
		} else {
			stmt = fmt.Sprintf(`ALTER TABLE router_config_change_events ADD COLUMN %s TEXT NOT NULL DEFAULT ''`, column)
		}
		if err := tx.Exec(stmt).Error; err != nil {
			return fmt.Errorf("add change-event column %s: %w", column, err)
		}
	}
	if err := tx.Exec(configControlPlanePhase8EventTargetsDDL).Error; err != nil {
		return fmt.Errorf("create change-event targets table: %w", err)
	}
	for _, stmt := range configControlPlanePhase8Indexes {
		if err := tx.Exec(stmt).Error; err != nil {
			return fmt.Errorf("create change-event index: %w", err)
		}
	}
	return nil
}

func verifyConfigControlPlanePhase8(tx *gorm.DB) error {
	if err := verifyConfigCacheWriteMigration(tx); err != nil {
		return err
	}
	for _, table := range configControlPlanePhase8Tables {
		if !tx.Migrator().HasTable(table) {
			return fmt.Errorf("required control-plane table %s is missing", table)
		}
	}
	for table, columns := range configControlPlanePhase8RequiredColumns {
		for _, column := range columns {
			if !tx.Migrator().HasColumn(table, column) {
				return fmt.Errorf("required control-plane column %s.%s is missing", table, column)
			}
		}
	}
	return nil
}

// ensureConfigAdminWriteSchema applies the phase-8 DDL idempotently inside a
// write transaction so Admin API group writes also work against databases
// whose migration ledger predates phase 8 (the same pattern as
// ensureConfigControlPlaneRuntimeTables for phase 6).
func ensureConfigAdminWriteSchema(tx *gorm.DB) error {
	if err := ensureConfigControlPlaneRuntimeTables(tx); err != nil {
		return err
	}
	if err := applyConfigControlPlanePhase8(tx); err != nil {
		return fmt.Errorf("ensure admin write schema: %w", err)
	}
	return nil
}

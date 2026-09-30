// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Phase six of the configuration control plane creates the canonical YAML
// document store, the runtime activation pointer, and the append-only change
// audit log. The migration is online and transactional: it creates new tables
// only and does not alter existing phase-1 through phase-5 schema.

import (
	"fmt"

	"gorm.io/gorm"
)

const configControlPlanePhase6MigrationID = 2026072006

// ConfigControlPlanePhase6MigrationDefinition returns the immutable migration
// definition for the phase-6 document-storage schema. The definition is
// finalized (manifest digest bound) before it is returned.
func ConfigControlPlanePhase6MigrationDefinition() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{
		ID:               configControlPlanePhase6MigrationID,
		Scope:            configControlPlaneScope,
		Name:             "create canonical YAML document storage",
		Release:          "2026.7",
		Checksum:         "28bd9c0769e32424fa70c96d5210b6c3c42cfdb0a671f7de2549602f9da4a68e",
		SchemaVersion:    6,
		Transactional:    true,
		MaintenanceMode:  "online",
		RollbackClass:    "restore-required",
		HandlerKey:       "router-config.phase6.apply.v1@applyConfigControlPlanePhase6",
		PostconditionKey: "router-config.phase6.schema.v1@verifyConfigControlPlanePhase6",
		Dependencies:     []int{configControlPlanePhase5MigrationID},
		ExecutionMode:    "transactional",
		LockClass:        "online",
		TimeoutClass:     "bounded",
		Apply:            applyConfigControlPlanePhase6,
		Verify:           verifyConfigControlPlanePhase6,
	})
}

var configControlPlanePhase6Tables = []string{
	"router_config_documents",
	"router_config_runtime",
	"router_config_change_events",
}

var configControlPlanePhase6RequiredColumns = map[string][]string{
	"router_config_documents":     {"config_set_id", "canonical_yaml", "content_sha256", "updated_at"},
	"router_config_runtime":       {"runtime_scope", "config_revision", "active_set_id"},
	"router_config_change_events": {"event_id", "runtime_scope", "config_set_id", "action", "actor", "at", "config_revision"},
}

var configControlPlanePhase6RequiredForeignKeys = []configControlPlaneForeignKey{
	{Table: "router_config_documents", Columns: []string{"config_set_id"}, ReferencedTable: "router_config_sets", ReferencedColumns: []string{"id"}},
	{Table: "router_config_runtime", Columns: []string{"active_set_id"}, ReferencedTable: "router_config_sets", ReferencedColumns: []string{"id"}},
	{Table: "router_config_change_events", Columns: []string{"config_set_id"}, ReferencedTable: "router_config_sets", ReferencedColumns: []string{"id"}},
}

var configControlPlanePhase6DDL = []string{
	`CREATE TABLE IF NOT EXISTS router_config_documents (config_set_id TEXT PRIMARY KEY REFERENCES router_config_sets(id), canonical_yaml TEXT NOT NULL, content_sha256 TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS router_config_runtime (runtime_scope TEXT PRIMARY KEY, config_revision BIGINT NOT NULL DEFAULT 0, active_set_id TEXT NOT NULL DEFAULT '' REFERENCES router_config_sets(id))`,
	`CREATE TABLE IF NOT EXISTS router_config_change_events (event_id TEXT PRIMARY KEY, runtime_scope TEXT NOT NULL, config_set_id TEXT NOT NULL REFERENCES router_config_sets(id), action TEXT NOT NULL, actor TEXT NOT NULL, at TEXT NOT NULL, config_revision BIGINT NOT NULL DEFAULT 0)`,
}

var configControlPlanePhase6PostgresComments = []string{
	`COMMENT ON TABLE router_config_documents IS 'Canonical YAML document for one configuration set; the single source of truth.'`,
	`COMMENT ON COLUMN router_config_documents.config_set_id IS 'Owning configuration-set identifier.'`,
	`COMMENT ON COLUMN router_config_documents.canonical_yaml IS 'Unexpanded canonical YAML; environment variable references are preserved and raw secrets are never stored.'`,
	`COMMENT ON COLUMN router_config_documents.content_sha256 IS 'SHA-256 hash of the canonical YAML bytes for integrity verification.'`,
	`COMMENT ON COLUMN router_config_documents.updated_at IS 'UTC timestamp of the last document update.'`,
	`COMMENT ON TABLE router_config_runtime IS 'Runtime activation pointer; exactly one row per runtime scope.'`,
	`COMMENT ON COLUMN router_config_runtime.runtime_scope IS 'Deployment/runtime scope owning the active configuration.'`,
	`COMMENT ON COLUMN router_config_runtime.config_revision IS 'Monotonically increasing revision number for the active configuration.'`,
	`COMMENT ON COLUMN router_config_runtime.active_set_id IS 'Currently active configuration-set identifier.'`,
	`COMMENT ON TABLE router_config_change_events IS 'Append-only audit log of configuration control-plane actions.'`,
	`COMMENT ON COLUMN router_config_change_events.event_id IS 'Opaque change-event identifier.'`,
	`COMMENT ON COLUMN router_config_change_events.runtime_scope IS 'Deployment/runtime scope that changed.'`,
	`COMMENT ON COLUMN router_config_change_events.config_set_id IS 'Configuration-set identifier that changed.'`,
	`COMMENT ON COLUMN router_config_change_events.action IS 'Action performed, such as import, activate, or archive.'`,
	`COMMENT ON COLUMN router_config_change_events.actor IS 'Sanitized actor identifier that performed the action.'`,
	`COMMENT ON COLUMN router_config_change_events.at IS 'UTC timestamp of the action.'`,
	`COMMENT ON COLUMN router_config_change_events.config_revision IS 'Configuration revision after the action.'`,
}

func applyConfigControlPlanePhase6(tx *gorm.DB) error {
	for _, stmt := range configControlPlanePhase6DDL {
		if err := tx.Exec(stmt).Error; err != nil {
			return fmt.Errorf("create canonical document storage: %w", err)
		}
	}
	if tx.Dialector.Name() == "postgres" {
		for _, stmt := range configControlPlanePhase6PostgresComments {
			if err := tx.Exec(stmt).Error; err != nil {
				return fmt.Errorf("comment canonical document storage: %w", err)
			}
		}
	}
	return nil
}

func verifyConfigControlPlanePhase6(tx *gorm.DB) error {
	if err := verifyConfigControlPlanePhase5(tx); err != nil {
		return err
	}
	for _, table := range configControlPlanePhase6Tables {
		if !tx.Migrator().HasTable(table) {
			return fmt.Errorf("required control-plane table %s is missing", table)
		}
	}
	for table, columns := range configControlPlanePhase6RequiredColumns {
		for _, column := range columns {
			if !tx.Migrator().HasColumn(table, column) {
				return fmt.Errorf("required control-plane column %s.%s is missing", table, column)
			}
		}
	}
	for _, foreignKey := range configControlPlanePhase6RequiredForeignKeys {
		if err := verifyConfigControlPlaneForeignKey(tx, foreignKey); err != nil {
			return err
		}
	}
	return nil
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Phase nine of the configuration control plane adds
// router_config_shadow_runs: the shared shadow-run registry for the Admin API
// shadow surface (#395 §5). A shadow run puts one candidate catalog target in
// shadow against a model group's served traffic at a bounded sample rate, so
// every replica serves identically and samples consistently.
//
// The table holds only safe scalars: candidate identity, sampling policy,
// lifecycle status, and aggregate counters (requests, errors, latency, token
// totals, estimated cost). Request content and credentials are never stored;
// the shadow replays carry no caller identity and record no per-request rows
// in this schema (aggregate counters only).
//
// Lifecycle changes bump router_config_runtime.config_revision so replicas
// converge through the existing phase-2 poller/notifier, and each lifecycle
// change is a router_config_change_events row (shadow_start, shadow_stop,
// shadow_promote) linked by run ID in the detail column.
//
// The phase is online, transactional, and idempotent (same pattern as phase
// 8): one new table and one partial unique index, no changes to existing
// rows.

import (
	"fmt"

	"gorm.io/gorm"
)

const configControlPlanePhase9MigrationID = 2026102702

// ConfigControlPlanePhase9MigrationDefinition returns the immutable migration
// definition for the phase-9 shadow-run registry.
func ConfigControlPlanePhase9MigrationDefinition() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{
		ID:               configControlPlanePhase9MigrationID,
		Scope:            configControlPlaneScope,
		Name:             "shadow run registry for the Admin API",
		Release:          "2026.11",
		Checksum:         MigrationChecksum("router-config.phase9.shadow-runs.v1"),
		SchemaVersion:    9,
		Transactional:    true,
		MaintenanceMode:  "online",
		RollbackClass:    "restore-required",
		HandlerKey:       "router-config.phase9.apply.v1@applyConfigControlPlanePhase9",
		PostconditionKey: "router-config.phase9.schema.v1@verifyConfigControlPlanePhase9",
		Dependencies:     []int{configControlPlanePhase8MigrationID},
		ExecutionMode:    "transactional",
		LockClass:        "online",
		TimeoutClass:     "bounded",
		Apply:            applyConfigControlPlanePhase9,
		Verify:           verifyConfigControlPlanePhase9,
	})
}

const configControlPlaneShadowRunsTable = "router_config_shadow_runs"

var configControlPlanePhase9DDL = []string{
	`CREATE TABLE IF NOT EXISTS router_config_shadow_runs (run_id TEXT PRIMARY KEY, runtime_scope TEXT NOT NULL, group_name TEXT NOT NULL, provider_name TEXT NOT NULL, model_ref TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '', sample_rate REAL NOT NULL, validation_status TEXT NOT NULL DEFAULT '', validation_workload TEXT NOT NULL DEFAULT '', validation_validated_at TEXT NOT NULL DEFAULT '', max_requests INTEGER NOT NULL DEFAULT 0, duration_seconds INTEGER NOT NULL DEFAULT 0, cost_cap_usd REAL NOT NULL DEFAULT 0, status TEXT NOT NULL, created_by TEXT NOT NULL, created_at TEXT NOT NULL, stopped_at TEXT NOT NULL DEFAULT '', promoted_set_id TEXT NOT NULL DEFAULT '', requests INTEGER NOT NULL DEFAULT 0, errors INTEGER NOT NULL DEFAULT 0, latency_ms_total REAL NOT NULL DEFAULT 0, prompt_tokens INTEGER NOT NULL DEFAULT 0, completion_tokens INTEGER NOT NULL DEFAULT 0, cost_usd REAL NOT NULL DEFAULT 0)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS router_config_shadow_runs_active ON router_config_shadow_runs (runtime_scope, group_name) WHERE status = 'active'`,
}

func applyConfigControlPlanePhase9(tx *gorm.DB) error {
	for _, stmt := range configControlPlanePhase9DDL {
		if err := tx.Exec(stmt).Error; err != nil {
			return fmt.Errorf("create shadow run registry: %w", err)
		}
	}
	return nil
}

func verifyConfigControlPlanePhase9(tx *gorm.DB) error {
	if err := verifyConfigControlPlanePhase8(tx); err != nil {
		return err
	}
	if !tx.Migrator().HasTable(configControlPlaneShadowRunsTable) {
		return fmt.Errorf("required control-plane table %s is missing", configControlPlaneShadowRunsTable)
	}
	for _, column := range []string{"run_id", "runtime_scope", "group_name", "provider_name", "model_ref", "model", "sample_rate", "validation_status", "validation_workload", "validation_validated_at", "status", "created_by", "created_at", "requests", "errors", "latency_ms_total", "prompt_tokens", "completion_tokens", "cost_usd"} {
		if !tx.Migrator().HasColumn(configControlPlaneShadowRunsTable, column) {
			return fmt.Errorf("required control-plane column %s.%s is missing", configControlPlaneShadowRunsTable, column)
		}
	}
	return nil
}

// ensureConfigShadowSchema applies the phase-9 DDL idempotently inside a
// shadow lifecycle transaction (same pattern as ensureConfigAdminWriteSchema).
func ensureConfigShadowSchema(tx *gorm.DB) error {
	if err := ensureConfigAdminWriteSchema(tx); err != nil {
		return err
	}
	if err := applyConfigControlPlanePhase9(tx); err != nil {
		return fmt.Errorf("ensure shadow run schema: %w", err)
	}
	return nil
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Phase eleven of the configuration control plane completes the admin-plane audit
// record (metrum-ai/router#816, ACP-022/023, ACP-066).
//
//   - router_config_change_events gains the admin audit columns it was missing:
//     actor_kind, actor_credential_id, actor_issuer, authz_domain, object,
//     authz_action, operation_id, request_id, outcome, before_hash, after_hash,
//     ticket. (The existing `action` column keeps its meaning: the change action,
//     for example group_targets_replace; `authz_action` is the authorized action.)
//     Every
//     column is NOT NULL with an empty default, so existing rows stay valid and no
//     row is rewritten.
//   - router_admin_credential_events is new: one redacted row per authentication
//     outcome and per credential lifecycle change (issue, rotate, revoke, expire).
//
// The change-event row IS the audit outbox row for a control-plane mutation: it is
// inserted in the same transaction as the change, so a failed audit insert aborts
// the change. There is no second log to reconcile.
//
// The migration is additive and online: column additions, one new table and two
// indexes. It stores identifiers only — never a token, verifier, claim, request
// body or upstream text.

import (
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	configControlPlanePhase11MigrationID = 2026110603
	adminCredentialEventsTable           = "router_admin_credential_events"
)

// ConfigControlPlanePhase11MigrationDefinition returns the immutable migration
// definition for the admin audit columns and the credential-event table.
func ConfigControlPlanePhase11MigrationDefinition() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{
		ID:               configControlPlanePhase11MigrationID,
		Scope:            configControlPlaneScope,
		Name:             "admin audit columns and credential events",
		Release:          "2026.11",
		Checksum:         MigrationChecksum("router-config.phase11.admin-audit.v1"),
		SchemaVersion:    11,
		Transactional:    true,
		MaintenanceMode:  "online",
		RollbackClass:    "restore-required",
		HandlerKey:       "router-config.phase11.apply.v1@applyConfigControlPlanePhase11",
		PostconditionKey: "router-config.phase11.schema.v1@verifyConfigControlPlanePhase11",
		Dependencies:     []int{configControlPlanePhase10MigrationID},
		ExecutionMode:    "transactional",
		LockClass:        "online",
		TimeoutClass:     "bounded",
		Apply:            applyConfigControlPlanePhase11,
		Verify:           verifyConfigControlPlanePhase11,
	})
}

// configControlPlanePhase11EventColumns are the audit columns added to the
// existing change-event log.
var configControlPlanePhase11EventColumns = []string{
	"actor_kind",
	"actor_credential_id",
	"actor_issuer",
	"authz_domain",
	"object",
	"authz_action",
	"operation_id",
	"request_id",
	"outcome",
	"before_hash",
	"after_hash",
	"ticket",
}

var adminCredentialEventsDDL = []string{
	`CREATE TABLE IF NOT EXISTS router_admin_credential_events (event_id TEXT PRIMARY KEY, at TEXT NOT NULL, kind TEXT NOT NULL, credential_id TEXT NOT NULL DEFAULT '', subject TEXT NOT NULL DEFAULT '', issuer TEXT NOT NULL DEFAULT '', outcome TEXT NOT NULL, reason_code TEXT NOT NULL DEFAULT '', request_id TEXT NOT NULL DEFAULT '', lifecycle TEXT NOT NULL DEFAULT 'auth', count INTEGER NOT NULL DEFAULT 1)`,
	`CREATE INDEX IF NOT EXISTS router_admin_credential_events_at ON router_admin_credential_events (at)`,
	`CREATE INDEX IF NOT EXISTS router_admin_credential_events_credential ON router_admin_credential_events (credential_id, at)`,
}

// applyConfigControlPlanePhase11 adds the audit columns and the credential-event
// table. Every statement is guarded so re-application is a no-op on SQLite and
// PostgreSQL.
func applyConfigControlPlanePhase11(tx *gorm.DB) error {
	migrator := tx.Migrator()
	if !migrator.HasTable("router_config_change_events") {
		if err := ensureConfigControlPlaneRuntimeTables(tx); err != nil {
			return err
		}
	}
	for _, column := range configControlPlanePhase11EventColumns {
		if migrator.HasColumn("router_config_change_events", column) {
			continue
		}
		stmt := fmt.Sprintf(`ALTER TABLE router_config_change_events ADD COLUMN %s TEXT NOT NULL DEFAULT ''`, column)
		if err := tx.Exec(stmt).Error; err != nil {
			return fmt.Errorf("add change-event audit column %s: %w", column, err)
		}
	}
	for _, stmt := range adminCredentialEventsDDL {
		if err := tx.Exec(stmt).Error; err != nil {
			return fmt.Errorf("create credential events schema: %w", err)
		}
	}
	return nil
}

func verifyConfigControlPlanePhase11(tx *gorm.DB) error {
	if err := verifyConfigControlPlanePhase10(tx); err != nil {
		return err
	}
	for _, column := range configControlPlanePhase11EventColumns {
		if !tx.Migrator().HasColumn("router_config_change_events", column) {
			return fmt.Errorf("required audit column router_config_change_events.%s is missing", column)
		}
	}
	if !tx.Migrator().HasTable(adminCredentialEventsTable) {
		return fmt.Errorf("required control-plane table %s is missing", adminCredentialEventsTable)
	}
	for _, column := range []string{"event_id", "at", "kind", "credential_id", "subject", "issuer", "outcome", "reason_code", "request_id", "lifecycle", "count"} {
		if !tx.Migrator().HasColumn(adminCredentialEventsTable, column) {
			return fmt.Errorf("required control-plane column %s.%s is missing", adminCredentialEventsTable, column)
		}
	}
	return nil
}

// ensureConfigAdminAuditSchema applies the phase-11 DDL idempotently inside a
// write transaction, so a database migrated only through phase 10 still records
// the full audit row (same pattern as ensureConfigAdminWriteSchema).
func ensureConfigAdminAuditSchema(tx *gorm.DB) error {
	if err := ensureConfigAdminWriteSchema(tx); err != nil {
		return err
	}
	if err := applyConfigControlPlanePhase11(tx); err != nil {
		return fmt.Errorf("ensure admin audit schema: %w", err)
	}
	return nil
}

// insertAdminCredentialEventTx writes one redacted credential event. Callers that
// mutate a credential call this inside the credential's transaction; callers that
// record an authentication outcome call it directly.
func insertAdminCredentialEventTx(tx *gorm.DB, event AdminCredentialEvent) error {
	if tx == nil {
		return fmt.Errorf("credential event requires a transaction")
	}
	if strings.TrimSpace(event.EventID) == "" {
		event.EventID = newConfigChangeEventID()
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	if strings.TrimSpace(event.Lifecycle) == "" {
		event.Lifecycle = "auth"
	}
	if err := tx.Exec(
		`INSERT INTO router_admin_credential_events (event_id, at, kind, credential_id, subject, issuer, outcome, reason_code, request_id, lifecycle, count) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1) ON CONFLICT DO NOTHING`,
		event.EventID, event.At.UTC().Format(time.RFC3339Nano), adminAuditFieldValue(event.Kind),
		adminAuditFieldValue(event.CredentialID), adminAuditFieldValue(event.Subject), adminAuditFieldValue(event.Issuer),
		adminAuditFieldValue(event.Outcome), adminAuditFieldValue(event.ReasonCode), adminAuditFieldValue(event.RequestID),
		adminAuditFieldValue(event.Lifecycle),
	).Error; err != nil {
		return fmt.Errorf("append credential event: %w", err)
	}
	return nil
}

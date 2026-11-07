// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Usage-database admin audit rows (metrum-ai/router#816, ACP-022/023).
//
// Some /admin/v1 mutations change data that lives in the usage database:
// content-capture deletion and expired-capture purge, and training outcome labels.
// Their audit row is written by the same transaction as the change, in the same
// database, so a failed audit insert aborts the change exactly as it does for a
// control-plane write.
//
// The table is additive and online: one new table and one index, no change to an
// existing row.

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

const (
	usageAdminAuditMigrationID = 2026110604
	usageAdminAuditTable       = "router_admin_audit_events"
)

// UsageAdminAuditMigrationDefinition returns the immutable migration definition
// for the usage-side admin audit table.
func UsageAdminAuditMigrationDefinition() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{
		ID:               usageAdminAuditMigrationID,
		Scope:            usageMigrationScope,
		Name:             "admin audit events for usage-database changes",
		Release:          "2026.11",
		Checksum:         MigrationChecksum("usage.admin-audit.v1"),
		SchemaVersion:    13,
		DataVersion:      0,
		Transactional:    true,
		MaintenanceMode:  "online",
		RollbackClass:    "restore-required",
		HandlerKey:       "usage.admin-audit.apply.v1@applyUsageAdminAuditMigration",
		PostconditionKey: "usage.admin-audit.schema.v1@verifyUsageAdminAuditMigration",
		Dependencies:     []int{usageResponseAffinityMigrationID},
		ExecutionMode:    "transactional",
		LockClass:        "online",
		TimeoutClass:     "bounded",
		Apply:            applyUsageAdminAuditMigration,
		Verify:           verifyUsageAdminAuditMigration,
	})
}

var usageAdminAuditDDL = []string{
	`CREATE TABLE IF NOT EXISTS router_admin_audit_events (event_id TEXT PRIMARY KEY, at TEXT NOT NULL, runtime_scope TEXT NOT NULL DEFAULT '', principal_subject TEXT NOT NULL DEFAULT '', principal_kind TEXT NOT NULL DEFAULT '', credential_id TEXT NOT NULL DEFAULT '', issuer TEXT NOT NULL DEFAULT '', domain TEXT NOT NULL DEFAULT '', object TEXT NOT NULL DEFAULT '', action TEXT NOT NULL DEFAULT '', operation_id TEXT NOT NULL DEFAULT '', request_id TEXT NOT NULL DEFAULT '', outcome TEXT NOT NULL, reason_code TEXT NOT NULL DEFAULT '', ticket TEXT NOT NULL DEFAULT '', config_set_id TEXT NOT NULL DEFAULT '', prev_config_set_id TEXT NOT NULL DEFAULT '', config_revision BIGINT NOT NULL DEFAULT 0, before_hash TEXT NOT NULL DEFAULT '', after_hash TEXT NOT NULL DEFAULT '', idempotency_key TEXT NOT NULL DEFAULT '', request_hash TEXT NOT NULL DEFAULT '', detail TEXT NOT NULL DEFAULT '')`,
	`CREATE INDEX IF NOT EXISTS router_admin_audit_events_at ON router_admin_audit_events (at)`,
	`CREATE INDEX IF NOT EXISTS router_admin_audit_events_object ON router_admin_audit_events (object, action, at)`,
}

func applyUsageAdminAuditMigration(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("admin audit migration requires database")
	}
	for _, stmt := range usageAdminAuditDDL {
		if err := db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("create usage admin audit schema: %w", err)
		}
	}
	return verifyUsageAdminAuditMigration(db)
}

func verifyUsageAdminAuditMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable(usageAdminAuditTable) {
		return fmt.Errorf("required usage table %s is missing", usageAdminAuditTable)
	}
	for _, column := range []string{
		"event_id", "at", "principal_subject", "principal_kind", "credential_id", "issuer",
		"domain", "object", "action", "operation_id", "request_id", "outcome", "reason_code",
		"ticket", "before_hash", "after_hash", "request_hash",
	} {
		if !db.Migrator().HasColumn(usageAdminAuditTable, column) {
			return fmt.Errorf("required usage column %s.%s is missing", usageAdminAuditTable, column)
		}
	}
	return nil
}

// insertAdminAuditUsageTx writes one usage-side audit row inside the caller's
// transaction. It never stores content: identifiers, digests and redacted free
// text only.
func insertAdminAuditUsageTx(tx *gorm.DB, record AdminAuditRecord) error {
	if tx == nil {
		return fmt.Errorf("admin audit row requires a transaction")
	}
	if record.EventID == "" {
		record.EventID = newConfigChangeEventID()
	}
	at := record.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	outcome := record.Outcome
	if outcome == "" {
		outcome = "committed"
	}
	if err := tx.Exec(
		`INSERT INTO router_admin_audit_events (event_id, at, runtime_scope, principal_subject, principal_kind, credential_id, issuer, domain, object, action, operation_id, request_id, outcome, reason_code, ticket, config_set_id, prev_config_set_id, config_revision, before_hash, after_hash, idempotency_key, request_hash, detail) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.EventID, at.UTC().Format(time.RFC3339Nano), adminAuditFieldValue(record.RuntimeScope),
		adminAuditFieldValue(record.PrincipalSubject), adminAuditFieldValue(record.PrincipalKind),
		adminAuditFieldValue(record.CredentialID), adminAuditFieldValue(record.Issuer),
		adminAuditFieldValue(record.Domain), adminAuditFieldValue(record.Object), adminAuditFieldValue(record.Action),
		adminAuditFieldValue(record.OperationID), adminAuditFieldValue(record.RequestID), adminAuditFieldValue(outcome),
		adminAuditFieldValue(record.ReasonCode), adminAuditFieldValue(record.Ticket),
		adminAuditFieldValue(record.ConfigSetID), adminAuditFieldValue(record.PrevConfigSetID), record.ConfigRevision,
		adminAuditFieldValue(record.BeforeHash), adminAuditFieldValue(record.AfterHash),
		adminAuditFieldValue(record.IdempotencyKey), adminAuditFieldValue(record.RequestHash),
		adminAuditFieldValue(record.Detail),
	).Error; err != nil {
		return fmt.Errorf("append usage admin audit event: %w", err)
	}
	return nil
}

// auditRecordFromMeta builds an audit record from the request's audit context.
func auditRecordFromMeta(meta AdminAuditMeta, reasonCode string) AdminAuditRecord {
	return AdminAuditRecord{
		At:               time.Now().UTC(),
		PrincipalSubject: meta.Subject,
		PrincipalKind:    meta.Kind,
		CredentialID:     meta.CredentialID,
		Issuer:           meta.Issuer,
		Domain:           meta.Domain,
		Object:           meta.Object,
		Action:           meta.Action,
		OperationID:      meta.OperationID,
		RequestID:        meta.RequestID,
		Outcome:          "committed",
		ReasonCode:       reasonCode,
		Ticket:           meta.Ticket,
		RequestHash:      meta.RequestHash,
	}
}

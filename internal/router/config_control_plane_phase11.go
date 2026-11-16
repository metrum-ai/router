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
	configControlPlanePhase11MigrationID = 2026110701
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
	`CREATE TABLE IF NOT EXISTS router_admin_credential_events (event_id TEXT PRIMARY KEY, at TEXT NOT NULL, kind TEXT NOT NULL, credential_id TEXT NOT NULL DEFAULT '', subject TEXT NOT NULL DEFAULT '', issuer TEXT NOT NULL DEFAULT '', outcome TEXT NOT NULL, reason_code TEXT NOT NULL DEFAULT '', request_id TEXT NOT NULL DEFAULT '', client_ip_hash TEXT NOT NULL DEFAULT '', lifecycle TEXT NOT NULL DEFAULT 'auth', count INTEGER NOT NULL DEFAULT 1, previous_hash TEXT NOT NULL DEFAULT '', row_hash TEXT NOT NULL DEFAULT '')`,
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
	for _, column := range []string{"event_id", "at", "kind", "credential_id", "subject", "issuer", "outcome", "reason_code", "request_id", "client_ip_hash", "lifecycle", "count"} {
		if !tx.Migrator().HasColumn(adminCredentialEventsTable, column) {
			return fmt.Errorf("required control-plane column %s.%s is missing", adminCredentialEventsTable, column)
		}
	}
	return nil
}

// insertAdminCredentialEventTx writes one redacted credential event. Callers that mutate a
// credential call it inside the credential's transaction; the auth path calls it directly.
// Identical outcomes within one second collapse into a single row with a counter, so a
// credential guessing flood cannot grow the table by one row per attempt.
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
	event.Kind = adminAuditFieldValue(event.Kind)
	event.CredentialID = adminAuditFieldValue(event.CredentialID)
	event.Subject = adminAuditFieldValue(event.Subject)
	event.Issuer = adminAuditFieldValue(event.Issuer)
	event.Outcome = adminAuditFieldValue(event.Outcome)
	event.ReasonCode = adminAuditFieldValue(event.ReasonCode)
	event.RequestID = adminAuditFieldValue(event.RequestID)
	event.ClientIPHash = adminAuditFieldValue(event.ClientIPHash)
	event.Lifecycle = adminAuditFieldValue(event.Lifecycle)
	if err := lockAdminAuditChain(tx, string(AdminDomainCredential)); err != nil {
		return fmt.Errorf("lock credential audit chain: %w", err)
	}
	if event.Outcome == "failure" && !adminCredentialFailureWithinCap(tx, event) {
		// Drop the record but keep the change path working: the cap exists so a varied key ID
		// cannot turn the audit table into the denial-of-service vector.
		return nil
	}
	// The collapse key includes the lifecycle and the outcome, so an issue followed by a
	// revoke of the same credential never merges. Successful outcomes collapse too, so one
	// busy credential cannot add a row per authenticated request.
	// Failures collapse within one second and successes within five minutes, both keyed by
	// lifecycle and outcome. The longer success window means a credential that scrapes
	// constantly adds one counted row per window, not one per request.
	window := time.Second
	if event.Outcome == "success" {
		window = adminCredentialSuccessWindow
	}
	collapsed := tx.Exec(
		`UPDATE router_admin_credential_events SET count = count + 1 WHERE event_id = (SELECT event_id FROM router_admin_credential_events WHERE kind = ? AND credential_id = ? AND outcome = ? AND reason_code = ? AND client_ip_hash = ? AND lifecycle = ? AND at >= ? ORDER BY at DESC LIMIT 1)`,
		adminAuditFieldValue(event.Kind), adminAuditFieldValue(event.CredentialID), adminAuditFieldValue(event.Outcome),
		adminAuditFieldValue(event.ReasonCode), adminAuditFieldValue(event.ClientIPHash),
		adminAuditFieldValue(event.Lifecycle),
		event.At.Add(-window).UTC().Format(time.RFC3339Nano),
	)
	if collapsed.Error != nil {
		return fmt.Errorf("collapse credential event: %w", collapsed.Error)
	}
	if collapsed.RowsAffected > 0 {
		return rebuildCredentialEventHashes(tx)
	}
	previous := readAdminAuditChainHead(tx, "credential", string(AdminDomainCredential), adminCredentialEventsTable, "")
	row := adminAuditExportRow{Source: "credential", EventID: event.EventID, At: event.At.UTC().Format(time.RFC3339Nano), Domain: string(AdminDomainCredential), PrincipalKind: event.Kind, PrincipalSubject: event.Subject, CredentialID: event.CredentialID, Issuer: event.Issuer, Outcome: event.Outcome, ReasonCode: event.ReasonCode, RequestID: event.RequestID, ClientIPHash: event.ClientIPHash, OperationID: event.Lifecycle, Lifecycle: event.Lifecycle, Count: 1, Kind: event.Kind, Subject: event.Subject}
	rowHash, err := adminAuditChainHash(previous, row)
	if err != nil {
		return err
	}
	if err := tx.Exec(
		`INSERT INTO router_admin_credential_events (event_id, at, kind, credential_id, subject, issuer, outcome, reason_code, request_id, client_ip_hash, lifecycle, count, previous_hash, row_hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?) ON CONFLICT DO NOTHING`,
		event.EventID, event.At.UTC().Format(time.RFC3339Nano), adminAuditFieldValue(event.Kind),
		adminAuditFieldValue(event.CredentialID), adminAuditFieldValue(event.Subject), adminAuditFieldValue(event.Issuer),
		adminAuditFieldValue(event.Outcome), adminAuditFieldValue(event.ReasonCode), adminAuditFieldValue(event.RequestID),
		adminAuditFieldValue(event.ClientIPHash), adminAuditFieldValue(event.Lifecycle), previous, rowHash,
	).Error; err != nil {
		return fmt.Errorf("append credential event: %w", err)
	}
	if err := updateAdminAuditChainHead(tx, "credential", string(AdminDomainCredential), previous, rowHash); err != nil {
		return fmt.Errorf("update credential audit chain anchor: %w", err)
	}
	return nil
}

func rebuildCredentialEventHashes(tx *gorm.DB) error {
	var rows []adminAuditCredentialExportRecord
	if err := tx.Model(&adminAuditCredentialExportRecord{}).Order("at ASC").Order("event_id ASC").Find(&rows).Error; err != nil {
		return err
	}
	previous := ""
	for _, item := range rows {
		row := adminAuditExportRow{Source: "credential", EventID: item.EventID, At: item.At, Domain: string(AdminDomainCredential), PrincipalKind: item.Kind, PrincipalSubject: item.Subject, CredentialID: item.CredentialID, Issuer: item.Issuer, Outcome: item.Outcome, ReasonCode: item.ReasonCode, RequestID: item.RequestID, ClientIPHash: item.ClientIPHash, OperationID: item.Lifecycle, Lifecycle: item.Lifecycle, Count: item.Count, Kind: item.Kind, Subject: item.Subject}
		hash, err := adminAuditChainHash(previous, row)
		if err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE router_admin_credential_events SET previous_hash = ?, row_hash = ? WHERE event_id = ?`, previous, hash, item.EventID).Error; err != nil {
			return err
		}
		previous = hash
	}
	if len(rows) > 0 {
		if err := updateAdminAuditChainHead(tx, "credential", string(AdminDomainCredential), rows[0].PreviousHash, previous); err != nil {
			return err
		}
	}
	return nil
}

// PurgeAdminAuditEventsBefore removes audit records older than cutoff from all admin audit
// tables, honouring the configured retention. It is bounded per call so a long-neglected
// deployment cannot issue one unbounded delete.
// adminAuditPurgeBatch bounds one delete statement.
const adminAuditPurgeBatch = 500

// adminAuditTable is one admin audit table and the column that orders it for pruning.
type adminAuditTable struct {
	Name      string
	IDCol     string
	TimeCol   string
	DomainCol string
	Source    string
}

// adminAuditTablesForDB returns the admin audit tables that live in the given database. The
// credential-event and usage audit tables are in different databases, so a janitor runs once
// per handle and only touches the tables that handle owns.
func adminAuditTablesForDB(db *gorm.DB) []adminAuditTable {
	if db == nil {
		return nil
	}
	var tables []adminAuditTable
	if db.Migrator().HasTable("router_config_change_events") {
		tables = append(tables, adminAuditTable{Name: "router_config_change_events", IDCol: "event_id", TimeCol: "at", DomainCol: "authz_domain", Source: "control"})
	}
	if db.Migrator().HasTable(adminCredentialEventsTable) {
		tables = append(tables, adminAuditTable{Name: adminCredentialEventsTable, IDCol: "event_id", TimeCol: "at", Source: "credential"})
	}
	if db.Migrator().HasTable(usageAdminAuditTable) {
		tables = append(tables, adminAuditTable{Name: usageAdminAuditTable, IDCol: "event_id", TimeCol: "at", DomainCol: "domain", Source: "usage"})
	}
	return tables
}

// PurgeAdminAuditEventsBefore removes one bounded batch of expired admin audit rows from each
// admin audit table present in db. The delete is portable: the subquery selects the primary
// keys to remove by id, which is valid on SQLite and on PostgreSQL.
func PurgeAdminAuditEventsBefore(db *gorm.DB, cutoff time.Time) (int64, error) {
	if db == nil {
		return 0, nil
	}
	stamp := cutoff.UTC().Format(time.RFC3339Nano)
	var removed int64
	for _, table := range adminAuditTablesForDB(db) {
		where := "candidate." + table.TimeCol + " < ?"
		args := []any{stamp}
		if table.DomainCol != "" {
			where += fmt.Sprintf(` AND EXISTS (SELECT 1 FROM %s newer WHERE newer.%s = candidate.%s AND newer.%s < ? AND (newer.%s > candidate.%s OR (newer.%s = candidate.%s AND newer.%s > candidate.%s)))`, table.Name, table.DomainCol, table.DomainCol, table.TimeCol, table.TimeCol, table.TimeCol, table.TimeCol, table.TimeCol, table.IDCol, table.IDCol)
			args = append(args, stamp)
		} else {
			where += fmt.Sprintf(` AND candidate.%s != (SELECT %s FROM %s WHERE %s < ? ORDER BY %s DESC, %s DESC LIMIT 1)`, table.IDCol, table.IDCol, table.Name, table.TimeCol, table.TimeCol, table.IDCol)
			args = append(args, stamp)
		}
		stmt := fmt.Sprintf(`DELETE FROM %s WHERE %s IN (SELECT candidate.%s FROM %s candidate WHERE %s ORDER BY candidate.%s, candidate.%s LIMIT %d)`, table.Name, table.IDCol, table.IDCol, table.Name, where, table.TimeCol, table.IDCol, adminAuditPurgeBatch)
		result := db.Exec(stmt, args...)
		if result.Error != nil {
			return removed, fmt.Errorf("prune %s: %w", table.Name, result.Error)
		}
		removed += result.RowsAffected
		if result.RowsAffected > 0 && table.Source != "" && db.Migrator().HasTable(adminAuditChainAnchorsTable) {
			domains := []string{}
			if table.DomainCol == "" {
				domains = append(domains, string(AdminDomainCredential))
			} else if err := db.Raw(`SELECT DISTINCT ` + table.DomainCol + ` FROM ` + table.Name).Scan(&domains).Error; err != nil {
				return removed, err
			}
			for _, domain := range domains {
				var start string
				q := `SELECT previous_hash FROM ` + table.Name + ` ORDER BY at ASC, event_id ASC LIMIT 1`
				if table.DomainCol != "" {
					q = `SELECT previous_hash FROM ` + table.Name + ` WHERE ` + table.DomainCol + `=? ORDER BY at ASC, event_id ASC LIMIT 1`
					if err := db.Raw(q, domain).Row().Scan(&start); err != nil {
						continue
					}
				} else {
					if err := db.Raw(q).Row().Scan(&start); err != nil {
						continue
					}
				}
				if err := setAdminAuditChainStart(db, table.Source, domain, start); err != nil {
					return removed, err
				}
			}
		}
	}
	return removed, nil
}

// adminCredentialFailureSourceCap bounds how many distinct failure rows one source address
// can create per minute, so a flood that varies the claimed key ID cannot grow the table
// without limit.
const adminCredentialFailureSourceCap = 10

// adminCredentialFailureWithinCap reports whether a failure event for this source may be
// recorded, given the events already stored in the last minute.
func adminCredentialFailureWithinCap(tx *gorm.DB, event AdminCredentialEvent) bool {
	if tx == nil {
		return false
	}
	// A failure without a source digest still counts against a fixed bucket, so omitting or
	// hiding the address cannot evade the cap.
	bucket := strings.TrimSpace(event.ClientIPHash)
	if bucket == "" {
		bucket = adminCredentialFailureUnknownBucket
	}
	var count int64
	if err := tx.Model(&adminCredentialEventRow{}).
		Where("client_ip_hash = ? AND outcome = 'failure' AND at >= ?", bucket, event.At.Add(-time.Minute).UTC().Format(time.RFC3339Nano)).
		Count(&count).Error; err != nil {
		// Fail closed: a cap that cannot be evaluated stops further failure rows rather than
		// letting an unbounded flood through.
		return false
	}
	return count < adminCredentialFailureSourceCap
}

// adminCredentialFailureUnknownBucket is the cap bucket for failures without a source digest.
const adminCredentialFailureUnknownBucket = "unknown-source"

// adminCredentialSuccessWindow is how long successful authentication outcomes for one
// credential aggregate into a single counted row.
const adminCredentialSuccessWindow = 5 * time.Minute

type adminCredentialEventRow struct {
	EventID string `gorm:"column:event_id;primaryKey"`
}

func (adminCredentialEventRow) TableName() string { return adminCredentialEventsTable }

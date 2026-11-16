// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"

	"gorm.io/gorm"
)

const (
	adminAuditChainControlMigrationID = 2026111628
	adminAuditChainUsageMigrationID   = 2026111628
)

const adminAuditChainMigrationBatchSize = 250

// AdminAuditChainUsageMigrationDefinition adds write-time audit links and durable chain anchors to the usage database.
func AdminAuditChainUsageMigrationDefinition() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{ID: adminAuditChainUsageMigrationID, Scope: usageMigrationScope, Name: "stored admin audit hash chains", Release: "2026.11", Checksum: MigrationChecksum("usage.admin-audit-chain.v2"), SchemaVersion: 23, Transactional: true, MaintenanceMode: "online", RollbackClass: "restore-required", HandlerKey: "usage.admin-audit-chain.apply.v1@applyAdminAuditChainUsage", PostconditionKey: "usage.admin-audit-chain.verify.v1@verifyAdminAuditChainUsage", Dependencies: []int{usageHierarchyAuditMigrationID}, ExecutionMode: "transactional", LockClass: "online", TimeoutClass: "bounded", Apply: applyAdminAuditChainUsage, Verify: verifyAdminAuditChainUsage})
}

func applyAdminAuditChainUsage(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(usageAdminAuditTable) {
		return fmt.Errorf("usage admin audit table is required")
	}
	for _, column := range []string{"previous_hash", "row_hash"} {
		if !db.Migrator().HasColumn(usageAdminAuditTable, column) {
			if err := db.Exec(`ALTER TABLE router_admin_audit_events ADD COLUMN ` + column + ` TEXT NOT NULL DEFAULT ''`).Error; err != nil {
				return err
			}
		}
	}
	if err := ensureAdminAuditChainAnchors(db); err != nil {
		return err
	}
	if err := backfillUsageAuditChain(db); err != nil {
		return err
	}
	return verifyAdminAuditChainUsage(db)
}

func backfillUsageAuditChain(db *gorm.DB) error {
	previous := map[string]string{}
	var cursorDomain, cursorAt, cursorID string
	hasCursor := false
	for {
		var batch []adminAuditExportRecord
		query := db.Model(&adminAuditExportRecord{}).Order("domain ASC").Order("at ASC").Order("event_id ASC").Limit(adminAuditChainMigrationBatchSize)
		if hasCursor {
			query = query.Where(`domain > ? OR (domain = ? AND (at > ? OR (at = ? AND event_id > ?)))`, cursorDomain, cursorDomain, cursorAt, cursorAt, cursorID)
		}
		if err := query.Find(&batch).Error; err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		for _, record := range batch {
			prev := previous[record.Domain]
			if record.RowHash == "" {
				row := adminAuditRowFromExportRecord(record, "usage")
				hash, err := adminAuditChainHash(prev, row)
				if err != nil {
					return err
				}
				if err := db.Exec(`UPDATE router_admin_audit_events SET previous_hash=?,row_hash=? WHERE event_id=? AND row_hash=''`, prev, hash, record.EventID).Error; err != nil {
					return err
				}
				record.RowHash = hash
			}
			previous[record.Domain] = record.RowHash
			if err := updateAdminAuditChainHead(db, "usage", record.Domain, prev, record.RowHash); err != nil {
				return err
			}
			cursorDomain, cursorAt, cursorID, hasCursor = record.Domain, record.At, record.EventID, true
		}
	}
}

func adminAuditRowFromExportRecord(record adminAuditExportRecord, source string) adminAuditExportRow {
	return adminAuditExportRow{Source: source, EventID: record.EventID, At: record.At, PrincipalSubject: record.PrincipalSubject, PrincipalKind: record.PrincipalKind, CredentialID: record.CredentialID, ClientID: record.ClientID, Actor: record.Actor, Domain: record.Domain, Object: record.Object, Action: record.Action, OperationID: record.OperationID, RequestID: record.RequestID, Outcome: record.Outcome, ReasonCode: record.ReasonCode, ConfigRevision: record.ConfigRevision, BeforeHash: record.BeforeHash, AfterHash: record.AfterHash, RuntimeScope: record.RuntimeScope, Issuer: record.Issuer, Ticket: record.Ticket, ConfigSetID: record.ConfigSetID, PrevConfigSetID: record.PrevConfigSetID, IdempotencyKey: record.IdempotencyKey, RequestHash: record.RequestHash, Detail: record.Detail}
}

func verifyAdminAuditChainUsage(db *gorm.DB) error {
	for _, c := range []string{"previous_hash", "row_hash"} {
		if !db.Migrator().HasColumn(usageAdminAuditTable, c) {
			return fmt.Errorf("missing %s.%s", usageAdminAuditTable, c)
		}
	}
	return nil
}

func applyAdminAuditChainControl(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("control database required")
	}
	if !db.Migrator().HasTable("router_config_change_events") {
		if err := ensureConfigControlPlaneRuntimeTables(db); err != nil {
			return err
		}
	}
	if err := ensureAdminAuditChainAnchors(db); err != nil {
		return err
	}
	for _, column := range []struct{ table, name string }{{"router_config_change_events", "previous_hash"}, {"router_config_change_events", "row_hash"}, {adminCredentialEventsTable, "previous_hash"}, {adminCredentialEventsTable, "row_hash"}} {
		if !db.Migrator().HasTable(column.table) {
			continue
		}
		if !db.Migrator().HasColumn(column.table, column.name) {
			if err := db.Exec(`ALTER TABLE ` + column.table + ` ADD COLUMN ` + column.name + ` TEXT NOT NULL DEFAULT ''`).Error; err != nil {
				return err
			}
		}
	}
	if err := backfillControlAuditChain(db); err != nil {
		return err
	}
	if db.Migrator().HasTable(adminCredentialEventsTable) {
		if err := backfillCredentialAuditChain(db); err != nil {
			return err
		}
	}
	return verifyAdminAuditChainControl(db)
}

func backfillControlAuditChain(db *gorm.DB) error {
	previous := map[string]string{}
	var cursorDomain, cursorAt, cursorID string
	hasCursor := false
	for {
		var batch []adminAuditControlExportRecord
		query := db.Model(&adminAuditControlExportRecord{}).Order("authz_domain ASC").Order("at ASC").Order("event_id ASC").Limit(adminAuditChainMigrationBatchSize)
		if hasCursor {
			query = query.Where(`authz_domain > ? OR (authz_domain = ? AND (at > ? OR (at = ? AND event_id > ?)))`, cursorDomain, cursorDomain, cursorAt, cursorAt, cursorID)
		}
		if err := query.Find(&batch).Error; err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		for _, e := range batch {
			prev := previous[e.AuthzDomain]
			if e.RowHash == "" {
				row := adminAuditExportRow{Source: "control", EventID: e.EventID, At: e.At, PrincipalSubject: e.Actor, PrincipalKind: e.ActorKind, CredentialID: e.ActorCredentialID, Domain: e.AuthzDomain, Object: e.Object, Action: e.AuthzAction, OperationID: e.OperationID, RequestID: e.RequestID, Outcome: e.Outcome, ReasonCode: e.Reason, ConfigRevision: e.ConfigRevision, BeforeHash: e.BeforeHash, AfterHash: e.AfterHash, RuntimeScope: e.RuntimeScope, Issuer: e.ActorIssuer, Ticket: e.Ticket, ConfigSetID: e.ConfigSetID, PrevConfigSetID: e.PrevConfigSetID, IdempotencyKey: e.IdempotencyKey, RequestHash: e.RequestHash, Detail: e.Detail, ControlAction: e.Action, GroupName: e.GroupName, Forced: e.Forced != 0}
				hash, err := adminAuditChainHash(prev, row)
				if err != nil {
					return err
				}
				if err := db.Exec(`UPDATE router_config_change_events SET previous_hash=?,row_hash=? WHERE event_id=? AND row_hash=''`, prev, hash, e.EventID).Error; err != nil {
					return err
				}
				e.RowHash = hash
			}
			previous[e.AuthzDomain] = e.RowHash
			if err := updateAdminAuditChainHead(db, "control", e.AuthzDomain, prev, e.RowHash); err != nil {
				return err
			}
			cursorDomain, cursorAt, cursorID, hasCursor = e.AuthzDomain, e.At, e.EventID, true
		}
	}
}

func backfillCredentialAuditChain(db *gorm.DB) error {
	var cursorAt, cursorID string
	hasCursor := false
	previous := ""
	for {
		var batch []adminAuditCredentialExportRecord
		query := db.Model(&adminAuditCredentialExportRecord{}).Order("at ASC").Order("event_id ASC").Limit(adminAuditChainMigrationBatchSize)
		if hasCursor {
			query = query.Where(`at > ? OR (at = ? AND event_id > ?)`, cursorAt, cursorAt, cursorID)
		}
		if err := query.Find(&batch).Error; err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		for _, item := range batch {
			if item.RowHash == "" {
				row := adminAuditExportRow{Source: "credential", EventID: item.EventID, At: item.At, Domain: string(AdminDomainCredential), PrincipalKind: item.Kind, PrincipalSubject: item.Subject, CredentialID: item.CredentialID, ClientID: item.ClientID, Actor: item.Actor, Issuer: item.Issuer, Outcome: item.Outcome, ReasonCode: item.ReasonCode, RequestID: item.RequestID, ClientIPHash: item.ClientIPHash, OperationID: item.Lifecycle, Lifecycle: item.Lifecycle, Count: item.Count, Kind: item.Kind, Subject: item.Subject}
				hash, err := adminAuditChainHash(previous, row)
				if err != nil {
					return err
				}
				if err := db.Exec(`UPDATE router_admin_credential_events SET previous_hash=?,row_hash=? WHERE event_id=? AND row_hash=''`, previous, hash, item.EventID).Error; err != nil {
					return err
				}
				item.RowHash = hash
			}
			previous = item.RowHash
			if err := updateAdminAuditChainHead(db, "credential", string(AdminDomainCredential), item.PreviousHash, item.RowHash); err != nil {
				return err
			}
			cursorAt, cursorID, hasCursor = item.At, item.EventID, true
		}
	}
}

func verifyAdminAuditChainControl(db *gorm.DB) error {
	for _, t := range []string{"router_config_change_events", adminCredentialEventsTable} {
		if !db.Migrator().HasTable(t) {
			continue
		}
		for _, c := range []string{"previous_hash", "row_hash"} {
			if !db.Migrator().HasColumn(t, c) {
				return fmt.Errorf("missing %s.%s", t, c)
			}
		}
	}
	return nil
}

// AdminAuditChainControlMigrationDefinition adds write-time audit links and durable chain anchors to the control-plane database.
func AdminAuditChainControlMigrationDefinition() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{ID: adminAuditChainControlMigrationID, Scope: configControlPlaneScope, Name: "stored admin audit hash chains", Release: "2026.11", Checksum: MigrationChecksum("router-config.admin-audit-chain.v2"), SchemaVersion: 19, Transactional: true, MaintenanceMode: "online", RollbackClass: "restore-required", HandlerKey: "router-config.admin-audit-chain.apply.v1@applyAdminAuditChainControl", PostconditionKey: "router-config.admin-audit-chain.verify.v1@verifyAdminAuditChainControl", Dependencies: []int{2026111611}, ExecutionMode: "transactional", LockClass: "online", TimeoutClass: "bounded", Apply: applyAdminAuditChainControl, Verify: verifyAdminAuditChainControl})
}

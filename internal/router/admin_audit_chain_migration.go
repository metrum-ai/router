// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"

	"gorm.io/gorm"
)

const (
	adminAuditChainControlMigrationID = 2026111605
	adminAuditChainUsageMigrationID   = 2026111602
)

// AdminAuditChainUsageMigrationDefinition adds write-time audit links and durable chain anchors to the usage database.
func AdminAuditChainUsageMigrationDefinition() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{ID: adminAuditChainUsageMigrationID, Scope: usageMigrationScope, Name: "stored admin audit hash chains", Release: "2026.11", Checksum: MigrationChecksum("usage.admin-audit-chain.v1"), SchemaVersion: 16, Transactional: true, MaintenanceMode: "online", RollbackClass: "restore-required", HandlerKey: "usage.admin-audit-chain.apply.v1@applyAdminAuditChainUsage", PostconditionKey: "usage.admin-audit-chain.verify.v1@verifyAdminAuditChainUsage", Dependencies: []int{usageAdminAuditMigrationID}, ExecutionMode: "transactional", LockClass: "online", TimeoutClass: "bounded", Apply: applyAdminAuditChainUsage, Verify: verifyAdminAuditChainUsage})
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
	var rows []adminAuditExportRecord
	if err := db.Model(&adminAuditExportRecord{}).Order("domain ASC").Order("at ASC").Order("event_id ASC").Find(&rows).Error; err != nil {
		return err
	}
	previous := map[string]string{}
	for _, record := range rows {
		row := adminAuditExportRow{Source: "usage", EventID: record.EventID, At: record.At, PrincipalSubject: record.PrincipalSubject, PrincipalKind: record.PrincipalKind, CredentialID: record.CredentialID, Domain: record.Domain, Object: record.Object, Action: record.Action, OperationID: record.OperationID, RequestID: record.RequestID, Outcome: record.Outcome, ReasonCode: record.ReasonCode, ConfigRevision: record.ConfigRevision, BeforeHash: record.BeforeHash, AfterHash: record.AfterHash, RuntimeScope: record.RuntimeScope, Issuer: record.Issuer, Ticket: record.Ticket, ConfigSetID: record.ConfigSetID, PrevConfigSetID: record.PrevConfigSetID, IdempotencyKey: record.IdempotencyKey, RequestHash: record.RequestHash, Detail: record.Detail}
		prev := previous[record.Domain]
		hash, err := adminAuditChainHash(prev, row)
		if err != nil {
			return err
		}
		if record.RowHash == "" {
			if err := db.Exec(`UPDATE router_admin_audit_events SET previous_hash=?,row_hash=? WHERE event_id=?`, prev, hash, record.EventID).Error; err != nil {
				return err
			}
		}
		previous[record.Domain] = hash
		if err := updateAdminAuditChainHead(db, "usage", record.Domain, prev, hash); err != nil {
			return err
		}
	}
	return verifyAdminAuditChainUsage(db)
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
	var events []adminAuditControlExportRecord
	if err := db.Model(&adminAuditControlExportRecord{}).Order("authz_domain ASC").Order("at ASC").Order("event_id ASC").Find(&events).Error; err != nil {
		return err
	}
	previous := map[string]string{}
	for _, e := range events {
		row := adminAuditExportRow{Source: "control", EventID: e.EventID, At: e.At, PrincipalSubject: e.Actor, PrincipalKind: e.ActorKind, CredentialID: e.ActorCredentialID, Domain: e.AuthzDomain, Object: e.Object, Action: e.AuthzAction, OperationID: e.OperationID, RequestID: e.RequestID, Outcome: e.Outcome, ReasonCode: e.Reason, ConfigRevision: e.ConfigRevision, BeforeHash: e.BeforeHash, AfterHash: e.AfterHash, RuntimeScope: e.RuntimeScope, Issuer: e.ActorIssuer, Ticket: e.Ticket, ConfigSetID: e.ConfigSetID, PrevConfigSetID: e.PrevConfigSetID, IdempotencyKey: e.IdempotencyKey, RequestHash: e.RequestHash, Detail: e.Detail, ControlAction: e.Action}
		prev := previous[e.AuthzDomain]
		hash, err := adminAuditChainHash(prev, row)
		if err != nil {
			return err
		}
		if e.RowHash == "" {
			if err := db.Exec(`UPDATE router_config_change_events SET previous_hash=?,row_hash=? WHERE event_id=?`, prev, hash, e.EventID).Error; err != nil {
				return err
			}
		}
		previous[e.AuthzDomain] = hash
		if err := updateAdminAuditChainHead(db, "control", e.AuthzDomain, prev, hash); err != nil {
			return err
		}
	}
	if db.Migrator().HasTable(adminCredentialEventsTable) {
		if err := rebuildCredentialEventHashes(db); err != nil {
			return err
		}
	}
	return verifyAdminAuditChainControl(db)
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
	return FinalizeMigrationDefinition(MigrationDefinition{ID: adminAuditChainControlMigrationID, Scope: configControlPlaneScope, Name: "stored admin audit hash chains", Release: "2026.11", Checksum: MigrationChecksum("router-config.admin-audit-chain.v1"), SchemaVersion: 16, Transactional: true, MaintenanceMode: "online", RollbackClass: "restore-required", HandlerKey: "router-config.admin-audit-chain.apply.v1@applyAdminAuditChainControl", PostconditionKey: "router-config.admin-audit-chain.verify.v1@verifyAdminAuditChainControl", Dependencies: []int{callerTokenRevocationMigrationID}, ExecutionMode: "transactional", LockClass: "online", TimeoutClass: "bounded", Apply: applyAdminAuditChainControl, Verify: verifyAdminAuditChainControl})
}

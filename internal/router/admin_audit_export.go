// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

type adminAuditExportRow struct {
	Source           string `json:"source"`
	EventID          string `json:"event_id"`
	At               string `json:"at"`
	PrincipalSubject string `json:"principal_subject"`
	PrincipalKind    string `json:"principal_kind"`
	CredentialID     string `json:"credential_id"`
	Domain           string `json:"domain"`
	Object           string `json:"object"`
	Action           string `json:"action"`
	OperationID      string `json:"operation_id"`
	RequestID        string `json:"request_id"`
	Outcome          string `json:"outcome"`
	ReasonCode       string `json:"reason_code"`
	ConfigRevision   int64  `json:"config_revision"`
	BeforeHash       string `json:"before_hash"`
	AfterHash        string `json:"after_hash"`
	RuntimeScope     string `json:"runtime_scope"`
	Issuer           string `json:"issuer"`
	Ticket           string `json:"ticket"`
	ConfigSetID      string `json:"config_set_id"`
	PrevConfigSetID  string `json:"prev_config_set_id"`
	IdempotencyKey   string `json:"idempotency_key"`
	RequestHash      string `json:"request_hash"`
	Detail           string `json:"detail"`
	Kind             string `json:"kind,omitempty"`
	Subject          string `json:"subject,omitempty"`
	ClientIPHash     string `json:"client_ip_hash,omitempty"`
	Lifecycle        string `json:"lifecycle,omitempty"`
	Count            int64  `json:"count,omitempty"`
	ControlAction    string `json:"control_action,omitempty"`
}

type adminAuditExportItem struct {
	adminAuditExportRow
	PreviousHash string `json:"previous_hash"`
	Hash         string `json:"hash"`
}

var errAdminAuditDeleteNotOldest = errors.New("admin audit deletion must remove the oldest retained event")

type adminAuditExportDocument struct {
	Items       []adminAuditExportItem `json:"items"`
	ChainFrom   string                 `json:"chain_from"`
	ChainTo     string                 `json:"chain_to"`
	Checksum    string                 `json:"checksum"`
	ChainStarts map[string]string      `json:"chain_starts,omitempty"`
	ChainHeads  map[string]string      `json:"chain_heads,omitempty"`
	HasMore     bool                   `json:"has_more,omitempty"`
	HasCursor   bool                   `json:"has_cursor,omitempty"`
}

type adminAuditExportRecord struct {
	Source                                                     string `gorm:"-"`
	EventID, At, PrincipalSubject, PrincipalKind, CredentialID string
	Domain, Object, Action, OperationID, RequestID, Outcome    string
	ReasonCode, BeforeHash, AfterHash                          string
	RuntimeScope, Issuer, Ticket, ConfigSetID, PrevConfigSetID string
	IdempotencyKey, RequestHash, Detail, PreviousHash, RowHash string
	ControlAction                                              string
	ClientIPHash                                               string
	CredentialLifecycle                                        string
	ConfigRevision                                             int64
	Count                                                      int64
}

func (adminAuditExportRecord) TableName() string { return usageAdminAuditTable }

type adminAuditControlExportRecord struct {
	EventID, At, Actor, ActorKind, ActorCredentialID, ActorIssuer string
	AuthzDomain, Object, AuthzAction, OperationID, RequestID      string
	Outcome, Reason, BeforeHash, AfterHash, ConfigSetID           string
	RuntimeScope, Ticket, PreviousHash, RowHash, Action           string
	IdempotencyKey, RequestHash, Detail, PrevConfigSetID          string
	ConfigRevision                                                int64
}

type adminAuditCredentialExportRecord struct {
	EventID, At, Kind, CredentialID, Subject, Issuer, Outcome             string
	ReasonCode, RequestID, ClientIPHash, Lifecycle, PreviousHash, RowHash string
	Count                                                                 int64
}

func (adminAuditCredentialExportRecord) TableName() string { return adminCredentialEventsTable }

func (adminAuditControlExportRecord) TableName() string { return "router_config_change_events" }

type adminAuditExportRequest struct {
	DB        *gorm.DB
	ControlDB *gorm.DB
}

func adminAuditExportOperationSpecs() []Operation {
	return []Operation{
		{ID: "exportAdminAudit", Method: "GET", Path: "/admin/v1/audit-events/export", Summary: "Export a verifiable page of admin audit events", Object: "admin:audit", Action: "read", Domain: AdminDomainCredential, NoSideEffect: true, Response: adminAuditExportDocument{}, Errors: []int{400, 503}, RateClass: AdminRateClassRead, HandlerKey: "admin.audit.export", Pagination: &AdminPaginationSpec{Sorts: []string{"at"}, DefaultSort: "at"}},
		{ID: "deleteAdminAuditEvent", Method: "DELETE", Path: "/admin/v1/audit-events/{event_id}", Summary: "Delete the oldest retained admin audit event", Description: "Deletion advances the anchored chain checkpoint; only the oldest retained event in a domain can be deleted.", Object: "admin:audit", Action: "delete", Domain: AdminDomainCredential, Response: adminAuditExportRow{}, Errors: []int{404, 409, 503}, RateClass: AdminRateClassWrite, HandlerKey: "admin.audit.delete"},
	}
}

func adminAuditExportHandlers(db, controlDB *gorm.DB) map[string]http.HandlerFunc {
	h := &adminAuditExportRequest{DB: db, ControlDB: controlDB}
	return map[string]http.HandlerFunc{"admin.audit.export": h.export, "admin.audit.delete": h.delete}
}

func adminAuditChainHash(previous string, row adminAuditExportRow) (string, error) {
	encoded, err := json.Marshal(row)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append(append([]byte(previous), '\n'), encoded...))
	return hex.EncodeToString(sum[:]), nil
}

func lockAdminAuditChain(tx *gorm.DB, domain string) error {
	if tx == nil {
		return fmt.Errorf("audit chain requires transaction")
	}
	if tx.Dialector.Name() == "postgres" {
		return tx.Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`, "router-admin-audit:"+domain).Error
	}
	return nil
}

const adminAuditChainAnchorsTable = "router_admin_audit_chain_anchors"

func ensureAdminAuditChainAnchors(tx *gorm.DB) error {
	return tx.Exec(`CREATE TABLE IF NOT EXISTS router_admin_audit_chain_anchors (source TEXT NOT NULL, domain TEXT NOT NULL, start_hash TEXT NOT NULL DEFAULT '', head_hash TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL, PRIMARY KEY(source, domain))`).Error
}

func readAdminAuditChainHead(tx *gorm.DB, source, domain, fallbackTable, domainColumn string) string {
	var head string
	if tx != nil && tx.Migrator().HasTable(adminAuditChainAnchorsTable) && tx.Raw(`SELECT head_hash FROM `+adminAuditChainAnchorsTable+` WHERE source=? AND domain=?`, source, domain).Row().Scan(&head) == nil {
		return head
	}
	if tx != nil && fallbackTable != "" {
		q := `SELECT a.row_hash FROM ` + fallbackTable + ` a WHERE a.row_hash != '' AND NOT EXISTS (SELECT 1 FROM ` + fallbackTable + ` n WHERE n.previous_hash=a.row_hash)`
		if domainColumn != "" {
			q = `SELECT a.row_hash FROM ` + fallbackTable + ` a WHERE a.` + domainColumn + `=? AND a.row_hash != '' AND NOT EXISTS (SELECT 1 FROM ` + fallbackTable + ` n WHERE n.` + domainColumn + `=a.` + domainColumn + ` AND n.previous_hash=a.row_hash)`
		}
		if domainColumn != "" {
			_ = tx.Raw(q+` LIMIT 1`, domain).Row().Scan(&head)
		} else {
			_ = tx.Raw(q + ` LIMIT 1`).Row().Scan(&head)
		}
	}
	return head
}

func updateAdminAuditChainHead(tx *gorm.DB, source, domain, previous, hash string) error {
	if tx == nil || !tx.Migrator().HasTable(adminAuditChainAnchorsTable) {
		return nil
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	return tx.Exec(`INSERT INTO `+adminAuditChainAnchorsTable+` (source,domain,start_hash,head_hash,updated_at) VALUES (?,?,?,?,?) ON CONFLICT(source,domain) DO UPDATE SET head_hash=excluded.head_hash,updated_at=excluded.updated_at`, source, domain, previous, hash, stamp).Error
}

func setAdminAuditChainStart(tx *gorm.DB, source, domain, start string) error {
	if tx == nil || !tx.Migrator().HasTable(adminAuditChainAnchorsTable) {
		return nil
	}
	return tx.Exec(`UPDATE `+adminAuditChainAnchorsTable+` SET start_hash=?,updated_at=? WHERE source=? AND domain=?`, start, time.Now().UTC().Format(time.RFC3339Nano), source, domain).Error
}

func adminAuditRowFromRecord(record AdminAuditRecord, source string) adminAuditExportRow {
	return adminAuditExportRow{Source: source, EventID: record.EventID, At: record.At.UTC().Format(time.RFC3339Nano), PrincipalSubject: record.PrincipalSubject, PrincipalKind: record.PrincipalKind, CredentialID: record.CredentialID, Domain: record.Domain, Object: record.Object, Action: record.Action, OperationID: record.OperationID, RequestID: record.RequestID, Outcome: record.Outcome, ReasonCode: record.ReasonCode, ConfigRevision: record.ConfigRevision, BeforeHash: record.BeforeHash, AfterHash: record.AfterHash, RuntimeScope: record.RuntimeScope, Issuer: record.Issuer, Ticket: record.Ticket, ConfigSetID: record.ConfigSetID, PrevConfigSetID: record.PrevConfigSetID, IdempotencyKey: record.IdempotencyKey, RequestHash: record.RequestHash, Detail: record.Detail}
}

// VerifyAdminAuditExport verifies the stored row hashes and inter-row links in an export.
func VerifyAdminAuditExport(reader io.Reader) error {
	var document adminAuditExportDocument
	if err := json.NewDecoder(reader).Decode(&document); err != nil {
		return err
	}
	previousBySource := make(map[string]string)
	seen := make(map[string]bool)
	lastExportHash := document.ChainFrom
	for i, item := range document.Items {
		if !seen[item.Source] && !document.HasCursor && document.ChainStarts[item.Source] != item.PreviousHash {
			return fmt.Errorf("audit export item %d does not match anchored chain start", i)
		}
		if previous, ok := previousBySource[item.Source]; ok && item.PreviousHash != previous {
			return fmt.Errorf("audit export item %d has a broken previous_hash", i)
		}
		hash, err := adminAuditChainHash(item.PreviousHash, item.adminAuditExportRow)
		if err != nil {
			return err
		}
		if hash != item.Hash {
			return fmt.Errorf("audit export item %d hash mismatch", i)
		}
		previousBySource[item.Source] = item.Hash
		seen[item.Source] = true
		lastExportHash = item.Hash
	}
	if document.ChainTo != lastExportHash || document.Checksum != lastExportHash {
		return fmt.Errorf("audit export checksum mismatch")
	}
	if !document.HasMore {
		for source, head := range document.ChainHeads {
			if !seen[source] && !document.HasCursor {
				return fmt.Errorf("audit export is missing the %s chain", source)
			}
			if seen[source] && previousBySource[source] != head {
				return fmt.Errorf("audit export is missing or reorders rows at %s chain head", source)
			}
		}
	}
	return nil
}

func (h *adminAuditExportRequest) auditChainAnchors(domain string) (map[string]string, map[string]string) {
	starts, heads := map[string]string{}, map[string]string{}
	add := func(db *gorm.DB, source, table, domainColumn string) {
		if db == nil || !db.Migrator().HasTable(table) {
			return
		}
		var start, head string
		if db.Migrator().HasTable(adminAuditChainAnchorsTable) {
			if err := db.Raw(`SELECT start_hash,head_hash FROM `+adminAuditChainAnchorsTable+` WHERE source=? AND domain=?`, source, domain).Row().Scan(&start, &head); err == nil {
				starts[source], heads[source] = start, head
			} else {
				var count int64
				_ = db.Table(table).Where(domainColumn+" = ?", domain).Count(&count).Error
				if count > 0 {
					starts[source], heads[source] = "!missing-anchor", "!missing-anchor"
				}
			}
			return
		}
		if err := db.Raw(`SELECT a.previous_hash FROM `+table+` a WHERE a.`+domainColumn+` = ? AND NOT EXISTS (SELECT 1 FROM `+table+` p WHERE p.`+domainColumn+` = a.`+domainColumn+` AND p.row_hash = a.previous_hash) ORDER BY a.at ASC, a.event_id ASC LIMIT 1`, domain).Row().Scan(&start); err != nil {
			return
		}
		if err := db.Raw(`SELECT a.row_hash FROM `+table+` a WHERE a.`+domainColumn+` = ? AND a.row_hash != '' AND NOT EXISTS (SELECT 1 FROM `+table+` n WHERE n.`+domainColumn+` = a.`+domainColumn+` AND n.previous_hash = a.row_hash) ORDER BY a.at DESC, a.event_id DESC LIMIT 1`, domain).Row().Scan(&head); err != nil {
			return
		}
		starts[source], heads[source] = start, head
	}
	add(h.ControlDB, "control", "router_config_change_events", "authz_domain")
	add(h.DB, "usage", usageAdminAuditTable, "domain")
	if h.ControlDB != nil && domain == string(AdminDomainCredential) && h.ControlDB.Migrator().HasTable(adminCredentialEventsTable) {
		var start, head string
		if h.ControlDB.Migrator().HasTable(adminAuditChainAnchorsTable) {
			if h.ControlDB.Raw(`SELECT start_hash,head_hash FROM `+adminAuditChainAnchorsTable+` WHERE source=? AND domain=?`, "credential", domain).Row().Scan(&start, &head) == nil {
				starts["credential"], heads["credential"] = start, head
			} else {
				var count int64
				_ = h.ControlDB.Table(adminCredentialEventsTable).Count(&count).Error
				if count > 0 {
					starts["credential"], heads["credential"] = "!missing-anchor", "!missing-anchor"
				}
			}
			return starts, heads
		}
		if h.ControlDB.Raw(`SELECT a.previous_hash FROM `+adminCredentialEventsTable+` a WHERE NOT EXISTS (SELECT 1 FROM `+adminCredentialEventsTable+` p WHERE p.row_hash = a.previous_hash) ORDER BY at ASC, event_id ASC LIMIT 1`).Row().Scan(&start) == nil && h.ControlDB.Raw(`SELECT a.row_hash FROM `+adminCredentialEventsTable+` a WHERE a.row_hash != '' AND NOT EXISTS (SELECT 1 FROM `+adminCredentialEventsTable+` n WHERE n.previous_hash = a.row_hash) ORDER BY at DESC, event_id DESC LIMIT 1`).Row().Scan(&head) == nil {
			starts["credential"], heads["credential"] = start, head
		}
	}
	return starts, heads
}

func (h *adminAuditExportRequest) export(w http.ResponseWriter, r *http.Request) {
	meta, ok := AdminAuditMetaFrom(r.Context())
	if !ok || strings.TrimSpace(meta.Subject) == "" || strings.TrimSpace(meta.Domain) == "" || h == nil || (h.DB == nil && h.ControlDB == nil) {
		writeAdminProblemCode(w, r, http.StatusServiceUnavailable, "unavailable", "")
		return
	}
	page, _ := AdminPageFrom(r.Context())
	limit := page.LimitOr(AdminV1DefaultLimit)
	values := page.CursorValues()
	if len(values) != 0 && len(values) != 4 {
		writeAdminProblemCode(w, r, http.StatusBadRequest, "invalid-cursor", "")
		return
	}
	previous := ""
	if len(values) == 4 {
		if values[0] == "" || values[1] == "" {
			writeAdminProblemCode(w, r, http.StatusBadRequest, "invalid-cursor", "")
			return
		}
		previous = values[3]
		if len(previous) != 64 {
			writeAdminProblemCode(w, r, http.StatusBadRequest, "invalid-cursor", "")
			return
		}
	}
	descending := page.Descending()
	var records []adminAuditExportRecord
	for _, source := range []struct {
		name string
		db   *gorm.DB
	}{
		{"control", h.ControlDB}, {"usage", h.DB},
	} {
		if source.db == nil {
			continue
		}
		if source.name == "control" {
			if !source.db.Migrator().HasTable("router_config_change_events") || !source.db.Migrator().HasColumn("router_config_change_events", "authz_domain") {
				continue
			}
			var batch []adminAuditControlExportRecord
			query := source.db.WithContext(r.Context()).Model(&adminAuditControlExportRecord{}).Where("authz_domain = ?", meta.Domain)
			if len(values) == 4 {
				if descending {
					query = query.Where("(at < ? OR (at = ? AND (event_id < ? OR (event_id = ? AND ? > ?))))", values[0], values[0], values[1], values[1], values[2], source.name)
				} else {
					query = query.Where("(at > ? OR (at = ? AND (event_id > ? OR (event_id = ? AND ? < ?))))", values[0], values[0], values[1], values[1], values[2], source.name)
				}
			}
			order := "ASC"
			if descending {
				order = "DESC"
			}
			if err := query.Order("at " + order).Order("event_id " + order).Limit(limit + 1).Find(&batch).Error; err != nil {
				writeAdminProblemCode(w, r, http.StatusServiceUnavailable, "unavailable", "")
				return
			}
			for _, item := range batch {
				records = append(records, adminAuditExportRecord{Source: source.name, EventID: item.EventID, At: item.At, PrincipalSubject: item.Actor, PrincipalKind: item.ActorKind, CredentialID: item.ActorCredentialID, Domain: item.AuthzDomain, Object: item.Object, Action: item.AuthzAction, OperationID: item.OperationID, RequestID: item.RequestID, Outcome: item.Outcome, ReasonCode: item.Reason, ConfigRevision: item.ConfigRevision, BeforeHash: item.BeforeHash, AfterHash: item.AfterHash, RuntimeScope: item.RuntimeScope, Issuer: item.ActorIssuer, Ticket: item.Ticket, ConfigSetID: item.ConfigSetID, PreviousHash: item.PreviousHash, RowHash: item.RowHash, IdempotencyKey: item.IdempotencyKey, Detail: item.Detail, PrevConfigSetID: item.PrevConfigSetID, ControlAction: item.Action})
			}
			continue
		}
		var batch []adminAuditExportRecord
		query := source.db.WithContext(r.Context()).Model(&adminAuditExportRecord{}).Where("domain = ?", meta.Domain)
		if len(values) == 4 {
			if descending {
				query = query.Where("(at < ? OR (at = ? AND (event_id < ? OR (event_id = ? AND ? > ?))))", values[0], values[0], values[1], values[1], values[2], source.name)
			} else {
				query = query.Where("(at > ? OR (at = ? AND (event_id > ? OR (event_id = ? AND ? < ?))))", values[0], values[0], values[1], values[1], values[2], source.name)
			}
		}
		order := "ASC"
		if descending {
			order = "DESC"
		}
		if err := query.Order("at " + order).Order("event_id " + order).Limit(limit + 1).Find(&batch).Error; err != nil {
			writeAdminProblemCode(w, r, http.StatusServiceUnavailable, "unavailable", "")
			return
		}
		for _, item := range batch {
			item.Source = source.name
			records = append(records, item)
		}
	}
	if h.ControlDB != nil && meta.Domain == string(AdminDomainCredential) && h.ControlDB.Migrator().HasTable(adminCredentialEventsTable) {
		var batch []adminAuditCredentialExportRecord
		query := h.ControlDB.WithContext(r.Context()).Model(&adminAuditCredentialExportRecord{})
		if len(values) == 4 {
			if descending {
				query = query.Where("(at < ? OR (at = ? AND event_id < ?))", values[0], values[0], values[1])
			} else {
				query = query.Where("(at > ? OR (at = ? AND event_id > ?))", values[0], values[0], values[1])
			}
		}
		order := "ASC"
		if descending {
			order = "DESC"
		}
		if err := query.Order("at " + order).Order("event_id " + order).Limit(limit + 1).Find(&batch).Error; err != nil {
			writeAdminProblemCode(w, r, http.StatusServiceUnavailable, "unavailable", "")
			return
		}
		for _, item := range batch {
			records = append(records, adminAuditExportRecord{Source: "credential", EventID: item.EventID, At: item.At, PrincipalSubject: item.Subject, PrincipalKind: item.Kind, CredentialID: item.CredentialID, Domain: string(AdminDomainCredential), OperationID: item.Lifecycle, CredentialLifecycle: item.Lifecycle, RequestID: item.RequestID, Outcome: item.Outcome, ReasonCode: item.ReasonCode, Issuer: item.Issuer, PreviousHash: item.PreviousHash, RowHash: item.RowHash, ClientIPHash: item.ClientIPHash, Count: item.Count})
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].At != records[j].At {
			if descending {
				return records[i].At > records[j].At
			}
			return records[i].At < records[j].At
		}
		if records[i].EventID != records[j].EventID {
			if descending {
				return records[i].EventID > records[j].EventID
			}
			return records[i].EventID < records[j].EventID
		}
		if descending {
			return records[i].Source > records[j].Source
		}
		return records[i].Source < records[j].Source
	})
	more := len(records) > limit
	if more {
		records = records[:limit]
	}
	result := adminAuditExportDocument{Items: make([]adminAuditExportItem, 0, len(records)), ChainFrom: previous, HasMore: more, HasCursor: len(values) == 4}
	result.ChainStarts, result.ChainHeads = h.auditChainAnchors(meta.Domain)
	firstBySource, lastBySource := map[string]string{}, map[string]string{}
	for _, record := range records {
		if _, ok := firstBySource[record.Source]; !ok {
			firstBySource[record.Source] = record.PreviousHash
		}
		lastBySource[record.Source] = record.RowHash
		row := adminAuditExportRow{Source: record.Source, EventID: record.EventID, At: record.At, PrincipalSubject: record.PrincipalSubject, PrincipalKind: record.PrincipalKind, CredentialID: record.CredentialID, Domain: record.Domain, Object: record.Object, Action: record.Action, OperationID: record.OperationID, RequestID: record.RequestID, Outcome: record.Outcome, ReasonCode: record.ReasonCode, ConfigRevision: record.ConfigRevision, BeforeHash: record.BeforeHash, AfterHash: record.AfterHash, RuntimeScope: record.RuntimeScope, Issuer: record.Issuer, Ticket: record.Ticket, ConfigSetID: record.ConfigSetID, PrevConfigSetID: record.PrevConfigSetID, IdempotencyKey: record.IdempotencyKey, RequestHash: record.RequestHash, Detail: record.Detail, ControlAction: record.ControlAction}
		if record.Source == "credential" {
			row.Kind = record.PrincipalKind
			row.Subject = record.PrincipalSubject
			row.ClientIPHash = record.ClientIPHash
			row.Count = record.Count
			row.Lifecycle = record.CredentialLifecycle
		}
		calculated, err := adminAuditChainHash(record.PreviousHash, row)
		if err != nil || record.RowHash == "" || calculated != record.RowHash {
			writeAdminProblemCode(w, r, http.StatusServiceUnavailable, "audit-chain-invalid", record.Source)
			return
		}
		result.Items = append(result.Items, adminAuditExportItem{adminAuditExportRow: row, PreviousHash: record.PreviousHash, Hash: record.RowHash})
		previous = record.RowHash
	}
	if !result.HasCursor {
		for source, first := range firstBySource {
			if anchor, ok := result.ChainStarts[source]; ok && first != anchor {
				writeAdminProblemCode(w, r, http.StatusServiceUnavailable, "audit-chain-invalid", source)
				return
			}
		}
	}
	if !result.HasMore {
		for source, head := range result.ChainHeads {
			if last, ok := lastBySource[source]; ok {
				if last != head {
					writeAdminProblemCode(w, r, http.StatusServiceUnavailable, "audit-chain-invalid", source)
					return
				}
			} else if !result.HasCursor {
				writeAdminProblemCode(w, r, http.StatusServiceUnavailable, "audit-chain-invalid", source)
				return
			}
		}
	}
	result.ChainTo = previous
	result.Checksum = previous
	if more && len(records) > 0 && page != nil {
		last := records[len(records)-1]
		page.PublishNext(last.At, last.EventID, last.Source, previous)
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *adminAuditExportRequest) delete(w http.ResponseWriter, r *http.Request) {
	meta, ok := AdminAuditMetaFrom(r.Context())
	if !ok || strings.TrimSpace(meta.Subject) == "" || strings.TrimSpace(meta.Domain) == "" || h == nil || h.DB == nil {
		writeAdminProblemCode(w, r, http.StatusServiceUnavailable, "unavailable", "")
		return
	}
	eventID := strings.TrimSpace(r.PathValue("event_id"))
	if eventID == "" || len(eventID) > 128 {
		writeAdminProblemCode(w, r, http.StatusNotFound, "not-found", "")
		return
	}
	var deleted adminAuditExportRecord
	err := h.DB.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if err := lockAdminAuditChain(tx, meta.Domain); err != nil {
			return err
		}
		if err := tx.Where("event_id = ? AND domain = ?", eventID, meta.Domain).First(&deleted).Error; err != nil {
			return err
		}
		var older int64
		if err := tx.Model(&adminAuditExportRecord{}).Where("domain = ? AND (at < ? OR (at = ? AND event_id < ?))", meta.Domain, deleted.At, deleted.At, deleted.EventID).Count(&older).Error; err != nil {
			return err
		}
		if older != 0 {
			return errAdminAuditDeleteNotOldest
		}
		result := tx.Where("event_id = ? AND domain = ?", eventID, meta.Domain).Delete(&adminAuditExportRecord{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		var remaining int64
		if err := tx.Model(&adminAuditExportRecord{}).Where("domain = ?", meta.Domain).Count(&remaining).Error; err != nil {
			return err
		}
		if remaining == 0 {
			if tx.Migrator().HasTable(adminAuditChainAnchorsTable) {
				if err := tx.Exec(`DELETE FROM `+adminAuditChainAnchorsTable+` WHERE source='usage' AND domain=?`, meta.Domain).Error; err != nil {
					return err
				}
			}
		} else if tx.Migrator().HasTable(adminAuditChainAnchorsTable) {
			if err := setAdminAuditChainStart(tx, "usage", meta.Domain, deleted.RowHash); err != nil {
				return err
			}
		}
		record := auditRecordFromMeta(meta, "")
		record.Domain = string(AdminDomainGlobal)
		record.Action = "delete"
		record.OperationID = "deleteAdminAuditEvent"
		record.Outcome = "committed"
		record.Detail = "deleted_event=" + adminAuditFieldValue(eventID)
		return insertAdminAuditUsageTx(tx, record)
	})
	if err != nil {
		if errors.Is(err, errAdminAuditDeleteNotOldest) {
			writeAdminProblemCode(w, r, http.StatusConflict, "conflict", "only the oldest retained event can be deleted")
			return
		}
		if err == gorm.ErrRecordNotFound {
			writeAdminProblemCode(w, r, http.StatusNotFound, "not-found", "")
		} else {
			writeAdminProblemCode(w, r, http.StatusServiceUnavailable, "unavailable", "")
		}
		return
	}
	writeJSON(w, http.StatusOK, adminAuditExportRow{Source: "usage", EventID: deleted.EventID, At: deleted.At, PrincipalSubject: deleted.PrincipalSubject, PrincipalKind: deleted.PrincipalKind, CredentialID: deleted.CredentialID, Domain: deleted.Domain, Object: deleted.Object, Action: deleted.Action, OperationID: deleted.OperationID, RequestID: deleted.RequestID, Outcome: deleted.Outcome, ReasonCode: deleted.ReasonCode, ConfigRevision: deleted.ConfigRevision, BeforeHash: deleted.BeforeHash, AfterHash: deleted.AfterHash})
}

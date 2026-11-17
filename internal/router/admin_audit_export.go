// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
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
	ClientID         string `json:"client_id,omitempty"`
	Actor            string `json:"actor,omitempty"`
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
	LatencyMS        *int64 `json:"latency_ms,omitempty"`
	Kind             string `json:"kind,omitempty"`
	Subject          string `json:"subject,omitempty"`
	ClientIPHash     string `json:"client_ip_hash,omitempty"`
	Lifecycle        string `json:"lifecycle,omitempty"`
	Count            int64  `json:"count,omitempty"`
	ControlAction    string `json:"control_action,omitempty"`
	GroupName        string `json:"group_name,omitempty"`
	Forced           bool   `json:"forced,omitempty"`
}

type adminAuditExportItem struct {
	adminAuditExportRow
	PreviousHash string `json:"previous_hash"`
	Hash         string `json:"hash"`
}

var errAdminAuditDeleteNotOldest = errors.New("admin audit deletion must remove the oldest retained event")
var errAdminAuditDeleteProtected = errors.New("admin audit deletion markers cannot be deleted")

type adminAuditExportDocument struct {
	Items        []adminAuditExportItem `json:"items"`
	ChainFrom    string                 `json:"chain_from"`
	ChainTo      string                 `json:"chain_to"`
	Checksum     string                 `json:"checksum"`
	ChainStarts  map[string]string      `json:"chain_starts,omitempty"`
	ChainHeads   map[string]string      `json:"chain_heads,omitempty"`
	CursorSource string                 `json:"cursor_source,omitempty"`
	HasMore      bool                   `json:"has_more,omitempty"`
	HasCursor    bool                   `json:"has_cursor,omitempty"`
}

type adminAuditExportRecord struct {
	Source                                                     string `gorm:"-"`
	EventID, At, PrincipalSubject, PrincipalKind, CredentialID string
	ClientID, Actor                                            string `gorm:"-"`
	Domain, Object, Action, OperationID, RequestID, Outcome    string
	ReasonCode, BeforeHash, AfterHash                          string
	RuntimeScope, Issuer, Ticket, ConfigSetID, PrevConfigSetID string
	IdempotencyKey, RequestHash, Detail, PreviousHash, RowHash string
	ControlAction                                              string
	ClientIPHash                                               string
	CredentialLifecycle                                        string
	ConfigRevision                                             int64
	Count                                                      int64
	GroupName                                                  string `gorm:"-"`
	Forced                                                     bool   `gorm:"-"`
}

func (adminAuditExportRecord) TableName() string { return usageAdminAuditTable }

type adminAuditControlExportRecord struct {
	EventID, At, Actor, ActorKind, ActorCredentialID, ActorIssuer string
	AuthzDomain, Object, AuthzAction, OperationID, RequestID      string
	Outcome, Reason, BeforeHash, AfterHash, ConfigSetID           string
	RuntimeScope, Ticket, PreviousHash, RowHash, Action           string
	IdempotencyKey, RequestHash, Detail, PrevConfigSetID          string
	GroupName                                                     string
	Forced                                                        int
	ConfigRevision                                                int64
}

type adminAuditCredentialExportRecord struct {
	EventID, At, Kind, CredentialID, Subject, Issuer, Outcome             string
	ClientID, Actor                                                       string
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
		{ID: "exportAdminAudit", Method: "GET", Path: "/admin/v1/audit-events/export", Summary: "Export a verifiable page of admin audit events", Object: "admin:audit", Action: "read", Domain: AdminDomainCredential, NoSideEffect: true, Response: adminAuditExportDocument{}, Errors: []int{400, 503}, RateClass: AdminRateClassRead, HandlerKey: "admin.audit.export", Pagination: &AdminPaginationSpec{Sorts: []string{"at"}, DefaultSort: "at", Directions: []string{"asc"}}},
		{ID: "deleteAdminAuditEvent", Method: "DELETE", Path: "/admin/v1/audit-events/{event_id}", Summary: "Delete the oldest retained admin audit event", Description: "Deletion advances the anchored chain checkpoint; only the oldest retained event in a domain can be deleted.", Object: "admin:audit", Action: "delete", Domain: AdminDomainCredential, Response: adminAuditExportRow{}, Errors: []int{404, 409, 503}, RateClass: AdminRateClassWrite, HandlerKey: "admin.audit.delete"},
	}
}

func adminAuditExportHandlers(db, controlDB *gorm.DB) map[string]http.HandlerFunc {
	h := &adminAuditExportRequest{DB: db, ControlDB: controlDB}
	return map[string]http.HandlerFunc{"admin.audit.export": h.export, "admin.audit.delete": h.delete}
}

func adminAuditFormatTime(at time.Time) string {
	return at.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
}

func adminAuditChainHash(previous string, row adminAuditExportRow) (string, error) {
	encoded, err := json.Marshal(row)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append(append([]byte(previous), '\n'), encoded...))
	return hex.EncodeToString(sum[:]), nil
}

var adminAuditDBLocks sync.Map

type adminAuditTxLock struct {
	mu   *sync.Mutex
	once sync.Once
}

func (l *adminAuditTxLock) release() {
	if l != nil {
		l.once.Do(l.mu.Unlock)
	}
}

type adminAuditLockedConnPool struct {
	gorm.ConnPool
	lock *adminAuditTxLock
}

func (p *adminAuditLockedConnPool) Commit() error {
	err := p.ConnPool.(gorm.TxCommitter).Commit()
	p.lock.release()
	return err
}

func (p *adminAuditLockedConnPool) Rollback() error {
	err := p.ConnPool.(gorm.TxCommitter).Rollback()
	p.lock.release()
	return err
}

func lockAdminAuditChain(tx *gorm.DB, source, domain string) (func(), error) {
	if tx == nil {
		return nil, fmt.Errorf("audit chain requires transaction")
	}
	if tx.Dialector.Name() == "postgres" {
		return func() {}, tx.Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`, "router-admin-audit:"+source+":"+domain).Error
	}
	if tx.Dialector.Name() != "sqlite" {
		return func() {}, nil
	}
	if _, alreadyLocked := tx.Statement.ConnPool.(*adminAuditLockedConnPool); alreadyLocked {
		return func() {}, nil
	}
	pool := tx.Config.ConnPool
	lock, _ := adminAuditDBLocks.LoadOrStore(pool, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	state := &adminAuditTxLock{mu: mu}
	// The anchor write acquires SQLite's database writer lock before reading the
	// head. This also orders distinct sql.DB handles opened on the same file.
	if err := ensureAdminAuditChainAnchors(tx); err != nil {
		mu.Unlock()
		return nil, err
	}
	if err := tx.Exec(`INSERT INTO `+adminAuditChainAnchorsTable+` (source,domain,start_hash,head_hash,updated_at) VALUES (?,?, '', '', ?) ON CONFLICT(source,domain) DO NOTHING`, source, domain, time.Now().UTC().Format(time.RFC3339Nano)).Error; err != nil {
		state.release()
		return nil, err
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); ok {
		tx.Statement.ConnPool = &adminAuditLockedConnPool{ConnPool: tx.Statement.ConnPool, lock: state}
		return func() {}, nil
	}
	return state.release, nil
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

// normalizeAdminAuditAppendTime keeps the stored export order consistent with
// the transaction-serialized chain order. Caller timestamps remain unchanged
// unless concurrent or delayed writes would otherwise sort before their
// chain predecessor.
func normalizeAdminAuditAppendTime(tx *gorm.DB, source, domain, table, domainColumn, eventID string, at time.Time) time.Time {
	head := readAdminAuditChainHead(tx, source, domain, table, domainColumn)
	if head == "" || table == "" {
		return at.UTC()
	}
	var lastAt, lastID string
	query := `SELECT at,event_id FROM ` + table + ` WHERE row_hash = ?`
	args := []any{head}
	if domainColumn != "" {
		query += ` AND ` + domainColumn + ` = ?`
		args = append(args, domain)
	}
	if err := tx.Raw(query+` LIMIT 1`, args...).Row().Scan(&lastAt, &lastID); err != nil {
		return at.UTC()
	}
	previousAt, err := time.Parse(time.RFC3339Nano, lastAt)
	if err != nil {
		return at.UTC()
	}
	at = at.UTC()
	if at.Before(previousAt) || (at.Equal(previousAt) && eventID <= lastID) {
		return previousAt.Add(time.Nanosecond)
	}
	return at
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
	row := adminAuditExportRow{Source: source, EventID: record.EventID, At: adminAuditFormatTime(record.At), PrincipalSubject: record.PrincipalSubject, PrincipalKind: record.PrincipalKind, CredentialID: record.CredentialID, Domain: record.Domain, Object: record.Object, Action: record.Action, OperationID: record.OperationID, RequestID: record.RequestID, Outcome: record.Outcome, ReasonCode: record.ReasonCode, ConfigRevision: record.ConfigRevision, BeforeHash: record.BeforeHash, AfterHash: record.AfterHash, RuntimeScope: record.RuntimeScope, Issuer: record.Issuer, Ticket: record.Ticket, ConfigSetID: record.ConfigSetID, PrevConfigSetID: record.PrevConfigSetID, IdempotencyKey: record.IdempotencyKey, RequestHash: record.RequestHash, Detail: record.Detail}
	applyMCPAuditExportDetails(&row)
	return row
}

func applyMCPAuditExportDetails(row *adminAuditExportRow) {
	if row == nil || !strings.HasPrefix(row.Detail, "mcp:v1:") {
		return
	}
	var detail struct {
		Tool      string `json:"tool"`
		LatencyMS int64  `json:"latency_ms"`
	}
	if json.Unmarshal([]byte(strings.TrimPrefix(row.Detail, "mcp:v1:")), &detail) != nil || detail.Tool == "" || detail.LatencyMS < 0 {
		return
	}
	row.Detail = detail.Tool
	row.LatencyMS = &detail.LatencyMS
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
		if i == 0 && document.HasCursor && item.Source == document.CursorSource && item.PreviousHash != document.ChainFrom {
			return fmt.Errorf("audit export page does not continue from the cursor hash")
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
				start, ok := document.ChainStarts[source]
				if !ok || start != head {
					return fmt.Errorf("audit export is missing the %s chain", source)
				}
			}
			if seen[source] && previousBySource[source] != head {
				return fmt.Errorf("audit export is missing or reorders rows at %s chain head", source)
			}
		}
	}
	return nil
}

// VerifyAdminAuditExportPages verifies a complete cursor-paginated export in
// order, including the cross-page source links that cannot be established from
// any one page alone.
func VerifyAdminAuditExportPages(readers ...io.Reader) error {
	if len(readers) == 0 {
		return fmt.Errorf("no audit export pages provided")
	}
	lastBySource := map[string]string{}
	seenSource := map[string]bool{}
	var previousPage adminAuditExportDocument
	for pageIndex, reader := range readers {
		var page adminAuditExportDocument
		data, err := io.ReadAll(reader)
		if err != nil {
			return err
		}
		if err := VerifyAdminAuditExport(bytes.NewReader(data)); err != nil {
			return fmt.Errorf("page %d: %w", pageIndex+1, err)
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return err
		}
		if pageIndex > 0 {
			if !previousPage.HasMore {
				return fmt.Errorf("page %d follows an export marked complete", pageIndex+1)
			}
			if page.ChainFrom != previousPage.ChainTo {
				return fmt.Errorf("page %d does not continue the previous page checksum", pageIndex+1)
			}
		}
		firstOnPage := map[string]bool{}
		for _, item := range page.Items {
			if !firstOnPage[item.Source] {
				if expected, ok := lastBySource[item.Source]; ok {
					if item.PreviousHash != expected {
						return fmt.Errorf("page %d breaks the %s chain across pages", pageIndex+1, item.Source)
					}
				} else if start, ok := page.ChainStarts[item.Source]; ok && item.PreviousHash != start {
					return fmt.Errorf("page %d does not start at the %s checkpoint", pageIndex+1, item.Source)
				}
				firstOnPage[item.Source] = true
			}
			lastBySource[item.Source] = item.Hash
			seenSource[item.Source] = true
		}
		previousPage = page
	}
	if previousPage.HasMore {
		return fmt.Errorf("audit export pages are incomplete")
	}
	for source, head := range previousPage.ChainHeads {
		if !seenSource[source] {
			start, ok := previousPage.ChainStarts[source]
			if !ok || start != head {
				return fmt.Errorf("audit export is missing the %s chain", source)
			}
			continue
		}
		if lastBySource[source] != head {
			return fmt.Errorf("audit export is missing or reorders rows at %s chain head", source)
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
	if descending {
		writeAdminProblemCode(w, r, http.StatusBadRequest, "invalid-sort", "audit export supports ascending order only")
		return
	}
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
				records = append(records, adminAuditExportRecord{Source: source.name, EventID: item.EventID, At: item.At, PrincipalSubject: item.Actor, PrincipalKind: item.ActorKind, CredentialID: item.ActorCredentialID, Domain: item.AuthzDomain, Object: item.Object, Action: item.AuthzAction, OperationID: item.OperationID, RequestID: item.RequestID, Outcome: item.Outcome, ReasonCode: item.Reason, ConfigRevision: item.ConfigRevision, BeforeHash: item.BeforeHash, AfterHash: item.AfterHash, RuntimeScope: item.RuntimeScope, Issuer: item.ActorIssuer, Ticket: item.Ticket, ConfigSetID: item.ConfigSetID, PreviousHash: item.PreviousHash, RowHash: item.RowHash, IdempotencyKey: item.IdempotencyKey, RequestHash: item.RequestHash, Detail: item.Detail, PrevConfigSetID: item.PrevConfigSetID, ControlAction: item.Action, GroupName: item.GroupName, Forced: item.Forced != 0})
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
			records = append(records, adminAuditExportRecord{Source: "credential", EventID: item.EventID, At: item.At, PrincipalSubject: item.Subject, PrincipalKind: item.Kind, CredentialID: item.CredentialID, ClientID: item.ClientID, Actor: item.Actor, Domain: string(AdminDomainCredential), OperationID: item.Lifecycle, CredentialLifecycle: item.Lifecycle, RequestID: item.RequestID, Outcome: item.Outcome, ReasonCode: item.ReasonCode, Issuer: item.Issuer, PreviousHash: item.PreviousHash, RowHash: item.RowHash, ClientIPHash: item.ClientIPHash, Count: item.Count})
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
	if len(values) == 4 {
		result.CursorSource = values[2]
	}
	result.ChainStarts, result.ChainHeads = h.auditChainAnchors(meta.Domain)
	firstBySource, lastBySource := map[string]string{}, map[string]string{}
	for _, record := range records {
		if _, ok := firstBySource[record.Source]; !ok {
			firstBySource[record.Source] = record.PreviousHash
		} else if record.PreviousHash != lastBySource[record.Source] {
			writeAdminProblemCode(w, r, http.StatusServiceUnavailable, "audit-chain-invalid", record.Source)
			return
		}
		lastBySource[record.Source] = record.RowHash
		row := adminAuditExportRow{Source: record.Source, EventID: record.EventID, At: record.At, PrincipalSubject: record.PrincipalSubject, PrincipalKind: record.PrincipalKind, CredentialID: record.CredentialID, ClientID: record.ClientID, Actor: record.Actor, Domain: record.Domain, Object: record.Object, Action: record.Action, OperationID: record.OperationID, RequestID: record.RequestID, Outcome: record.Outcome, ReasonCode: record.ReasonCode, ConfigRevision: record.ConfigRevision, BeforeHash: record.BeforeHash, AfterHash: record.AfterHash, RuntimeScope: record.RuntimeScope, Issuer: record.Issuer, Ticket: record.Ticket, ConfigSetID: record.ConfigSetID, PrevConfigSetID: record.PrevConfigSetID, IdempotencyKey: record.IdempotencyKey, RequestHash: record.RequestHash, Detail: record.Detail, ControlAction: record.ControlAction, GroupName: record.GroupName, Forced: record.Forced}
		applyMCPAuditExportDetails(&row)
		if record.Source == "credential" {
			row.Kind = record.PrincipalKind
			row.ClientID = record.ClientID
			row.Actor = record.Actor
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
				start, ok := result.ChainStarts[source]
				if !ok || start != head {
					writeAdminProblemCode(w, r, http.StatusServiceUnavailable, "audit-chain-invalid", source)
					return
				}
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
		unlock, err := lockAdminAuditChain(tx, "usage", meta.Domain)
		if err != nil {
			return err
		}
		locked := true
		defer func() {
			if locked {
				unlock()
			}
		}()
		if err := tx.Where("event_id = ? AND domain = ?", eventID, meta.Domain).First(&deleted).Error; err != nil {
			return err
		}
		if strings.HasPrefix(deleted.Detail, "deleted_event=") {
			return errAdminAuditDeleteProtected
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
		unlock()
		locked = false
		record := auditRecordFromMeta(meta, "")
		record.Domain = AdminGlobalDomain
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
		if errors.Is(err, errAdminAuditDeleteProtected) {
			writeAdminProblemCode(w, r, http.StatusConflict, "audit-marker-protected", "deletion markers cannot be deleted")
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

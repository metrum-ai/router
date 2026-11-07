// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Idempotency-Key store for the /admin/v1 contract (ACP-016).
//
// Scope is (principal kind, principal subject, domain, operation), never the
// request path string, so a key cannot be replayed across operations or across
// domains. Records live 24 h by default and are persisted, so a retry after a
// process restart still replays the recorded result.
//
// Two idempotency modes exist in the registry:
//
//   - Handler: the configuration control-plane writes already record
//     idempotency_key + request_hash in router_config_change_events inside the
//     write transaction. That stays the single source of truth for those
//     operations and is not duplicated here.
//   - Store: every other operation whose POST/DELETE must be replay-safe
//     (caller issue/rotate/revoke, training export/label, content-capture
//     delete/purge) uses this store.
//
// Fail-closed rules: a store error fails the request (503) instead of running
// the operation unguarded; a response too large to record still commits but a
// later replay answers 409 "in doubt" rather than executing again; a record
// left in_progress past the in-doubt window is never re-executed automatically.
//
// No secret is stored. Keys are opaque client strings (bounded) and the request
// hash is a digest. A recorded body is an API response, and an operation whose
// response carries a credential (a caller token) is never recorded at all: the
// SensitiveResponse flag turns its record into "completed, unrecorded", so a retry
// answers 409 in doubt instead of replaying the token a second time.

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

const (
	// adminIdempotencyInDoubtWindow is how long an in_progress record is
	// honoured before a concurrent attempt is refused as "in doubt".
	adminIdempotencyInDoubtWindow = 60 * time.Second
	// adminIdempotencyTimeFormat is fixed-width so string comparison in SQL is
	// chronological on both SQLite and PostgreSQL.
	adminIdempotencyTimeFormat = "2006-01-02T15:04:05.000000Z"
	// adminIdempotencyPruneBatch bounds one opportunistic prune.
	adminIdempotencyPruneBatch = 500
)

// AdminIdempotencyConfig is the admin.api.idempotency block.
type AdminIdempotencyConfig struct {
	TTL              time.Duration `yaml:"ttl" json:"ttl"`
	MaxKeyBytes      int           `yaml:"max_key_bytes" json:"maxKeyBytes"`
	MaxResponseBytes int           `yaml:"max_response_bytes" json:"maxResponseBytes"`
}

func (c AdminIdempotencyConfig) normalized() AdminIdempotencyConfig {
	out := c
	if out.TTL <= 0 {
		out.TTL = 24 * time.Hour
	}
	if out.MaxKeyBytes <= 0 {
		out.MaxKeyBytes = 128
	}
	if out.MaxResponseBytes <= 0 {
		out.MaxResponseBytes = 1 << 20
	}
	return out
}

// adminIdempotencyScope identifies one idempotency scope.
type adminIdempotencyScope struct {
	PrincipalKind    string
	PrincipalSubject string
	Domain           string
	Operation        string
	Key              string
	RequestHash      string
}

func (s adminIdempotencyScope) validate(maxKeyBytes int) error {
	if strings.TrimSpace(s.PrincipalSubject) == "" || strings.TrimSpace(s.Operation) == "" {
		return fmt.Errorf("idempotency scope is incomplete")
	}
	if s.Key == "" || len(s.Key) > maxKeyBytes || strings.ContainsAny(s.Key, "\r\n\t") {
		return fmt.Errorf("invalid idempotency key")
	}
	return nil
}

// adminIdempotencyRecord is a stored record.
type adminIdempotencyRecord struct {
	State               string
	ResponseStatus      int
	ResponseContentType string
	ResponseETag        string
	ResponseBody        string
	// Sensitive marks a response that must never be persisted (it carries a
	// credential). A sensitive record keeps its claim but stores no body, so a replay
	// is refused as in doubt rather than returning the credential again.
	Sensitive   bool
	RequestHash string
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// adminIdempotencyOutcome is the decision for one request.
type adminIdempotencyOutcome int

const (
	adminIdempotencyProceed adminIdempotencyOutcome = iota
	adminIdempotencyReplay
	adminIdempotencyConflict
	adminIdempotencyInDoubt
)

// AdminIdempotencyKeyStore is the persistence seam for Idempotency-Key
// records. #816 reuses it for its retention metrics.
type AdminIdempotencyKeyStore interface {
	// Begin claims a key. It returns Proceed when the caller may run the
	// operation, Replay with the recorded response, Conflict when the same key
	// was used with a different body, and InDoubt when an earlier attempt did
	// not finish.
	Begin(scope adminIdempotencyScope, now time.Time) (adminIdempotencyRecord, adminIdempotencyOutcome, error)
	// Complete records the finished response.
	Complete(scope adminIdempotencyScope, record adminIdempotencyRecord, now time.Time) error
	// Abort releases a claim after a failed operation so a corrected retry with
	// the same key is allowed.
	Abort(scope adminIdempotencyScope) error
	// PruneExpired removes at most one bounded batch of expired records.
	PruneExpired(now time.Time) (int64, error)
}

// ---------------------------------------------------------------------------
// SQL store
// ---------------------------------------------------------------------------

const adminIdempotencyTable = "router_admin_idempotency"

var adminIdempotencyDDL = []string{
	`CREATE TABLE IF NOT EXISTS router_admin_idempotency (principal_kind TEXT NOT NULL, principal_subject TEXT NOT NULL, domain TEXT NOT NULL, operation TEXT NOT NULL, idempotency_key TEXT NOT NULL, request_hash TEXT NOT NULL DEFAULT '', state TEXT NOT NULL, response_status INTEGER NOT NULL DEFAULT 0, response_content_type TEXT NOT NULL DEFAULT '', response_etag TEXT NOT NULL DEFAULT '', response_body TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, expires_at TEXT NOT NULL, PRIMARY KEY (principal_kind, principal_subject, domain, operation, idempotency_key))`,
	`CREATE INDEX IF NOT EXISTS router_admin_idempotency_expires ON router_admin_idempotency (expires_at)`,
}

type sqlAdminIdempotencyStore struct {
	db  *gorm.DB
	cfg AdminIdempotencyConfig
}

// NewSQLAdminIdempotencyStore returns a store on db. The table is created by
// the migration that owns the scope (config control plane or usage); ensure is
// idempotent so a snapshot restored before the migration still works.
func NewSQLAdminIdempotencyStore(db *gorm.DB, cfg AdminIdempotencyConfig) (AdminIdempotencyKeyStore, error) {
	if db == nil {
		return nil, fmt.Errorf("idempotency store requires a database")
	}
	for _, stmt := range adminIdempotencyDDL {
		if err := db.Exec(stmt).Error; err != nil {
			return nil, fmt.Errorf("ensure idempotency schema: %w", err)
		}
	}
	return &sqlAdminIdempotencyStore{db: db, cfg: cfg.normalized()}, nil
}

func adminIdempotencyTime(t time.Time) string { return t.UTC().Format(adminIdempotencyTimeFormat) }

func (s *sqlAdminIdempotencyStore) Begin(scope adminIdempotencyScope, now time.Time) (adminIdempotencyRecord, adminIdempotencyOutcome, error) {
	if err := scope.validate(s.cfg.MaxKeyBytes); err != nil {
		return adminIdempotencyRecord{}, adminIdempotencyProceed, err
	}
	rows, err := s.load(scope)
	if err != nil {
		return adminIdempotencyRecord{}, adminIdempotencyProceed, err
	}
	if len(rows) > 0 {
		record := rows[0]
		if !record.ExpiresAt.IsZero() && !now.Before(record.ExpiresAt) {
			if err := s.deleteExpired(scope, record.ExpiresAt); err != nil {
				return adminIdempotencyRecord{}, adminIdempotencyProceed, err
			}
		} else {
			return s.classify(record, scope, now)
		}
	}
	expires := now.Add(s.cfg.TTL)
	result := s.db.Exec(
		`INSERT INTO router_admin_idempotency (principal_kind, principal_subject, domain, operation, idempotency_key, request_hash, state, response_status, response_content_type, response_etag, response_body, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, 0, '', '', '', ?, ?) ON CONFLICT DO NOTHING`,
		scope.PrincipalKind, scope.PrincipalSubject, scope.Domain, scope.Operation, scope.Key, scope.RequestHash,
		"in_progress", adminIdempotencyTime(now), adminIdempotencyTime(expires),
	)
	if result.Error != nil {
		return adminIdempotencyRecord{}, adminIdempotencyProceed, fmt.Errorf("claim idempotency key: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		// Another writer won the claim; classify its record.
		rows, err := s.load(scope)
		if err != nil {
			return adminIdempotencyRecord{}, adminIdempotencyProceed, err
		}
		if len(rows) == 0 {
			return adminIdempotencyRecord{}, adminIdempotencyProceed, fmt.Errorf("idempotency claim disappeared")
		}
		return s.classify(rows[0], scope, now)
	}
	return adminIdempotencyRecord{State: "in_progress", CreatedAt: now}, adminIdempotencyProceed, nil
}

func (s *sqlAdminIdempotencyStore) classify(record adminIdempotencyRecord, scope adminIdempotencyScope, now time.Time) (adminIdempotencyRecord, adminIdempotencyOutcome, error) {
	if record.RequestHash != scope.RequestHash {
		return adminIdempotencyRecord{}, adminIdempotencyConflict, nil
	}
	switch record.State {
	case "completed":
		return record, adminIdempotencyReplay, nil
	default:
		if now.Sub(record.CreatedAt) > adminIdempotencyInDoubtWindow {
			return record, adminIdempotencyInDoubt, nil
		}
		return record, adminIdempotencyInDoubt, nil
	}
}

type adminIdempotencyRow struct {
	PrincipalKind       string `gorm:"column:principal_kind"`
	PrincipalSubject    string `gorm:"column:principal_subject"`
	Domain              string `gorm:"column:domain"`
	Operation           string `gorm:"column:operation"`
	IdempotencyKey      string `gorm:"column:idempotency_key"`
	RequestHash         string `gorm:"column:request_hash"`
	State               string `gorm:"column:state"`
	ResponseStatus      int    `gorm:"column:response_status"`
	ResponseContentType string `gorm:"column:response_content_type"`
	ResponseETag        string `gorm:"column:response_etag"`
	ResponseBody        string `gorm:"column:response_body"`
	CreatedAt           string `gorm:"column:created_at"`
	ExpiresAt           string `gorm:"column:expires_at"`
}

// TableName pins the gorm table name; without it gorm would derive
// "admin_idempotency_rows" from the struct name.
func (adminIdempotencyRow) TableName() string { return adminIdempotencyTable }

func (s *sqlAdminIdempotencyStore) load(scope adminIdempotencyScope) ([]adminIdempotencyRecord, error) {
	var rows []adminIdempotencyRow
	if err := s.db.Where(
		"principal_kind = ? AND principal_subject = ? AND domain = ? AND operation = ? AND idempotency_key = ?",
		scope.PrincipalKind, scope.PrincipalSubject, scope.Domain, scope.Operation, scope.Key,
	).Limit(1).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load idempotency record: %w", err)
	}
	out := make([]adminIdempotencyRecord, 0, len(rows))
	for _, row := range rows {
		created, _ := time.Parse(adminIdempotencyTimeFormat, row.CreatedAt)
		expires, _ := time.Parse(adminIdempotencyTimeFormat, row.ExpiresAt)
		out = append(out, adminIdempotencyRecord{
			State: row.State, ResponseStatus: row.ResponseStatus, ResponseContentType: row.ResponseContentType,
			ResponseETag: row.ResponseETag, ResponseBody: row.ResponseBody, RequestHash: row.RequestHash,
			CreatedAt: created, ExpiresAt: expires,
		})
	}
	return out, nil
}

// deleteExpired removes an expired record only if it still carries the expiry that
// was observed, so a concurrently re-claimed row is never deleted by the loser.
func (s *sqlAdminIdempotencyStore) deleteExpired(scope adminIdempotencyScope, observedExpiry time.Time) error {
	return s.db.Exec(
		`DELETE FROM router_admin_idempotency WHERE principal_kind = ? AND principal_subject = ? AND domain = ? AND operation = ? AND idempotency_key = ? AND expires_at = ?`,
		scope.PrincipalKind, scope.PrincipalSubject, scope.Domain, scope.Operation, scope.Key,
		adminIdempotencyTime(observedExpiry),
	).Error
}

func (s *sqlAdminIdempotencyStore) delete(scope adminIdempotencyScope) error {
	return s.db.Exec(
		`DELETE FROM router_admin_idempotency WHERE principal_kind = ? AND principal_subject = ? AND domain = ? AND operation = ? AND idempotency_key = ?`,
		scope.PrincipalKind, scope.PrincipalSubject, scope.Domain, scope.Operation, scope.Key,
	).Error
}

func (s *sqlAdminIdempotencyStore) Complete(scope adminIdempotencyScope, record adminIdempotencyRecord, now time.Time) error {
	body := record.ResponseBody
	if record.Sensitive {
		body = ""
		record.State = "completed_unrecorded"
	} else if len(body) > s.cfg.MaxResponseBytes {
		// Too large to record: keep the claim so a replay is refused as in
		// doubt instead of re-executing the operation.
		body = ""
		record.State = "completed_unrecorded"
	} else if record.State == "" || record.State == "in_progress" {
		record.State = "completed"
	}
	expires := now.Add(s.cfg.TTL)
	result := s.db.Exec(
		`UPDATE router_admin_idempotency SET state = ?, response_status = ?, response_content_type = ?, response_etag = ?, response_body = ?, expires_at = ? WHERE principal_kind = ? AND principal_subject = ? AND domain = ? AND operation = ? AND idempotency_key = ?`,
		record.State, record.ResponseStatus, record.ResponseContentType, record.ResponseETag, body, adminIdempotencyTime(expires),
		scope.PrincipalKind, scope.PrincipalSubject, scope.Domain, scope.Operation, scope.Key,
	)
	if result.Error != nil {
		return fmt.Errorf("complete idempotency record: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("idempotency record disappeared before completion")
	}
	return nil
}

func (s *sqlAdminIdempotencyStore) Abort(scope adminIdempotencyScope) error {
	if err := s.delete(scope); err != nil {
		return fmt.Errorf("release idempotency claim: %w", err)
	}
	return nil
}

func (s *sqlAdminIdempotencyStore) PruneExpired(now time.Time) (int64, error) {
	result := s.db.Exec(
		`DELETE FROM router_admin_idempotency WHERE expires_at < ? AND idempotency_key IN (SELECT idempotency_key FROM router_admin_idempotency WHERE expires_at < ? LIMIT ?)`,
		adminIdempotencyTime(now), adminIdempotencyTime(now), adminIdempotencyPruneBatch,
	)
	if result.Error != nil {
		return 0, fmt.Errorf("prune idempotency records: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// ---------------------------------------------------------------------------
// In-memory store (no database configured)
// ---------------------------------------------------------------------------

// memoryAdminIdempotencyStore keeps the same semantics without a database. It
// is bounded by maxEntries so a hostile client cannot grow process memory; the
// registry only uses it for planes with no database at all, and the operator
// guide states that records then do not survive a restart.
type memoryAdminIdempotencyStore struct {
	mu         sync.Mutex
	cfg        AdminIdempotencyConfig
	maxEntries int
	records    map[string]memoryAdminIdempotencyRecord
}

type memoryAdminIdempotencyRecord struct {
	record         adminIdempotencyRecord
	requestHash    string
	createdAt      time.Time
	lastAccessUnix int64
}

// NewMemoryAdminIdempotencyStore returns a bounded in-process store.
func NewMemoryAdminIdempotencyStore(cfg AdminIdempotencyConfig, maxEntries int) AdminIdempotencyKeyStore {
	if maxEntries <= 0 {
		maxEntries = 1024
	}
	return &memoryAdminIdempotencyStore{cfg: cfg.normalized(), maxEntries: maxEntries, records: map[string]memoryAdminIdempotencyRecord{}}
}

func memoryAdminIdempotencyKey(scope adminIdempotencyScope) string {
	return strings.Join([]string{scope.PrincipalKind, scope.PrincipalSubject, scope.Domain, scope.Operation, scope.Key}, "\x00")
}

func (s *memoryAdminIdempotencyStore) Begin(scope adminIdempotencyScope, now time.Time) (adminIdempotencyRecord, adminIdempotencyOutcome, error) {
	if err := scope.validate(s.cfg.MaxKeyBytes); err != nil {
		return adminIdempotencyRecord{}, adminIdempotencyProceed, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := memoryAdminIdempotencyKey(scope)
	existing, ok := s.records[key]
	if ok {
		if now.Sub(existing.createdAt) > s.cfg.TTL {
			delete(s.records, key)
		} else if existing.requestHash != scope.RequestHash {
			return adminIdempotencyRecord{}, adminIdempotencyConflict, nil
		} else if existing.record.State == "completed" {
			return existing.record, adminIdempotencyReplay, nil
		} else {
			return existing.record, adminIdempotencyInDoubt, nil
		}
	}
	if len(s.records) >= s.maxEntries {
		s.evictLocked(now)
		if len(s.records) >= s.maxEntries {
			return adminIdempotencyRecord{}, adminIdempotencyProceed, errors.New("idempotency store is full")
		}
	}
	record := adminIdempotencyRecord{State: "in_progress", RequestHash: scope.RequestHash, CreatedAt: now}
	s.records[key] = memoryAdminIdempotencyRecord{record: record, requestHash: scope.RequestHash, createdAt: now, lastAccessUnix: now.UnixNano()}
	return record, adminIdempotencyProceed, nil
}

func (s *memoryAdminIdempotencyStore) evictLocked(now time.Time) {
	// Drop expired records first, then the least recently used.
	for key, value := range s.records {
		if now.Sub(value.createdAt) > s.cfg.TTL {
			delete(s.records, key)
		}
	}
	for len(s.records) >= s.maxEntries {
		oldestKey, oldest := "", int64(1<<62)
		for key, value := range s.records {
			if value.lastAccessUnix < oldest {
				oldestKey, oldest = key, value.lastAccessUnix
			}
		}
		if oldestKey == "" {
			return
		}
		delete(s.records, oldestKey)
	}
}

func (s *memoryAdminIdempotencyStore) Complete(scope adminIdempotencyScope, record adminIdempotencyRecord, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := memoryAdminIdempotencyKey(scope)
	existing, ok := s.records[key]
	if !ok {
		return errors.New("idempotency record disappeared before completion")
	}
	if record.Sensitive {
		record.ResponseBody = ""
		record.State = "completed_unrecorded"
	} else if len(record.ResponseBody) > s.cfg.MaxResponseBytes {
		record.ResponseBody = ""
		record.State = "completed_unrecorded"
	} else if record.State == "" || record.State == "in_progress" {
		record.State = "completed"
	}
	record.RequestHash = existing.requestHash
	existing.record = record
	existing.lastAccessUnix = now.UnixNano()
	s.records[key] = existing
	return nil
}

func (s *memoryAdminIdempotencyStore) Abort(scope adminIdempotencyScope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, memoryAdminIdempotencyKey(scope))
	return nil
}

func (s *memoryAdminIdempotencyStore) PruneExpired(now time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var removed int64
	for key, value := range s.records {
		if now.Sub(value.createdAt) > s.cfg.TTL {
			delete(s.records, key)
			removed++
		}
	}
	return removed, nil
}

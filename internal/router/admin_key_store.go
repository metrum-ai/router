// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// #815 admin-key lifecycle store. Every validity decision reads the
// authoritative control-plane rows (revocation, expiry, overlap deadline,
// generation acceptance), so a replica never extends trust from a cache. Only
// PBKDF2-HMAC-SHA3-512 verifiers are stored; the one-time secret is returned by
// issue/rotate and never persisted or projected.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	adminKeyMetadataTable      = "admin_api_key_metadata"
	adminKeyGenerationTable    = "admin_api_key_generations"
	adminKeyDefaultMaxLife     = 720 * time.Hour
	adminKeyDefaultOverlap     = 5 * time.Minute
	adminKeyMaxOverlap         = time.Hour
	adminKeyMaxLiveGenerations = 2
)

type adminKeyIssueRequest struct {
	Subject     string // optional; the server always generates adminkey:<id>
	ExpiresAt   time.Time
	MaxLifetime time.Duration
}
type adminKeyRotateRequest struct{ Overlap time.Duration }
type adminKeyListRequest struct{}
type adminKeySecret struct {
	ID, Subject, Secret string
	Generation          int
	ExpiresAt           time.Time
}
type adminKeyMetadata struct {
	ID          string     `json:"id"`
	Subject     string     `json:"subject"`
	Status      string     `json:"status"`
	ExpiresAt   time.Time  `json:"expiresAt"`
	CreatedAt   time.Time  `json:"createdAt"`
	RevokedAt   *time.Time `json:"revokedAt,omitempty"`
	Generations int        `json:"generations"`
	Revision    int64      `json:"revision"`
}
type adminKeyListResult struct {
	Keys     []adminKeyMetadata `json:"keys"`
	Revision int64              `json:"revision"`
}

// adminKeyStore reads and mutates only the authoritative rows.
type adminKeyStore struct {
	db  *gorm.DB
	now func() time.Time
}

func newAdminKeyStore(db *gorm.DB, now func() time.Time) *adminKeyStore {
	if now == nil {
		now = time.Now
	}
	return &adminKeyStore{db: db, now: now}
}

func (s *adminKeyStore) issue(ctx context.Context, req adminKeyIssueRequest) (adminKeySecret, error) {
	maxLifetime := req.MaxLifetime
	if maxLifetime == 0 {
		maxLifetime = adminKeyDefaultMaxLife
	}
	if maxLifetime < time.Hour || maxLifetime > adminKeyDefaultMaxLife {
		return adminKeySecret{}, errors.New("admin key maximum lifetime invalid")
	}
	now := s.now().UTC()
	if req.ExpiresAt.IsZero() || !req.ExpiresAt.After(now) || req.ExpiresAt.Sub(now) > maxLifetime {
		return adminKeySecret{}, errors.New("admin key expiry invalid")
	}
	id, err := randomAdminKeyID()
	if err != nil {
		return adminKeySecret{}, err
	}
	secret, err := randomAdminKeySecret(id)
	if err != nil {
		return adminKeySecret{}, err
	}
	verifier, err := HashCallerToken(secret, rand.Reader, 0)
	if err != nil {
		return adminKeySecret{}, err
	}
	out := adminKeySecret{ID: id, Subject: "adminkey:" + id, Generation: 1, ExpiresAt: req.ExpiresAt.UTC()}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("INSERT INTO "+adminKeyMetadataTable+
			" (id, subject, status, expires_at, created_at, revision) VALUES (?, ?, 'active', ?, ?, 1)",
			id, out.Subject, out.ExpiresAt, now).Error; err != nil {
			return err
		}
		return tx.Exec("INSERT INTO "+adminKeyGenerationTable+
			" (key_id, generation, verifier, created_at, overlap_until) VALUES (?, 1, ?, ?, NULL)",
			id, verifier, now).Error
	})
	if err != nil {
		return adminKeySecret{}, err
	}
	out.Secret = secret
	return out, nil
}

func (s *adminKeyStore) rotate(ctx context.Context, id string, req adminKeyRotateRequest) (adminKeySecret, error) {
	if req.Overlap < 0 || req.Overlap > adminKeyMaxOverlap {
		return adminKeySecret{}, errors.New("admin key rotation overlap invalid")
	}
	now := s.now().UTC()
	var out adminKeySecret
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var meta []struct {
			Subject   string
			Status    string
			ExpiresAt time.Time
			RevokedAt *time.Time
			Revision  int64
		}
		if err := tx.Raw("SELECT subject, status, expires_at, revoked_at, revision FROM "+adminKeyMetadataTable+" WHERE id = ?", id).Scan(&meta).Error; err != nil {
			return err
		}
		if len(meta) != 1 {
			return errAdminKeyAbsent
		}
		row := meta[0]
		if row.RevokedAt != nil || row.Status != "active" || !row.ExpiresAt.After(now) {
			return errAdminKeyRevoked
		}
		var live int
		if err := tx.Raw("SELECT COUNT(*) FROM "+adminKeyGenerationTable+" WHERE key_id = ? AND (overlap_until IS NULL OR overlap_until > ?)", id, now).Scan(&live).Error; err != nil {
			return err
		}
		if live >= adminKeyMaxLiveGenerations {
			return errAdminKeyOverlapActive
		}
		var nextGeneration int
		if err := tx.Raw("SELECT COALESCE(MAX(generation), 0) + 1 FROM "+adminKeyGenerationTable+" WHERE key_id = ?", id).Scan(&nextGeneration).Error; err != nil {
			return err
		}
		secret, err := randomAdminKeySecret(id)
		if err != nil {
			return err
		}
		verifier, err := HashCallerToken(secret, rand.Reader, 0)
		if err != nil {
			return err
		}
		// The previous generation expires at min(overlap deadline, key expiry).
		deadline := now.Add(req.Overlap)
		if deadline.After(row.ExpiresAt) {
			deadline = row.ExpiresAt
		}
		if err := tx.Exec("UPDATE "+adminKeyGenerationTable+" SET overlap_until = ? WHERE key_id = ? AND overlap_until IS NULL", deadline, id).Error; err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO "+adminKeyGenerationTable+
			" (key_id, generation, verifier, created_at, overlap_until) VALUES (?, ?, ?, ?, NULL)",
			id, nextGeneration, verifier, now).Error; err != nil {
			return err
		}
		if err := tx.Exec("UPDATE "+adminKeyMetadataTable+" SET revision = revision + 1 WHERE id = ?", id).Error; err != nil {
			return err
		}
		out = adminKeySecret{ID: id, Subject: row.Subject, Secret: secret, Generation: nextGeneration, ExpiresAt: row.ExpiresAt.UTC()}
		return nil
	})
	return out, err
}

func (s *adminKeyStore) revoke(ctx context.Context, id string) error {
	now := s.now().UTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Exec("UPDATE "+adminKeyMetadataTable+
			" SET status = 'revoked', revoked_at = ?, revision = revision + 1 WHERE id = ? AND revoked_at IS NULL", now, id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errAdminKeyAbsent
		}
		return tx.Exec("UPDATE "+adminKeyGenerationTable+" SET overlap_until = ? WHERE key_id = ? AND overlap_until IS NULL", now, id).Error
	})
}

func (s *adminKeyStore) list(ctx context.Context, _ adminKeyListRequest) (adminKeyListResult, error) {
	var rows []struct {
		ID        string
		Subject   string
		Status    string
		ExpiresAt time.Time
		CreatedAt time.Time
		RevokedAt *time.Time
		Revision  int64
	}
	if err := s.db.WithContext(ctx).Raw("SELECT id, subject, status, expires_at, created_at, revoked_at, revision FROM " +
		adminKeyMetadataTable + " ORDER BY created_at, id").Scan(&rows).Error; err != nil {
		return adminKeyListResult{}, err
	}
	out := adminKeyListResult{Keys: make([]adminKeyMetadata, 0, len(rows))}
	for _, row := range rows {
		var generations int
		if err := s.db.WithContext(ctx).Raw("SELECT COUNT(*) FROM "+adminKeyGenerationTable+" WHERE key_id = ?", row.ID).Scan(&generations).Error; err != nil {
			return adminKeyListResult{}, err
		}
		out.Keys = append(out.Keys, adminKeyMetadata{ID: row.ID, Subject: row.Subject, Status: row.Status,
			ExpiresAt: row.ExpiresAt.UTC(), CreatedAt: row.CreatedAt.UTC(), RevokedAt: row.RevokedAt, Generations: generations, Revision: row.Revision})
		if row.Revision > out.Revision {
			out.Revision = row.Revision
		}
	}
	sort.Slice(out.Keys, func(i, j int) bool { return out.Keys[i].ID < out.Keys[j].ID })
	return out, nil
}

func (s *adminKeyStore) get(ctx context.Context, id string) (adminKeyMetadata, error) {
	all, err := s.list(ctx, adminKeyListRequest{})
	if err != nil {
		return adminKeyMetadata{}, err
	}
	for _, key := range all.Keys {
		if key.ID == id {
			return key, nil
		}
	}
	return adminKeyMetadata{}, errAdminKeyAbsent
}

// accepts is the authoritative per-authentication check: key active and
// unrevoked, current time before the absolute expiry, and this generation still
// inside its overlap window. A verifier-cache hit never bypasses it.
func (s *adminKeyStore) accepts(ctx context.Context, id string, generation int, verifier callerTokenVerifier) (bool, error) {
	now := s.now().UTC()
	var rows []struct {
		ExpiresAt    time.Time
		RevokedAt    *time.Time
		Status       string
		OverlapUntil *time.Time
	}
	if err := s.db.WithContext(ctx).Raw("SELECT m.expires_at, m.revoked_at, m.status, g.overlap_until FROM "+
		adminKeyMetadataTable+" m JOIN "+adminKeyGenerationTable+" g ON g.key_id = m.id WHERE m.id = ? AND g.generation = ?",
		id, generation).Scan(&rows).Error; err != nil {
		return false, err
	}
	if len(rows) != 1 {
		return false, nil
	}
	row := rows[0]
	if row.Status != "active" || row.RevokedAt != nil || !row.ExpiresAt.After(now) {
		return false, nil
	}
	if row.OverlapUntil != nil && !row.OverlapUntil.After(now) {
		return false, nil
	}
	_ = verifier
	return true, nil
}

var (
	errAdminKeyAbsent        = errors.New("admin key not found")
	errAdminKeyRevoked       = errors.New("admin key revoked or expired")
	errAdminKeyOverlapActive = errors.New("admin key rotation overlap still active")
)

func randomAdminKeyID() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "key_" + hex.EncodeToString(raw), nil
}

func randomAdminKeySecret(id string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return adminAPIKeyPrefix + "_" + strings.TrimPrefix(id, "key_") + "_" + hex.EncodeToString(raw), nil
}

// adminKeyPhase13MigrationDefinition returns the immutable migration for the
// API-issued key tables (#815). Dependencies carry the #816 audit phase when it
// is present.
func adminKeyPhase13MigrationDefinition(previous int) MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{
		ID:               2026110604,
		Scope:            configControlPlaneScope,
		Name:             "admin API-issued key metadata and generations",
		Release:          "2026.11",
		Checksum:         MigrationChecksum("router-config.phase13.admin-keys.v1"),
		SchemaVersion:    13,
		Transactional:    true,
		MaintenanceMode:  "online",
		RollbackClass:    "restore-required",
		HandlerKey:       "router-config.phase13.apply.v1@applyAdminKeyPhase13",
		PostconditionKey: "router-config.phase13.schema.v1@verifyAdminKeyPhase13",
		Dependencies:     []int{previous},
		ExecutionMode:    "transactional",
		LockClass:        "online",
		TimeoutClass:     "bounded",
		Apply:            applyAdminKeyPhase13,
		Verify:           verifyAdminKeyPhase13,
	})
}

func applyAdminKeyPhase13(tx *gorm.DB) error {
	for _, stmt := range []string{
		"CREATE TABLE IF NOT EXISTS " + adminKeyMetadataTable + " (id TEXT PRIMARY KEY, subject TEXT NOT NULL, status TEXT NOT NULL, expires_at TIMESTAMP NOT NULL, created_at TIMESTAMP NOT NULL, revoked_at TIMESTAMP NULL, revision BIGINT NOT NULL DEFAULT 1)",
		"CREATE TABLE IF NOT EXISTS " + adminKeyGenerationTable + " (key_id TEXT NOT NULL, generation INTEGER NOT NULL, verifier TEXT NOT NULL, created_at TIMESTAMP NOT NULL, overlap_until TIMESTAMP NULL, PRIMARY KEY (key_id, generation), FOREIGN KEY (key_id) REFERENCES " + adminKeyMetadataTable + "(id))",
		"CREATE UNIQUE INDEX IF NOT EXISTS admin_api_key_subject_unique ON " + adminKeyMetadataTable + "(subject)",
	} {
		if err := tx.Exec(stmt).Error; err != nil {
			return fmt.Errorf("create admin key store: %w", err)
		}
	}
	return nil
}

func verifyAdminKeyPhase13(tx *gorm.DB) error {
	for _, table := range []string{adminKeyMetadataTable, adminKeyGenerationTable} {
		if !tx.Migrator().HasTable(table) {
			return fmt.Errorf("required control-plane table %s is missing", table)
		}
	}
	return nil
}

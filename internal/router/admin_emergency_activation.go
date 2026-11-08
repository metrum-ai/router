// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	errAdminEmergencyInactive    = errors.New("emergency credential inactive")
	errAdminEmergencyInvalid     = errors.New("invalid emergency activation")
	errAdminEmergencyPersistence = errors.New("emergency persistence unavailable")
)

const adminEmergencyActivationTable = "router_admin_emergency_activations"

// This descriptor comes from validated local configuration, never a request.
// Fingerprint binds the complete credential configuration and verifier/pin;
// replacement credentials must not inherit a previous activation.
type adminEmergencyCredential struct {
	ID, Subject, Issuer, Fingerprint string
	ExpiresAt                        time.Time
}

// Safe metadata only. The protected binding digest stays in the DB record.
type adminEmergencyActivation struct {
	ActivationID string    `json:"activation_id"`
	CredentialID string    `json:"credential_id"`
	Subject      string    `json:"subject"`
	Issuer       string    `json:"issuer"`
	Reason       string    `json:"reason"`
	ActivatedAt  time.Time `json:"activated_at"`
	Until        time.Time `json:"until"`
}

type adminEmergencyActivationRow struct {
	ActivationID        string `gorm:"primaryKey" json:"-" yaml:"-"`
	CredentialID        string `json:"-" yaml:"-"`
	BindingDigest       string `json:"-" yaml:"-"`
	Subject             string `json:"-" yaml:"-"`
	Issuer              string `json:"-" yaml:"-"`
	Reason              string `json:"-" yaml:"-"`
	ActivatedAtMS       int64  `gorm:"column:activated_at_ms" json:"-" yaml:"-"`
	UntilMS             int64  `gorm:"column:until_ms" json:"-" yaml:"-"`
	CredentialExpiresMS int64  `gorm:"column:credential_expires_ms" json:"-" yaml:"-"`
	DeactivatedAtMS     int64  `gorm:"column:deactivated_at_ms" json:"-" yaml:"-"`
}

func (adminEmergencyActivationRow) TableName() string { return adminEmergencyActivationTable }
func (r adminEmergencyActivationRow) metadata() adminEmergencyActivation {
	return adminEmergencyActivation{ActivationID: r.ActivationID, CredentialID: r.CredentialID, Subject: r.Subject, Issuer: r.Issuer, Reason: r.Reason, ActivatedAt: time.UnixMilli(r.ActivatedAtMS).UTC(), Until: time.UnixMilli(r.UntilMS).UTC()}
}

type adminEmergencyActivationStore struct {
	db           *gorm.DB
	now          func() time.Time
	redactReason func(string) string
}

// redactReason is the shared audit redactor supplied by integration, not an
// independent event-redaction policy. A missing redactor fails closed.
func newAdminEmergencyActivationStore(db *gorm.DB, replicas int, now func() time.Time, redactReason func(string) string) (*adminEmergencyActivationStore, error) {
	if db == nil || db.Dialector == nil || replicas < 1 || redactReason == nil {
		return nil, errAdminEmergencyInvalid
	}
	switch db.Dialector.Name() {
	case "sqlite":
		if replicas != 1 {
			return nil, errAdminEmergencyInvalid
		}
	case "postgres":
	default:
		return nil, errAdminEmergencyInvalid
	}
	if now == nil {
		now = time.Now
	}
	if !db.Migrator().HasTable(adminEmergencyActivationTable) {
		return nil, errAdminEmergencyPersistence
	}
	return &adminEmergencyActivationStore{db: db, now: now, redactReason: redactReason}, nil
}

// The control-plane emergency-ledger migration applies this DDL; constructors
// never auto-migrate. Until that migration is registered by the deployment slice,
// a database without the ledger starts without emergency credentials and refuses
// an emergency enrollment rather than substituting a no-op adapter.
func applyAdminEmergencyActivationSchema(tx *gorm.DB) error {
	for _, sql := range []string{
		`CREATE TABLE IF NOT EXISTS router_admin_emergency_activations (activation_id TEXT PRIMARY KEY, credential_id TEXT NOT NULL, binding_digest TEXT NOT NULL, subject TEXT NOT NULL, issuer TEXT NOT NULL, reason TEXT NOT NULL, activated_at_ms BIGINT NOT NULL, until_ms BIGINT NOT NULL, credential_expires_ms BIGINT NOT NULL, deactivated_at_ms BIGINT NOT NULL DEFAULT 0)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS router_admin_emergency_one_active ON router_admin_emergency_activations (credential_id) WHERE deactivated_at_ms = 0`,
	} {
		if err := tx.Exec(sql).Error; err != nil {
			return errAdminEmergencyPersistence
		}
	}
	return nil
}

func validAdminEmergencyCredential(c adminEmergencyCredential, now time.Time) bool {
	if len(c.ID) == 0 || len(c.ID) > 128 || len(c.Fingerprint) != 64 || len(c.Subject) > 1024 || strings.TrimSpace(c.Subject) == "" || len(c.Issuer) > 2048 || strings.TrimSpace(c.Issuer) == "" {
		return false
	}
	for _, v := range c.ID {
		if !(v >= 'a' && v <= 'z' || v >= 'A' && v <= 'Z' || v >= '0' && v <= '9' || v == '-' || v == '_' || v == '.') {
			return false
		}
	}
	decoded, err := hex.DecodeString(c.Fingerprint)
	return err == nil && len(decoded) == 32 && strings.ToLower(c.Fingerprint) == c.Fingerprint && c.ExpiresAt.After(now) && c.ExpiresAt.Sub(now) <= time.Hour
}

// record is a required transaction-bound adapter to #816's credential sink.
// It must commit the lifecycle event through tx, not a separately opened DB.
// No principal or grant is created by this store.
func (s *adminEmergencyActivationStore) Activate(ctx context.Context, c adminEmergencyCredential, reason string, until time.Time, record func(*gorm.DB, adminEmergencyActivation) error) (adminEmergencyActivation, error) {
	var zero adminEmergencyActivation
	if ctx == nil || ctx.Err() != nil || record == nil {
		return zero, errAdminEmergencyInvalid
	}
	now := s.now().UTC()
	// Validate the deadline we can actually persist, not a later fractional
	// instant. This also makes the final pre-commit check use identical precision.
	until = until.UTC().Truncate(time.Millisecond)
	if !validAdminEmergencyCredential(c, now) || len(reason) > 512 || strings.TrimSpace(reason) == "" || !until.After(now) || until.After(c.ExpiresAt) || until.Sub(now) > time.Hour {
		return zero, errAdminEmergencyInvalid
	}
	reason = strings.TrimSpace(s.redactReason(reason))
	if reason == "" || len(reason) > 512 {
		return zero, errAdminEmergencyInvalid
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return zero, errAdminEmergencyPersistence
	}
	row := adminEmergencyActivationRow{ActivationID: hex.EncodeToString(id[:]), CredentialID: c.ID, BindingDigest: c.Fingerprint, Subject: c.Subject, Issuer: c.Issuer, Reason: reason, ActivatedAtMS: now.UnixMilli(), UntilMS: until.UnixMilli(), CredentialExpiresMS: c.ExpiresAt.UnixMilli()}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&adminEmergencyActivationRow{}).Where("credential_id = ? AND deactivated_at_ms = 0", c.ID).Update("deactivated_at_ms", now.UnixMilli()).Error; err != nil {
			return errAdminEmergencyPersistence
		}
		if err := tx.Create(&row).Error; err != nil {
			return errAdminEmergencyPersistence
		}
		if err := record(tx, row.metadata()); err != nil {
			return errAdminEmergencyPersistence
		}
		if ctx.Err() != nil || !s.now().Before(until) {
			return errAdminEmergencyInvalid
		}
		return nil
	})
	if err != nil {
		return zero, errAdminEmergencyPersistence
	}
	return row.metadata(), nil
}

// Use follows successful cryptographic validation and precedes dispatch. It
// rereads durable activation state every time; there is no activation cache.
func (s *adminEmergencyActivationStore) Use(ctx context.Context, c adminEmergencyCredential, record func(*gorm.DB, adminEmergencyActivation) error) (adminEmergencyActivation, error) {
	var zero adminEmergencyActivation
	if ctx == nil || ctx.Err() != nil || record == nil {
		return zero, errAdminEmergencyInvalid
	}
	now := s.now().UTC()
	if !validAdminEmergencyCredential(c, now) {
		return zero, errAdminEmergencyInactive
	}
	var row adminEmergencyActivationRow
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("credential_id = ? AND binding_digest = ? AND subject = ? AND issuer = ? AND credential_expires_ms = ? AND deactivated_at_ms = 0 AND activated_at_ms <= ? AND until_ms > ?", c.ID, c.Fingerprint, c.Subject, c.Issuer, c.ExpiresAt.UnixMilli(), now.UnixMilli(), now.UnixMilli()).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errAdminEmergencyInactive
		}
		if err != nil {
			return errAdminEmergencyPersistence
		}
		if err := record(tx, row.metadata()); err != nil {
			return errAdminEmergencyPersistence
		}
		check := s.now().UTC()
		if ctx.Err() != nil || check.UnixMilli() >= row.UntilMS || !c.ExpiresAt.After(check) {
			return errAdminEmergencyInactive
		}
		return nil
	})
	if errors.Is(err, errAdminEmergencyInactive) {
		return zero, errAdminEmergencyInactive
	}
	if err != nil {
		return zero, errAdminEmergencyPersistence
	}
	return row.metadata(), nil
}

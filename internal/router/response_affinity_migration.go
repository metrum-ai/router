// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"

	"gorm.io/gorm"
)

// responseAffinityRecord is dormant metadata for owner-bound native Responses
// affinity (#533). The migration alone does not enable stored Responses, write
// affinity rows, purge them or change any request behavior.
//
// The table deliberately has no bearer-token, prompt or response-content
// column. Later reviewed writers must bind the owning caller token, tenant and
// project, use keyed fingerprints and authenticated encryption with explicit
// key identifiers, and integrate expiry with retention and legal holds before
// storing anything here. The schema cannot enforce those controls.
type responseAffinityRecord struct {
	ID int64 `gorm:"column:id;primaryKey;autoIncrement"`
	// OwnerTokenFingerprint is a keyed fingerprint of the caller token that
	// owns the response, never the token itself. The key identifier makes
	// rotation and lookup under previous epochs explicit.
	OwnerTokenFingerprint string `gorm:"column:owner_token_fingerprint;type:text;not null;uniqueIndex:idx_response_affinity_owner_lookup,priority:1"`
	OwnerFingerprintKeyID string `gorm:"column:owner_fingerprint_key_id;type:text;not null"`
	Tenant                string `gorm:"column:tenant;type:text;not null;uniqueIndex:idx_response_affinity_owner_lookup,priority:2"`
	Project               string `gorm:"column:project;type:text;not null;uniqueIndex:idx_response_affinity_owner_lookup,priority:3"`
	Environment           string `gorm:"column:environment;type:text;not null;uniqueIndex:idx_response_affinity_owner_lookup,priority:4"`
	// ResponseLookupHash is the keyed lookup key for the client-visible
	// response id. Uniqueness is scoped to the owner and account domain so an
	// externally supplied id is never treated as globally unique.
	ResponseLookupHash string `gorm:"column:response_lookup_hash;type:text;not null;uniqueIndex:idx_response_affinity_owner_lookup,priority:5"`
	LookupHashKeyID    string `gorm:"column:lookup_hash_key_id;type:text;not null"`
	// UpstreamIDCiphertext is the authenticated ciphertext of the upstream
	// response id. Nonce and key id are separate so key lifecycle is explicit.
	UpstreamIDCiphertext string `gorm:"column:upstream_id_ciphertext;type:text;not null"`
	UpstreamIDNonce      string `gorm:"column:upstream_id_nonce;type:text;not null"`
	UpstreamIDKeyID      string `gorm:"column:upstream_id_key_id;type:text;not null"`
	// Provider, AccountRef, Model, ProviderDialect, Endpoint and ModelGroup
	// identify the exact upstream that holds the stored response. A pin is only
	// valid for the same target identity; there is no silent re-route.
	Provider        string `gorm:"column:provider;type:text;not null"`
	AccountRef      string `gorm:"column:account_ref;type:text;not null"`
	Model           string `gorm:"column:model;type:text;not null"`
	ModelGroup      string `gorm:"column:model_group;type:text;not null"`
	ProviderDialect string `gorm:"column:provider_dialect;type:text;not null"`
	Endpoint        string `gorm:"column:endpoint;type:text;not null"`
	// State is the local lifecycle state: in_progress, completed, cancelled,
	// deleted or expired. It is local metadata only; expiring it does not prove
	// upstream deletion.
	State string `gorm:"column:state;type:text;not null"`
	// Background marks responses created with background: true. UsageSettled
	// and SettledAt record the idempotent background settlement decision.
	Background   bool   `gorm:"column:background;not null;default:false"`
	UsageSettled bool   `gorm:"column:usage_settled;not null;default:false"`
	SettledAt    string `gorm:"column:settled_at;type:text;not null"`
	// UpstreamDeleteState is empty when no upstream DELETE was attempted, then
	// pending, confirmed or failed, so a failed provider delete never claims
	// upstream deletion.
	UpstreamDeleteState string `gorm:"column:upstream_delete_state;type:text;not null"`
	TombstonedAt        string `gorm:"column:tombstoned_at;type:text;not null"`
	// LastErrorClass is a bounded, value-free diagnostic class.
	LastErrorClass string `gorm:"column:last_error_class;type:text;not null"`
	// Timestamps are RFC 3339 text like the rest of the usage database so the
	// retention purge can compare them with its string cutoffs on SQLite and
	// PostgreSQL alike.
	CreatedAt string `gorm:"column:created_at;type:text;not null"`
	UpdatedAt string `gorm:"column:updated_at;type:text;not null"`
	ExpiresAt string `gorm:"column:expires_at;type:text;not null;index:idx_response_affinity_expiry"`
}

func (responseAffinityRecord) TableName() string { return "response_affinity" }

func applyResponseAffinityMigration(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("response affinity migration requires database")
	}
	if !db.Migrator().HasTable(&responseAffinityRecord{}) {
		if err := db.Migrator().CreateTable(&responseAffinityRecord{}); err != nil {
			return fmt.Errorf("create response affinity: %w", err)
		}
	}
	return verifyResponseAffinityMigration(db)
}

func verifyResponseAffinityMigration(db *gorm.DB) error {
	if err := ensureUsageRelationalSchema(db); err != nil {
		return err
	}
	return verifyUsageRelationalModel(db, &responseAffinityRecord{})
}

// revertResponseAffinityMigration is the tested reverse step for this
// schema-only migration. It is used by migration tests and by operators on a
// disposable database after taking a backup. It drops the dormant table,
// removes the applied ledger and attempt rows for this migration ID, and
// verifies the schema-11 postcondition, so an earlier binary sees a database
// it accepts again.
//
// A supported production downgrade is still restore-required: the reverse step
// is destructive, cannot be discovered from the ledger by an earlier binary,
// and does not reconstruct history. It removes local affinity metadata only;
// the upstream provider keeps its own copy of each response, and this step does
// not claim upstream deletion.
func revertResponseAffinityMigration(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("response affinity migration requires database")
	}
	if db.Migrator().HasTable(&responseAffinityRecord{}) {
		if err := db.Migrator().DropTable(&responseAffinityRecord{}); err != nil {
			return fmt.Errorf("drop response affinity: %w", err)
		}
	}
	// Attempts reference the ledger row, so remove them first.
	if err := db.Where("scope = ? AND migration_id = ?", usageMigrationScope, usageResponseAffinityMigrationID).Delete(&migrationAttemptRecord{}).Error; err != nil {
		return fmt.Errorf("remove response affinity attempts: %w", err)
	}
	if err := db.Where("scope = ? AND migration_id = ?", usageMigrationScope, usageResponseAffinityMigrationID).Delete(&migrationLedgerRecord{}).Error; err != nil {
		return fmt.Errorf("remove response affinity ledger: %w", err)
	}
	return verifyUsageEndpointMigration(db)
}

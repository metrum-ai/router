// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// responseAffinityRecord is dormant metadata for owner-bound native Responses.
// The migration alone does not enable operations or write any rows. Future
// writers must authenticate and bind owner, tenant and project, use keyed
// fingerprints and authenticated encryption, validate account references,
// and integrate retention and legal holds before writing. The schema itself
// does not enforce these requirements; it has no raw-token, prompt or
// response-content columns.
type responseAffinityRecord struct {
	ID                    int64     `gorm:"column:id;primaryKey;autoIncrement"`
	OwnerTokenFingerprint string    `gorm:"column:owner_token_fingerprint;type:text;not null;uniqueIndex:idx_response_affinity_owner_lookup,priority:1"`
	Tenant                string    `gorm:"column:tenant;type:text;not null;uniqueIndex:idx_response_affinity_owner_lookup,priority:2"`
	Project               string    `gorm:"column:project;type:text;not null;uniqueIndex:idx_response_affinity_owner_lookup,priority:3"`
	ResponseLookupHash    string    `gorm:"column:response_lookup_hash;type:text;not null;uniqueIndex:idx_response_affinity_owner_lookup,priority:4"`
	UpstreamIDCiphertext  string    `gorm:"column:upstream_id_ciphertext;type:text;not null"`
	UpstreamIDNonce       string    `gorm:"column:upstream_id_nonce;type:text;not null"`
	UpstreamIDKeyID       string    `gorm:"column:upstream_id_key_id;type:text;not null"`
	Provider              string    `gorm:"column:provider;type:text;not null"`
	AccountRef            string    `gorm:"column:account_ref;type:text;not null"`
	Model                 string    `gorm:"column:model;type:text;not null"`
	ModelGroup            string    `gorm:"column:model_group;type:text;not null"`
	Environment           string    `gorm:"column:environment;type:text;not null"`
	State                 string    `gorm:"column:state;type:text;not null"`
	CreatedAt             time.Time `gorm:"column:created_at;not null"`
	ExpiresAt             time.Time `gorm:"column:expires_at;not null;index:idx_response_affinity_expiry"`
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

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	callerTokenRevocationMigrationID = 2026111604
	revokedCallerTokensTable         = "router_revoked_caller_tokens"
)

type revokedCallerTokenRow struct {
	TokenHash  string `gorm:"column:token_hash;primaryKey"`
	RevokedAt  string `gorm:"column:revoked_at;not null"`
	ReasonCode string `gorm:"column:reason_code;not null"`
}

func (revokedCallerTokenRow) TableName() string { return revokedCallerTokensTable }

func callerTokenRevocationMigrationDefinition() MigrationDefinition {
	return MigrationDefinition{
		ID:               callerTokenRevocationMigrationID,
		Scope:            configControlPlaneScope,
		Name:             "sticky caller token revocation denylist",
		Release:          "2026.11",
		Checksum:         MigrationChecksum("router-config.phase15.sticky-caller-token-revocation.v1"),
		SchemaVersion:    15,
		Transactional:    true,
		MaintenanceMode:  "online",
		RollbackClass:    "restore-required",
		HandlerKey:       "router-config.phase15.apply.v1@applyCallerTokenRevocationMigration",
		PostconditionKey: "router-config.phase15.schema.v1@verifyCallerTokenRevocationMigration",
		Dependencies:     []int{callerTokenMetadataMigrationID},
		ExecutionMode:    "transactional",
		LockClass:        "online",
		TimeoutClass:     "bounded",
		Apply:            applyCallerTokenRevocationMigration,
		Verify:           verifyCallerTokenRevocationMigration,
	}
}

func applyCallerTokenRevocationMigration(db *gorm.DB) error {
	if err := db.Exec("CREATE TABLE IF NOT EXISTS " + revokedCallerTokensTable + " (token_hash TEXT PRIMARY KEY, revoked_at TEXT NOT NULL, reason_code TEXT NOT NULL)").Error; err != nil {
		return err
	}
	// Preserve revocations recorded by the previous config-set-coupled format.
	var legacy []string
	if err := db.Model(&callerRow{}).Distinct("revoked_token_hash").Where("revoked_token_hash <> ''").Pluck("revoked_token_hash", &legacy).Error; err != nil {
		return err
	}
	for _, hash := range legacy {
		if strings.TrimSpace(hash) == "" {
			continue
		}
		row := revokedCallerTokenRow{TokenHash: hash, RevokedAt: time.Now().UTC().Format(time.RFC3339Nano), ReasonCode: "legacy_config_revocation"}
		if err := db.Exec("INSERT INTO "+revokedCallerTokensTable+" (token_hash, revoked_at, reason_code) VALUES (?, ?, ?) ON CONFLICT (token_hash) DO NOTHING", row.TokenHash, row.RevokedAt, row.ReasonCode).Error; err != nil {
			return fmt.Errorf("migrate legacy caller revocation: %w", err)
		}
	}
	return verifyCallerTokenRevocationMigration(db)
}

func verifyCallerTokenRevocationMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable(revokedCallerTokensTable) {
		return fmt.Errorf("required control-plane table %s is missing", revokedCallerTokensTable)
	}
	for _, column := range []string{"token_hash", "revoked_at", "reason_code"} {
		if !db.Migrator().HasColumn(revokedCallerTokenRow{}, column) {
			return fmt.Errorf("required control-plane column %s.%s is missing", revokedCallerTokensTable, column)
		}
	}
	return nil
}

func loadRevokedCallerTokenHashes(db *gorm.DB) (map[string]struct{}, error) {
	if db == nil {
		return map[string]struct{}{}, nil
	}
	var hashes []string
	if err := db.Model(&revokedCallerTokenRow{}).Order("token_hash ASC").Pluck("token_hash", &hashes).Error; err != nil {
		return nil, fmt.Errorf("load revoked caller verifiers: %w", err)
	}
	out := make(map[string]struct{}, len(hashes))
	for _, hash := range hashes {
		out[hash] = struct{}{}
	}
	return out, nil
}

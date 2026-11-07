// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Phase ten of the configuration control plane adds the admin-plane
// Idempotency-Key store (metrum-ai/router#811, ACP-016).
//
// One table, one composite primary key:
//
//	router_admin_idempotency (principal_kind, principal_subject, domain,
//	  operation, idempotency_key) → request_hash, state, recorded response
//
// The scope deliberately excludes the raw request path so a key cannot be
// replayed across operations or domains, and it excludes the credential ID so
// rotating a credential does not hand a client a fresh replay window.
//
// The table stores no secret: the key is an opaque client string (length-bounded
// at the API), request_hash is a SHA-256 digest, and the recorded body is an API
// response that contains no credential material by construction.
//
// The migration is online, transactional and idempotent: it creates one table
// and one index and never rewrites an existing row. The same DDL is applied by
// the store constructor, so a database that was migrated only through phase 9
// still works before the next router-migrate run.

import (
	"fmt"

	"gorm.io/gorm"
)

const configControlPlanePhase10MigrationID = 2026110700

// ConfigControlPlanePhase10MigrationDefinition returns the immutable migration
// definition for the admin-plane idempotency store.
func ConfigControlPlanePhase10MigrationDefinition() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{
		ID:               configControlPlanePhase10MigrationID,
		Scope:            configControlPlaneScope,
		Name:             "admin API Idempotency-Key store",
		Release:          "2026.11",
		Checksum:         MigrationChecksum("router-config.phase10.admin-idempotency.v1"),
		SchemaVersion:    10,
		Transactional:    true,
		MaintenanceMode:  "online",
		RollbackClass:    "restore-required",
		HandlerKey:       "router-config.phase10.apply.v1@applyConfigControlPlanePhase10",
		PostconditionKey: "router-config.phase10.schema.v1@verifyConfigControlPlanePhase10",
		Dependencies:     []int{configControlPlanePhase9MigrationID},
		ExecutionMode:    "transactional",
		LockClass:        "online",
		TimeoutClass:     "bounded",
		Apply:            applyConfigControlPlanePhase10,
		Verify:           verifyConfigControlPlanePhase10,
	})
}

func applyConfigControlPlanePhase10(tx *gorm.DB) error {
	for _, stmt := range adminIdempotencyDDL {
		if err := tx.Exec(stmt).Error; err != nil {
			return fmt.Errorf("create admin idempotency store: %w", err)
		}
	}
	return nil
}

func verifyConfigControlPlanePhase10(tx *gorm.DB) error {
	if err := verifyConfigControlPlanePhase9(tx); err != nil {
		return err
	}
	if !tx.Migrator().HasTable(adminIdempotencyTable) {
		return fmt.Errorf("required control-plane table %s is missing", adminIdempotencyTable)
	}
	for _, column := range []string{
		"principal_kind", "principal_subject", "domain", "operation", "idempotency_key",
		"request_hash", "state", "response_status", "response_content_type", "response_etag",
		"response_body", "created_at", "expires_at",
	} {
		if !tx.Migrator().HasColumn(adminIdempotencyTable, column) {
			return fmt.Errorf("required control-plane column %s.%s is missing", adminIdempotencyTable, column)
		}
	}
	return nil
}

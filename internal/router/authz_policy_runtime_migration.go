// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"errors"
	"gorm.io/gorm"
)

// The explicit ledger handler (PolicyRuntimeMigrationDefinition) runs from the
// migration command only: no serving constructor, request path or reload creates
// the singleton table. An absent table leaves PostgreSQL mutations stale while
// reads continue.
func applyPolicyRuntimeMigration(db *gorm.DB) error {
	if db == nil {
		return errors.New("policy runtime migration requires database")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var active []authzPolicySetRecord
		if err := tx.Where("status = ?", authzPolicyStatusActive).Limit(2).Find(&active).Error; err != nil {
			return err
		}
		if len(active) > 1 {
			return errors.New("policy runtime migration requires one unambiguous active policy")
		}
		if tx.Migrator().HasTable(&policyMutationRevision{}) {
			return verifyPolicyRuntimeMigration(tx)
		}
		if err := tx.Migrator().CreateTable(&policyMutationRevision{}); err != nil {
			return err
		}
		row := policyMutationRevision{ID: 1}
		if len(active) == 1 {
			row.ActiveSetID = active[0].ID
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		return verifyPolicyRuntimeMigration(tx)
	})
}
func verifyPolicyRuntimeMigration(db *gorm.DB) error {
	if db == nil {
		return errors.New("policy runtime migration requires database")
	}
	if err := verifyUsageRelationalModel(db, &policyMutationRevision{}); err != nil {
		return err
	}
	var rows []policyMutationRevision
	if err := db.Limit(2).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) != 1 || rows[0].ID != 1 || rows[0].Revision < 0 || rows[0].ConfigRevision < 0 {
		return errors.New("policy runtime singleton is missing or invalid")
	}
	var active []authzPolicySetRecord
	if err := db.Where("status = ?", authzPolicyStatusActive).Limit(2).Find(&active).Error; err != nil {
		return err
	}
	if len(active) > 1 || (len(active) == 0 && rows[0].ActiveSetID != "") || (len(active) == 1 && rows[0].ActiveSetID != active[0].ID) {
		return errors.New("policy runtime active authority does not match policy state")
	}
	return nil
}

// policyRuntimeMigrationID is the ledger identity of the policy runtime
// singleton. The audit migrations (#869) own 2026110701/0702/0703, so this
// definition takes the next free usage-scope identity and schema version.
const policyRuntimeMigrationID = 2026110704

// PolicyRuntimeMigrationDefinition seeds the policy runtime singleton from the
// single active policy set. It is online, transactional and data-preserving: an
// already migrated singleton is verified instead of recreated, and cumulative
// revisions are never reset.
func PolicyRuntimeMigrationDefinition() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{
		ID:               policyRuntimeMigrationID,
		Scope:            usageMigrationScope,
		Name:             "policy runtime singleton revision",
		Release:          "2026.11",
		Checksum:         MigrationChecksum("usage.policy-runtime.v1"),
		SchemaVersion:    15,
		DataVersion:      0,
		Transactional:    true,
		MaintenanceMode:  "online",
		RollbackClass:    "restore-required",
		HandlerKey:       "usage.policy-runtime.apply.v1@applyPolicyRuntimeMigration",
		PostconditionKey: "usage.policy-runtime.schema.v1@verifyPolicyRuntimeMigration",
		Dependencies:     []int{usageAdminDenialColumnsMigrationID},
		ExecutionMode:    "transactional",
		LockClass:        "online",
		TimeoutClass:     "bounded",
		Apply:            applyPolicyRuntimeMigration,
		Verify:           verifyPolicyRuntimeMigration,
	})
}

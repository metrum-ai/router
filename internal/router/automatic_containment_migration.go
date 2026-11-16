// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"errors"

	"gorm.io/gorm"
)

const automaticContainmentMigrationID = 2026111601

type automaticContainmentActionRecord struct {
	CallerID        string `gorm:"primaryKey;size:128"`
	DetectorID      string `gorm:"size:64;not null"`
	Action          string `gorm:"size:16;not null"`
	ThrottlePercent int    `gorm:"not null;default:100"`
	CreatedAt       string `gorm:"size:40;not null;index"`
	ExpiresAt       string `gorm:"size:40;not null;index"`
	RequestID       string `gorm:"-"`
	Evidence        string `gorm:"-"`
}

func (automaticContainmentActionRecord) TableName() string { return "router_auto_containment_actions" }

type automaticContainmentGuardRecord struct {
	ID       int   `gorm:"primaryKey"`
	Revision int64 `gorm:"not null"`
}

type automaticContainmentEventRecord struct {
	ID         uint64 `gorm:"primaryKey;autoIncrement"`
	CallerID   string `gorm:"size:128;not null;index"`
	DetectorID string `gorm:"size:64;not null"`
	Action     string `gorm:"size:16;not null"`
	CreatedAt  string `gorm:"size:40;not null;index"`
}

func (automaticContainmentEventRecord) TableName() string { return "router_auto_containment_events" }

func (automaticContainmentGuardRecord) TableName() string { return "router_auto_containment_guard" }

func AutomaticContainmentMigrationDefinition() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{
		ID: automaticContainmentMigrationID, Scope: usageMigrationScope,
		Name: "persist automatic containment actions", Release: "2026.11",
		Checksum:      MigrationChecksum("usage.automatic-containment.v1"),
		SchemaVersion: 16, DataVersion: 0, Transactional: true,
		MaintenanceMode: "online", RollbackClass: "restore-required",
		HandlerKey:       "usage.automatic-containment.apply.v1@applyAutomaticContainmentMigration",
		PostconditionKey: "usage.automatic-containment.schema.v1@verifyAutomaticContainmentMigration",
		Dependencies:     []int{policyRuntimeMigrationID}, ExecutionMode: "transactional",
		LockClass: "online", TimeoutClass: "bounded",
		Apply: applyAutomaticContainmentMigration, Verify: verifyAutomaticContainmentMigration,
	})
}

func applyAutomaticContainmentMigration(db *gorm.DB) error {
	if db == nil {
		return errors.New("automatic containment migration requires database")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		for _, model := range []any{&automaticContainmentActionRecord{}, &automaticContainmentGuardRecord{}, &automaticContainmentEventRecord{}} {
			if !tx.Migrator().HasTable(model) {
				if err := tx.Migrator().CreateTable(model); err != nil {
					return err
				}
			}
		}
		if err := tx.FirstOrCreate(&automaticContainmentGuardRecord{ID: 1}, automaticContainmentGuardRecord{ID: 1}).Error; err != nil {
			return err
		}
		return verifyAutomaticContainmentMigration(tx)
	})
}

func verifyAutomaticContainmentMigration(db *gorm.DB) error {
	if db == nil {
		return errors.New("automatic containment migration requires database")
	}
	if err := verifyUsageRelationalModel(db, &automaticContainmentActionRecord{}); err != nil {
		return err
	}
	if err := verifyUsageRelationalModel(db, &automaticContainmentGuardRecord{}); err != nil {
		return err
	}
	if err := verifyUsageRelationalModel(db, &automaticContainmentEventRecord{}); err != nil {
		return err
	}
	var rows []automaticContainmentGuardRecord
	if err := db.Limit(2).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) != 1 || rows[0].ID != 1 || rows[0].Revision < 0 {
		return errors.New("automatic containment guard row is missing or invalid")
	}
	return nil
}

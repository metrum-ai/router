// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "gorm.io/gorm"

// These aggregates contain caller/token totals only, never response IDs or
// content. Daily totals must outlive individual response affinity rows so that
// expiration cannot erase a caller's lifetime-token accounting.
type backgroundQuotaDaily struct {
	CallerID string `gorm:"column:caller_id;primaryKey;type:text"`
	Day      string `gorm:"column:day;primaryKey;type:text"`
	Tokens   int64  `gorm:"column:tokens;not null;default:0"`
}

func (backgroundQuotaDaily) TableName() string { return "response_background_quota_daily" }

type backgroundQuotaSecond struct {
	CallerID string `gorm:"column:caller_id;primaryKey;type:text"`
	Second   int64  `gorm:"column:second;primaryKey"`
	Tokens   int64  `gorm:"column:tokens;not null;default:0"`
}

func (backgroundQuotaSecond) TableName() string { return "response_background_quota_second" }

func applyBackgroundSettlementMigration(db *gorm.DB) error {
	if !db.Migrator().HasColumn(&responseAffinityRecord{}, "BillingSnapshot") {
		if err := db.Migrator().AddColumn(&responseAffinityRecord{}, "BillingSnapshot"); err != nil {
			return err
		}
	}
	for _, model := range []any{&backgroundQuotaDaily{}, &backgroundQuotaSecond{}} {
		if !db.Migrator().HasTable(model) {
			if err := db.Migrator().CreateTable(model); err != nil {
				return err
			}
		}
	}
	return verifyBackgroundSettlementMigration(db)
}

func verifyBackgroundSettlementMigration(db *gorm.DB) error {
	for _, model := range []any{&responseAffinityRecord{}, &backgroundQuotaDaily{}, &backgroundQuotaSecond{}} {
		if err := verifyUsageRelationalModel(db, model); err != nil {
			return err
		}
	}
	return nil
}

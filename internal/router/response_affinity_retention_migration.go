// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

func applyResponseAffinityRequestMigration(db *gorm.DB) error {
	if !db.Migrator().HasColumn(&responseAffinityRecord{}, "RequestID") {
		if err := db.Migrator().AddColumn(&responseAffinityRecord{}, "RequestID"); err != nil {
			return err
		}
	}
	if !db.Migrator().HasColumn(&responseAffinityRecord{}, "CreatedTS") {
		if err := db.Migrator().AddColumn(&responseAffinityRecord{}, "CreatedTS"); err != nil {
			return err
		}
	}
	if !db.Migrator().HasColumn(&responseAffinityRecord{}, "CreatedEndTS") {
		if err := db.Migrator().AddColumn(&responseAffinityRecord{}, "CreatedEndTS"); err != nil {
			return err
		}
	}
	// Backfill a fixed-width UTC timestamp for legal-hold comparisons without
	// relying on database-specific date conversion functions.
	var after string
	for {
		var rows []responseAffinityRecord
		if err := db.Select("response_id", "created_at").Where("response_id > ? AND created_at > 0 AND (created_ts = ? OR created_end_ts = ?)", after, "", "").Order("response_id").Limit(256).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			if err := db.Model(&responseAffinityRecord{}).Where("response_id = ?", row.ResponseID).Updates(map[string]any{"created_ts": formatUsageTime(time.UnixMilli(row.CreatedAt)), "created_end_ts": formatUsageTime(time.UnixMilli(row.CreatedAt).Add(time.Millisecond))}).Error; err != nil {
				return err
			}
		}
		after = rows[len(rows)-1].ResponseID
	}
	return verifyResponseAffinityRequestMigration(db)
}

func verifyResponseAffinityRequestMigration(db *gorm.DB) error {
	if !db.Migrator().HasColumn(&responseAffinityRecord{}, "RequestID") || !db.Migrator().HasColumn(&responseAffinityRecord{}, "CreatedTS") || !db.Migrator().HasColumn(&responseAffinityRecord{}, "CreatedEndTS") {
		return errors.New("response affinity retention correlation columns are missing")
	}
	return verifyUsageRelationalModelExcept(db, &responseAffinityRecord{}, map[string]map[string]struct{}{"response_affinity": {"billing_snapshot": {}}})
}

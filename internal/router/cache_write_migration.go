// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "gorm.io/gorm"

var usageCacheWriteColumns = map[string]map[string]struct{}{
	"request_usage": {"cache_write_input_tokens": {}, "cache_write_price_per_million_usd": {}},
}

func applyUsageCacheWriteMigration(db *gorm.DB) error {
	for _, field := range []string{"CacheWriteInputTokens", "CacheWritePricePerMillionUSD"} {
		if !db.Migrator().HasColumn(&usageRecord{}, field) {
			if err := db.Migrator().AddColumn(&usageRecord{}, field); err != nil {
				return err
			}
		}
	}
	return verifyUsageCacheWriteMigration(db)
}
func verifyUsageCacheWriteMigration(db *gorm.DB) error {
	return ensureUsageRelationalSchemaExcept(db, mergeUsageColumnSets(usageRequestKindColumns, usageTokenBreakdownColumns))
}

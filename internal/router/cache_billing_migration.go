// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "gorm.io/gorm"

var usageCacheBillingColumns = map[string]map[string]struct{}{
	"request_usage": {"cost_list_usd": {}, "cost_billed_usd": {}},
}

func applyUsageCacheBillingMigration(db *gorm.DB) error {
	for _, field := range []string{"CostListUSD", "CostBilledUSD"} {
		if !db.Migrator().HasColumn(&usageRecord{}, field) {
			if err := db.Migrator().AddColumn(&usageRecord{}, field); err != nil {
				return err
			}
		}
	}
	// Historical total is the best available list-price estimate. Only known
	// response-cache hits are free; unknown cache states retain their old charge.
	// Preserve existing list estimates when adopting a non-serving tool's schema,
	// or when this step is retried: total may already be the zero billed alias.
	if err := db.Exec(`UPDATE request_usage SET cost_list_usd = CASE WHEN cost_list_usd = 0 THEN total_cost_usd ELSE cost_list_usd END,
 cost_billed_usd = CASE WHEN cache = 'hit' THEN 0 ELSE total_cost_usd END,
 total_cost_usd = CASE WHEN cache = 'hit' THEN 0 ELSE total_cost_usd END`).Error; err != nil {
		return err
	}
	return verifyUsageCacheBillingMigration(db)
}

func verifyUsageCacheBillingMigration(db *gorm.DB) error {
	return ensureUsageRelationalSchema(db)
}

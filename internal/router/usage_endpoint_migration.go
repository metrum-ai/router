// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "gorm.io/gorm"

var usageEndpointColumns = map[string]map[string]struct{}{"request_usage": {"endpoint": {}}}

func applyUsageEndpointMigration(db *gorm.DB) error {
	if !db.Migrator().HasColumn(&usageRecord{}, "Endpoint") {
		if err := db.Migrator().AddColumn(&usageRecord{}, "Endpoint"); err != nil {
			return err
		}
	}
	// Existing records cannot reliably be classified from stored metadata.
	return verifyUsageEndpointMigration(db)
}

func verifyUsageEndpointMigration(db *gorm.DB) error {
	return ensureUsageRelationalSchema(db)
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"gorm.io/gorm"
)

var usageRequestKindColumns = map[string]map[string]struct{}{"request_usage": {"request_kind": {}}}

func applyUsageRequestKindMigration(db *gorm.DB) error {
	if !db.Migrator().HasColumn(&usageRecord{}, "RequestKind") {
		if err := db.Migrator().AddColumn(&usageRecord{}, "RequestKind"); err != nil {
			return err
		}
	}
	// Historic rows cannot reliably distinguish count calls from inference.
	// Leave them unknown; new count events are zero-token, zero-cost records.
	return verifyUsageRequestKindMigration(db)
}

func verifyUsageRequestKindMigration(db *gorm.DB) error {
	if !db.Migrator().HasColumn(&usageRecord{}, "RequestKind") {
		return fmt.Errorf("missing request_kind column")
	}
	return nil
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// usageMigrationRunnerWithoutPostureReport keeps response-affinity migration
// tests scoped to their feature instead of coupling them to the global usage
// schema head. The posture report migration has its own migration tests.
func usageMigrationRunnerWithoutPostureReport(cfg UsageDBConfig) (*migrationRunner, func() error, error) {
	db, err := openUsageDB(cfg)
	if err != nil {
		return nil, nil, err
	}
	definitions := make([]MigrationDefinition, 0, len(usageMigrationDefinitions)-1)
	for _, definition := range usageMigrationDefinitions {
		if definition.ID == securityPostureReportMigrationID {
			continue
		}
		definitions = append(definitions, definition)
	}
	runner, err := NewMigrationRunnerWithDataJobs(db, usageMigrationScope, usageMigrationCompatibility, definitions, usageDataJobDefinitions)
	if err != nil {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		return nil, nil, err
	}
	closeFn := func() error {
		sqlDB, err := db.DB()
		if err != nil {
			return err
		}
		return sqlDB.Close()
	}
	return runner, closeFn, nil
}

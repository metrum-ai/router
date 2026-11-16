// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// usageMigrationRunnerWithLatestSchema applies the full usage manifest in
// focused migration tests, including later schema migrations.
func usageMigrationRunnerWithLatestSchema(cfg UsageDBConfig) (*migrationRunner, func() error, error) {
	db, err := openUsageDB(cfg)
	if err != nil {
		return nil, nil, err
	}
	runner, err := NewMigrationRunnerWithDataJobs(db, usageMigrationScope, usageMigrationCompatibility, usageMigrationDefinitions, usageDataJobDefinitions)
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

// usageMigrationRunnerWithoutPostureReport skips the non-serving posture report
// migration while applying all schema migrations through the current head.
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
	runner, err := NewMigrationRunnerWithDataJobs(db, usageMigrationScope, MigrationCompatibility{MinSchema: 0, MaxSchema: 19, MinData: 0, MaxData: 1}, definitions, usageDataJobDefinitions)
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

// usageMigrationDefinitionsBeforeResponseAffinity returns the released schema
// manifest used to verify that older binaries reject the next usage schema.
func usageMigrationDefinitionsBeforeResponseAffinity() []MigrationDefinition {
	definitions := make([]MigrationDefinition, 0, len(usageMigrationDefinitions))
	for _, definition := range usageMigrationDefinitions {
		if definition.SchemaVersion <= 11 {
			definitions = append(definitions, definition)
		}
	}
	return definitions
}

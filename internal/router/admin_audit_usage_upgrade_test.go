// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"testing"

	"gorm.io/gorm"
)

// v471UsageMigrationCount is the number of usage migrations the v4.7.1 release
// shipped (schema version 12). The v4.8.0 additions are appended after them, so a
// runner built from this prefix produces exactly the database an upgraded
// deployment brings to v4.8.0: every v4.7.1 column and index already applied, none
// of the v4.8.0 ones, and the historical-validation data job validated.
const v471UsageMigrationCount = 13

func openUpgradeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "usage.sqlite")})
	if err != nil {
		t.Fatalf("open usage database: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// buildV471UsageDatabase leaves db in the state a released v4.7.1 deployment has:
// usage schema 12, data version 1, every bound data job validated.
func buildV471UsageDatabase(t *testing.T, db *gorm.DB) {
	t.Helper()
	if v471UsageMigrationCount >= len(usageMigrationDefinitions) {
		t.Fatalf("release manifest no longer contains v4.8.0 migrations after the v4.7.1 prefix")
	}
	compatibility := MigrationCompatibility{MinSchema: 0, MaxSchema: 12, MinData: 0, MaxData: 1}
	legacy, err := NewMigrationRunnerWithDataJobs(db, usageMigrationScope, compatibility,
		usageMigrationDefinitions[:v471UsageMigrationCount], usageDataJobDefinitions)
	if err != nil {
		t.Fatalf("build v4.7.1 migration runner: %v", err)
	}
	if err := legacy.ApplyPending("v471-shape"); err != nil {
		t.Fatalf("apply v4.7.1 migrations: %v", err)
	}
	if _, err := legacy.RunDataJob(context.Background(), "historical-usage-validation-v1", "v471-shape",
		DataJobCheckpoint{Ordinal: 0}); err != nil {
		t.Fatalf("run v4.7.1 data job: %v", err)
	}
	status, err := legacy.Verify()
	if err != nil {
		t.Fatalf("verify v4.7.1 shape: %v", err)
	}
	if status.SchemaVersion != 12 || status.DataVersion != 1 || !UsageMigrationServingCompatible(status) {
		t.Fatalf("v4.7.1 shape is not serving compatible: %+v", status)
	}
	// The released v4.7.1 binary's model did not declare the v4.8.0 denial-column
	// index, so its baseline handler never created it and its own postconditions
	// passed without it. Today's baseline handler does know the index, so applying
	// the v4.7.1 migration prefix in this binary creates it; dropping it here
	// restores the shape the released binary actually wrote.
	if err := db.Exec("DROP INDEX IF EXISTS " + usageAdminDenialColumnsIndex).Error; err != nil {
		t.Fatalf("drop index to reproduce the released v4.7.1 shape: %v", err)
	}
	if db.Migrator().HasIndex("security_access_events", usageAdminDenialColumnsIndex) {
		t.Fatalf("could not reproduce the released v4.7.1 shape: %s still present", usageAdminDenialColumnsIndex)
	}
}

// upgradeToV480 runs the documented v4.8.0 deployment job against an existing
// database: apply every pending migration, then verify-serving.
func upgradeToV480(t *testing.T, db *gorm.DB) MigrationStatus {
	t.Helper()
	runner, err := newUsageMigrationRunner(db)
	if err != nil {
		t.Fatalf("build v4.8.0 migration runner: %v", err)
	}
	if err := runner.ApplyPending("v480-upgrade"); err != nil {
		t.Fatalf("apply v4.8.0 migrations: %v", err)
	}
	status, err := runner.Verify()
	if err != nil {
		t.Fatalf("verify-serving after the v4.8.0 deployment job: %v", err)
	}
	if status.SchemaVersion != 15 || status.DataVersion != 1 || !UsageMigrationServingCompatible(status) {
		t.Fatalf("upgraded database is not serving compatible: %+v", status)
	}
	return status
}

// A database created by the released v4.7.1 binaries has the v4.8.0 denial columns
// only after this migration runs, and it never receives a model index from a
// migration it does not run. The migration therefore has to create the index, or
// verify-serving and deployment-job startup both fail on every upgraded database.
func TestUpgradeFromV471CreatesAdminDenialIndex(t *testing.T) {
	db := openUpgradeTestDB(t)
	buildV471UsageDatabase(t, db)

	if db.Migrator().HasIndex("security_access_events", usageAdminDenialColumnsIndex) {
		t.Fatalf("v4.7.1 shape unexpectedly already has %s", usageAdminDenialColumnsIndex)
	}

	upgradeToV480(t, db)

	if !db.Migrator().HasIndex("security_access_events", usageAdminDenialColumnsIndex) {
		t.Fatalf("upgraded database is missing %s", usageAdminDenialColumnsIndex)
	}
	for _, column := range usageAdminDenialColumns {
		if !db.Migrator().HasColumn("security_access_events", column) {
			t.Fatalf("upgraded database is missing column %s", column)
		}
	}
}

// The baseline postcondition that verify-serving re-runs must pass on the upgraded
// database, because it is the same check deployment-job startup performs.
func TestUpgradeFromV471PassesBaselinePostcondition(t *testing.T) {
	db := openUpgradeTestDB(t)
	buildV471UsageDatabase(t, db)
	upgradeToV480(t, db)

	for _, definition := range usageMigrationDefinitions {
		if definition.Verify == nil {
			continue
		}
		if err := definition.Verify(db); err != nil {
			t.Fatalf("postcondition %d (%s) fails on an upgraded database: %v", definition.ID, definition.Name, err)
		}
	}
}

type usageSchemaShape struct {
	tables  []string
	columns map[string][]string
	indexes map[string][]string
}

func readUsageSchemaShape(t *testing.T, db *gorm.DB) usageSchemaShape {
	t.Helper()
	shape := usageSchemaShape{columns: map[string][]string{}, indexes: map[string][]string{}}
	rows, err := db.Raw(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`).Rows()
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table: %v", err)
		}
		shape.tables = append(shape.tables, name)
	}
	for _, table := range shape.tables {
		columnRows, err := db.Raw(fmt.Sprintf(`SELECT name FROM pragma_table_info('%s') ORDER BY name`, table)).Rows()
		if err != nil {
			t.Fatalf("list columns of %s: %v", table, err)
		}
		for columnRows.Next() {
			var column string
			if err := columnRows.Scan(&column); err != nil {
				t.Fatalf("scan column of %s: %v", table, err)
			}
			shape.columns[table] = append(shape.columns[table], column)
		}
		columnRows.Close()
		indexRows, err := db.Raw(`SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = ? AND name NOT LIKE 'sqlite_%' ORDER BY name`, table).Rows()
		if err != nil {
			t.Fatalf("list indexes of %s: %v", table, err)
		}
		for indexRows.Next() {
			var index string
			if err := indexRows.Scan(&index); err != nil {
				t.Fatalf("scan index of %s: %v", table, err)
			}
			shape.indexes[table] = append(shape.indexes[table], index)
		}
		indexRows.Close()
	}
	sort.Strings(shape.tables)
	return shape
}

// Every table, column and index a fresh v4.8.0 install has must also exist after
// upgrading a v4.7.1 database. This is the guard against another model-declared
// index or column that no migration creates on the upgrade path.
func TestUpgradedUsageSchemaMatchesFreshInstall(t *testing.T) {
	fresh := openUpgradeTestDB(t)
	freshRunner, err := newUsageMigrationRunner(fresh)
	if err != nil {
		t.Fatalf("build fresh runner: %v", err)
	}
	if err := freshRunner.ApplyPending("fresh-install"); err != nil {
		t.Fatalf("apply on a fresh database: %v", err)
	}

	upgraded := openUpgradeTestDB(t)
	buildV471UsageDatabase(t, upgraded)
	upgradeToV480(t, upgraded)

	freshShape := readUsageSchemaShape(t, fresh)
	upgradedShape := readUsageSchemaShape(t, upgraded)

	missingTables := difference(freshShape.tables, upgradedShape.tables)
	if len(missingTables) != 0 {
		t.Fatalf("tables missing on the upgraded database: %v", missingTables)
	}
	var missingColumns, missingIndexes []string
	for _, table := range freshShape.tables {
		for _, column := range difference(freshShape.columns[table], upgradedShape.columns[table]) {
			missingColumns = append(missingColumns, table+"."+column)
		}
		for _, index := range difference(freshShape.indexes[table], upgradedShape.indexes[table]) {
			missingIndexes = append(missingIndexes, table+"."+index)
		}
	}
	if len(missingColumns) != 0 {
		t.Fatalf("columns missing on the upgraded database: %v", missingColumns)
	}
	if len(missingIndexes) != 0 {
		t.Fatalf("indexes missing on the upgraded database: %v", missingIndexes)
	}
}

func difference(want, got []string) []string {
	present := make(map[string]struct{}, len(got))
	for _, value := range got {
		present[value] = struct{}{}
	}
	var missing []string
	for _, value := range want {
		if _, ok := present[value]; !ok {
			missing = append(missing, value)
		}
	}
	return missing
}

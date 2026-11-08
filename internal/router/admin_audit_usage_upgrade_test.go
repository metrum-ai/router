// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// usageV471SchemaFile is the schema and ledger of a usage database written by the
// RELEASED v4.7.1 binaries (usage schema version 12, data version 1, historical
// validation job validated). It is generated from the published asset, not
// synthesised from this binary's handlers, so the upgrade tests see exactly the
// tables, columns and indexes a customer upgrading from v4.7.1 has.
const usageV471SchemaFile = "testdata/usage-v4.7.1-schema.sql"

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

// loadV471UsageDatabase turns an empty database into the one a released v4.7.1
// deployment has, by executing the captured schema and ledger statements in order.
func loadV471UsageDatabase(t *testing.T, db *gorm.DB) {
	t.Helper()
	raw, err := os.ReadFile(usageV471SchemaFile)
	if err != nil {
		t.Fatalf("read %s: %v", usageV471SchemaFile, err)
	}
	var statement strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		statement.WriteString(line)
		statement.WriteString("\n")
		if !strings.HasSuffix(trimmed, ";") {
			continue
		}
		if err := db.Exec(statement.String()).Error; err != nil {
			t.Fatalf("load v4.7.1 schema statement failed: %v\n%s", err, statement.String())
		}
		statement.Reset()
	}
	if statement.Len() != 0 {
		t.Fatalf("v4.7.1 schema file ends with an unterminated statement: %s", statement.String())
	}

	runner, err := newUsageMigrationRunner(db)
	if err != nil {
		t.Fatalf("build migration runner on the loaded v4.7.1 database: %v", err)
	}
	status, err := runner.Status()
	if err != nil {
		t.Fatalf("status of the loaded v4.7.1 database: %v", err)
	}
	if status.SchemaVersion != 12 || status.DataVersion != 1 || !status.Compatible || status.State != "pending" {
		t.Fatalf("loaded database is not the released v4.7.1 shape: %+v", status)
	}
	if len(status.Pending) != 3 {
		t.Fatalf("expected exactly the three v4.8.0 usage migrations pending, got %d", len(status.Pending))
	}
	if db.Migrator().HasIndex("security_access_events", usageAdminDenialColumnsIndex) {
		t.Fatalf("the released v4.7.1 schema must not contain %s", usageAdminDenialColumnsIndex)
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

// A database created by the released v4.7.1 binaries has no
// security_access_events.idx_security_access_object: the index is declared on the
// v4.8.0 GORM model, so a fresh install gets it from the baseline handler while an
// existing database only gains it from migration 2026110703. Without that step,
// verify-serving and deployment-job startup both fail on every upgraded database.
func TestUpgradeFromV471CreatesAdminDenialIndex(t *testing.T) {
	db := openUpgradeTestDB(t)
	loadV471UsageDatabase(t, db)

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
	loadV471UsageDatabase(t, db)
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
// upgrading the captured v4.7.1 database. Because both sides are real schemas
// (testdata from the released binary, and this binary's fresh install), this is the
// guard against another model-declared index or column that no migration creates on
// the upgrade path.
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
	loadV471UsageDatabase(t, upgraded)
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

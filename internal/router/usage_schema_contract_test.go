// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestUsageProvenancePostgresSchema(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("USAGE_B_POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("USAGE_B_POSTGRES_TEST_DSN required for disposable provenance schema test")
	}
	if !strings.Contains(dsn, "usage_b_provenance") {
		t.Fatal("disposable usage_b_provenance database required")
	}
	cfg := UsageDBConfig{Driver: "postgres", DSN: dsn, MigrationPolicy: usageDBMigrationPolicyAutoSafe}
	runner, closeDB, err := UsageMigrationRunner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.ApplyPending("usage-b-test"); err != nil {
		_ = closeDB()
		t.Fatal(err)
	}
	if err := runner.ApplyMaintenancePendingWithEvidence("usage-b-test", "disposable-empty-test-database"); err != nil {
		_ = closeDB()
		t.Fatal(err)
	}
	if err := runner.ApplyPending("usage-b-test"); err != nil {
		_ = closeDB()
		t.Fatal(err)
	}
	defer closeDB()
	// Inspect the migrated schema directly: serving additionally requires the
	// unrelated historical-validation data job to be certified.
	store := &usageStore{db: runner.db}
	if err := verifyUsageProvenanceMigration(store.db); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&usageRecord{RequestID: "provenance", UsageSource: "estimated"}).Error; err != nil {
		t.Fatal(err)
	}
	var row usageRecord
	if err := store.db.Where("request_id = ?", "provenance").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.UsageSource != "estimated" {
		t.Fatal("lost provenance")
	}
}

type usagePostgresDefaultContractFixture struct {
	ID    int    `gorm:"primaryKey"`
	Value string `gorm:"type:text;not null;default:''"`
}

func (usagePostgresDefaultContractFixture) TableName() string {
	return "usage_postgres_default_contract_fixture"
}

// usagePostgresCastMetadataDialector restores the raw PostgreSQL catalog
// spelling for just the fixture default. GORM's postgres migrator strips text
// casts before exposing ColumnTypes metadata, while the router must accept the
// cast-bearing catalog representation during strict verification.
type usagePostgresCastMetadataDialector struct {
	gorm.Dialector
	defaults map[string]string
}

func (d usagePostgresCastMetadataDialector) Migrator(db *gorm.DB) gorm.Migrator {
	return usagePostgresCastMetadataMigrator{
		Migrator: d.Dialector.Migrator(db),
		defaults: d.defaults,
	}
}

type usagePostgresCastMetadataMigrator struct {
	gorm.Migrator
	defaults map[string]string
}

func (m usagePostgresCastMetadataMigrator) ColumnTypes(value any) ([]gorm.ColumnType, error) {
	columns, err := m.Migrator.ColumnTypes(value)
	if err != nil {
		return nil, err
	}
	for i, column := range columns {
		if value, ok := m.defaults[column.Name()]; ok {
			columns[i] = usageCastDefaultColumnType{underlying: column, value: value}
		}
	}
	return columns, nil
}

type usageCastDefaultColumnType struct {
	underlying gorm.ColumnType
	value      string
}

func (c usageCastDefaultColumnType) Name() string                { return c.underlying.Name() }
func (c usageCastDefaultColumnType) DatabaseTypeName() string    { return c.underlying.DatabaseTypeName() }
func (c usageCastDefaultColumnType) ColumnType() (string, bool)  { return c.underlying.ColumnType() }
func (c usageCastDefaultColumnType) PrimaryKey() (bool, bool)    { return c.underlying.PrimaryKey() }
func (c usageCastDefaultColumnType) AutoIncrement() (bool, bool) { return c.underlying.AutoIncrement() }
func (c usageCastDefaultColumnType) Length() (int64, bool)       { return c.underlying.Length() }
func (c usageCastDefaultColumnType) DecimalSize() (int64, int64, bool) {
	return c.underlying.DecimalSize()
}
func (c usageCastDefaultColumnType) Nullable() (bool, bool)  { return c.underlying.Nullable() }
func (c usageCastDefaultColumnType) Unique() (bool, bool)    { return c.underlying.Unique() }
func (c usageCastDefaultColumnType) ScanType() reflect.Type  { return c.underlying.ScanType() }
func (c usageCastDefaultColumnType) Comment() (string, bool) { return c.underlying.Comment() }

func (c usageCastDefaultColumnType) DefaultValue() (string, bool) {
	return c.value, true
}

func openUsageSchemaContractDB(t *testing.T) *gorm.DB {
	t.Helper()
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store.db
}

func openDisposablePostgresUsageSchemaDB(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for {
		db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
		if err == nil {
			sqlDB, sqlErr := db.DB()
			if sqlErr == nil {
				sqlErr = sqlDB.Ping()
			}
			if sqlErr == nil {
				t.Cleanup(func() { _ = sqlDB.Close() })
				return db
			}
			lastErr = sqlErr
			if sqlDB != nil {
				_ = sqlDB.Close()
			}
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			t.Fatalf("connect disposable PostgreSQL usage-schema test database: %v", lastErr)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestUsageSchemaContractRejectsRequiredColumnAndIndexDrift(t *testing.T) {
	db := openUsageSchemaContractDB(t)
	if err := db.Exec("DROP INDEX idx_request_usage_ts").Error; err != nil {
		t.Fatal(err)
	}
	if err := ensureUsageRelationalSchema(db); err == nil {
		t.Fatal("schema contract accepted a missing required index")
	}

	db = openUsageSchemaContractDB(t)
	if err := db.Exec("DROP INDEX idx_request_usage_ts").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ALTER TABLE request_usage DROP COLUMN ts").Error; err != nil {
		t.Fatal(err)
	}
	if err := ensureUsageRelationalSchema(db); err == nil || !strings.Contains(err.Error(), "request_usage.ts") {
		t.Fatal("schema contract accepted a missing required column")
	}
}

func TestUsageTargetRegionDiagnosticsMigrationVerifiesRequiredColumn(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := applyUsageTargetRegionDiagnosticsMigration(store.db); err != nil {
		t.Fatal(err)
	}
	if !store.db.Migrator().HasColumn(&usageRecord{}, "TargetRegion") {
		t.Fatal("target_region column missing after migration")
	}
}

func TestUsagePostgresIndexMetadataVerificationRejectsSemanticDrift(t *testing.T) {
	stmt := &gorm.Statement{DB: openUsageSchemaContractDB(t)}
	if err := stmt.Parse(&usageRecord{}); err != nil {
		t.Fatal(err)
	}
	var contract *schema.Index
	for _, index := range stmt.Schema.ParseIndexes() {
		if index.Name == "idx_request_usage_provider_model" {
			contract = index
			break
		}
	}
	if contract == nil {
		t.Fatal("request_usage provider-model index contract missing")
	}
	expected := usagePostgresIndexMetadata{Unique: false, Valid: true, Ready: true, Live: true, NoPredicate: true, KeyCount: 2, AttributeCount: 2, Keys: []string{`"target_provider"`, `"target_model"`}}
	tests := []struct {
		name     string
		metadata usagePostgresIndexMetadata
		wantErr  bool
	}{
		{name: "expected catalog spelling", metadata: expected},
		{name: "wrong order", metadata: usagePostgresIndexMetadata{Valid: true, Ready: true, Live: true, NoPredicate: true, KeyCount: 2, AttributeCount: 2, Keys: []string{"target_model", "target_provider"}}, wantErr: true},
		{name: "unexpected uniqueness", metadata: usagePostgresIndexMetadata{Unique: true, Valid: true, Ready: true, Live: true, NoPredicate: true, KeyCount: 2, AttributeCount: 2, Keys: []string{"target_provider", "target_model"}}, wantErr: true},
		{name: "partial", metadata: usagePostgresIndexMetadata{Valid: true, Ready: true, Live: true, KeyCount: 2, AttributeCount: 2, Keys: []string{"target_provider", "target_model"}}, wantErr: true},
		{name: "included column", metadata: usagePostgresIndexMetadata{Valid: true, Ready: true, Live: true, NoPredicate: true, KeyCount: 2, AttributeCount: 3, Keys: []string{"target_provider", "target_model"}}, wantErr: true},
		{name: "invalid", metadata: usagePostgresIndexMetadata{Ready: true, Live: true, NoPredicate: true, KeyCount: 2, AttributeCount: 2, Keys: []string{"target_provider", "target_model"}}, wantErr: true},
		{name: "not ready", metadata: usagePostgresIndexMetadata{Valid: true, Live: true, NoPredicate: true, KeyCount: 2, AttributeCount: 2, Keys: []string{"target_provider", "target_model"}}, wantErr: true},
		{name: "not live", metadata: usagePostgresIndexMetadata{Valid: true, Ready: true, NoPredicate: true, KeyCount: 2, AttributeCount: 2, Keys: []string{"target_provider", "target_model"}}, wantErr: true},
		{name: "extra key", metadata: usagePostgresIndexMetadata{Valid: true, Ready: true, Live: true, NoPredicate: true, KeyCount: 3, AttributeCount: 3, Keys: []string{"target_provider", "target_model", "ts"}}, wantErr: true},
		{name: "wrong key", metadata: usagePostgresIndexMetadata{Valid: true, Ready: true, Live: true, NoPredicate: true, KeyCount: 2, AttributeCount: 2, Keys: []string{"caller_id", "target_model"}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := verifyUsagePostgresIndexMetadata("request_usage", contract, tt.metadata)
			if (err != nil) != tt.wantErr {
				t.Fatalf("verifyUsagePostgresIndexMetadata() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestUsagePostgresUniqueIndexMetadataVerification(t *testing.T) {
	stmt := &gorm.Statement{DB: openUsageSchemaContractDB(t)}
	if err := stmt.Parse(&usageRollupRunRecord{}); err != nil {
		t.Fatal(err)
	}
	var contract *schema.Index
	for _, index := range stmt.Schema.ParseIndexes() {
		if index.Name == "idx_usage_rollup_run_unique" {
			contract = index
			break
		}
	}
	if contract == nil || contract.Class != "UNIQUE" {
		t.Fatal("usage-rollup unique index contract missing")
	}
	keys := make([]string, len(contract.Fields))
	for i, field := range contract.Fields {
		keys[i] = `"` + field.DBName + `"`
	}
	expected := usagePostgresIndexMetadata{Unique: true, Valid: true, Ready: true, Live: true, NoPredicate: true, KeyCount: len(keys), AttributeCount: len(keys), Keys: keys}
	if err := verifyUsagePostgresIndexMetadata(stmt.Schema.Table, contract, expected); err != nil {
		t.Fatalf("unique index contract rejected matching PostgreSQL metadata: %v", err)
	}
	expected.Unique = false
	if err := verifyUsagePostgresIndexMetadata(stmt.Schema.Table, contract, expected); err == nil {
		t.Fatal("unique index contract accepted non-unique PostgreSQL metadata")
	}
}

func TestUsageSchemaContractVersionedReasoningTelemetryVerification(t *testing.T) {
	db := openUsageSchemaContractDB(t)
	for _, statement := range []string{
		"ALTER TABLE request_usage DROP COLUMN reasoning_tokens",
		"ALTER TABLE request_usage DROP COLUMN reasoning_attempt_count",
		"ALTER TABLE request_usage DROP COLUMN reasoning_successful_attempt_count",
		"ALTER TABLE request_usage DROP COLUMN reasoning_reported_attempt_count",
		"ALTER TABLE request_attempts DROP COLUMN reasoning_tokens",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := verifyUsageLegacyBaseline(db); err != nil {
		t.Fatalf("v1 baseline rejected its historical schema: %v", err)
	}
	if err := verifyUsageReasoningTelemetryMigration(db); err == nil || !strings.Contains(err.Error(), "reasoning_tokens") {
		t.Fatalf("v2 reasoning verifier accepted missing reasoning contract: %v", err)
	}
	if err := db.Exec("DROP INDEX idx_request_usage_ts").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ALTER TABLE request_usage DROP COLUMN ts").Error; err != nil {
		t.Fatal(err)
	}
	if err := verifyUsageLegacyBaseline(db); err == nil || !strings.Contains(err.Error(), "request_usage.ts") {
		t.Fatalf("v1 baseline accepted unrelated historical-contract drift: %v", err)
	}
}

func TestUsageSchemaContractRejectsRequiredForeignKeyDrift(t *testing.T) {
	db := openUsageSchemaContractDB(t)
	// SQLite cannot drop a foreign key in place. Replacing this normalized child
	// table without its FK is the portable drift shape the verifier must reject.
	if err := db.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE request_token_estimates_drift AS SELECT * FROM request_token_estimates").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DROP TABLE request_token_estimates").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ALTER TABLE request_token_estimates_drift RENAME TO request_token_estimates").Error; err != nil {
		t.Fatal(err)
	}
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(&usageRecord{}); err != nil {
		t.Fatal(err)
	}
	if err := verifyUsageForeignKeys(db, stmt.Schema); err == nil {
		t.Fatal("schema contract accepted a missing required foreign key")
	}
}

func TestUsageSchemaContractRejectsForeignKeyActionDrift(t *testing.T) {
	db := openUsageSchemaContractDB(t)
	var createSQL string
	if err := db.Raw("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?", "request_token_estimates").Scan(&createSQL).Error; err != nil {
		t.Fatal(err)
	}
	if createSQL == "" || !strings.Contains(createSQL, "ON DELETE CASCADE") {
		t.Fatalf("unexpected request_token_estimates DDL: %q", createSQL)
	}
	// Rebuild the child using identical columns/references but a weaker delete
	// action. HasConstraint still sees a FK, so this proves the verifier checks
	// the full physical contract rather than only its existence.
	driftSQL := strings.Replace(createSQL, "ON DELETE CASCADE", "ON DELETE RESTRICT", 1)
	if err := db.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DROP TABLE request_token_estimates").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(driftSQL).Error; err != nil {
		t.Fatal(err)
	}
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(&usageRecord{}); err != nil {
		t.Fatal(err)
	}
	if err := verifyUsageForeignKeys(db, stmt.Schema); err == nil || !strings.Contains(err.Error(), "does not match contract") {
		t.Fatalf("schema contract accepted foreign-key action drift: %v", err)
	}
}

type usageCheckContractFixture struct {
	ID    int `gorm:"primaryKey"`
	Value int `gorm:"check:chk_usage_fixture_positive,value > 0"`
}

func (usageCheckContractFixture) TableName() string { return "usage_check_contract_fixture" }

func TestUsageSchemaContractRejectsCheckConstraintSemanticDrift(t *testing.T) {
	db := openUsageSchemaContractDB(t)
	if err := db.AutoMigrate(&usageCheckContractFixture{}); err != nil {
		t.Fatal(err)
	}
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(&usageCheckContractFixture{}); err != nil {
		t.Fatal(err)
	}
	if err := verifyUsageCheckConstraints(db, &usageCheckContractFixture{}, stmt.Schema); err != nil {
		t.Fatalf("check fixture contract rejected its schema: %v", err)
	}
	if err := db.Exec("DROP TABLE usage_check_contract_fixture").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE usage_check_contract_fixture (id integer PRIMARY KEY, value integer, CONSTRAINT chk_usage_fixture_positive CHECK (value >= 0))").Error; err != nil {
		t.Fatal(err)
	}
	if err := verifyUsageCheckConstraints(db, &usageCheckContractFixture{}, stmt.Schema); err == nil || !strings.Contains(err.Error(), "does not match contract") {
		t.Fatalf("schema contract accepted check semantic drift: %v", err)
	}
}

func TestUsageDefaultCompatibilityNormalizesOnlyPostgresTextCasts(t *testing.T) {
	tests := []struct {
		name     string
		driver   string
		expected string
		actual   string
		want     bool
	}{
		{name: "postgres text cast", driver: "postgres", expected: "''", actual: "''::text", want: true},
		{name: "postgres GORM empty expected text cast", driver: "postgres", expected: "", actual: "''::text", want: true},
		{name: "postgres parenthesized text cast", driver: "postgres", expected: "''", actual: "(( '' :: pg_catalog.text ))", want: true},
		{name: "postgres character varying cast", driver: "postgresql", expected: "''", actual: "''::character varying", want: true},
		{name: "postgres escaped literal cast", driver: "postgres", expected: "'O''Brien'", actual: "E'O''Brien'::text", want: true},
		{name: "sqlite keeps its literal spelling", driver: "sqlite", expected: "''", actual: "''", want: true},
		{name: "sqlite does not accept postgres cast syntax", driver: "sqlite", expected: "''", actual: "''::text", want: false},
		{name: "different text default", driver: "postgres", expected: "''", actual: "'changed'::text", want: false},
		{name: "non-text cast", driver: "postgres", expected: "''", actual: "''::integer", want: false},
		{name: "concatenated expression", driver: "postgres", expected: "''", actual: "''::text || 'changed'::text", want: false},
		{name: "function expression", driver: "postgres", expected: "''", actual: "coalesce(''::text, 'changed'::text)", want: false},
		{name: "matching function expressions remain rejected", driver: "postgres", expected: "now()", actual: "now()", want: false},
		{name: "sqlite boolean numeric spelling remains compatible", driver: "sqlite", expected: "false", actual: "0", want: true},
		{name: "boolean true does not equal false", driver: "postgres", expected: "false", actual: "true", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := usageDefaultCompatible(tt.driver, tt.expected, tt.actual); got != tt.want {
				t.Fatalf("usageDefaultCompatible(%q, %q, %q) = %v, want %v", tt.driver, tt.expected, tt.actual, got, tt.want)
			}
		})
	}
}

// TestUsageSchemaContractPostgresTextDefaults verifies the exact metadata path
// against a disposable PostgreSQL database. The unit table above is always run;
// this opt-in test proves the driver's real ColumnTypes DefaultValue spelling.
func TestUsageSchemaContractPostgresTextDefaults(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("SMART_ROUTER_POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("SMART_ROUTER_POSTGRES_TEST_DSN is required for disposable PostgreSQL default coverage")
	}
	if os.Getenv("SMART_ROUTER_POSTGRES_TEST_ALLOW") != "issue-720" || !strings.Contains(strings.ToLower(dsn), "smart_router_issue_720") {
		t.Fatal("PostgreSQL default coverage requires the explicit issue-720 disposable database guard")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&usagePostgresDefaultContractFixture{}); err != nil {
		t.Fatal(err)
	}
	var actual string
	if err := db.Raw(`SELECT column_default FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = ? AND column_name = 'value'`, (usagePostgresDefaultContractFixture{}).TableName()).Scan(&actual).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(actual, "::") {
		t.Fatalf("PostgreSQL text default catalog metadata = %q, want cast-bearing literal", actual)
	}
	if !usageDefaultCompatible("postgres", "''", actual) {
		t.Fatalf("PostgreSQL text default catalog metadata = %q, want compatible cast-bearing literal", actual)
	}
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(&usagePostgresDefaultContractFixture{}); err != nil {
		t.Fatal(err)
	}
	if !usageDefaultCompatible("postgres", stmt.Schema.LookUpField("Value").DefaultValue, actual) {
		t.Fatalf("PostgreSQL text default expected %q and actual %q are incompatible", stmt.Schema.LookUpField("Value").DefaultValue, actual)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	verificationDB, err := gorm.Open(usagePostgresCastMetadataDialector{
		Dialector: postgres.New(postgres.Config{Conn: sqlDB}),
		defaults:  map[string]string{"value": actual},
	}, &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyUsageRelationalModel(verificationDB, &usagePostgresDefaultContractFixture{}); err != nil {
		t.Fatalf("PostgreSQL text default contract rejected: %v", err)
	}
}

// TestUsageSchemaContractPostgresIndexes exercises the PostgreSQL catalog
// path against the complete checked-in usage model set. It deliberately
// isolates index verification from the separately tracked default-contract
// regression so this issue can prove the exact index contract independently.
func TestUsageSchemaContractPostgresIndexes(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("SMART_ROUTER_POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("SMART_ROUTER_POSTGRES_TEST_DSN is required for disposable PostgreSQL usage-index coverage")
	}
	if os.Getenv("SMART_ROUTER_POSTGRES_TEST_ALLOW") != "issue-713-indexes" || !strings.Contains(strings.ToLower(dsn), "smart_router_issue_713") {
		t.Fatal("PostgreSQL usage-index coverage requires the explicit issue-713 disposable database guard")
	}
	db := openDisposablePostgresUsageSchemaDB(t, dsn)
	if err := db.AutoMigrate(usageRelationalModels()...); err != nil {
		t.Fatal(err)
	}
	for _, model := range usageRelationalModels() {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(model); err != nil {
			t.Fatal(err)
		}
		if err := verifyUsageIndexesPostgres(db, stmt.Schema); err != nil {
			t.Fatalf("PostgreSQL index contract rejected %s: %v", stmt.Schema.Table, err)
		}
	}
}

// TestUsageSchemaContractPostgresFullStore verifies the complete legacy
// OpenUsageStore migration and strict relational contract against a fresh,
// disposable PostgreSQL database. It is intentionally opt-in because its DSN
// must never point at an operational database.
func TestUsageSchemaContractPostgresFullStore(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("SMART_ROUTER_POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("SMART_ROUTER_POSTGRES_TEST_DSN is required for disposable PostgreSQL usage-store coverage")
	}
	if os.Getenv("SMART_ROUTER_POSTGRES_TEST_ALLOW") != "issue-713" || !strings.Contains(strings.ToLower(dsn), "smart_router_issue_713") {
		t.Fatal("PostgreSQL usage-store coverage requires the explicit issue-713 disposable database guard")
	}
	store, err := OpenUsageStore(UsageDBConfig{Driver: "postgres", DSN: dsn})
	if err != nil {
		t.Fatalf("OpenUsageStore PostgreSQL contract rejected: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := ensureUsageRelationalSchema(store.db); err != nil {
		t.Fatalf("PostgreSQL usage-store relational contract rejected after open: %v", err)
	}
}

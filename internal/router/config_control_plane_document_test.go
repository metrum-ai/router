// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// applyConfigControlPlaneMigrationsThroughPhase6ForTest applies all
// control-plane migrations through the online phase-6 document storage.
func applyConfigControlPlaneMigrationsThroughPhase6ForTest(t *testing.T, r *migrationRunner) {
	t.Helper()
	applyConfigControlPlaneMigrationsForTest(t, r)
}

func TestConfigControlPlanePhase6MigrationManifestIsStrictAndExecutable(t *testing.T) {
	def := ConfigControlPlanePhase6MigrationDefinition()
	if def.ManifestDigest == "" {
		t.Fatal("phase-6 migration must have a manifest digest")
	}
	if !migrationHandlerKeyMatches(def.HandlerKey, def.Apply) {
		t.Fatalf("phase-6 apply handler key mismatch: %q", def.HandlerKey)
	}
	if !migrationHandlerKeyMatches(def.PostconditionKey, def.Verify) {
		t.Fatalf("phase-6 verify handler key mismatch: %q", def.PostconditionKey)
	}
	if def.SchemaVersion != 6 {
		t.Fatalf("phase-6 schema version = %d, want 6", def.SchemaVersion)
	}
	if def.MaintenanceMode != "online" {
		t.Fatalf("phase-6 maintenance mode = %q, want online", def.MaintenanceMode)
	}
	if len(def.Dependencies) != 1 || def.Dependencies[0] != configControlPlanePhase5MigrationID {
		t.Fatalf("phase-6 dependencies = %v, want [%d]", def.Dependencies, configControlPlanePhase5MigrationID)
	}
}

func TestConfigControlPlanePhase6CreatesDocumentTables(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsThroughPhase6ForTest(t, r)
	for _, table := range configControlPlanePhase6Tables {
		if !r.db.Migrator().HasTable(table) {
			t.Fatalf("missing phase-6 table %s", table)
		}
	}
	for table, columns := range configControlPlanePhase6RequiredColumns {
		for _, column := range columns {
			if !r.db.Migrator().HasColumn(table, column) {
				t.Fatalf("missing phase-6 column %s.%s", table, column)
			}
		}
	}
}

const testCanonicalYAML = `server:
  identifiers:
    mode: passthrough
providers:
  mock:
    base_url: https://mock.example/v1
    dialect: openai
    api_key_env: MOCK_API_KEY
models:
  default:
    strategy: static
    targets:
      - provider: mock
        model: mock-model
callers:
  - id: alice
    token_sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    allow:
      - default
`

func TestImportCanonicalYAMLStoresDraftDocument(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsThroughPhase6ForTest(t, r)
	db := r.db

	configSetID, err := ImportCanonicalYAML(db, "staging", "initial", []byte(testCanonicalYAML), "test-actor")
	if err != nil {
		t.Fatalf("ImportCanonicalYAML: %v", err)
	}
	if configSetID == "" {
		t.Fatal("ImportCanonicalYAML returned empty config set ID")
	}

	// Verify the document row.
	var doc configControlPlaneDocumentRow
	if err := db.Where("config_set_id = ?", configSetID).First(&doc).Error; err != nil {
		t.Fatalf("load document: %v", err)
	}
	if doc.CanonicalYAML == "" {
		t.Fatal("stored canonical YAML is empty")
	}
	if doc.ContentSHA256 == "" {
		t.Fatal("stored content SHA-256 is empty")
	}
	if doc.UpdatedAt == "" {
		t.Fatal("stored updated_at is empty")
	}

	// Verify the config set row.
	var set configSetRow
	if err := db.Where("id = ?", configSetID).First(&set).Error; err != nil {
		t.Fatalf("load config set: %v", err)
	}
	if set.RuntimeScope != "staging" || set.Status != "draft" || set.ValidationStatus != "valid" {
		t.Fatalf("unexpected config set: %+v", set)
	}

	// Verify the change event.
	var event configControlPlaneChangeEventRow
	if err := db.Where("config_set_id = ? AND action = ?", configSetID, "import").First(&event).Error; err != nil {
		t.Fatalf("load change event: %v", err)
	}
	if event.RuntimeScope != "staging" || event.Actor != "test-actor" {
		t.Fatalf("unexpected change event: %+v", event)
	}
}

func TestImportExportCanonicalYAMLRoundTrip(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsThroughPhase6ForTest(t, r)
	db := r.db

	configSetID, err := ImportCanonicalYAML(db, "staging", "roundtrip", []byte(testCanonicalYAML), "test-actor")
	if err != nil {
		t.Fatalf("ImportCanonicalYAML: %v", err)
	}

	exported, err := ExportCanonicalYAML(db, configSetID)
	if err != nil {
		t.Fatalf("ExportCanonicalYAML: %v", err)
	}
	if string(exported) != testCanonicalYAML {
		t.Fatalf("export round-trip mismatch:\nimported:\n%s\nexported:\n%s", testCanonicalYAML, string(exported))
	}
}

func TestImportCanonicalYAMLRejectsRawSecrets(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsThroughPhase6ForTest(t, r)
	db := r.db

	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "openai-style key",
			yaml: `server:
  identifiers:
    mode: passthrough
providers:
  mock:
    base_url: https://mock.example/v1
    dialect: openai
    api_key: sk-abcdefghijklmnop1234567890
models:
  default:
    strategy: static
    targets:
      - provider: mock
        model: mock-model
callers:
  - id: alice
    token_sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    allow:
      - default
`,
		},
		{
			name: "github-style token",
			yaml: `server:
  identifiers:
    mode: passthrough
providers:
  mock:
    base_url: https://mock.example/v1
    dialect: openai
    api_key: ghp_abcdefghijklmnop1234567890
models:
  default:
    strategy: static
    targets:
      - provider: mock
        model: mock-model
callers:
  - id: alice
    token_sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    allow:
      - default
`,
		},
		{
			name: "xai-style key",
			yaml: `server:
  identifiers:
    mode: passthrough
providers:
  mock:
    base_url: https://mock.example/v1
    dialect: openai
    api_key: xai-abcdefghijklmnop1234567890
models:
  default:
    strategy: static
    targets:
      - provider: mock
        model: mock-model
callers:
  - id: alice
    token_sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    allow:
      - default
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ImportCanonicalYAML(db, "staging", "secret-test", []byte(tt.yaml), "test-actor")
			if err == nil {
				t.Fatal("ImportCanonicalYAML must reject raw-looking secrets")
			}
			if !strings.Contains(err.Error(), "raw-looking secret") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestImportCanonicalYAMLRejectsInvalidConfig(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsThroughPhase6ForTest(t, r)
	db := r.db

	// Missing providers.
	yamlDoc := `server:
  identifiers:
    mode: passthrough
models:
  default:
    strategy: static
    targets:
      - provider: mock
        model: mock-model
callers:
  - id: alice
    token_sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    allow:
      - default
`
	_, err = ImportCanonicalYAML(db, "staging", "invalid", []byte(yamlDoc), "test-actor")
	if err == nil {
		t.Fatal("ImportCanonicalYAML must reject invalid config")
	}
	if !strings.Contains(err.Error(), "validate canonical YAML") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadActiveCanonicalYAMLReturnsActiveDocument(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsThroughPhase6ForTest(t, r)
	db := r.db

	configSetID, err := ImportCanonicalYAML(db, "staging", "active-test", []byte(testCanonicalYAML), "test-actor")
	if err != nil {
		t.Fatalf("ImportCanonicalYAML: %v", err)
	}

	// Promote the draft to active.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.Exec(
		`UPDATE router_config_sets SET status = ?, activated_at = ? WHERE id = ?`,
		"active", now, configSetID,
	).Error; err != nil {
		t.Fatalf("activate config set: %v", err)
	}

	loaded, err := LoadActiveCanonicalYAML(db, "staging")
	if err != nil {
		t.Fatalf("LoadActiveCanonicalYAML: %v", err)
	}
	if string(loaded) != testCanonicalYAML {
		t.Fatalf("active document mismatch:\nimported:\n%s\nloaded:\n%s", testCanonicalYAML, string(loaded))
	}
}

func TestLoadActiveCanonicalYAMLFailsWhenNoActiveSet(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsThroughPhase6ForTest(t, r)
	db := r.db

	_, err = LoadActiveCanonicalYAML(db, "staging")
	if err == nil {
		t.Fatal("LoadActiveCanonicalYAML must fail when no active set exists")
	}
	if !strings.Contains(err.Error(), "no validated active config set") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExportCanonicalYAMLFailsForMissingSet(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsThroughPhase6ForTest(t, r)
	db := r.db

	_, err = ExportCanonicalYAML(db, "nonexistent")
	if err == nil {
		t.Fatal("ExportCanonicalYAML must fail for missing config set")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestImportCanonicalYAMLRejectsDeprecatedLicenseKey(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsThroughPhase6ForTest(t, r)
	db := r.db

	yamlDoc := `server:
  license:
    key: some-license-key
  identifiers:
    mode: passthrough
providers:
  mock:
    base_url: https://mock.example/v1
    dialect: openai
    api_key_env: MOCK_API_KEY
models:
  default:
    strategy: static
    targets:
      - provider: mock
        model: mock-model
callers:
  - id: alice
    token_sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    allow:
      - default
`
	_, err = ImportCanonicalYAML(db, "staging", "license-test", []byte(yamlDoc), "test-actor")
	if err == nil {
		t.Fatal("ImportCanonicalYAML must reject deprecated server.license key")
	}
	if !strings.Contains(err.Error(), "server.license is rejected") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestImportCanonicalYAMLRejectsEmptyInputs(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsThroughPhase6ForTest(t, r)
	db := r.db

	tests := []struct {
		name         string
		runtimeScope string
		setName      string
		yamlBytes    []byte
		actor        string
		wantErr      string
	}{
		{"empty scope", "", "test", []byte(testCanonicalYAML), "actor", "runtime scope is required"},
		{"empty name", "staging", "", []byte(testCanonicalYAML), "actor", "name is required"},
		{"empty yaml", "staging", "test", nil, "actor", "YAML document is required"},
		{"empty actor", "staging", "test", []byte(testCanonicalYAML), "", "actor is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ImportCanonicalYAML(db, tt.runtimeScope, tt.setName, tt.yamlBytes, tt.actor)
			if err == nil {
				t.Fatal("ImportCanonicalYAML must reject empty inputs")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

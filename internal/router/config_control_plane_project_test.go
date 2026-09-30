// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newRelationalProjectionTestDB applies the full control-plane migration
// chain and returns a database with one draft configuration set ready to
// receive a projection.
func newRelationalProjectionTestDB(t *testing.T, configSetID string) (*migrationRunner, func() error) {
	t.Helper()
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	applyConfigControlPlaneMigrationsForTest(t, r)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := r.db.Exec(`INSERT INTO router_config_sets (id, runtime_scope, name, status, validation_status, created_at) VALUES (?, ?, ?, ?, ?, ?)`, configSetID, "staging", configSetID, "draft", "valid", now).Error; err != nil {
		t.Fatal(err)
	}
	return r, closeDB
}

// minimalProjectionConfig returns a small but representative validated
// Config: one provider with one catalogued model, one model group with one
// target, and one caller with one allowed group.
func minimalProjectionConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Listen:            ":8080",
			DefaultModelGroup: "default",
		},
		StatePath: "router-state.json",
		Provider: map[string]ProviderConfig{
			"mock": {
				BaseURL:    "https://mock.example/v1",
				Dialect:    "openai",
				APIKey:     "sk-raw-secret-must-never-be-stored",
				APIKeyEnv:  "MOCK_API_KEY",
				KeyID:      "key-1",
				AuthScheme: "bearer",
				Headers: map[string]string{
					"X-Title":       "projection-test",
					"User-Agent":    "router-test/1.0",
					"Authorization": "Bearer must-not-be-stored",
				},
				Models: map[string]ProviderModel{
					"small": {
						Model:                    "mock-small",
						ContextTokens:            8192,
						InputPricePerMillionUSD:  0.1,
						OutputPricePerMillionUSD: 0.2,
						RPM:                      60,
						Tier:                     "cheap",
						Cost:                     1,
						InputModalities:          []string{"text"},
						OutputModalities:         []string{"text"},
						ToolSupport: ToolSupport{
							OpenAIChat: []string{"tools"},
						},
					},
				},
			},
		},
		Models: map[string]ModelGroup{
			"default": {
				Strategy: "static",
				Targets: []Target{
					{Provider: "mock", ModelRef: "small", Weight: 100},
				},
			},
		},
		Callers: []CallerConfig{
			{
				ID:          "caller-1",
				OwnerUser:   "owner@example.com",
				Project:     "demo",
				Environment: "staging",
				Status:      "active",
				TokenSHA256: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
				TokenID:     "tok-1",
				Allow:       []string{"default"},
				Rate:        RateConfig{RPM: 10, TPM: 1000, Concurrent: 2},
			},
		},
	}
}

func TestProjectConfigToRelationalWritesServerProviderGroupAndCallerRows(t *testing.T) {
	r, closeDB := newRelationalProjectionTestDB(t, "set-1")
	defer func() { _ = closeDB() }()
	db := r.db

	if err := ProjectConfigToRelational(db, "set-1", minimalProjectionConfig()); err != nil {
		t.Fatal(err)
	}

	// Server row carries the scalar fields; license columns stay at defaults.
	var server serverConfigRow
	if err := db.Where("config_set_id = ?", "set-1").First(&server).Error; err != nil {
		t.Fatal(err)
	}
	if server.Listen != ":8080" || server.DefaultModelGroup != "default" || server.StatePath != "router-state.json" {
		t.Fatalf("server projection mismatch: %+v", server)
	}

	// Provider row stores only the environment reference, never the raw key.
	var provider struct {
		BaseURL    string `gorm:"column:base_url"`
		Dialect    string `gorm:"column:dialect"`
		APIKeyEnv  string `gorm:"column:api_key_env"`
		KeyID      string `gorm:"column:key_id"`
		AuthScheme string `gorm:"column:auth_scheme"`
	}
	if err := db.Table("router_config_providers").Where("config_set_id = ? AND provider_name = ?", "set-1", "mock").First(&provider).Error; err != nil {
		t.Fatal(err)
	}
	if provider.APIKeyEnv != "MOCK_API_KEY" || provider.KeyID != "key-1" || provider.AuthScheme != "bearer" {
		t.Fatalf("provider projection mismatch: %+v", provider)
	}

	// Only allowlisted non-secret headers are persisted; Authorization is dropped.
	var headerNames []string
	if err := db.Table("router_config_provider_headers").Where("config_set_id = ? AND provider_name = ?", "set-1", "mock").Order("header_name ASC").Pluck("header_name", &headerNames).Error; err != nil {
		t.Fatal(err)
	}
	if len(headerNames) != 2 || headerNames[0] != "User-Agent" || headerNames[1] != "X-Title" {
		t.Fatalf("expected only non-secret headers to be stored, got %v", headerNames)
	}

	// Catalog model row plus its capability record.
	var model providerModelRow
	if err := db.Table("router_config_provider_models").Where("config_set_id = ? AND provider_name = ? AND model_ref = ?", "set-1", "mock", "small").First(&model).Error; err != nil {
		t.Fatal(err)
	}
	if model.Model != "mock-small" || model.ContextTokens != 8192 {
		t.Fatalf("model projection mismatch: %+v", model)
	}
	var capabilityCount int64
	if err := db.Table("router_config_provider_model_capabilities").Where("config_set_id = ? AND provider_name = ? AND model_ref = ?", "set-1", "mock", "small").Count(&capabilityCount).Error; err != nil {
		t.Fatal(err)
	}
	if capabilityCount != 1 {
		t.Fatalf("expected exactly one capability row, got %d", capabilityCount)
	}
	var toolCapability string
	if err := db.Table("router_config_provider_model_tool_support").Where("config_set_id = ? AND provider_name = ? AND model_ref = ? AND api_surface = ?", "set-1", "mock", "small", "openai_chat").Pluck("capability", &toolCapability).Error; err != nil {
		t.Fatal(err)
	}
	if toolCapability != "tools" {
		t.Fatalf("tool support projection mismatch: %q", toolCapability)
	}

	// Model group and ordered target.
	var group modelGroupRow
	if err := db.Table("router_config_model_groups").Where("config_set_id = ? AND group_name = ?", "set-1", "default").First(&group).Error; err != nil {
		t.Fatal(err)
	}
	if group.Strategy != "static" {
		t.Fatalf("group projection mismatch: %+v", group)
	}
	var target modelGroupTargetRow
	if err := db.Table("router_config_model_group_targets").Where("config_set_id = ? AND group_name = ?", "set-1", "default").First(&target).Error; err != nil {
		t.Fatal(err)
	}
	if target.Sequence != 1 || target.ProviderName != "mock" || !target.ModelRef.Valid || target.ModelRef.String != "small" || target.Weight != 100 {
		t.Fatalf("target projection mismatch: %+v", target)
	}

	// Caller row stores only the token hash and identifier.
	var caller callerRow
	if err := db.Table("router_config_callers").Where("config_set_id = ? AND caller_id = ?", "set-1", "caller-1").First(&caller).Error; err != nil {
		t.Fatal(err)
	}
	if caller.TokenSHA256 != "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef" || caller.TokenID != "tok-1" || caller.RPM != 10 || caller.TPM != 1000 || caller.Concurrent != 2 {
		t.Fatalf("caller projection mismatch: %+v", caller)
	}
	var allowedGroup string
	if err := db.Table("router_config_caller_allowed_groups").Where("config_set_id = ? AND caller_id = ?", "set-1", "caller-1").Pluck("group_name", &allowedGroup).Error; err != nil {
		t.Fatal(err)
	}
	if allowedGroup != "default" {
		t.Fatalf("caller allowed group projection mismatch: %q", allowedGroup)
	}
}

func TestProjectConfigToRelationalNeverStoresRawSecrets(t *testing.T) {
	r, closeDB := newRelationalProjectionTestDB(t, "set-1")
	defer func() { _ = closeDB() }()
	db := r.db

	if err := ProjectConfigToRelational(db, "set-1", minimalProjectionConfig()); err != nil {
		t.Fatal(err)
	}

	// The provider table has no column capable of holding the raw key; prove
	// the raw value appears nowhere in any projected row.
	for _, table := range []string{
		"router_config_providers",
		"router_config_provider_headers",
		"router_config_provider_models",
		"router_config_callers",
	} {
		rows, err := db.Table(table).Where("config_set_id = ?", "set-1").Rows()
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			scan := make([]any, len(columns))
			for i := range values {
				scan[i] = &values[i]
			}
			if err := rows.Scan(scan...); err != nil {
				t.Fatal(err)
			}
			for _, value := range values {
				if s, ok := value.(string); ok && strings.Contains(s, "sk-raw-secret") {
					t.Fatalf("raw provider secret leaked into %s", table)
				}
			}
		}
		_ = rows.Close()
	}
}

func TestProjectConfigToRelationalReplacesExistingChildren(t *testing.T) {
	r, closeDB := newRelationalProjectionTestDB(t, "set-1")
	defer func() { _ = closeDB() }()
	db := r.db

	if err := ProjectConfigToRelational(db, "set-1", minimalProjectionConfig()); err != nil {
		t.Fatal(err)
	}

	// Second projection with a modified config replaces rather than appends.
	updated := minimalProjectionConfig()
	updated.Server.Listen = ":9090"
	updated.Provider = map[string]ProviderConfig{
		"mock": {
			BaseURL:    updated.Provider["mock"].BaseURL,
			Dialect:    updated.Provider["mock"].Dialect,
			APIKey:     updated.Provider["mock"].APIKey,
			APIKeyEnv:  updated.Provider["mock"].APIKeyEnv,
			KeyID:      updated.Provider["mock"].KeyID,
			AuthScheme: updated.Provider["mock"].AuthScheme,
			Headers:    map[string]string{"X-Title": "replaced"},
			Models:     updated.Provider["mock"].Models,
		},
	}
	updated.Callers = nil
	if err := ProjectConfigToRelational(db, "set-1", updated); err != nil {
		t.Fatal(err)
	}

	var server serverConfigRow
	if err := db.Where("config_set_id = ?", "set-1").First(&server).Error; err != nil {
		t.Fatal(err)
	}
	if server.Listen != ":9090" {
		t.Fatalf("replace pass did not update server row: %+v", server)
	}
	var headerCount int64
	if err := db.Table("router_config_provider_headers").Where("config_set_id = ?", "set-1").Count(&headerCount).Error; err != nil {
		t.Fatal(err)
	}
	if headerCount != 1 {
		t.Fatalf("replace pass must not append duplicate headers, got %d", headerCount)
	}
	var callerCount int64
	if err := db.Table("router_config_callers").Where("config_set_id = ?", "set-1").Count(&callerCount).Error; err != nil {
		t.Fatal(err)
	}
	if callerCount != 0 {
		t.Fatalf("replace pass must delete removed callers, got %d", callerCount)
	}
	var modelCount int64
	if err := db.Table("router_config_provider_models").Where("config_set_id = ?", "set-1").Count(&modelCount).Error; err != nil {
		t.Fatal(err)
	}
	if modelCount != 1 {
		t.Fatalf("replace pass must not duplicate models, got %d", modelCount)
	}
}

func TestProjectConfigToRelationalSkipsMissingOptionalTables(t *testing.T) {
	r, closeDB := newRelationalProjectionTestDB(t, "set-1")
	defer func() { _ = closeDB() }()
	db := r.db

	cfg := minimalProjectionConfig()
	cfg.Users = []UserConfig{{ID: "user-1", Name: "User One", Status: "active"}}
	cfg.Projects = []ProjectConfig{{ID: "proj-1", Name: "Project One", Status: "active"}}
	cfg.ProjectMemberships = []ProjectMembershipConfig{{UserID: "user-1", Project: "proj-1", Role: "member", Status: "active"}}

	// The current control-plane schema has no users/projects/memberships
	// tables; the projection must skip them without failing.
	if err := ProjectConfigToRelational(db, "set-1", cfg); err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasTable("router_config_users") || db.Migrator().HasTable("router_config_projects") || db.Migrator().HasTable("router_config_project_memberships") {
		t.Fatal("projection must not create optional identity tables")
	}
}

func TestProjectConfigHookMatchesProjectConfigToRelational(t *testing.T) {
	r, closeDB := newRelationalProjectionTestDB(t, "set-1")
	defer func() { _ = closeDB() }()

	if ProjectConfigHook == nil {
		t.Fatal("ProjectConfigHook must be exported for document import")
	}
	if err := ProjectConfigHook(r.db, "set-1", minimalProjectionConfig()); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := r.db.Table("router_config_providers").Where("config_set_id = ?", "set-1").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("hook projection mismatch: %d providers", count)
	}
}

func TestProjectConfigToRelationalRejectsInvalidInputs(t *testing.T) {
	r, closeDB := newRelationalProjectionTestDB(t, "set-1")
	defer func() { _ = closeDB() }()

	if err := ProjectConfigToRelational(nil, "set-1", minimalProjectionConfig()); err == nil {
		t.Fatal("nil database must be rejected")
	}
	if err := ProjectConfigToRelational(r.db, "  ", minimalProjectionConfig()); err == nil {
		t.Fatal("blank config set id must be rejected")
	}
	if err := ProjectConfigToRelational(r.db, "set-1", nil); err == nil {
		t.Fatal("nil config must be rejected")
	}
}

func TestProjectConfigToRelationalRoundTripsThroughLoader(t *testing.T) {
	t.Setenv("MOCK_API_KEY", "test-only-projection-provider-key")
	r, closeDB := newRelationalProjectionTestDB(t, "set-1")
	defer func() { _ = closeDB() }()
	db := r.db

	if err := ProjectConfigToRelational(db, "set-1", minimalProjectionConfig()); err != nil {
		t.Fatal(err)
	}
	// Promote the draft set to active so the read path accepts it.
	if err := db.Exec(`UPDATE router_config_sets SET status = ?, validation_status = ? WHERE id = ?`, "active", "valid", "set-1").Error; err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadActiveConfigFromDB(db, "staging", IdentifierConfig{Mode: "passthrough"})
	if err != nil {
		t.Fatal(err)
	}
	provider, ok := loaded.Provider["mock"]
	if !ok {
		t.Fatal("round-trip lost provider")
	}
	if provider.APIKey != "test-only-projection-provider-key" || provider.APIKeyEnv != "MOCK_API_KEY" {
		t.Fatal("round-trip must resolve the credential from the environment reference")
	}
	if provider.Headers["X-Title"] != "projection-test" {
		t.Fatalf("round-trip lost non-secret header: %+v", provider.Headers)
	}
	if _, leaked := provider.Headers["Authorization"]; leaked {
		t.Fatal("round-trip must never resurrect a secret header")
	}
	if target := loaded.Models["default"].Targets[0]; target.Provider != "mock" || target.ModelRef != "small" || target.Weight != 100 {
		t.Fatalf("round-trip target mismatch: %+v", target)
	}
	if len(loaded.Callers) != 1 || loaded.Callers[0].TokenSHA256 != "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef" {
		t.Fatalf("round-trip caller mismatch: %+v", loaded.Callers)
	}
}

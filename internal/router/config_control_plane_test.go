// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func applyConfigControlPlaneMigrationsForTest(t *testing.T, r *migrationRunner) {
	t.Helper()
	if err := r.ApplyPending("test-online"); err == nil || !strings.Contains(err.Error(), "requires explicit maintenance runner") {
		t.Fatalf("online control-plane migration must stop before maintenance DDL, got %v", err)
	}
	if err := r.ApplyMaintenancePending("test-maintenance"); err != nil {
		t.Fatalf("explicit control-plane maintenance migration: %v", err)
	}
}

func TestConfigControlPlaneMigrationManifestIsStrictAndExecutable(t *testing.T) {
	if _, err := NewMigrationRunner(&gorm.DB{}, configControlPlaneScope, configControlPlaneCompatibility, configControlPlaneMigrationDefinitions); err != nil {
		t.Fatalf("config control-plane strict manifest rejected: %v", err)
	}
	for _, definition := range configControlPlaneMigrationDefinitions {
		if definition.ManifestDigest == "" || !migrationHandlerKeyMatches(definition.HandlerKey, definition.Apply) || !migrationHandlerKeyMatches(definition.PostconditionKey, definition.Verify) {
			t.Fatalf("migration %d lacks a strict executable manifest binding: %#v", definition.ID, definition)
		}
	}
	tampered := configControlPlaneMigrationDefinitions[0]
	tampered.Apply = configControlPlaneMigrationDefinitions[1].Apply
	if _, err := NewMigrationRunner(&gorm.DB{}, configControlPlaneScope, configControlPlaneCompatibility, []MigrationDefinition{tampered}); err == nil {
		t.Fatal("config apply handler pointer swap must fail closed")
	}
}

func TestConfigControlPlanePhase1MigratesRelationalSchema(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsForTest(t, r)
	status, err := r.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if !status.Compatible || status.State != "current" || status.SchemaVersion != 5 {
		t.Fatalf("unexpected migration status: %+v", status)
	}
	for _, table := range ConfigControlPlaneTableNames() {
		if !r.db.Migrator().HasTable(table) {
			t.Fatalf("missing table %s", table)
		}
	}
	allDDL := append(append([]string(nil), configControlPlaneDDL...), configControlPlanePhase4DDL...)
	for _, stmt := range allDDL {
		lower := strings.ToLower(stmt)
		for _, forbidden := range []string{" json", "jsonb", "[]", " array"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("control-plane DDL contains forbidden relational type %q: %s", forbidden, stmt)
			}
		}
	}
}

func TestLoadActiveConfigFromDBReadsValidatedCoreProjection(t *testing.T) {
	t.Setenv("MOCK_API_KEY", "test-only-control-plane-provider-key")
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsForTest(t, r)
	db := r.db
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, seed := range []struct {
		sql    string
		values []any
	}{
		{`INSERT INTO router_config_sets (id, runtime_scope, name, status, validation_status, created_at) VALUES (?, ?, ?, ?, ?, ?)`, []any{"set-1", "staging", "initial", "active", "valid", now}},
		{`INSERT INTO router_config_server (config_set_id, listen, default_model_group, state_path, license_enabled, license_path, license_state_path) VALUES (?, ?, ?, ?, ?, ?, ?)`, []any{"set-1", ":8080", "default", "router-state.json", true, "license.json", "license-state.json"}},
		{`INSERT INTO router_config_providers (config_set_id, provider_name, base_url, dialect, api_key_env) VALUES (?, ?, ?, ?, ?)`, []any{"set-1", "mock", "https://mock.example/v1", "openai", "MOCK_API_KEY"}},
		{`INSERT INTO router_config_provider_headers (config_set_id, provider_name, header_name, header_value) VALUES (?, ?, ?, ?)`, []any{"set-1", "mock", "X-Title", "test"}},
		{`INSERT INTO router_config_provider_models (config_set_id, provider_name, model_ref, model, context_tokens, input_price_per_million_usd, output_price_per_million_usd) VALUES (?, ?, ?, ?, ?, ?, ?)`, []any{"set-1", "mock", "small", "mock-small", 8192, 0.1, 0.2}},
		{`INSERT INTO router_config_provider_model_capabilities (config_set_id, provider_name, model_ref) VALUES (?, ?, ?)`, []any{"set-1", "mock", "small"}},
		{`INSERT INTO router_config_model_groups (config_set_id, group_name, strategy) VALUES (?, ?, ?)`, []any{"set-1", "default", "static"}},
		{`INSERT INTO router_config_model_group_targets (config_set_id, group_name, sequence, provider_name, model_ref, weight) VALUES (?, ?, ?, ?, ?, ?)`, []any{"set-1", "default", 1, "mock", "small", 100}},
	} {
		if err := db.Exec(seed.sql, seed.values...).Error; err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := LoadActiveConfigFromDB(db, "staging")
	if err != nil {
		t.Fatal(err)
	}
	provider, ok := cfg.Provider["mock"]
	if !ok || cfg.Server.DefaultModelGroup != "default" || provider.Headers["X-Title"] != "test" {
		t.Fatal("server/provider projection mismatch")
	}
	if provider.APIKey != "test-only-control-plane-provider-key" || provider.APIKeyEnv != "MOCK_API_KEY" {
		t.Fatal("provider credential environment reference was not resolved into the runtime config")
	}
	if !cfg.Server.License.Enabled || cfg.Server.License.Path != "license.json" || cfg.Server.License.StatePath != "license-state.json" {
		t.Fatalf("license projection mismatch: %+v", cfg.Server.License)
	}
	if target := cfg.Models["default"].Targets[0]; target.Provider != "mock" || target.ModelRef != "small" || target.Weight != 100 {
		t.Fatalf("target projection mismatch: %+v", target)
	}
	if model := provider.Models["small"]; model.Model != "mock-small" || model.ContextTokens != 8192 {
		t.Fatalf("model projection mismatch: %+v", model)
	}
}

func TestLoadActiveConfigFromDBLoadsNormalizedProviderModelCapabilities(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsForTest(t, r)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, seed := range []struct {
		sql    string
		values []any
	}{
		{`INSERT INTO router_config_sets (id, runtime_scope, name, status, validation_status, created_at) VALUES (?, ?, ?, ?, ?, ?)`, []any{"set-1", "staging", "capabilities", "active", "valid", now}},
		{`INSERT INTO router_config_server (config_set_id, default_model_group, license_enabled, license_path) VALUES (?, ?, ?, ?)`, []any{"set-1", "default", true, "license.json"}},
		{`INSERT INTO router_config_providers (config_set_id, provider_name, base_url, dialect) VALUES (?, ?, ?, ?)`, []any{"set-1", "mock", "https://mock.example/v1", "openai-chat"}},
		{`INSERT INTO router_config_provider_models (config_set_id, provider_name, model_ref, model, context_tokens) VALUES (?, ?, ?, ?, ?)`, []any{"set-1", "mock", "capable", "mock-capable", 32768}},
		{`INSERT INTO router_config_provider_model_capabilities (config_set_id, provider_name, model_ref, image_input_price_per_million_tokens_usd, image_input_price_per_image_usd, rpm, tier, cost, reasoning_supported, reasoning_mode, reasoning_control, reasoning_default_on, reasoning_min_budget_tokens, reasoning_max_budget_tokens, reasoning_budget_must_be_less_than_max_tokens, reasoning_stream_block, reasoning_rejects_max_tokens, reasoning_rejects_temperature, reasoning_rejects_top_p, reasoning_supports_summaries, honors_max_tokens, force_store_false, output_token_field, max_request_bytes, max_estimated_input_tokens, min_requested_output_tokens, max_requested_output_tokens, max_tool_schema_bytes, supports_large_coding_agent_payloads, request_shape_validation_status, request_shape_validation_notes, responses_to_chat_enabled, responses_to_chat_text, responses_to_chat_function_tools, responses_to_chat_tool_choice, responses_to_chat_structured_outputs, responses_to_chat_reasoning, responses_to_chat_images, responses_to_chat_streaming, responses_to_chat_validation_status, responses_to_chat_validation_notes) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, []any{"set-1", "mock", "capable", 1.25, 0.006, 17, "tested", 2, true, "opt_in", "effort_enum", true, 32, 256, false, "thinking", true, true, false, true, false, true, "max_completion_tokens", 2048, 400, 16, 256, 512, false, "passed", "synthetic capability projection", true, true, true, true, true, true, true, true, "passed", "synthetic bridge projection"}},
		{`INSERT INTO router_config_model_groups (config_set_id, group_name, strategy) VALUES (?, ?, ?)`, []any{"set-1", "default", "static"}},
		{`INSERT INTO router_config_model_group_targets (config_set_id, group_name, sequence, provider_name, model_ref, weight) VALUES (?, ?, ?, ?, ?, ?)`, []any{"set-1", "default", 1, "mock", "capable", 100}},
	} {
		if err := r.db.Exec(seed.sql, seed.values...).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		surface    string
		capability string
	}{
		{"openai_chat", "tools"},
		{"openai_responses", "function"},
		{"anthropic_messages", "client_tools"},
		{"provider_hosted", "tool_calls"},
	} {
		if err := r.db.Exec(`INSERT INTO router_config_provider_model_tool_support (config_set_id, provider_name, model_ref, api_surface, capability) VALUES (?, ?, ?, ?, ?)`, "set-1", "mock", "capable", row.surface, row.capability).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		direction string
		modality  string
	}{
		{"input", "text"},
		{"input", "image"},
		{"output", "text"},
	} {
		if err := r.db.Exec(`INSERT INTO router_config_provider_model_modalities (config_set_id, provider_name, model_ref, direction, modality) VALUES (?, ?, ?, ?, ?)`, "set-1", "mock", "capable", row.direction, row.modality).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, dialect := range []string{"openai-chat", "openai-responses"} {
		if err := r.db.Exec(`INSERT INTO router_config_provider_model_request_inbound_dialects (config_set_id, provider_name, model_ref, inbound_dialect) VALUES (?, ?, ?, ?)`, "set-1", "mock", "capable", dialect).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, feature := range []string{"previous_response_id", "stream_options"} {
		if err := r.db.Exec(`INSERT INTO router_config_provider_model_request_unsupported_features (config_set_id, provider_name, model_ref, feature) VALUES (?, ?, ?, ?)`, "set-1", "mock", "capable", feature).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, bridge := range []struct {
		direction string
		values    []any
	}{
		{
			direction: "chat_to_responses",
			values:    []any{true, true, true, true, true, true, true, true, true, true, "redis", "X-Bridge-Session", 30, 5, "redis.example:6379", "router", 1, "REDIS_USER", "REDIS_PASSWORD", true, "redis.example", false, 10, 20, 30, 4},
		},
		{
			direction: "responses_to_chat",
			values:    []any{true, true, true, true, true, true, true, true, true, false, "", "", 0, 0, "", "", 0, "", "", false, "", false, 0, 0, 0, 0},
		},
	} {
		values := append([]any{"set-1", "mock", "capable", bridge.direction}, bridge.values...)
		if err := r.db.Exec(`INSERT INTO router_config_provider_model_bridges (config_set_id, provider_name, model_ref, direction, enabled, text_enabled, tools, tool_choice, parallel_tool_calls, structured_outputs, images, reasoning, streaming, stateful_sessions_enabled, stateful_sessions_backend, stateful_sessions_session_header, stateful_sessions_ttl_seconds, stateful_sessions_max_entries, stateful_sessions_redis_address, stateful_sessions_redis_namespace, stateful_sessions_redis_db, stateful_sessions_redis_username_env, stateful_sessions_redis_password_env, stateful_sessions_redis_tls_enabled, stateful_sessions_redis_tls_server_name, stateful_sessions_redis_tls_insecure_skip_verify, stateful_sessions_redis_connect_timeout_ms, stateful_sessions_redis_read_timeout_ms, stateful_sessions_redis_write_timeout_ms, stateful_sessions_redis_pool_size) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, values...).Error; err != nil {
			t.Fatal(err)
		}
	}

	cfg, err := LoadActiveConfigFromDB(r.db, "staging")
	if err != nil {
		t.Fatal(err)
	}
	model := cfg.Provider["mock"].Models["capable"]
	if model.ImageInputPricePerMillionTokensUSD != 1.25 || model.ImageInputPricePerImageUSD != 0.006 || model.RPM != 17 || model.Tier != "tested" || model.Cost != 2 {
		t.Fatalf("catalog scalar capability projection mismatch: %#v", model)
	}
	if !model.Reasoning.Supported || model.Reasoning.Mode != "opt_in" || model.Reasoning.Control != "effort_enum" || !model.Reasoning.DefaultOn || model.Reasoning.MinBudgetTokens != 32 || model.Reasoning.MaxBudgetTokens != 256 || !model.Reasoning.RejectsMaxTokens || !model.Reasoning.RejectsTemperature || model.Reasoning.RejectsTopP || !model.Reasoning.SupportsSummaries {
		t.Fatalf("reasoning capability projection mismatch: %#v", model.Reasoning)
	}
	if model.HonorsMaxTokens == nil || *model.HonorsMaxTokens || !model.ForceStoreFalse || model.OutputTokenField != "max_completion_tokens" {
		t.Fatalf("max-token/output-token capability projection mismatch: %#v", model)
	}
	if strings.Join(model.ToolSupport.OpenAIChat, ",") != "tools" || strings.Join(model.ToolSupport.OpenAIResponses, ",") != "function" || strings.Join(model.ToolSupport.AnthropicMessages, ",") != "client_tools" || strings.Join(model.ToolSupport.ProviderHosted, ",") != "tool_calls" {
		t.Fatalf("tool capability projection mismatch: %#v", model.ToolSupport)
	}
	if strings.Join(model.InputModalities, ",") != "image,text" || strings.Join(model.OutputModalities, ",") != "text" {
		t.Fatalf("modality projection mismatch: input=%#v output=%#v", model.InputModalities, model.OutputModalities)
	}
	shape := model.RequestShapeSupport
	if shape.MaxRequestBytes != 2048 || shape.MaxEstimatedInputTokens != 400 || shape.MinRequestedOutputTokens != 16 || shape.MaxRequestedOutputTokens != 256 || shape.MaxToolSchemaBytes != 512 || shape.SupportsLargeCodingAgentPayloads == nil || *shape.SupportsLargeCodingAgentPayloads || shape.ValidationStatus != "passed" || shape.ValidationNotes != "synthetic capability projection" || strings.Join(shape.SupportedInboundDialects, ",") != "openai-chat,openai-responses" || strings.Join(shape.UnsupportedRequestFeatures, ",") != "previous_response_id,stream_options" {
		t.Fatalf("request-shape capability projection mismatch: %#v", shape)
	}
	if !model.ResponsesToChat.Enabled || !model.ResponsesToChat.Text || !model.ResponsesToChat.FunctionTools || !model.ResponsesToChat.ToolChoice || !model.ResponsesToChat.StructuredOutputs || !model.ResponsesToChat.Reasoning || !model.ResponsesToChat.Images || !model.ResponsesToChat.Streaming || model.ResponsesToChat.ValidationStatus != "passed" {
		t.Fatalf("legacy responses-to-chat capability projection mismatch: %#v", model.ResponsesToChat)
	}
	chatBridge := model.Bridges.ChatToResponses
	if !chatBridge.Enabled || chatBridge.Text == nil || !*chatBridge.Text || !chatBridge.Tools || !chatBridge.ToolChoice || !chatBridge.ParallelToolCalls || !chatBridge.StructuredOutputs || !chatBridge.Images || !chatBridge.Reasoning || !chatBridge.Streaming || !chatBridge.StatefulSessions.Enabled || chatBridge.StatefulSessions.Backend != "redis" || chatBridge.StatefulSessions.Redis.Address != "redis.example:6379" || chatBridge.StatefulSessions.Redis.Password != "" || chatBridge.StatefulSessions.Redis.PasswordEnv != "REDIS_PASSWORD" {
		t.Fatalf("chat-to-responses bridge capability projection mismatch: %#v", chatBridge)
	}
	responsesBridge := model.Bridges.ResponsesToChat
	if !responsesBridge.Enabled || responsesBridge.Text == nil || !*responsesBridge.Text || !responsesBridge.Tools || !responsesBridge.ToolChoice || !responsesBridge.ParallelToolCalls || !responsesBridge.StructuredOutputs || !responsesBridge.Images || !responsesBridge.Reasoning || !responsesBridge.Streaming || responsesBridge.StatefulSessions.Enabled {
		t.Fatalf("responses-to-chat bridge capability projection mismatch: %#v", responsesBridge)
	}
	target := cfg.Models["default"].Targets[0]
	if target.Model != "mock-capable" || target.HonorsMaxTokens == nil || *target.HonorsMaxTokens || !target.ForceStoreFalse || target.OutputTokenField != "max_completion_tokens" || strings.Join(target.InputModalities, ",") != "image,text" || !target.Bridges.ChatToResponses.Enabled || !target.ResponsesToChat.Enabled {
		t.Fatalf("model_ref target did not inherit catalog capability projection: %#v", target)
	}
}

func TestLoadActiveConfigFromDBFailsClosedWithoutProviderModelCapabilityRecord(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsForTest(t, r)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, seed := range []struct {
		sql    string
		values []any
	}{
		{`INSERT INTO router_config_sets (id, runtime_scope, name, status, validation_status, created_at) VALUES (?, ?, ?, ?, ?, ?)`, []any{"set-1", "staging", "missing-capabilities", "active", "valid", now}},
		{`INSERT INTO router_config_server (config_set_id, default_model_group, license_enabled, license_path) VALUES (?, ?, ?, ?)`, []any{"set-1", "default", true, "license.json"}},
		{`INSERT INTO router_config_providers (config_set_id, provider_name, base_url, dialect) VALUES (?, ?, ?, ?)`, []any{"set-1", "mock", "https://mock.example/v1", "openai-chat"}},
		{`INSERT INTO router_config_provider_models (config_set_id, provider_name, model_ref, model) VALUES (?, ?, ?, ?)`, []any{"set-1", "mock", "missing", "mock-missing"}},
		{`INSERT INTO router_config_model_groups (config_set_id, group_name, strategy) VALUES (?, ?, ?)`, []any{"set-1", "default", "static"}},
		{`INSERT INTO router_config_model_group_targets (config_set_id, group_name, sequence, provider_name, model_ref, weight) VALUES (?, ?, ?, ?, ?, ?)`, []any{"set-1", "default", 1, "mock", "missing", 100}},
	} {
		if err := r.db.Exec(seed.sql, seed.values...).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LoadActiveConfigFromDB(r.db, "staging"); err == nil || !strings.Contains(err.Error(), "has no capability record") {
		t.Fatalf("missing provider-model capability record must fail closed, got %v", err)
	}
}

func TestConfigControlPlanePhase4BackfillsExistingProviderModelCapabilities(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	phase3Runner, err := NewMigrationRunner(r.db, configControlPlaneScope, MigrationCompatibility{MinSchema: 0, MaxSchema: 3, MinData: 0, MaxData: 0}, configControlPlaneMigrationDefinitions[:3])
	if err != nil {
		t.Fatal(err)
	}
	if err := phase3Runner.ApplyPending("test-online"); err == nil || !strings.Contains(err.Error(), "requires explicit maintenance runner") {
		t.Fatalf("phase-3 setup must stop before maintenance migrations, got %v", err)
	}
	if err := phase3Runner.ApplyMaintenancePending("test-maintenance"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, seed := range []struct {
		sql    string
		values []any
	}{
		{`INSERT INTO router_config_sets (id, runtime_scope, name, status, validation_status, created_at) VALUES (?, ?, ?, ?, ?, ?)`, []any{"set-1", "staging", "phase-three", "draft", "valid", now}},
		{`INSERT INTO router_config_providers (config_set_id, provider_name, base_url, dialect) VALUES (?, ?, ?, ?)`, []any{"set-1", "mock", "https://mock.example/v1", "openai-chat"}},
		{`INSERT INTO router_config_provider_models (config_set_id, provider_name, model_ref, model) VALUES (?, ?, ?, ?)`, []any{"set-1", "mock", "existing", "mock-existing"}},
	} {
		if err := r.db.Exec(seed.sql, seed.values...).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := r.ApplyMaintenancePending("test-maintenance"); err != nil {
		t.Fatal(err)
	}
	var capability providerModelCapabilityRow
	if err := r.db.Where("config_set_id = ? AND provider_name = ? AND model_ref = ?", "set-1", "mock", "existing").First(&capability).Error; err != nil {
		t.Fatalf("phase-4 migration did not backfill the existing provider model capability record: %v", err)
	}
	if capability.HonorsMaxTokens.Valid || capability.ForceStoreFalse || capability.OutputTokenField != "" || capability.ReasoningSupported {
		t.Fatalf("phase-4 capability backfill must preserve all-default prior semantics, got %#v", capability)
	}
}

func TestConfigControlPlanePhase4VerifierRejectsMissingCapabilityForeignKey(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsForTest(t, r)
	if err := r.db.Exec(`DROP TABLE router_config_provider_model_modalities`).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.db.Exec(`CREATE TABLE router_config_provider_model_modalities (config_set_id TEXT NOT NULL, provider_name TEXT NOT NULL, model_ref TEXT NOT NULL, direction TEXT NOT NULL, modality TEXT NOT NULL, PRIMARY KEY (config_set_id, provider_name, model_ref, direction, modality))`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.Verify(); err == nil || !strings.Contains(err.Error(), "router_config_provider_model_modalities") {
		t.Fatalf("expected missing provider-model capability foreign-key verification failure, got %v", err)
	}
}

func TestConfigControlPlanePhase4PostgresCommentsCoverCapabilitySchema(t *testing.T) {
	comments := strings.Join(configControlPlanePhase4PostgresComments, "\n")
	for _, table := range configControlPlanePhase4Tables {
		if !strings.Contains(comments, "COMMENT ON TABLE "+table+" ") {
			t.Fatalf("missing PostgreSQL table comment for %s", table)
		}
		for _, column := range configControlPlanePhase4RequiredColumns[table] {
			if !strings.Contains(comments, "COMMENT ON COLUMN "+table+"."+column+" ") {
				t.Fatalf("missing PostgreSQL column comment for %s.%s", table, column)
			}
		}
	}
}

func TestLoadActiveConfigFromDBFailsClosedWithoutValidatedActiveSet(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsForTest(t, r)
	if _, err := LoadActiveConfigFromDB(r.db, "staging"); err == nil || !strings.Contains(err.Error(), "no validated active") {
		t.Fatalf("expected closed failure, got %v", err)
	}
}

func TestLoadActiveConfigFromDBFailsClosedWithMultipleValidatedActiveSets(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsForTest(t, r)
	if err := r.db.Exec(`DROP INDEX router_config_one_active_set_per_scope`).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, id := range []string{"set-1", "set-2"} {
		if err := r.db.Exec(`INSERT INTO router_config_sets (id, runtime_scope, name, status, validation_status, created_at) VALUES (?, ?, ?, ?, ?, ?)`, id, "staging", id, "active", "valid", now).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LoadActiveConfigFromDB(r.db, "staging"); err == nil || !strings.Contains(err.Error(), "multiple validated active") {
		t.Fatalf("expected ambiguous active-set failure, got %v", err)
	}
}

func TestConfigControlPlaneAllowsOnlyOneActiveSetPerScope(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsForTest(t, r)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := r.db.Exec(`INSERT INTO router_config_sets (id, runtime_scope, name, status, validation_status, created_at) VALUES (?, ?, ?, ?, ?, ?)`, "set-1", "staging", "one", "active", "valid", now).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.db.Exec(`INSERT INTO router_config_sets (id, runtime_scope, name, status, validation_status, created_at) VALUES (?, ?, ?, ?, ?, ?)`, "set-2", "staging", "two", "active", "valid", now).Error; err == nil {
		t.Fatal("second active set in one runtime scope must be rejected")
	}
}

func TestConfigControlPlaneEnforcesTargetProviderAndModelForeignKeys(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsForTest(t, r)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, seed := range []struct {
		sql    string
		values []any
	}{
		{`INSERT INTO router_config_sets (id, runtime_scope, name, status, validation_status, created_at) VALUES (?, ?, ?, ?, ?, ?)`, []any{"set-1", "staging", "one", "draft", "valid", now}},
		{`INSERT INTO router_config_model_groups (config_set_id, group_name) VALUES (?, ?)`, []any{"set-1", "group"}},
	} {
		if err := r.db.Exec(seed.sql, seed.values...).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := r.db.Exec(`INSERT INTO router_config_model_group_targets (config_set_id, group_name, sequence, provider_name, model_ref) VALUES (?, ?, ?, ?, ?)`, "set-1", "group", 1, "missing", "small").Error; err == nil {
		t.Fatal("target with missing provider/model must be rejected")
	}
}

func TestConfigControlPlaneVerifierRejectsMissingProviderHeaderForeignKey(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	phase1Runner, err := NewMigrationRunner(r.db, configControlPlaneScope, MigrationCompatibility{MinSchema: 0, MaxSchema: 1, MinData: 0, MaxData: 0}, configControlPlaneMigrationDefinitions[:1])
	if err != nil {
		t.Fatal(err)
	}
	if err := phase1Runner.ApplyPending("test-runner"); err != nil {
		t.Fatal(err)
	}
	if err := r.db.Exec(`DROP TABLE router_config_provider_headers`).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.db.Exec(`CREATE TABLE router_config_provider_headers (config_set_id TEXT NOT NULL, provider_name TEXT NOT NULL, header_name TEXT NOT NULL, header_value TEXT NOT NULL, PRIMARY KEY (config_set_id, provider_name, header_name))`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := phase1Runner.Verify(); err == nil || !strings.Contains(err.Error(), "router_config_provider_headers") {
		t.Fatalf("expected missing provider-header foreign key verification failure, got %v", err)
	}
}

func TestConfigControlPlaneRejectsCredentialBearingProviderHeadersAtDatabaseBoundary(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsForTest(t, r)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, seed := range []struct {
		sql    string
		values []any
	}{
		{`INSERT INTO router_config_sets (id, runtime_scope, name, status, validation_status, created_at) VALUES (?, ?, ?, ?, ?, ?)`, []any{"set-1", "staging", "one", "active", "valid", now}},
		{`INSERT INTO router_config_providers (config_set_id, provider_name, base_url, dialect) VALUES (?, ?, ?, ?)`, []any{"set-1", "mock", "https://mock.example/v1", "openai"}},
	} {
		if err := r.db.Exec(seed.sql, seed.values...).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, headerName := range []string{"Authorization", "Proxy-Authorization", "X-Goog-Api-Key", "Ocp-Apim-Subscription-Key", " X-Title "} {
		if err := r.db.Exec(`INSERT INTO router_config_provider_headers (config_set_id, provider_name, header_name, header_value) VALUES (?, ?, ?, ?)`, "set-1", "mock", headerName, "not-a-real-secret").Error; err == nil {
			t.Fatalf("database accepted disallowed provider header %q", headerName)
		}
	}
	if err := r.db.Exec(`INSERT INTO router_config_provider_headers (config_set_id, provider_name, header_name, header_value) VALUES (?, ?, ?, ?)`, "set-1", "mock", "X-Title", "non-secret-metadata").Error; err != nil {
		t.Fatalf("database rejected approved non-secret header: %v", err)
	}
	if err := r.db.Exec(`INSERT INTO router_config_provider_headers (config_set_id, provider_name, header_name, header_value) VALUES (?, ?, ?, ?)`, "set-1", "mock", "x-title", "conflicting-non-secret-metadata").Error; err == nil {
		t.Fatal("database accepted case-insensitive duplicate provider header")
	}
	if err := r.db.Exec(`INSERT INTO router_config_provider_headers (config_set_id, provider_name, header_name, header_value) VALUES (?, ?, ?, ?)`, "set-1", "mock", "User-Agent", "non-secret-metadata").Error; err != nil {
		t.Fatalf("database rejected second approved non-secret header: %v", err)
	}
	if err := r.db.Exec(`UPDATE router_config_provider_headers SET header_name = ? WHERE config_set_id = ? AND provider_name = ? AND header_name = ?`, "X-TITLE", "set-1", "mock", "User-Agent").Error; err == nil {
		t.Fatal("database accepted a case-insensitive duplicate provider header through update")
	}
	if err := r.db.Exec(`UPDATE router_config_provider_headers SET header_name = ? WHERE config_set_id = ? AND provider_name = ? AND header_name = ?`, "Authorization", "set-1", "mock", "X-Title").Error; err == nil {
		t.Fatal("database accepted credential-bearing provider header through update")
	}
}

func TestConfigControlPlanePhase2UpgradesExistingAllowedProviderHeaders(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	phase1Runner, err := NewMigrationRunner(r.db, configControlPlaneScope, MigrationCompatibility{MinSchema: 0, MaxSchema: 1, MinData: 0, MaxData: 0}, configControlPlaneMigrationDefinitions[:1])
	if err != nil {
		t.Fatal(err)
	}
	if err := phase1Runner.ApplyPending("test-runner"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, seed := range []struct {
		sql    string
		values []any
	}{
		{`INSERT INTO router_config_sets (id, runtime_scope, name, status, validation_status, created_at) VALUES (?, ?, ?, ?, ?, ?)`, []any{"set-1", "staging", "one", "draft", "valid", now}},
		{`INSERT INTO router_config_providers (config_set_id, provider_name, base_url, dialect) VALUES (?, ?, ?, ?)`, []any{"set-1", "mock", "https://mock.example/v1", "openai"}},
		{`INSERT INTO router_config_provider_headers (config_set_id, provider_name, header_name, header_value) VALUES (?, ?, ?, ?)`, []any{"set-1", "mock", "X-Title", "non-secret-metadata"}},
	} {
		if err := r.db.Exec(seed.sql, seed.values...).Error; err != nil {
			t.Fatal(err)
		}
	}
	applyConfigControlPlaneMigrationsForTest(t, r)
	if _, err := r.Verify(); err != nil {
		t.Fatal(err)
	}
	var header providerHeaderRow
	if err := r.db.Where("config_set_id = ? AND provider_name = ? AND header_name = ?", "set-1", "mock", "X-Title").First(&header).Error; err != nil {
		t.Fatalf("approved provider header was not preserved by phase 2: %v", err)
	}
	if header.HeaderValue != "non-secret-metadata" {
		t.Fatalf("provider header value = %q, want preserved non-secret metadata", header.HeaderValue)
	}
	if err := r.db.Exec(`INSERT INTO router_config_provider_headers (config_set_id, provider_name, header_name, header_value) VALUES (?, ?, ?, ?)`, "set-1", "mock", "Authorization", "not-a-real-secret").Error; err == nil {
		t.Fatal("phase 2 migration did not reject credential-bearing provider headers")
	}
}

func TestConfigControlPlanePhase3FailsClosedOnExistingCaseInsensitiveHeaderDuplicates(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	phase2Runner, err := NewMigrationRunner(r.db, configControlPlaneScope, MigrationCompatibility{MinSchema: 0, MaxSchema: 2, MinData: 0, MaxData: 0}, configControlPlaneMigrationDefinitions[:2])
	if err != nil {
		t.Fatal(err)
	}
	if err := phase2Runner.ApplyPending("test-online"); err == nil || !strings.Contains(err.Error(), "requires explicit maintenance runner") {
		t.Fatalf("phase 2 must require explicit maintenance, got %v", err)
	}
	if err := phase2Runner.ApplyMaintenancePending("test-maintenance"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, seed := range []struct {
		sql    string
		values []any
	}{
		{`INSERT INTO router_config_sets (id, runtime_scope, name, status, validation_status, created_at) VALUES (?, ?, ?, ?, ?, ?)`, []any{"set-1", "staging", "one", "draft", "valid", now}},
		{`INSERT INTO router_config_providers (config_set_id, provider_name, base_url, dialect) VALUES (?, ?, ?, ?)`, []any{"set-1", "mock", "https://mock.example/v1", "openai"}},
		{`INSERT INTO router_config_provider_headers (config_set_id, provider_name, header_name, header_value) VALUES (?, ?, ?, ?)`, []any{"set-1", "mock", "X-Title", "one"}},
		{`INSERT INTO router_config_provider_headers (config_set_id, provider_name, header_name, header_value) VALUES (?, ?, ?, ?)`, []any{"set-1", "mock", "x-title", "two"}},
	} {
		if err := r.db.Exec(seed.sql, seed.values...).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := r.ApplyMaintenancePending("test-maintenance"); err == nil {
		t.Fatal("phase 3 must fail closed instead of silently choosing a duplicate provider header")
	}
	status, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.SchemaVersion != 2 || status.State != "failed" {
		t.Fatalf("duplicate header migration status = %+v, want failed schema 2", status)
	}
}

func TestLoadActiveConfigFromDBRejectsCaseVariantHeadersBeforePhase3(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	phase2Runner, err := NewMigrationRunner(r.db, configControlPlaneScope, MigrationCompatibility{MinSchema: 0, MaxSchema: 2, MinData: 0, MaxData: 0}, configControlPlaneMigrationDefinitions[:2])
	if err != nil {
		t.Fatal(err)
	}
	if err := phase2Runner.ApplyPending("test-online"); err == nil || !strings.Contains(err.Error(), "requires explicit maintenance runner") {
		t.Fatalf("phase 2 must require explicit maintenance, got %v", err)
	}
	if err := phase2Runner.ApplyMaintenancePending("test-maintenance"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, seed := range []struct {
		sql    string
		values []any
	}{
		{`INSERT INTO router_config_sets (id, runtime_scope, name, status, validation_status, created_at) VALUES (?, ?, ?, ?, ?, ?)`, []any{"set-1", "staging", "one", "active", "valid", now}},
		{`INSERT INTO router_config_server (config_set_id, default_model_group, license_enabled, license_path) VALUES (?, ?, ?, ?)`, []any{"set-1", "default", true, "license.json"}},
		{`INSERT INTO router_config_providers (config_set_id, provider_name, base_url, dialect) VALUES (?, ?, ?, ?)`, []any{"set-1", "mock", "https://mock.example/v1", "openai"}},
		{`INSERT INTO router_config_provider_headers (config_set_id, provider_name, header_name, header_value) VALUES (?, ?, ?, ?)`, []any{"set-1", "mock", "X-Title", "one"}},
		{`INSERT INTO router_config_provider_headers (config_set_id, provider_name, header_name, header_value) VALUES (?, ?, ?, ?)`, []any{"set-1", "mock", "x-title", "two"}},
	} {
		if err := r.db.Exec(seed.sql, seed.values...).Error; err != nil {
			t.Fatal(err)
		}
	}
	status, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.SchemaVersion != 2 || status.State != "pending" {
		t.Fatalf("phase-2 migration status = %+v, want pending schema 2", status)
	}
	if _, err := LoadActiveConfigFromDB(r.db, "staging"); err == nil || !strings.Contains(err.Error(), "case-insensitive duplicate header") {
		t.Fatalf("expected case-variant header read failure before phase 3, got %v", err)
	}
}

func TestConfigControlPlanePhase3VerifierRejectsIndexSemanticDrift(t *testing.T) {
	for name, statement := range map[string]string{
		"non-unique":       `CREATE INDEX router_config_provider_headers_name_ci ON router_config_provider_headers(config_set_id, provider_name, LOWER(header_name))`,
		"wrong-expression": `CREATE UNIQUE INDEX router_config_provider_headers_name_ci ON router_config_provider_headers(config_set_id, provider_name, LOWER(header_value))`,
		"wrong-order":      `CREATE UNIQUE INDEX router_config_provider_headers_name_ci ON router_config_provider_headers(provider_name, config_set_id, LOWER(header_name))`,
		"partial":          `CREATE UNIQUE INDEX router_config_provider_headers_name_ci ON router_config_provider_headers(config_set_id, provider_name, LOWER(header_name)) WHERE header_name IS NOT NULL`,
	} {
		t.Run(name, func(t *testing.T) {
			r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = closeDB() }()
			applyConfigControlPlaneMigrationsForTest(t, r)
			if err := r.db.Exec(`DROP INDEX router_config_provider_headers_name_ci`).Error; err != nil {
				t.Fatal(err)
			}
			if err := r.db.Exec(statement).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := r.Verify(); err == nil || !strings.Contains(err.Error(), configControlPlaneProviderHeaderNameCIIndex) {
				t.Fatalf("expected phase-3 index semantic verification failure, got %v", err)
			}
		})
	}
}

func TestConfigControlPlanePhase1VerifierRejectsActiveSetIndexSemanticDrift(t *testing.T) {
	for name, statement := range map[string]string{
		"non-unique":      `CREATE INDEX router_config_one_active_set_per_scope ON router_config_sets(runtime_scope) WHERE status = 'active'`,
		"wrong-key":       `CREATE UNIQUE INDEX router_config_one_active_set_per_scope ON router_config_sets(name) WHERE status = 'active'`,
		"extra-key":       `CREATE UNIQUE INDEX router_config_one_active_set_per_scope ON router_config_sets(runtime_scope, name) WHERE status = 'active'`,
		"wrong-predicate": `CREATE UNIQUE INDEX router_config_one_active_set_per_scope ON router_config_sets(runtime_scope) WHERE status = 'valid'`,
		"not-partial":     `CREATE UNIQUE INDEX router_config_one_active_set_per_scope ON router_config_sets(runtime_scope)`,
	} {
		t.Run(name, func(t *testing.T) {
			r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = closeDB() }()
			applyConfigControlPlaneMigrationsForTest(t, r)
			if err := r.db.Exec(`DROP INDEX router_config_one_active_set_per_scope`).Error; err != nil {
				t.Fatal(err)
			}
			if err := r.db.Exec(statement).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := r.Verify(); err == nil || !strings.Contains(err.Error(), configControlPlaneOneActiveSetPerScopeIndex) {
				t.Fatalf("expected active-set index semantic verification failure, got %v", err)
			}
		})
	}
}

func TestConfigControlPlanePostgresProviderHeaderCIIndexKeyVerification(t *testing.T) {
	for _, test := range []struct {
		name     string
		metadata configControlPlanePostgresIndexMetadata
		wantErr  bool
	}{
		{name: "expected", metadata: configControlPlanePostgresIndexMetadata{Unique: true, Valid: true, Ready: true, Live: true, NoPredicate: true, KeyCount: 3, AttributeCount: 3, Keys: []string{"config_set_id", "provider_name", "lower(header_name)"}}},
		{name: "expected-quoted", metadata: configControlPlanePostgresIndexMetadata{Unique: true, Valid: true, Ready: true, Live: true, NoPredicate: true, KeyCount: 3, AttributeCount: 3, Keys: []string{`"config_set_id"`, `"provider_name"`, `lower("header_name")`}}},
		{name: "non-unique", metadata: configControlPlanePostgresIndexMetadata{Valid: true, Ready: true, Live: true, NoPredicate: true, KeyCount: 3, AttributeCount: 3, Keys: []string{"config_set_id", "provider_name", "lower(header_name)"}}, wantErr: true},
		{name: "wrong-expression", metadata: configControlPlanePostgresIndexMetadata{Unique: true, Valid: true, Ready: true, Live: true, NoPredicate: true, KeyCount: 3, AttributeCount: 3, Keys: []string{"config_set_id", "provider_name", "lower(header_value)"}}, wantErr: true},
		{name: "partial", metadata: configControlPlanePostgresIndexMetadata{Unique: true, Valid: true, Ready: true, Live: true, KeyCount: 3, AttributeCount: 3, Keys: []string{"config_set_id", "provider_name", "lower(header_name)"}}, wantErr: true},
		{name: "included-column", metadata: configControlPlanePostgresIndexMetadata{Unique: true, Valid: true, Ready: true, Live: true, NoPredicate: true, KeyCount: 3, AttributeCount: 4, Keys: []string{"config_set_id", "provider_name", "lower(header_name)"}}, wantErr: true},
		{name: "invalid", metadata: configControlPlanePostgresIndexMetadata{Unique: true, Ready: true, Live: true, NoPredicate: true, KeyCount: 3, AttributeCount: 3, Keys: []string{"config_set_id", "provider_name", "lower(header_name)"}}, wantErr: true},
		{name: "not-ready", metadata: configControlPlanePostgresIndexMetadata{Unique: true, Valid: true, Live: true, NoPredicate: true, KeyCount: 3, AttributeCount: 3, Keys: []string{"config_set_id", "provider_name", "lower(header_name)"}}, wantErr: true},
		{name: "not-live", metadata: configControlPlanePostgresIndexMetadata{Unique: true, Valid: true, Ready: true, NoPredicate: true, KeyCount: 3, AttributeCount: 3, Keys: []string{"config_set_id", "provider_name", "lower(header_name)"}}, wantErr: true},
		{name: "wrong-order", metadata: configControlPlanePostgresIndexMetadata{Unique: true, Valid: true, Ready: true, Live: true, NoPredicate: true, KeyCount: 3, AttributeCount: 3, Keys: []string{"provider_name", "config_set_id", "lower(header_name)"}}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := verifyConfigControlPlaneProviderHeaderCIIndexPostgresMetadata(test.metadata)
			if (err != nil) != test.wantErr {
				t.Fatalf("PostgreSQL index metadata verification error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

func TestConfigControlPlanePostgresOneActiveSetIndexSemanticVerification(t *testing.T) {
	for _, test := range []struct {
		name     string
		metadata configControlPlanePostgresOneActiveSetIndexMetadata
		wantErr  bool
	}{
		{name: "expected", metadata: configControlPlanePostgresOneActiveSetIndexMetadata{Unique: true, Valid: true, Ready: true, Live: true, KeyCount: 1, AttributeCount: 1, Predicate: "(status = 'active'::text)", Keys: []string{"runtime_scope"}}},
		{name: "expected-quoted", metadata: configControlPlanePostgresOneActiveSetIndexMetadata{Unique: true, Valid: true, Ready: true, Live: true, KeyCount: 1, AttributeCount: 1, Predicate: `(("status")::text = 'active'::text)`, Keys: []string{`"runtime_scope"`}}},
		{name: "non-unique", metadata: configControlPlanePostgresOneActiveSetIndexMetadata{Valid: true, Ready: true, Live: true, KeyCount: 1, AttributeCount: 1, Predicate: "(status = 'active'::text)", Keys: []string{"runtime_scope"}}, wantErr: true},
		{name: "wrong-key", metadata: configControlPlanePostgresOneActiveSetIndexMetadata{Unique: true, Valid: true, Ready: true, Live: true, KeyCount: 1, AttributeCount: 1, Predicate: "(status = 'active'::text)", Keys: []string{"name"}}, wantErr: true},
		{name: "extra-key", metadata: configControlPlanePostgresOneActiveSetIndexMetadata{Unique: true, Valid: true, Ready: true, Live: true, KeyCount: 2, AttributeCount: 2, Predicate: "(status = 'active'::text)", Keys: []string{"runtime_scope", "name"}}, wantErr: true},
		{name: "wrong-predicate", metadata: configControlPlanePostgresOneActiveSetIndexMetadata{Unique: true, Valid: true, Ready: true, Live: true, KeyCount: 1, AttributeCount: 1, Predicate: "(status = 'valid'::text)", Keys: []string{"runtime_scope"}}, wantErr: true},
		{name: "not-partial", metadata: configControlPlanePostgresOneActiveSetIndexMetadata{Unique: true, Valid: true, Ready: true, Live: true, KeyCount: 1, AttributeCount: 1, Keys: []string{"runtime_scope"}}, wantErr: true},
		{name: "included-column", metadata: configControlPlanePostgresOneActiveSetIndexMetadata{Unique: true, Valid: true, Ready: true, Live: true, KeyCount: 1, AttributeCount: 2, Predicate: "(status = 'active'::text)", Keys: []string{"runtime_scope"}}, wantErr: true},
		{name: "invalid", metadata: configControlPlanePostgresOneActiveSetIndexMetadata{Unique: true, Ready: true, Live: true, KeyCount: 1, AttributeCount: 1, Predicate: "(status = 'active'::text)", Keys: []string{"runtime_scope"}}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := verifyConfigControlPlaneOneActiveSetPerScopeIndexPostgresMetadata(test.metadata)
			if (err != nil) != test.wantErr {
				t.Fatalf("PostgreSQL active-set index metadata verification error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

func TestLoadActiveConfigFromDBAcceptsInlineTargetWithoutModelRef(t *testing.T) {
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDB() }()
	applyConfigControlPlaneMigrationsForTest(t, r)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, seed := range []struct {
		sql    string
		values []any
	}{
		{`INSERT INTO router_config_sets (id, runtime_scope, name, status, validation_status, created_at) VALUES (?, ?, ?, ?, ?, ?)`, []any{"set-1", "staging", "one", "active", "valid", now}},
		{`INSERT INTO router_config_server (config_set_id, default_model_group, license_enabled, license_path) VALUES (?, ?, ?, ?)`, []any{"set-1", "default", true, "license.json"}},
		{`INSERT INTO router_config_providers (config_set_id, provider_name, base_url, dialect) VALUES (?, ?, ?, ?)`, []any{"set-1", "mock", "https://mock.example/v1", "openai"}},
		{`INSERT INTO router_config_model_groups (config_set_id, group_name, strategy) VALUES (?, ?, ?)`, []any{"set-1", "default", "static"}},
		{`INSERT INTO router_config_model_group_targets (config_set_id, group_name, sequence, provider_name, model, weight) VALUES (?, ?, ?, ?, ?, ?)`, []any{"set-1", "default", 1, "mock", "inline-model", 100}},
	} {
		if err := r.db.Exec(seed.sql, seed.values...).Error; err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := LoadActiveConfigFromDB(r.db, "staging")
	if err != nil {
		t.Fatal(err)
	}
	target := cfg.Models["default"].Targets[0]
	if target.ModelRef != "" || target.Model != "inline-model" {
		t.Fatalf("inline target = %+v, want empty model_ref and inline model", target)
	}
}

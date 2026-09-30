// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Write-side relational projection from a validated *Config into the
// existing router_config_* tables. The projection is intentionally scoped:
// it never accepts or stores raw provider secrets, raw caller tokens, or
// non-allowlisted provider headers. YAML remains the serving default; the
// relational projection exists so the document import path can write a draft
// configuration set without introducing a full write API, secret storage, or
// runtime hot reload.
//
// The projection uses replace semantics: existing children for the supplied
// config_set_id are deleted before the new rows are inserted. This keeps
// draft sets idempotent across repeated imports without affecting sibling
// sets. Secret material (provider api_key values, raw caller tokens) never
// crosses the database boundary; only environment references and hashes are
// persisted.

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"
)

// ProjectConfigHook is the package-level binding consumed by the document
// import path. Keeping it as a single exported var lets the document import
// reach the bounded write-side projection without hard-wiring internals.
var ProjectConfigHook = ProjectConfigToRelational

// ProjectConfigToRelational writes a replace-style projection of cfg into
// the router_config_* tables owned by configSetID. Children for that set are
// deleted before the new rows are inserted; sibling sets are untouched. The
// owning router_config_sets row is managed by the caller and is never
// created, updated, or deleted here.
//
// The projection is intentionally narrow:
//   - Provider secrets: only api_key_env / key_id / auth_scheme are stored;
//     the raw api_key value is never written.
//   - Provider headers: only the allowlisted non-secret metadata headers
//     (HTTP-Referer, User-Agent, X-Title) are persisted; other headers are
//     dropped to honor the database-level allowlist check.
//   - Caller tokens: only token_sha256 and token_id are stored; raw caller
//     tokens are never represented.
//   - Users / projects / memberships: written only when the corresponding
//     tables already exist. The current control-plane schema does not create
//     them, so the projection skips that work rather than failing.
func ProjectConfigToRelational(tx *gorm.DB, configSetID string, cfg *Config) error {
	if tx == nil {
		return fmt.Errorf("project config: database handle is required")
	}
	configSetID = strings.TrimSpace(configSetID)
	if configSetID == "" {
		return fmt.Errorf("project config: config_set_id is required")
	}
	if cfg == nil {
		return fmt.Errorf("project config: validated config is required")
	}

	if err := deleteControlPlaneSetChildren(tx, configSetID); err != nil {
		return err
	}
	if err := projectControlPlaneServer(tx, configSetID, cfg); err != nil {
		return err
	}
	if err := projectControlPlaneProviders(tx, configSetID, cfg); err != nil {
		return err
	}
	if err := projectControlPlaneModelGroups(tx, configSetID, cfg); err != nil {
		return err
	}
	if err := projectControlPlaneCallers(tx, configSetID, cfg); err != nil {
		return err
	}
	projectControlPlaneUsersProjectsMemberships(tx, configSetID, cfg)
	return nil
}

// controlPlaneProjectionChildTables lists every projected child table in
// reverse foreign-key order so a replace pass can delete leaves first. The
// owning router_config_sets row is deliberately excluded.
var controlPlaneProjectionChildTables = []string{
	"router_config_provider_model_bridges",
	"router_config_provider_model_request_unsupported_features",
	"router_config_provider_model_request_inbound_dialects",
	"router_config_provider_model_modalities",
	"router_config_provider_model_tool_support",
	"router_config_provider_model_capabilities",
	"router_config_caller_allowed_groups",
	"router_config_model_group_targets",
	"router_config_model_groups",
	"router_config_provider_models",
	"router_config_provider_headers",
	"router_config_providers",
	"router_config_callers",
	"router_config_server",
}

// deleteControlPlaneSetChildren removes every projected child row owned by
// configSetID. Tables that do not exist yet (older schema versions) are
// skipped so the projection stays usable across phased migrations.
func deleteControlPlaneSetChildren(tx *gorm.DB, configSetID string) error {
	for _, table := range controlPlaneProjectionChildTables {
		if !tx.Migrator().HasTable(table) {
			continue
		}
		// Table names come from the checked-in allowlist above, never from
		// caller input, so the quoted identifier is safe to interpolate.
		stmt := fmt.Sprintf(`DELETE FROM %s WHERE config_set_id = ?`, table)
		if err := tx.Exec(stmt, configSetID).Error; err != nil {
			return fmt.Errorf("clear control-plane children for %s: %w", table, err)
		}
	}
	return nil
}

// projectControlPlaneServer writes the single scalar server row for the set.
// License columns are deployment-owned and stay at their SQL defaults.
func projectControlPlaneServer(tx *gorm.DB, configSetID string, cfg *Config) error {
	row := map[string]any{
		"config_set_id":       configSetID,
		"listen":              cfg.Server.Listen,
		"default_model_group": cfg.Server.DefaultModelGroup,
		"state_path":          cfg.StatePath,
	}
	if err := tx.Table("router_config_server").Create(row).Error; err != nil {
		return fmt.Errorf("insert server row: %w", err)
	}
	return nil
}

// projectControlPlaneProviders writes provider rows, their allowlisted
// headers, model rows, capability rows, and capability child rows. Provider
// names and model refs are sorted for deterministic output.
func projectControlPlaneProviders(tx *gorm.DB, configSetID string, cfg *Config) error {
	providerNames := make([]string, 0, len(cfg.Provider))
	for name := range cfg.Provider {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)
	for _, name := range providerNames {
		provider := cfg.Provider[name]
		if err := projectControlPlaneProvider(tx, configSetID, name, provider); err != nil {
			return err
		}
		if err := projectControlPlaneProviderHeaders(tx, configSetID, name, provider); err != nil {
			return err
		}
		if err := projectControlPlaneProviderModels(tx, configSetID, name, provider); err != nil {
			return err
		}
	}
	return nil
}

// projectControlPlaneProvider never writes the raw API key. Only the
// environment-variable reference, the non-secret key identifier, and the
// authentication scheme cross the relational boundary.
func projectControlPlaneProvider(tx *gorm.DB, configSetID, name string, provider ProviderConfig) error {
	row := map[string]any{
		"config_set_id": configSetID,
		"provider_name": name,
		"base_url":      provider.BaseURL,
		"dialect":       provider.Dialect,
		"api_key_env":   provider.APIKeyEnv,
		"key_id":        provider.KeyID,
		"auth_scheme":   provider.AuthScheme,
	}
	if err := tx.Table("router_config_providers").Create(row).Error; err != nil {
		return fmt.Errorf("insert provider %q: %w", name, err)
	}
	return nil
}

// projectControlPlaneProviderHeaders persists only the allowlisted
// non-secret headers. Anything outside the allowlist is intentionally
// dropped here: the database-level CHECK constraint would otherwise reject
// the row, and the read path fails closed on those values, so dropping
// keeps the projection and load contracts symmetric.
func projectControlPlaneProviderHeaders(tx *gorm.DB, configSetID, name string, provider ProviderConfig) error {
	headerNames := make([]string, 0, len(provider.Headers))
	for headerName := range provider.Headers {
		headerNames = append(headerNames, headerName)
	}
	sort.Strings(headerNames)
	for _, headerName := range headerNames {
		if !controlPlaneAllowedProviderHeader(headerName) {
			continue
		}
		row := map[string]any{
			"config_set_id": configSetID,
			"provider_name": name,
			"header_name":   headerName,
			"header_value":  provider.Headers[headerName],
		}
		if err := tx.Table("router_config_provider_headers").Create(row).Error; err != nil {
			return fmt.Errorf("insert provider %q header %q: %w", name, headerName, err)
		}
	}
	return nil
}

// projectControlPlaneProviderModels writes each catalog model plus its
// scalar capability row and the bounded child rows that carry repeated
// capability metadata.
func projectControlPlaneProviderModels(tx *gorm.DB, configSetID, name string, provider ProviderConfig) error {
	modelRefs := make([]string, 0, len(provider.Models))
	for modelRef := range provider.Models {
		modelRefs = append(modelRefs, modelRef)
	}
	sort.Strings(modelRefs)
	for _, modelRef := range modelRefs {
		model := provider.Models[modelRef]
		if err := projectControlPlaneProviderModel(tx, configSetID, name, modelRef, model); err != nil {
			return err
		}
		if err := projectControlPlaneProviderModelCapabilities(tx, configSetID, name, modelRef, model); err != nil {
			return err
		}
		if err := projectControlPlaneProviderModelToolSupport(tx, configSetID, name, modelRef, model.ToolSupport); err != nil {
			return err
		}
		if err := projectControlPlaneProviderModelModalities(tx, configSetID, name, modelRef, model); err != nil {
			return err
		}
		if err := projectControlPlaneProviderModelRequestShape(tx, configSetID, name, modelRef, model.RequestShapeSupport); err != nil {
			return err
		}
		if err := projectControlPlaneProviderModelBridges(tx, configSetID, name, modelRef, model.Bridges); err != nil {
			return err
		}
	}
	return nil
}

func projectControlPlaneProviderModel(tx *gorm.DB, configSetID, name, modelRef string, model ProviderModel) error {
	row := map[string]any{
		"config_set_id":                      configSetID,
		"provider_name":                      name,
		"model_ref":                          modelRef,
		"model":                              model.Model,
		"dialect":                            model.Dialect,
		"display_name":                       model.DisplayName,
		"context_tokens":                     model.ContextTokens,
		"input_price_per_million_usd":        model.InputPricePerMillionUSD,
		"output_price_per_million_usd":       model.OutputPricePerMillionUSD,
		"cached_input_price_per_million_usd": model.CachedInputPricePerMillionUSD,
		"pricing_source":                     model.PricingSource,
		"pricing_updated_at":                 model.PricingUpdatedAt,
		"pricing_notes":                      model.PricingNotes,
	}
	if err := tx.Table("router_config_provider_models").Create(row).Error; err != nil {
		return fmt.Errorf("insert provider %q model %q: %w", name, modelRef, err)
	}
	return nil
}

// projectControlPlaneProviderModelCapabilities writes the scalar capability
// record for one provider model. Every catalog model gets exactly one row so
// the read path can fail closed when a capability record is missing.
func projectControlPlaneProviderModelCapabilities(tx *gorm.DB, configSetID, name, modelRef string, model ProviderModel) error {
	row := map[string]any{
		"config_set_id": configSetID,
		"provider_name": name,
		"model_ref":     modelRef,
		"image_input_price_per_million_tokens_usd": model.ImageInputPricePerMillionTokensUSD,
		"image_input_price_per_image_usd":          model.ImageInputPricePerImageUSD,
		"rpm":                                      model.RPM,
		"tier":                                     model.Tier,
		"cost":                                     model.Cost,
		"reasoning_supported":                      model.Reasoning.Supported,
		"reasoning_mode":                           model.Reasoning.Mode,
		"reasoning_control":                        model.Reasoning.Control,
		"reasoning_default_on":                     model.Reasoning.DefaultOn,
		"reasoning_min_budget_tokens":              model.Reasoning.MinBudgetTokens,
		"reasoning_max_budget_tokens":              model.Reasoning.MaxBudgetTokens,
		"reasoning_budget_must_be_less_than_max_tokens": model.Reasoning.BudgetMustBeLessThanMaxTokens,
		"reasoning_stream_block":                        model.Reasoning.StreamBlock,
		"reasoning_rejects_max_tokens":                  model.Reasoning.RejectsMaxTokens,
		"reasoning_rejects_temperature":                 model.Reasoning.RejectsTemperature,
		"reasoning_rejects_top_p":                       model.Reasoning.RejectsTopP,
		"reasoning_supports_summaries":                  model.Reasoning.SupportsSummaries,
		"honors_max_tokens":                             controlPlaneSQLNullBool(model.HonorsMaxTokens),
		"force_store_false":                             model.ForceStoreFalse,
		"output_token_field":                            model.OutputTokenField,
		"max_request_bytes":                             model.RequestShapeSupport.MaxRequestBytes,
		"max_estimated_input_tokens":                    model.RequestShapeSupport.MaxEstimatedInputTokens,
		"min_requested_output_tokens":                   model.RequestShapeSupport.MinRequestedOutputTokens,
		"max_requested_output_tokens":                   model.RequestShapeSupport.MaxRequestedOutputTokens,
		"max_tool_schema_bytes":                         model.RequestShapeSupport.MaxToolSchemaBytes,
		"supports_large_coding_agent_payloads":          controlPlaneSQLNullBool(model.RequestShapeSupport.SupportsLargeCodingAgentPayloads),
		"request_shape_validation_status":               model.RequestShapeSupport.ValidationStatus,
		"request_shape_validation_notes":                model.RequestShapeSupport.ValidationNotes,
		"responses_to_chat_enabled":                     model.ResponsesToChat.Enabled,
		"responses_to_chat_text":                        model.ResponsesToChat.Text,
		"responses_to_chat_function_tools":              model.ResponsesToChat.FunctionTools,
		"responses_to_chat_tool_choice":                 model.ResponsesToChat.ToolChoice,
		"responses_to_chat_structured_outputs":          model.ResponsesToChat.StructuredOutputs,
		"responses_to_chat_reasoning":                   model.ResponsesToChat.Reasoning,
		"responses_to_chat_images":                      model.ResponsesToChat.Images,
		"responses_to_chat_streaming":                   model.ResponsesToChat.Streaming,
		"responses_to_chat_validation_status":           model.ResponsesToChat.ValidationStatus,
		"responses_to_chat_validation_notes":            model.ResponsesToChat.ValidationNotes,
	}
	if err := tx.Table("router_config_provider_model_capabilities").Create(row).Error; err != nil {
		return fmt.Errorf("insert provider %q model %q capabilities: %w", name, modelRef, err)
	}
	return nil
}

// controlPlaneSQLNullBool converts an optional *bool into a driver-friendly
// sql.NullBool so NULL preserves the router default on the read path.
func controlPlaneSQLNullBool(value *bool) sql.NullBool {
	if value == nil {
		return sql.NullBool{}
	}
	return sql.NullBool{Bool: *value, Valid: true}
}

func projectControlPlaneProviderModelToolSupport(tx *gorm.DB, configSetID, name, modelRef string, support ToolSupport) error {
	surfaces := []struct {
		apiSurface   string
		capabilities []string
	}{
		{"openai_chat", support.OpenAIChat},
		{"openai_responses", support.OpenAIResponses},
		{"anthropic_messages", support.AnthropicMessages},
		{"provider_hosted", support.ProviderHosted},
	}
	for _, surface := range surfaces {
		capabilities := append([]string(nil), surface.capabilities...)
		sort.Strings(capabilities)
		for _, capability := range capabilities {
			row := map[string]any{
				"config_set_id": configSetID,
				"provider_name": name,
				"model_ref":     modelRef,
				"api_surface":   surface.apiSurface,
				"capability":    capability,
			}
			if err := tx.Table("router_config_provider_model_tool_support").Create(row).Error; err != nil {
				return fmt.Errorf("insert provider %q model %q tool support %q/%q: %w", name, modelRef, surface.apiSurface, capability, err)
			}
		}
	}
	return nil
}

func projectControlPlaneProviderModelModalities(tx *gorm.DB, configSetID, name, modelRef string, model ProviderModel) error {
	directions := []struct {
		direction  string
		modalities []string
	}{
		{"input", model.InputModalities},
		{"output", model.OutputModalities},
	}
	for _, direction := range directions {
		modalities := append([]string(nil), direction.modalities...)
		sort.Strings(modalities)
		for _, modality := range modalities {
			row := map[string]any{
				"config_set_id": configSetID,
				"provider_name": name,
				"model_ref":     modelRef,
				"direction":     direction.direction,
				"modality":      modality,
			}
			if err := tx.Table("router_config_provider_model_modalities").Create(row).Error; err != nil {
				return fmt.Errorf("insert provider %q model %q modality %q/%q: %w", name, modelRef, direction.direction, modality, err)
			}
		}
	}
	return nil
}

// projectControlPlaneProviderModelRequestShape writes the repeated
// request-shape child rows (supported inbound dialects and intentionally
// unsupported request features).
func projectControlPlaneProviderModelRequestShape(tx *gorm.DB, configSetID, name, modelRef string, shape RequestShapeSupport) error {
	dialects := append([]string(nil), shape.SupportedInboundDialects...)
	sort.Strings(dialects)
	for _, dialect := range dialects {
		row := map[string]any{
			"config_set_id":   configSetID,
			"provider_name":   name,
			"model_ref":       modelRef,
			"inbound_dialect": dialect,
		}
		if err := tx.Table("router_config_provider_model_request_inbound_dialects").Create(row).Error; err != nil {
			return fmt.Errorf("insert provider %q model %q inbound dialect %q: %w", name, modelRef, dialect, err)
		}
	}
	features := append([]string(nil), shape.UnsupportedRequestFeatures...)
	sort.Strings(features)
	for _, feature := range features {
		row := map[string]any{
			"config_set_id": configSetID,
			"provider_name": name,
			"model_ref":     modelRef,
			"feature":       feature,
		}
		if err := tx.Table("router_config_provider_model_request_unsupported_features").Create(row).Error; err != nil {
			return fmt.Errorf("insert provider %q model %q unsupported feature %q: %w", name, modelRef, feature, err)
		}
	}
	return nil
}

// projectControlPlaneProviderModelBridges writes one row per enabled bridge
// direction. Directions whose bridge is entirely unset are skipped so the
// read path sees an empty bridge set rather than a disabled placeholder.
func projectControlPlaneProviderModelBridges(tx *gorm.DB, configSetID, name, modelRef string, bridges BridgeSupport) error {
	directions := []struct {
		direction string
		bridge    DialectBridgeSupport
	}{
		{"chat_to_responses", bridges.ChatToResponses},
		{"responses_to_chat", bridges.ResponsesToChat},
	}
	for _, entry := range directions {
		if !controlPlaneBridgeConfigured(entry.bridge) {
			continue
		}
		row := map[string]any{
			"config_set_id":                           configSetID,
			"provider_name":                           name,
			"model_ref":                               modelRef,
			"direction":                               entry.direction,
			"enabled":                                 entry.bridge.Enabled,
			"text_enabled":                            controlPlaneSQLNullBool(entry.bridge.Text),
			"tools":                                   entry.bridge.Tools,
			"tool_choice":                             entry.bridge.ToolChoice,
			"parallel_tool_calls":                     entry.bridge.ParallelToolCalls,
			"structured_outputs":                      entry.bridge.StructuredOutputs,
			"images":                                  entry.bridge.Images,
			"reasoning":                               entry.bridge.Reasoning,
			"streaming":                               entry.bridge.Streaming,
			"stateful_sessions_enabled":               entry.bridge.StatefulSessions.Enabled,
			"stateful_sessions_backend":               entry.bridge.StatefulSessions.Backend,
			"stateful_sessions_session_header":        entry.bridge.StatefulSessions.SessionHeader,
			"stateful_sessions_ttl_seconds":           entry.bridge.StatefulSessions.TTLSeconds,
			"stateful_sessions_max_entries":           entry.bridge.StatefulSessions.MaxEntries,
			"stateful_sessions_redis_address":         entry.bridge.StatefulSessions.Redis.Address,
			"stateful_sessions_redis_namespace":       entry.bridge.StatefulSessions.Redis.Namespace,
			"stateful_sessions_redis_db":              entry.bridge.StatefulSessions.Redis.DB,
			"stateful_sessions_redis_username_env":    entry.bridge.StatefulSessions.Redis.UsernameEnv,
			"stateful_sessions_redis_password_env":    entry.bridge.StatefulSessions.Redis.PasswordEnv,
			"stateful_sessions_redis_tls_enabled":     entry.bridge.StatefulSessions.Redis.TLS.Enabled,
			"stateful_sessions_redis_tls_server_name": entry.bridge.StatefulSessions.Redis.TLS.ServerName,
			"stateful_sessions_redis_tls_insecure_skip_verify": entry.bridge.StatefulSessions.Redis.TLS.InsecureSkipVerify,
			"stateful_sessions_redis_connect_timeout_ms":       entry.bridge.StatefulSessions.Redis.ConnectTimeoutMS,
			"stateful_sessions_redis_read_timeout_ms":          entry.bridge.StatefulSessions.Redis.ReadTimeoutMS,
			"stateful_sessions_redis_write_timeout_ms":         entry.bridge.StatefulSessions.Redis.WriteTimeoutMS,
			"stateful_sessions_redis_pool_size":                entry.bridge.StatefulSessions.Redis.PoolSize,
		}
		if err := tx.Table("router_config_provider_model_bridges").Create(row).Error; err != nil {
			return fmt.Errorf("insert provider %q model %q bridge %q: %w", name, modelRef, entry.direction, err)
		}
	}
	return nil
}

// controlPlaneBridgeConfigured reports whether a bridge direction carries any
// explicit configuration worth persisting. Redis credentials themselves are
// never stored; only environment-variable references are.
func controlPlaneBridgeConfigured(bridge DialectBridgeSupport) bool {
	if bridge.Enabled || bridge.Text != nil || bridge.Tools || bridge.ToolChoice ||
		bridge.ParallelToolCalls || bridge.StructuredOutputs || bridge.Images ||
		bridge.Reasoning || bridge.Streaming {
		return true
	}
	sessions := bridge.StatefulSessions
	if sessions.Enabled || sessions.Backend != "" || sessions.SessionHeader != "" ||
		sessions.TTLSeconds != 0 || sessions.MaxEntries != 0 {
		return true
	}
	redis := sessions.Redis
	return redis.Address != "" || redis.Namespace != "" || redis.DB != 0 ||
		redis.UsernameEnv != "" || redis.PasswordEnv != "" || redis.TLS.Enabled ||
		redis.TLS.ServerName != "" || redis.TLS.InsecureSkipVerify ||
		redis.ConnectTimeoutMS != 0 || redis.ReadTimeoutMS != 0 ||
		redis.WriteTimeoutMS != 0 || redis.PoolSize != 0
}

// projectControlPlaneModelGroups writes model-group rows plus their ordered
// targets. Target sequence is one-based to match the existing read-path
// ordering convention.
func projectControlPlaneModelGroups(tx *gorm.DB, configSetID string, cfg *Config) error {
	groupNames := make([]string, 0, len(cfg.Models))
	for name := range cfg.Models {
		groupNames = append(groupNames, name)
	}
	sort.Strings(groupNames)
	for _, name := range groupNames {
		group := cfg.Models[name]
		row := map[string]any{
			"config_set_id":      configSetID,
			"group_name":         name,
			"strategy":           group.Strategy,
			"attempt_timeout_ms": group.AttemptTimeoutMS,
		}
		if err := tx.Table("router_config_model_groups").Create(row).Error; err != nil {
			return fmt.Errorf("insert model group %q: %w", name, err)
		}
		for index, target := range group.Targets {
			if err := projectControlPlaneModelGroupTarget(tx, configSetID, name, index+1, target); err != nil {
				return err
			}
		}
	}
	return nil
}

// projectControlPlaneModelGroupTarget writes one ordered target row. An
// empty model_ref is stored as NULL so the optional provider-model foreign
// key is not engaged for inline targets.
func projectControlPlaneModelGroupTarget(tx *gorm.DB, configSetID, groupName string, sequence int, target Target) error {
	var modelRef any
	if target.ModelRef != "" {
		modelRef = target.ModelRef
	}
	row := map[string]any{
		"config_set_id": configSetID,
		"group_name":    groupName,
		"sequence":      sequence,
		"provider_name": target.Provider,
		"model_ref":     modelRef,
		"model":         target.Model,
		"dialect":       target.Dialect,
		"weight":        target.Weight,
		"rpm":           target.RPM,
		"tier":          target.Tier,
		"cost":          target.Cost,
	}
	if err := tx.Table("router_config_model_group_targets").Create(row).Error; err != nil {
		return fmt.Errorf("insert model group %q target %d: %w", groupName, sequence, err)
	}
	return nil
}

// projectControlPlaneCallers writes caller identity rows plus their allowed
// model groups. Only token_sha256 and token_id are persisted; raw caller
// tokens are never represented in the relational projection.
func projectControlPlaneCallers(tx *gorm.DB, configSetID string, cfg *Config) error {
	callers := append([]CallerConfig(nil), cfg.Callers...)
	sort.Slice(callers, func(i, j int) bool { return callers[i].ID < callers[j].ID })
	for _, caller := range callers {
		row := map[string]any{
			"config_set_id": configSetID,
			"caller_id":     caller.ID,
			"owner_user":    caller.OwnerUser,
			"project":       caller.Project,
			"environment":   caller.Environment,
			"status":        caller.Status,
			"token_sha256":  caller.TokenSHA256,
			"token_id":      caller.TokenID,
			"metrics_admin": caller.MetricsAdmin,
			"content_admin": caller.ContentAdmin,
			"rpm":           caller.Rate.RPM,
			"tpm":           caller.Rate.TPM,
			"concurrent":    caller.Rate.Concurrent,
		}
		if err := tx.Table("router_config_callers").Create(row).Error; err != nil {
			return fmt.Errorf("insert caller %q: %w", caller.ID, err)
		}
		allowed := append([]string(nil), caller.Allow...)
		sort.Strings(allowed)
		for _, groupName := range allowed {
			allowRow := map[string]any{
				"config_set_id": configSetID,
				"caller_id":     caller.ID,
				"group_name":    groupName,
			}
			if err := tx.Table("router_config_caller_allowed_groups").Create(allowRow).Error; err != nil {
				return fmt.Errorf("insert caller %q allowed group %q: %w", caller.ID, groupName, err)
			}
		}
	}
	return nil
}

// projectControlPlaneUsersProjectsMemberships writes users, projects, and
// project memberships only when the corresponding tables already exist. The
// current control-plane schema does not create them, so this is a no-op
// today; the conditional keeps the projection forward-compatible with a
// later phase that introduces those tables without changing this writer.
func projectControlPlaneUsersProjectsMemberships(tx *gorm.DB, configSetID string, cfg *Config) {
	if tx.Migrator().HasTable("router_config_users") {
		users := append([]UserConfig(nil), cfg.Users...)
		sort.Slice(users, func(i, j int) bool { return users[i].ID < users[j].ID })
		for _, user := range users {
			row := map[string]any{
				"config_set_id": configSetID,
				"user_id":       user.ID,
				"name":          user.Name,
				"email":         user.Email,
				"type":          user.Type,
				"status":        user.Status,
				"description":   user.Description,
			}
			// Best-effort: the table shape is owned by a future migration, so
			// insert errors are intentionally tolerated here.
			_ = tx.Table("router_config_users").Create(row).Error
		}
	}
	if tx.Migrator().HasTable("router_config_projects") {
		projects := append([]ProjectConfig(nil), cfg.Projects...)
		sort.Slice(projects, func(i, j int) bool { return projects[i].ID < projects[j].ID })
		for _, project := range projects {
			row := map[string]any{
				"config_set_id": configSetID,
				"project_id":    project.ID,
				"name":          project.Name,
				"status":        project.Status,
				"description":   project.Description,
			}
			_ = tx.Table("router_config_projects").Create(row).Error
		}
	}
	if tx.Migrator().HasTable("router_config_project_memberships") {
		memberships := append([]ProjectMembershipConfig(nil), cfg.ProjectMemberships...)
		sort.Slice(memberships, func(i, j int) bool {
			if memberships[i].UserID != memberships[j].UserID {
				return memberships[i].UserID < memberships[j].UserID
			}
			return memberships[i].Project < memberships[j].Project
		})
		for _, membership := range memberships {
			row := map[string]any{
				"config_set_id": configSetID,
				"user_id":       membership.UserID,
				"project":       membership.Project,
				"role":          membership.Role,
				"status":        membership.Status,
				"source":        membership.Source,
				"joined_at":     membership.JoinedAt,
			}
			_ = tx.Table("router_config_project_memberships").Create(row).Error
		}
	}
}

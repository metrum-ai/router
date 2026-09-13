// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Phase one of the configuration control plane deliberately provides a small,
// read-only relational projection. YAML remains the serving default. The
// projection is useful for validating the migration contract and loading a
// simple active router configuration without introducing a write API, secret
// storage, PostgREST, or runtime hot reload.

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"gorm.io/gorm"
)

const configControlPlaneScope = "router_config"

const configControlPlanePhase1MigrationID = 2026072001

const configControlPlanePhase2MigrationID = 2026072002

const configControlPlanePhase3MigrationID = 2026072003

const configControlPlanePhase4MigrationID = 2026072004

const configControlPlanePhase5MigrationID = 2026072005

const configControlPlaneProviderHeaderNameConstraint = "router_config_provider_headers_non_secret_name_ck"

const configControlPlaneProviderHeaderNameCheck = "header_name = TRIM(header_name) AND LOWER(header_name) IN ('http-referer', 'user-agent', 'x-title')"

const configControlPlaneProviderHeaderNameCIIndex = "router_config_provider_headers_name_ci"

const configControlPlaneOneActiveSetPerScopeIndex = "router_config_one_active_set_per_scope"

var configControlPlaneCompatibility = MigrationCompatibility{MinSchema: 0, MaxSchema: 5, MinData: 0, MaxData: 0}

var configControlPlaneMigrationDefinitions = []MigrationDefinition{
	{
		ID:               configControlPlanePhase1MigrationID,
		Scope:            configControlPlaneScope,
		Name:             "create relational read-only configuration projection",
		Release:          "2026.7",
		Checksum:         "a2c3e1ea4db3422e260a17b0b03ae67a8f7f07e0282ec2f39d0879b64f9d0f9a",
		SchemaVersion:    1,
		Transactional:    true,
		MaintenanceMode:  "online",
		RollbackClass:    "restore-required",
		HandlerKey:       "router-config.phase1.apply.v1@applyConfigControlPlanePhase1",
		PostconditionKey: "router-config.phase1.schema.v1@verifyConfigControlPlanePhase1",
		ExecutionMode:    "transactional",
		LockClass:        "online",
		TimeoutClass:     "bounded",
		Apply:            applyConfigControlPlanePhase1,
		Verify:           verifyConfigControlPlanePhase1,
	},
	{
		ID:               configControlPlanePhase2MigrationID,
		Scope:            configControlPlaneScope,
		Name:             "enforce non-secret provider header names at database boundary",
		Release:          "2026.7",
		Checksum:         "714e7b191083cbb9ca0490990e394dfe54a0acc4a9da54760e4f85e0ba5f9baf",
		SchemaVersion:    2,
		Transactional:    true,
		MaintenanceMode:  "maintenance",
		RollbackClass:    "restore-required",
		HandlerKey:       "router-config.phase2.apply.v1@applyConfigControlPlanePhase2",
		PostconditionKey: "router-config.phase2.schema.v1@verifyConfigControlPlanePhase2",
		Dependencies:     []int{configControlPlanePhase1MigrationID},
		ExecutionMode:    "transactional",
		LockClass:        "maintenance",
		TimeoutClass:     "maintenance",
		Apply:            applyConfigControlPlanePhase2,
		Verify:           verifyConfigControlPlanePhase2,
	},
	{
		ID:               configControlPlanePhase3MigrationID,
		Scope:            configControlPlaneScope,
		Name:             "enforce case-insensitive provider header uniqueness",
		Release:          "2026.7",
		Checksum:         "c2b317747db0dd6b43fee33e113ef4574d5304ffc24085b37c3ac7cf95f86959",
		SchemaVersion:    3,
		Transactional:    true,
		MaintenanceMode:  "maintenance",
		RollbackClass:    "restore-required",
		HandlerKey:       "router-config.phase3.apply.v1@applyConfigControlPlanePhase3",
		PostconditionKey: "router-config.phase3.schema.v1@verifyConfigControlPlanePhase3",
		Dependencies:     []int{configControlPlanePhase2MigrationID},
		ExecutionMode:    "transactional",
		LockClass:        "maintenance",
		TimeoutClass:     "maintenance",
		Apply:            applyConfigControlPlanePhase3,
		Verify:           verifyConfigControlPlanePhase3,
	},
	{
		ID:               configControlPlanePhase4MigrationID,
		Scope:            configControlPlaneScope,
		Name:             "project provider-model capability metadata relationally",
		Release:          "2026.7",
		Checksum:         "a22b2ec3ee8bdee8c28ce58fec61b1e1f91d3745791ef442b1fb6f9ad50f2e46",
		SchemaVersion:    4,
		Transactional:    true,
		MaintenanceMode:  "maintenance",
		RollbackClass:    "restore-required",
		HandlerKey:       "router-config.phase4.apply.v1@applyConfigControlPlanePhase4",
		PostconditionKey: "router-config.phase4.schema.v1@verifyConfigControlPlanePhase4",
		Dependencies:     []int{configControlPlanePhase3MigrationID},
		ExecutionMode:    "transactional",
		LockClass:        "maintenance",
		TimeoutClass:     "maintenance",
		Apply:            applyConfigControlPlanePhase4,
		Verify:           verifyConfigControlPlanePhase4,
	},
	{
		ID:               configControlPlanePhase5MigrationID,
		Scope:            configControlPlaneScope,
		Name:             "add nullable cached-input catalog price",
		Release:          "2026.9",
		Checksum:         "f3281972e06a6fc8f215286c88b61b6f0ad82863aa0fa599082d1268eaa50161",
		SchemaVersion:    5,
		Transactional:    true,
		MaintenanceMode:  "maintenance",
		RollbackClass:    "restore-required",
		HandlerKey:       "router-config.phase5.apply.v1@applyConfigControlPlanePhase5",
		PostconditionKey: "router-config.phase5.schema.v1@verifyConfigControlPlanePhase5",
		Dependencies:     []int{configControlPlanePhase4MigrationID},
		ExecutionMode:    "transactional",
		LockClass:        "maintenance",
		TimeoutClass:     "maintenance",
		Apply:            applyConfigControlPlanePhase5,
		Verify:           verifyConfigControlPlanePhase5,
	},
}

func init() {
	for i := range configControlPlaneMigrationDefinitions {
		configControlPlaneMigrationDefinitions[i] = FinalizeMigrationDefinition(configControlPlaneMigrationDefinitions[i])
	}
}

// ConfigControlPlaneMigrationRunner opens a dedicated config-control-plane
// database scope. It never initializes or changes the usage schema.
func ConfigControlPlaneMigrationRunner(cfg UsageDBConfig) (*migrationRunner, func() error, error) {
	db, err := openUsageDB(cfg)
	if err != nil {
		return nil, nil, err
	}
	r, err := NewMigrationRunner(db, configControlPlaneScope, configControlPlaneCompatibility, configControlPlaneMigrationDefinitions)
	if err != nil {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
		return nil, nil, err
	}
	return r, func() error {
		sqlDB, err := db.DB()
		if err != nil {
			return err
		}
		return sqlDB.Close()
	}, nil
}

// LoadActiveConfigFromDB loads the one validated active set for runtimeScope.
// It is intentionally read-only and fails closed for unsupported relational
// fields rather than silently dropping configuration. Caller token hashes are
// retained for verification; raw caller tokens are never represented here.
func LoadActiveConfigFromDB(db *gorm.DB, runtimeScope string) (*Config, error) {
	if db == nil {
		return nil, errors.New("config control-plane database is required")
	}
	runtimeScope = strings.TrimSpace(runtimeScope)
	if runtimeScope == "" {
		return nil, errors.New("config runtime scope is required")
	}
	var sets []configSetRow
	if err := db.Where("runtime_scope = ? AND status = ? AND validation_status = ?", runtimeScope, "active", "valid").Limit(2).Find(&sets).Error; err != nil {
		return nil, fmt.Errorf("load active config set: %w", err)
	}
	if len(sets) == 0 {
		return nil, fmt.Errorf("no validated active config set for runtime scope %q", runtimeScope)
	}
	if len(sets) != 1 {
		return nil, fmt.Errorf("multiple validated active config sets for runtime scope %q", runtimeScope)
	}
	set := sets[0]
	var server serverConfigRow
	if err := db.Where("config_set_id = ?", set.ID).First(&server).Error; err != nil {
		return nil, fmt.Errorf("load server config: %w", err)
	}
	cfg := &Config{Server: ServerConfig{
		Listen:            server.Listen,
		DefaultModelGroup: server.DefaultModelGroup,
		License: LicenseConfig{
			Enabled:    server.LicenseEnabled,
			Path:       server.LicensePath,
			StatePath:  server.LicenseStatePath,
			enabledSet: true,
		},
	}, StatePath: server.StatePath, Provider: map[string]ProviderConfig{}, Models: map[string]ModelGroup{}}
	var providers []providerRow
	if err := db.Where("config_set_id = ?", set.ID).Order("provider_name ASC").Find(&providers).Error; err != nil {
		return nil, err
	}
	for _, row := range providers {
		apiKey, err := resolveControlPlaneProviderAPIKey(row.ProviderName, row.APIKeyEnv)
		if err != nil {
			return nil, err
		}
		p := ProviderConfig{BaseURL: row.BaseURL, Dialect: row.Dialect, APIKey: apiKey, APIKeyEnv: row.APIKeyEnv, KeyID: row.KeyID, AuthScheme: row.AuthScheme, Headers: map[string]string{}, Models: map[string]ProviderModel{}}
		var headers []providerHeaderRow
		if err := db.Where("config_set_id = ? AND provider_name = ?", set.ID, row.ProviderName).Order("header_name ASC").Find(&headers).Error; err != nil {
			return nil, err
		}
		canonicalHeaderNames := make(map[string]string, len(headers))
		for _, h := range headers {
			if !controlPlaneAllowedProviderHeader(h.HeaderName) {
				return nil, fmt.Errorf("provider %q uses unsupported header %q; provider headers are limited to approved non-secret metadata headers and credentials must use api_key_env or a deployment secret reference", row.ProviderName, h.HeaderName)
			}
			canonicalHeaderName := strings.ToLower(h.HeaderName)
			if existing, exists := canonicalHeaderNames[canonicalHeaderName]; exists {
				return nil, fmt.Errorf("provider %q has case-insensitive duplicate header names %q and %q", row.ProviderName, existing, h.HeaderName)
			}
			canonicalHeaderNames[canonicalHeaderName] = h.HeaderName
			p.Headers[h.HeaderName] = h.HeaderValue
		}
		var models []providerModelRow
		if err := db.Where("config_set_id = ? AND provider_name = ?", set.ID, row.ProviderName).Order("model_ref ASC").Find(&models).Error; err != nil {
			return nil, err
		}
		for _, model := range models {
			capabilities, err := loadProviderModelCapabilities(db, set.ID, row.ProviderName, model.ModelRef)
			if err != nil {
				return nil, err
			}
			p.Models[model.ModelRef] = ProviderModel{
				Model:                              model.Model,
				Dialect:                            model.Dialect,
				DisplayName:                        model.DisplayName,
				ContextTokens:                      model.ContextTokens,
				InputPricePerMillionUSD:            model.InputPricePerMillionUSD,
				OutputPricePerMillionUSD:           model.OutputPricePerMillionUSD,
				CachedInputPricePerMillionUSD:      model.CachedInputPricePerMillionUSD,
				ImageInputPricePerMillionTokensUSD: capabilities.ImageInputPricePerMillionTokensUSD,
				ImageInputPricePerImageUSD:         capabilities.ImageInputPricePerImageUSD,
				PricingSource:                      model.PricingSource,
				PricingUpdatedAt:                   model.PricingUpdatedAt,
				PricingNotes:                       model.PricingNotes,
				ToolSupport:                        capabilities.ToolSupport,
				Reasoning:                          capabilities.Reasoning,
				InputModalities:                    capabilities.InputModalities,
				OutputModalities:                   capabilities.OutputModalities,
				HonorsMaxTokens:                    capabilities.HonorsMaxTokens,
				ForceStoreFalse:                    capabilities.ForceStoreFalse,
				OutputTokenField:                   capabilities.OutputTokenField,
				RequestShapeSupport:                capabilities.RequestShapeSupport,
				ResponsesToChat:                    capabilities.ResponsesToChat,
				Bridges:                            capabilities.Bridges,
				RPM:                                capabilities.RPM,
				Tier:                               capabilities.Tier,
				Cost:                               capabilities.Cost,
			}
		}
		cfg.Provider[row.ProviderName] = p
	}
	var groups []modelGroupRow
	if err := db.Where("config_set_id = ?", set.ID).Order("group_name ASC").Find(&groups).Error; err != nil {
		return nil, err
	}
	for _, row := range groups {
		group := ModelGroup{Strategy: row.Strategy, AttemptTimeoutMS: row.AttemptTimeoutMS}
		var targets []modelGroupTargetRow
		if err := db.Where("config_set_id = ? AND group_name = ?", set.ID, row.GroupName).Order("sequence ASC").Find(&targets).Error; err != nil {
			return nil, err
		}
		for _, target := range targets {
			modelRef := ""
			if target.ModelRef.Valid {
				modelRef = target.ModelRef.String
			}
			group.Targets = append(group.Targets, Target{Provider: target.ProviderName, ModelRef: modelRef, Model: target.Model, Dialect: target.Dialect, Weight: target.Weight, RPM: target.RPM, Tier: target.Tier, Cost: target.Cost})
		}
		cfg.Models[row.GroupName] = group
	}
	var callers []callerRow
	if err := db.Where("config_set_id = ?", set.ID).Order("caller_id ASC").Find(&callers).Error; err != nil {
		return nil, err
	}
	for _, row := range callers {
		caller := CallerConfig{ID: row.CallerID, OwnerUser: row.OwnerUser, Project: row.Project, Environment: row.Environment, Status: row.Status, TokenSHA256: row.TokenSHA256, TokenID: row.TokenID, MetricsAdmin: row.MetricsAdmin, ContentAdmin: row.ContentAdmin, Rate: RateConfig{RPM: row.RPM, TPM: row.TPM, Concurrent: row.Concurrent}}
		var allowed []callerAllowedGroupRow
		if err := db.Where("config_set_id = ? AND caller_id = ?", set.ID, row.CallerID).Order("group_name ASC").Find(&allowed).Error; err != nil {
			return nil, err
		}
		for _, allow := range allowed {
			caller.Allow = append(caller.Allow, allow.GroupName)
		}
		cfg.Callers = append(cfg.Callers, caller)
	}
	cfg.setDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate active config set %q: %w", set.ID, err)
	}
	return cfg, nil
}

// resolveControlPlaneProviderAPIKey resolves only the deployment-local
// environment reference. The relational projection never stores or logs the
// credential value, and an unset value remains valid for catalog-only rows.
func resolveControlPlaneProviderAPIKey(providerName, envName string) (string, error) {
	if envName == "" {
		return "", nil
	}
	if envName != strings.TrimSpace(envName) || !validEnvName(envName) {
		return "", fmt.Errorf("provider %q has invalid api_key_env", providerName)
	}
	apiKey, _ := os.LookupEnv(envName)
	return apiKey, nil
}

func controlPlaneAllowedProviderHeader(name string) bool {
	if name != strings.TrimSpace(name) {
		return false
	}
	switch strings.ToLower(name) {
	case "http-referer", "user-agent", "x-title":
		return true
	default:
		return false
	}
}

// loadProviderModelCapabilities projects the relational capability records
// into the existing ProviderModel shape. Every catalog model must have exactly
// one scalar capability row; missing state fails closed rather than silently
// weakening a reviewed eligibility contract.
func loadProviderModelCapabilities(db *gorm.DB, configSetID, providerName, modelRef string) (ProviderModel, error) {
	var capabilityRows []providerModelCapabilityRow
	if err := db.Where("config_set_id = ? AND provider_name = ? AND model_ref = ?", configSetID, providerName, modelRef).Limit(2).Find(&capabilityRows).Error; err != nil {
		return ProviderModel{}, fmt.Errorf("load provider %q model %q capabilities: %w", providerName, modelRef, err)
	}
	if len(capabilityRows) == 0 {
		return ProviderModel{}, fmt.Errorf("provider %q model %q has no capability record", providerName, modelRef)
	}
	if len(capabilityRows) != 1 {
		return ProviderModel{}, fmt.Errorf("provider %q model %q has multiple capability records", providerName, modelRef)
	}
	row := capabilityRows[0]
	model := ProviderModel{
		ImageInputPricePerMillionTokensUSD: row.ImageInputPricePerMillionTokensUSD,
		ImageInputPricePerImageUSD:         row.ImageInputPricePerImageUSD,
		RPM:                                row.RPM,
		Tier:                               row.Tier,
		Cost:                               row.Cost,
		Reasoning: ReasoningSupport{
			Supported:                     row.ReasoningSupported,
			Mode:                          row.ReasoningMode,
			Control:                       row.ReasoningControl,
			DefaultOn:                     row.ReasoningDefaultOn,
			MinBudgetTokens:               row.ReasoningMinBudgetTokens,
			MaxBudgetTokens:               row.ReasoningMaxBudgetTokens,
			BudgetMustBeLessThanMaxTokens: row.ReasoningBudgetMustBeLessThanMaxTokens,
			StreamBlock:                   row.ReasoningStreamBlock,
			RejectsMaxTokens:              row.ReasoningRejectsMaxTokens,
			RejectsTemperature:            row.ReasoningRejectsTemperature,
			RejectsTopP:                   row.ReasoningRejectsTopP,
			SupportsSummaries:             row.ReasoningSupportsSummaries,
		},
		HonorsMaxTokens:  controlPlaneOptionalBool(row.HonorsMaxTokens),
		ForceStoreFalse:  row.ForceStoreFalse,
		OutputTokenField: row.OutputTokenField,
		RequestShapeSupport: RequestShapeSupport{
			MaxRequestBytes:                  row.MaxRequestBytes,
			MaxEstimatedInputTokens:          row.MaxEstimatedInputTokens,
			MinRequestedOutputTokens:         row.MinRequestedOutputTokens,
			MaxRequestedOutputTokens:         row.MaxRequestedOutputTokens,
			MaxToolSchemaBytes:               row.MaxToolSchemaBytes,
			SupportsLargeCodingAgentPayloads: controlPlaneOptionalBool(row.SupportsLargeCodingAgentPayloads),
			ValidationStatus:                 row.RequestShapeValidationStatus,
			ValidationNotes:                  row.RequestShapeValidationNotes,
		},
		ResponsesToChat: ResponsesToChatBridge{
			Enabled:           row.ResponsesToChatEnabled,
			Text:              row.ResponsesToChatText,
			FunctionTools:     row.ResponsesToChatFunctionTools,
			ToolChoice:        row.ResponsesToChatToolChoice,
			StructuredOutputs: row.ResponsesToChatStructuredOutputs,
			Reasoning:         row.ResponsesToChatReasoning,
			Images:            row.ResponsesToChatImages,
			Streaming:         row.ResponsesToChatStreaming,
			ValidationStatus:  row.ResponsesToChatValidationStatus,
			ValidationNotes:   row.ResponsesToChatValidationNotes,
		},
	}

	var toolRows []providerModelToolSupportRow
	if err := db.Where("config_set_id = ? AND provider_name = ? AND model_ref = ?", configSetID, providerName, modelRef).Order("api_surface ASC, capability ASC").Find(&toolRows).Error; err != nil {
		return ProviderModel{}, fmt.Errorf("load provider %q model %q tool support: %w", providerName, modelRef, err)
	}
	for _, tool := range toolRows {
		switch tool.APISurface {
		case "openai_chat":
			model.ToolSupport.OpenAIChat = append(model.ToolSupport.OpenAIChat, tool.Capability)
		case "openai_responses":
			model.ToolSupport.OpenAIResponses = append(model.ToolSupport.OpenAIResponses, tool.Capability)
		case "anthropic_messages":
			model.ToolSupport.AnthropicMessages = append(model.ToolSupport.AnthropicMessages, tool.Capability)
		case "provider_hosted":
			model.ToolSupport.ProviderHosted = append(model.ToolSupport.ProviderHosted, tool.Capability)
		default:
			return ProviderModel{}, fmt.Errorf("provider %q model %q has unsupported tool API surface %q", providerName, modelRef, tool.APISurface)
		}
	}

	var modalityRows []providerModelModalityRow
	if err := db.Where("config_set_id = ? AND provider_name = ? AND model_ref = ?", configSetID, providerName, modelRef).Order("direction ASC, modality ASC").Find(&modalityRows).Error; err != nil {
		return ProviderModel{}, fmt.Errorf("load provider %q model %q modalities: %w", providerName, modelRef, err)
	}
	for _, modality := range modalityRows {
		switch modality.Direction {
		case "input":
			model.InputModalities = append(model.InputModalities, modality.Modality)
		case "output":
			model.OutputModalities = append(model.OutputModalities, modality.Modality)
		default:
			return ProviderModel{}, fmt.Errorf("provider %q model %q has unsupported modality direction %q", providerName, modelRef, modality.Direction)
		}
	}

	var inboundDialectRows []providerModelRequestInboundDialectRow
	if err := db.Where("config_set_id = ? AND provider_name = ? AND model_ref = ?", configSetID, providerName, modelRef).Order("inbound_dialect ASC").Find(&inboundDialectRows).Error; err != nil {
		return ProviderModel{}, fmt.Errorf("load provider %q model %q inbound dialects: %w", providerName, modelRef, err)
	}
	seenInboundDialects := map[string]bool{}
	for _, dialect := range inboundDialectRows {
		if seenInboundDialects[dialect.InboundDialect] {
			return ProviderModel{}, fmt.Errorf("provider %q model %q has duplicate inbound dialect %q", providerName, modelRef, dialect.InboundDialect)
		}
		seenInboundDialects[dialect.InboundDialect] = true
		model.RequestShapeSupport.SupportedInboundDialects = append(model.RequestShapeSupport.SupportedInboundDialects, dialect.InboundDialect)
	}

	var unsupportedFeatureRows []providerModelRequestUnsupportedFeatureRow
	if err := db.Where("config_set_id = ? AND provider_name = ? AND model_ref = ?", configSetID, providerName, modelRef).Order("feature ASC").Find(&unsupportedFeatureRows).Error; err != nil {
		return ProviderModel{}, fmt.Errorf("load provider %q model %q unsupported request features: %w", providerName, modelRef, err)
	}
	seenUnsupportedFeatures := map[string]bool{}
	for _, feature := range unsupportedFeatureRows {
		if seenUnsupportedFeatures[feature.Feature] {
			return ProviderModel{}, fmt.Errorf("provider %q model %q has duplicate unsupported request feature %q", providerName, modelRef, feature.Feature)
		}
		seenUnsupportedFeatures[feature.Feature] = true
		model.RequestShapeSupport.UnsupportedRequestFeatures = append(model.RequestShapeSupport.UnsupportedRequestFeatures, feature.Feature)
	}

	var bridgeRows []providerModelBridgeRow
	if err := db.Where("config_set_id = ? AND provider_name = ? AND model_ref = ?", configSetID, providerName, modelRef).Order("direction ASC").Find(&bridgeRows).Error; err != nil {
		return ProviderModel{}, fmt.Errorf("load provider %q model %q bridges: %w", providerName, modelRef, err)
	}
	seenBridgeDirections := map[string]bool{}
	for _, bridgeRow := range bridgeRows {
		if seenBridgeDirections[bridgeRow.Direction] {
			return ProviderModel{}, fmt.Errorf("provider %q model %q has duplicate bridge direction %q", providerName, modelRef, bridgeRow.Direction)
		}
		seenBridgeDirections[bridgeRow.Direction] = true
		bridge := bridgeRow.dialectBridgeSupport()
		switch bridgeRow.Direction {
		case "chat_to_responses":
			model.Bridges.ChatToResponses = bridge
		case "responses_to_chat":
			model.Bridges.ResponsesToChat = bridge
		default:
			return ProviderModel{}, fmt.Errorf("provider %q model %q has unsupported bridge direction %q", providerName, modelRef, bridgeRow.Direction)
		}
	}
	return model, nil
}

func controlPlaneOptionalBool(value sql.NullBool) *bool {
	if !value.Valid {
		return nil
	}
	resolved := value.Bool
	return &resolved
}

func applyConfigControlPlanePhase1(tx *gorm.DB) error {
	for _, stmt := range configControlPlaneDDL {
		if err := tx.Exec(stmt).Error; err != nil {
			return fmt.Errorf("config control-plane bootstrap: %w", err)
		}
	}
	if tx.Dialector.Name() == "postgres" {
		for _, stmt := range configControlPlanePostgresComments {
			if err := tx.Exec(stmt).Error; err != nil {
				return fmt.Errorf("config control-plane comments: %w", err)
			}
		}
	}
	return nil
}

func verifyConfigControlPlanePhase1(tx *gorm.DB) error {
	for _, table := range configControlPlaneTables {
		if !tx.Migrator().HasTable(table) {
			return fmt.Errorf("required control-plane table %s is missing", table)
		}
	}
	for table, columns := range configControlPlaneRequiredColumns {
		for _, column := range columns {
			if !tx.Migrator().HasColumn(table, column) {
				return fmt.Errorf("required control-plane column %s.%s is missing", table, column)
			}
		}
	}
	for table, indexes := range configControlPlaneRequiredIndexes {
		for _, index := range indexes {
			if !tx.Migrator().HasIndex(table, index) {
				return fmt.Errorf("required control-plane index %s.%s is missing", table, index)
			}
		}
	}
	if err := verifyConfigControlPlaneOneActiveSetPerScopeIndex(tx); err != nil {
		return err
	}
	for table, constraints := range configControlPlaneRequiredConstraints {
		for _, constraint := range constraints {
			if !tx.Migrator().HasConstraint(table, constraint) {
				return fmt.Errorf("required control-plane foreign key %s.%s is missing", table, constraint)
			}
		}
	}
	for _, foreignKey := range configControlPlaneRequiredForeignKeys {
		if err := verifyConfigControlPlaneForeignKey(tx, foreignKey); err != nil {
			return err
		}
	}
	return nil
}

func verifyConfigControlPlaneOneActiveSetPerScopeIndex(tx *gorm.DB) error {
	switch tx.Dialector.Name() {
	case "sqlite":
		return verifyConfigControlPlaneOneActiveSetPerScopeIndexSQLite(tx)
	case "postgres":
		return verifyConfigControlPlaneOneActiveSetPerScopeIndexPostgres(tx)
	default:
		return fmt.Errorf("unsupported control-plane database driver %q", tx.Dialector.Name())
	}
}

func verifyConfigControlPlaneOneActiveSetPerScopeIndexSQLite(tx *gorm.DB) error {
	var unique, partial int
	if err := tx.Raw(`SELECT "unique", partial FROM pragma_index_list('router_config_sets') WHERE name = ?`, configControlPlaneOneActiveSetPerScopeIndex).Row().Scan(&unique, &partial); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("required control-plane index router_config_sets.%s is missing", configControlPlaneOneActiveSetPerScopeIndex)
		}
		return fmt.Errorf("inspect control-plane index router_config_sets.%s: %w", configControlPlaneOneActiveSetPerScopeIndex, err)
	}
	if unique != 1 {
		return fmt.Errorf("required control-plane index router_config_sets.%s must be unique", configControlPlaneOneActiveSetPerScopeIndex)
	}
	if partial != 1 {
		return fmt.Errorf("required control-plane index router_config_sets.%s must be partial for active sets", configControlPlaneOneActiveSetPerScopeIndex)
	}
	var definition string
	if err := tx.Raw(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, configControlPlaneOneActiveSetPerScopeIndex).Row().Scan(&definition); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("required control-plane index router_config_sets.%s is missing", configControlPlaneOneActiveSetPerScopeIndex)
		}
		return fmt.Errorf("inspect control-plane index definition router_config_sets.%s: %w", configControlPlaneOneActiveSetPerScopeIndex, err)
	}
	const expected = "createuniqueindexrouter_config_one_active_set_per_scopeonrouter_config_sets(runtime_scope)wherestatus='active'"
	if normalizeConfigControlPlaneIndexDefinition(definition) != expected {
		return fmt.Errorf("required control-plane index router_config_sets.%s has unexpected SQLite key or predicate", configControlPlaneOneActiveSetPerScopeIndex)
	}
	return nil
}

func verifyConfigControlPlaneOneActiveSetPerScopeIndexPostgres(tx *gorm.DB) error {
	const query = `SELECT index_info.indisunique,
		index_info.indisvalid,
		index_info.indisready,
		index_info.indislive,
		index_info.indnkeyatts,
		index_info.indnatts,
		pg_get_expr(index_info.indpred, index_info.indrelid, false),
		pg_get_indexdef(index_info.indexrelid, 1, false)
	FROM pg_index AS index_info
	JOIN pg_class AS index_class ON index_class.oid = index_info.indexrelid
	JOIN pg_namespace AS index_namespace ON index_namespace.oid = index_class.relnamespace
	JOIN pg_class AS table_class ON table_class.oid = index_info.indrelid
	JOIN pg_namespace AS table_namespace ON table_namespace.oid = table_class.relnamespace
	WHERE table_namespace.nspname = current_schema()
		AND index_namespace.nspname = current_schema()
		AND table_class.relname = 'router_config_sets'
		AND index_class.relname = ?`
	metadata := configControlPlanePostgresOneActiveSetIndexMetadata{}
	var predicate, key sql.NullString
	if err := tx.Raw(query, configControlPlaneOneActiveSetPerScopeIndex).Row().Scan(&metadata.Unique, &metadata.Valid, &metadata.Ready, &metadata.Live, &metadata.KeyCount, &metadata.AttributeCount, &predicate, &key); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("required control-plane index router_config_sets.%s is missing", configControlPlaneOneActiveSetPerScopeIndex)
		}
		return fmt.Errorf("inspect control-plane index router_config_sets.%s: %w", configControlPlaneOneActiveSetPerScopeIndex, err)
	}
	metadata.Predicate = predicate.String
	metadata.Keys = []string{key.String}
	return verifyConfigControlPlaneOneActiveSetPerScopeIndexPostgresMetadata(metadata)
}

type configControlPlanePostgresOneActiveSetIndexMetadata struct {
	Unique         bool
	Valid          bool
	Ready          bool
	Live           bool
	KeyCount       int
	AttributeCount int
	Predicate      string
	Keys           []string
}

func verifyConfigControlPlaneOneActiveSetPerScopeIndexPostgresMetadata(metadata configControlPlanePostgresOneActiveSetIndexMetadata) error {
	if !metadata.Unique {
		return fmt.Errorf("required control-plane index router_config_sets.%s must be unique", configControlPlaneOneActiveSetPerScopeIndex)
	}
	if !metadata.Valid || !metadata.Ready || !metadata.Live {
		return fmt.Errorf("required control-plane index router_config_sets.%s must be valid, ready, and live", configControlPlaneOneActiveSetPerScopeIndex)
	}
	expected := []string{"runtime_scope"}
	if metadata.KeyCount != len(expected) || metadata.AttributeCount != len(expected) || len(metadata.Keys) != len(expected) {
		return fmt.Errorf("required control-plane index router_config_sets.%s must have exactly %d key", configControlPlaneOneActiveSetPerScopeIndex, len(expected))
	}
	for index, key := range metadata.Keys {
		if normalizeConfigControlPlaneIndexDefinition(key) != expected[index] {
			return fmt.Errorf("required control-plane index router_config_sets.%s has unexpected key %d", configControlPlaneOneActiveSetPerScopeIndex, index+1)
		}
	}
	if normalizeConfigControlPlaneOneActiveSetPredicate(metadata.Predicate) != "status='active'" {
		return fmt.Errorf("required control-plane index router_config_sets.%s must use the exact predicate status = 'active'", configControlPlaneOneActiveSetPerScopeIndex)
	}
	return nil
}

func normalizeConfigControlPlaneOneActiveSetPredicate(value string) string {
	value = normalizeConfigControlPlaneIndexDefinition(value)
	value = strings.ReplaceAll(value, "::text", "")
	value = strings.ReplaceAll(value, "(", "")
	return strings.ReplaceAll(value, ")", "")
}

// applyConfigControlPlanePhase2 puts the provider-header allowlist in the
// database instead of relying solely on a read-path validation. That means a
// direct SQL import or any future write API cannot durably store a credential
// header that the router might later forward upstream.
func applyConfigControlPlanePhase2(tx *gorm.DB) error {
	switch tx.Dialector.Name() {
	case "sqlite":
		return applyConfigControlPlaneProviderHeaderConstraintSQLite(tx)
	case "postgres":
		stmt := fmt.Sprintf(`ALTER TABLE router_config_provider_headers ADD CONSTRAINT %s CHECK (%s)`, configControlPlaneProviderHeaderNameConstraint, configControlPlaneProviderHeaderNameCheck)
		if err := tx.Exec(stmt).Error; err != nil {
			return fmt.Errorf("add provider-header allowlist constraint: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported control-plane database driver %q", tx.Dialector.Name())
	}
}

func applyConfigControlPlaneProviderHeaderConstraintSQLite(tx *gorm.DB) error {
	constraint := fmt.Sprintf("CONSTRAINT %s CHECK (%s)", configControlPlaneProviderHeaderNameConstraint, configControlPlaneProviderHeaderNameCheck)
	for _, stmt := range []string{
		`CREATE TABLE router_config_provider_headers_rebuild (config_set_id TEXT NOT NULL, provider_name TEXT NOT NULL, header_name TEXT NOT NULL, header_value TEXT NOT NULL, ` + constraint + `, PRIMARY KEY (config_set_id, provider_name, header_name), FOREIGN KEY (config_set_id, provider_name) REFERENCES router_config_providers(config_set_id, provider_name))`,
		`INSERT INTO router_config_provider_headers_rebuild (config_set_id, provider_name, header_name, header_value) SELECT config_set_id, provider_name, header_name, header_value FROM router_config_provider_headers`,
		`DROP TABLE router_config_provider_headers`,
		`ALTER TABLE router_config_provider_headers_rebuild RENAME TO router_config_provider_headers`,
	} {
		if err := tx.Exec(stmt).Error; err != nil {
			return fmt.Errorf("rebuild provider-header table with non-secret allowlist: %w", err)
		}
	}
	return nil
}

func verifyConfigControlPlanePhase2(tx *gorm.DB) error {
	if err := verifyConfigControlPlanePhase1(tx); err != nil {
		return err
	}
	if !tx.Migrator().HasConstraint("router_config_provider_headers", configControlPlaneProviderHeaderNameConstraint) {
		return fmt.Errorf("required control-plane constraint router_config_provider_headers.%s is missing", configControlPlaneProviderHeaderNameConstraint)
	}
	return nil
}

// applyConfigControlPlanePhase3 prevents case variants such as X-Title and
// x-title from becoming two map keys that later collapse to one HTTP header.
// The expression is supported by both PostgreSQL and SQLite. Existing
// ambiguous rows make this migration fail and require an explicit reviewed
// cleanup rather than silently selecting one metadata value.
func applyConfigControlPlanePhase3(tx *gorm.DB) error {
	stmt := fmt.Sprintf(`CREATE UNIQUE INDEX %s ON router_config_provider_headers(config_set_id, provider_name, LOWER(header_name))`, configControlPlaneProviderHeaderNameCIIndex)
	if err := tx.Exec(stmt).Error; err != nil {
		return fmt.Errorf("add case-insensitive provider-header uniqueness: %w", err)
	}
	return nil
}

func verifyConfigControlPlanePhase3(tx *gorm.DB) error {
	if err := verifyConfigControlPlanePhase2(tx); err != nil {
		return err
	}
	switch tx.Dialector.Name() {
	case "sqlite":
		return verifyConfigControlPlaneProviderHeaderCIIndexSQLite(tx)
	case "postgres":
		return verifyConfigControlPlaneProviderHeaderCIIndexPostgres(tx)
	default:
		return fmt.Errorf("unsupported control-plane database driver %q", tx.Dialector.Name())
	}
}

// applyConfigControlPlanePhase4 preserves catalog capability metadata in
// scalar and child rows. Capability values are inherited only by targets that
// name the catalog model through model_ref; target-specific overrides remain
// outside this bounded read-only projection.
func applyConfigControlPlanePhase4(tx *gorm.DB) error {
	for _, stmt := range configControlPlanePhase4DDL {
		if err := tx.Exec(stmt).Error; err != nil {
			return fmt.Errorf("create provider-model capability projection: %w", err)
		}
	}
	if err := backfillConfigControlPlaneProviderModelCapabilities(tx); err != nil {
		return err
	}
	if tx.Dialector.Name() == "postgres" {
		for _, stmt := range configControlPlanePhase4PostgresComments {
			if err := tx.Exec(stmt).Error; err != nil {
				return fmt.Errorf("comment provider-model capability projection: %w", err)
			}
		}
	}
	return nil
}

// backfillConfigControlPlaneProviderModelCapabilities gives existing phase-3
// catalog rows an explicit all-default capability record. The record preserves
// pre-phase-4 behavior while making future loader reads fail closed when a
// newly inserted provider model omits its required capability identity.
func backfillConfigControlPlaneProviderModelCapabilities(tx *gorm.DB) error {
	var stmt string
	switch tx.Dialector.Name() {
	case "sqlite":
		stmt = `INSERT OR IGNORE INTO router_config_provider_model_capabilities (config_set_id, provider_name, model_ref) SELECT config_set_id, provider_name, model_ref FROM router_config_provider_models`
	case "postgres":
		stmt = `INSERT INTO router_config_provider_model_capabilities (config_set_id, provider_name, model_ref) SELECT config_set_id, provider_name, model_ref FROM router_config_provider_models ON CONFLICT (config_set_id, provider_name, model_ref) DO NOTHING`
	default:
		return fmt.Errorf("unsupported control-plane database driver %q", tx.Dialector.Name())
	}
	if err := tx.Exec(stmt).Error; err != nil {
		return fmt.Errorf("backfill provider-model capability records: %w", err)
	}
	return nil
}

func verifyConfigControlPlanePhase4(tx *gorm.DB) error {
	if err := verifyConfigControlPlanePhase3(tx); err != nil {
		return err
	}
	for _, table := range configControlPlanePhase4Tables {
		if !tx.Migrator().HasTable(table) {
			return fmt.Errorf("required control-plane capability table %s is missing", table)
		}
	}
	for table, columns := range configControlPlanePhase4RequiredColumns {
		for _, column := range columns {
			if !tx.Migrator().HasColumn(table, column) {
				return fmt.Errorf("required control-plane capability column %s.%s is missing", table, column)
			}
		}
	}
	for _, foreignKey := range configControlPlanePhase4RequiredForeignKeys {
		if err := verifyConfigControlPlaneForeignKey(tx, foreignKey); err != nil {
			return err
		}
	}
	return nil
}

func applyConfigControlPlanePhase5(tx *gorm.DB) error {
	if !tx.Migrator().HasColumn(&providerModelRow{}, "CachedInputPricePerMillionUSD") {
		if err := tx.Migrator().AddColumn(&providerModelRow{}, "CachedInputPricePerMillionUSD"); err != nil {
			return fmt.Errorf("add cached-input catalog price column: %w", err)
		}
	}
	if tx.Dialector.Name() == "postgres" {
		if err := tx.Exec(`COMMENT ON COLUMN router_config_provider_models.cached_input_price_per_million_usd IS 'Optional cached-input price per million tokens. NULL means unknown.'`).Error; err != nil {
			return fmt.Errorf("comment cached-input catalog price column: %w", err)
		}
	}
	return verifyConfigControlPlanePhase5(tx)
}

func verifyConfigControlPlanePhase5(tx *gorm.DB) error {
	if err := verifyConfigControlPlanePhase4(tx); err != nil {
		return err
	}
	if !tx.Migrator().HasColumn(&providerModelRow{}, "CachedInputPricePerMillionUSD") {
		return fmt.Errorf("required control-plane column router_config_provider_models.cached_input_price_per_million_usd is missing")
	}
	return nil
}

func verifyConfigControlPlaneProviderHeaderCIIndexSQLite(tx *gorm.DB) error {
	var unique int
	if err := tx.Raw(`SELECT "unique" FROM pragma_index_list('router_config_provider_headers') WHERE name = ?`, configControlPlaneProviderHeaderNameCIIndex).Row().Scan(&unique); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("required control-plane index router_config_provider_headers.%s is missing", configControlPlaneProviderHeaderNameCIIndex)
		}
		return fmt.Errorf("inspect control-plane index router_config_provider_headers.%s: %w", configControlPlaneProviderHeaderNameCIIndex, err)
	}
	if unique != 1 {
		return fmt.Errorf("required control-plane index router_config_provider_headers.%s must be unique", configControlPlaneProviderHeaderNameCIIndex)
	}
	var definition string
	if err := tx.Raw(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, configControlPlaneProviderHeaderNameCIIndex).Row().Scan(&definition); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("required control-plane index router_config_provider_headers.%s is missing", configControlPlaneProviderHeaderNameCIIndex)
		}
		return fmt.Errorf("inspect control-plane index definition router_config_provider_headers.%s: %w", configControlPlaneProviderHeaderNameCIIndex, err)
	}
	const expected = "createuniqueindexrouter_config_provider_headers_name_cionrouter_config_provider_headers(config_set_id,provider_name,lower(header_name))"
	if normalizeConfigControlPlaneIndexDefinition(definition) != expected {
		return fmt.Errorf("required control-plane index router_config_provider_headers.%s has unexpected SQLite keys or expression", configControlPlaneProviderHeaderNameCIIndex)
	}
	return nil
}

func verifyConfigControlPlaneProviderHeaderCIIndexPostgres(tx *gorm.DB) error {
	const query = `SELECT index_info.indisunique,
		index_info.indisvalid,
		index_info.indisready,
		index_info.indislive,
		index_info.indnkeyatts,
		index_info.indnatts,
		index_info.indpred IS NULL,
		pg_get_indexdef(index_info.indexrelid, 1, false),
		pg_get_indexdef(index_info.indexrelid, 2, false),
		pg_get_indexdef(index_info.indexrelid, 3, false)
	FROM pg_index AS index_info
	JOIN pg_class AS index_class ON index_class.oid = index_info.indexrelid
	JOIN pg_namespace AS index_namespace ON index_namespace.oid = index_class.relnamespace
	JOIN pg_class AS table_class ON table_class.oid = index_info.indrelid
	JOIN pg_namespace AS table_namespace ON table_namespace.oid = table_class.relnamespace
	WHERE table_namespace.nspname = current_schema()
		AND index_namespace.nspname = current_schema()
		AND table_class.relname = 'router_config_provider_headers'
		AND index_class.relname = ?`
	metadata := configControlPlanePostgresIndexMetadata{}
	var keyOne, keyTwo, keyThree sql.NullString
	if err := tx.Raw(query, configControlPlaneProviderHeaderNameCIIndex).Row().Scan(&metadata.Unique, &metadata.Valid, &metadata.Ready, &metadata.Live, &metadata.KeyCount, &metadata.AttributeCount, &metadata.NoPredicate, &keyOne, &keyTwo, &keyThree); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("required control-plane index router_config_provider_headers.%s is missing", configControlPlaneProviderHeaderNameCIIndex)
		}
		return fmt.Errorf("inspect control-plane index router_config_provider_headers.%s: %w", configControlPlaneProviderHeaderNameCIIndex, err)
	}
	metadata.Keys = []string{keyOne.String, keyTwo.String, keyThree.String}
	return verifyConfigControlPlaneProviderHeaderCIIndexPostgresMetadata(metadata)
}

type configControlPlanePostgresIndexMetadata struct {
	Unique         bool
	Valid          bool
	Ready          bool
	Live           bool
	KeyCount       int
	AttributeCount int
	NoPredicate    bool
	Keys           []string
}

func verifyConfigControlPlaneProviderHeaderCIIndexPostgresMetadata(metadata configControlPlanePostgresIndexMetadata) error {
	if !metadata.Unique {
		return fmt.Errorf("required control-plane index router_config_provider_headers.%s must be unique", configControlPlaneProviderHeaderNameCIIndex)
	}
	if !metadata.Valid || !metadata.Ready || !metadata.Live {
		return fmt.Errorf("required control-plane index router_config_provider_headers.%s must be valid, ready, and live", configControlPlaneProviderHeaderNameCIIndex)
	}
	if !metadata.NoPredicate {
		return fmt.Errorf("required control-plane index router_config_provider_headers.%s must not be partial", configControlPlaneProviderHeaderNameCIIndex)
	}
	expected := []string{"config_set_id", "provider_name", "lower(header_name)"}
	if metadata.KeyCount != len(expected) || metadata.AttributeCount != len(expected) || len(metadata.Keys) != len(expected) {
		return fmt.Errorf("required control-plane index router_config_provider_headers.%s must have exactly %d keys", configControlPlaneProviderHeaderNameCIIndex, len(expected))
	}
	for index, key := range metadata.Keys {
		if normalizeConfigControlPlaneIndexDefinition(key) != expected[index] {
			return fmt.Errorf("required control-plane index router_config_provider_headers.%s has unexpected key %d", configControlPlaneProviderHeaderNameCIIndex, index+1)
		}
	}
	return nil
}

func normalizeConfigControlPlaneIndexDefinition(value string) string {
	value = strings.ReplaceAll(strings.ToLower(value), `"`, "")
	return strings.Join(strings.Fields(value), "")
}

var configControlPlaneTables = []string{"router_config_sets", "router_config_server", "router_config_providers", "router_config_provider_headers", "router_config_provider_models", "router_config_model_groups", "router_config_model_group_targets", "router_config_callers", "router_config_caller_allowed_groups"}

var configControlPlanePhase4Tables = []string{
	"router_config_provider_model_capabilities",
	"router_config_provider_model_tool_support",
	"router_config_provider_model_modalities",
	"router_config_provider_model_request_inbound_dialects",
	"router_config_provider_model_request_unsupported_features",
	"router_config_provider_model_bridges",
}

var configControlPlaneRequiredColumns = map[string][]string{
	"router_config_sets":                  {"id", "runtime_scope", "name", "status", "validation_status", "created_by", "created_at", "activated_at"},
	"router_config_server":                {"config_set_id", "listen", "default_model_group", "state_path", "license_enabled", "license_path", "license_state_path"},
	"router_config_providers":             {"config_set_id", "provider_name", "base_url", "dialect", "api_key_env", "key_id", "auth_scheme"},
	"router_config_provider_headers":      {"config_set_id", "provider_name", "header_name", "header_value"},
	"router_config_provider_models":       {"config_set_id", "provider_name", "model_ref", "model", "dialect", "display_name", "context_tokens", "input_price_per_million_usd", "output_price_per_million_usd", "pricing_source", "pricing_updated_at", "pricing_notes"},
	"router_config_model_groups":          {"config_set_id", "group_name", "strategy", "attempt_timeout_ms"},
	"router_config_model_group_targets":   {"config_set_id", "group_name", "sequence", "provider_name", "model_ref", "model", "dialect", "weight", "rpm", "tier", "cost"},
	"router_config_callers":               {"config_set_id", "caller_id", "owner_user", "project", "environment", "status", "token_sha256", "token_id", "metrics_admin", "content_admin", "rpm", "tpm", "concurrent"},
	"router_config_caller_allowed_groups": {"config_set_id", "caller_id", "group_name"},
}

var configControlPlanePhase4RequiredColumns = map[string][]string{
	"router_config_provider_model_capabilities": {
		"config_set_id", "provider_name", "model_ref",
		"image_input_price_per_million_tokens_usd", "image_input_price_per_image_usd", "rpm", "tier", "cost",
		"reasoning_supported", "reasoning_mode", "reasoning_control", "reasoning_default_on", "reasoning_min_budget_tokens", "reasoning_max_budget_tokens", "reasoning_budget_must_be_less_than_max_tokens", "reasoning_stream_block", "reasoning_rejects_max_tokens", "reasoning_rejects_temperature", "reasoning_rejects_top_p", "reasoning_supports_summaries",
		"honors_max_tokens", "force_store_false", "output_token_field",
		"max_request_bytes", "max_estimated_input_tokens", "min_requested_output_tokens", "max_requested_output_tokens", "max_tool_schema_bytes", "supports_large_coding_agent_payloads", "request_shape_validation_status", "request_shape_validation_notes",
		"responses_to_chat_enabled", "responses_to_chat_text", "responses_to_chat_function_tools", "responses_to_chat_tool_choice", "responses_to_chat_structured_outputs", "responses_to_chat_reasoning", "responses_to_chat_images", "responses_to_chat_streaming", "responses_to_chat_validation_status", "responses_to_chat_validation_notes",
	},
	"router_config_provider_model_tool_support":                 {"config_set_id", "provider_name", "model_ref", "api_surface", "capability"},
	"router_config_provider_model_modalities":                   {"config_set_id", "provider_name", "model_ref", "direction", "modality"},
	"router_config_provider_model_request_inbound_dialects":     {"config_set_id", "provider_name", "model_ref", "inbound_dialect"},
	"router_config_provider_model_request_unsupported_features": {"config_set_id", "provider_name", "model_ref", "feature"},
	"router_config_provider_model_bridges": {
		"config_set_id", "provider_name", "model_ref", "direction", "enabled", "text_enabled", "tools", "tool_choice", "parallel_tool_calls", "structured_outputs", "images", "reasoning", "streaming",
		"stateful_sessions_enabled", "stateful_sessions_backend", "stateful_sessions_session_header", "stateful_sessions_ttl_seconds", "stateful_sessions_max_entries",
		"stateful_sessions_redis_address", "stateful_sessions_redis_namespace", "stateful_sessions_redis_db", "stateful_sessions_redis_username_env", "stateful_sessions_redis_password_env", "stateful_sessions_redis_tls_enabled", "stateful_sessions_redis_tls_server_name", "stateful_sessions_redis_tls_insecure_skip_verify", "stateful_sessions_redis_connect_timeout_ms", "stateful_sessions_redis_read_timeout_ms", "stateful_sessions_redis_write_timeout_ms", "stateful_sessions_redis_pool_size",
	},
}

var configControlPlaneRequiredIndexes = map[string][]string{
	"router_config_sets":                {configControlPlaneOneActiveSetPerScopeIndex},
	"router_config_model_group_targets": {"router_config_targets_provider_model"},
}

var configControlPlaneRequiredConstraints = map[string][]string{
	"router_config_model_group_targets": {"router_config_targets_group_fk", "router_config_targets_provider_fk", "router_config_targets_provider_model_fk"},
}

type configControlPlaneForeignKey struct {
	Table             string
	Columns           []string
	ReferencedTable   string
	ReferencedColumns []string
}

type configControlPlaneForeignKeyRow struct {
	ConstraintID     string `gorm:"column:constraint_id"`
	Sequence         int    `gorm:"column:sequence"`
	ReferencedTable  string `gorm:"column:referenced_table"`
	LocalColumn      string `gorm:"column:local_column"`
	ReferencedColumn string `gorm:"column:referenced_column"`
}

var configControlPlaneRequiredForeignKeys = []configControlPlaneForeignKey{
	{Table: "router_config_server", Columns: []string{"config_set_id"}, ReferencedTable: "router_config_sets", ReferencedColumns: []string{"id"}},
	{Table: "router_config_providers", Columns: []string{"config_set_id"}, ReferencedTable: "router_config_sets", ReferencedColumns: []string{"id"}},
	{Table: "router_config_provider_headers", Columns: []string{"config_set_id", "provider_name"}, ReferencedTable: "router_config_providers", ReferencedColumns: []string{"config_set_id", "provider_name"}},
	{Table: "router_config_provider_models", Columns: []string{"config_set_id", "provider_name"}, ReferencedTable: "router_config_providers", ReferencedColumns: []string{"config_set_id", "provider_name"}},
	{Table: "router_config_model_groups", Columns: []string{"config_set_id"}, ReferencedTable: "router_config_sets", ReferencedColumns: []string{"id"}},
	{Table: "router_config_model_group_targets", Columns: []string{"config_set_id", "group_name"}, ReferencedTable: "router_config_model_groups", ReferencedColumns: []string{"config_set_id", "group_name"}},
	{Table: "router_config_model_group_targets", Columns: []string{"config_set_id", "provider_name"}, ReferencedTable: "router_config_providers", ReferencedColumns: []string{"config_set_id", "provider_name"}},
	{Table: "router_config_model_group_targets", Columns: []string{"config_set_id", "provider_name", "model_ref"}, ReferencedTable: "router_config_provider_models", ReferencedColumns: []string{"config_set_id", "provider_name", "model_ref"}},
	{Table: "router_config_callers", Columns: []string{"config_set_id"}, ReferencedTable: "router_config_sets", ReferencedColumns: []string{"id"}},
	{Table: "router_config_caller_allowed_groups", Columns: []string{"config_set_id", "caller_id"}, ReferencedTable: "router_config_callers", ReferencedColumns: []string{"config_set_id", "caller_id"}},
	{Table: "router_config_caller_allowed_groups", Columns: []string{"config_set_id", "group_name"}, ReferencedTable: "router_config_model_groups", ReferencedColumns: []string{"config_set_id", "group_name"}},
}

var configControlPlanePhase4RequiredForeignKeys = []configControlPlaneForeignKey{
	{Table: "router_config_provider_model_capabilities", Columns: []string{"config_set_id", "provider_name", "model_ref"}, ReferencedTable: "router_config_provider_models", ReferencedColumns: []string{"config_set_id", "provider_name", "model_ref"}},
	{Table: "router_config_provider_model_tool_support", Columns: []string{"config_set_id", "provider_name", "model_ref"}, ReferencedTable: "router_config_provider_model_capabilities", ReferencedColumns: []string{"config_set_id", "provider_name", "model_ref"}},
	{Table: "router_config_provider_model_modalities", Columns: []string{"config_set_id", "provider_name", "model_ref"}, ReferencedTable: "router_config_provider_model_capabilities", ReferencedColumns: []string{"config_set_id", "provider_name", "model_ref"}},
	{Table: "router_config_provider_model_request_inbound_dialects", Columns: []string{"config_set_id", "provider_name", "model_ref"}, ReferencedTable: "router_config_provider_model_capabilities", ReferencedColumns: []string{"config_set_id", "provider_name", "model_ref"}},
	{Table: "router_config_provider_model_request_unsupported_features", Columns: []string{"config_set_id", "provider_name", "model_ref"}, ReferencedTable: "router_config_provider_model_capabilities", ReferencedColumns: []string{"config_set_id", "provider_name", "model_ref"}},
	{Table: "router_config_provider_model_bridges", Columns: []string{"config_set_id", "provider_name", "model_ref"}, ReferencedTable: "router_config_provider_model_capabilities", ReferencedColumns: []string{"config_set_id", "provider_name", "model_ref"}},
}

func verifyConfigControlPlaneForeignKey(tx *gorm.DB, required configControlPlaneForeignKey) error {
	var rows []configControlPlaneForeignKeyRow
	switch tx.Dialector.Name() {
	case "sqlite":
		// required.Table comes from the checked-in allowlist above, never a caller
		// value. SQLite reports every foreign-key column with a stable numeric id
		// and zero-based sequence.
		query := fmt.Sprintf(`SELECT id AS constraint_id, seq AS sequence, "table" AS referenced_table, "from" AS local_column, "to" AS referenced_column FROM pragma_foreign_key_list('%s')`, required.Table)
		if err := tx.Raw(query).Scan(&rows).Error; err != nil {
			return fmt.Errorf("inspect control-plane foreign keys for %s: %w", required.Table, err)
		}
	case "postgres":
		const query = `SELECT con.conname AS constraint_id,
			local_key.ordinality AS sequence,
			referenced_table.relname AS referenced_table,
			local_column.attname AS local_column,
			referenced_column.attname AS referenced_column
		FROM pg_constraint con
		JOIN pg_class local_table ON local_table.oid = con.conrelid
		JOIN pg_namespace local_namespace ON local_namespace.oid = local_table.relnamespace
		JOIN pg_class referenced_table ON referenced_table.oid = con.confrelid
		JOIN unnest(con.conkey) WITH ORDINALITY AS local_key(attnum, ordinality) ON TRUE
		JOIN unnest(con.confkey) WITH ORDINALITY AS referenced_key(attnum, ordinality) ON referenced_key.ordinality = local_key.ordinality
		JOIN pg_attribute local_column ON local_column.attrelid = con.conrelid AND local_column.attnum = local_key.attnum
		JOIN pg_attribute referenced_column ON referenced_column.attrelid = con.confrelid AND referenced_column.attnum = referenced_key.attnum
		WHERE con.contype = 'f' AND local_namespace.nspname = current_schema() AND local_table.relname = ?
		ORDER BY con.conname, local_key.ordinality`
		if err := tx.Raw(query, required.Table).Scan(&rows).Error; err != nil {
			return fmt.Errorf("inspect control-plane foreign keys for %s: %w", required.Table, err)
		}
	default:
		return fmt.Errorf("unsupported control-plane database driver %q", tx.Dialector.Name())
	}
	byConstraint := map[string][]configControlPlaneForeignKeyRow{}
	for _, row := range rows {
		byConstraint[row.ConstraintID] = append(byConstraint[row.ConstraintID], row)
	}
	for _, candidate := range byConstraint {
		sort.Slice(candidate, func(i, j int) bool { return candidate[i].Sequence < candidate[j].Sequence })
		if len(candidate) != len(required.Columns) || candidate[0].ReferencedTable != required.ReferencedTable {
			continue
		}
		matched := true
		for index, row := range candidate {
			if row.LocalColumn != required.Columns[index] || row.ReferencedColumn != required.ReferencedColumns[index] {
				matched = false
				break
			}
		}
		if matched {
			return nil
		}
	}
	return fmt.Errorf("required control-plane foreign key %s(%s) -> %s(%s) is missing", required.Table, strings.Join(required.Columns, ","), required.ReferencedTable, strings.Join(required.ReferencedColumns, ","))
}

var configControlPlaneDDL = []string{
	`CREATE TABLE IF NOT EXISTS router_config_sets (id TEXT PRIMARY KEY, runtime_scope TEXT NOT NULL, name TEXT NOT NULL, status TEXT NOT NULL, validation_status TEXT NOT NULL, created_by TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, activated_at TEXT NOT NULL DEFAULT '', UNIQUE (runtime_scope, name))`,
	`CREATE UNIQUE INDEX IF NOT EXISTS router_config_one_active_set_per_scope ON router_config_sets(runtime_scope) WHERE status = 'active'`,
	`CREATE TABLE IF NOT EXISTS router_config_server (config_set_id TEXT PRIMARY KEY REFERENCES router_config_sets(id), listen TEXT NOT NULL DEFAULT '', default_model_group TEXT NOT NULL DEFAULT '', state_path TEXT NOT NULL DEFAULT '', license_enabled BOOLEAN NOT NULL DEFAULT TRUE, license_path TEXT NOT NULL DEFAULT '', license_state_path TEXT NOT NULL DEFAULT '')`,
	`CREATE TABLE IF NOT EXISTS router_config_providers (config_set_id TEXT NOT NULL REFERENCES router_config_sets(id), provider_name TEXT NOT NULL, base_url TEXT NOT NULL, dialect TEXT NOT NULL, api_key_env TEXT NOT NULL DEFAULT '', key_id TEXT NOT NULL DEFAULT '', auth_scheme TEXT NOT NULL DEFAULT '', PRIMARY KEY (config_set_id, provider_name))`,
	`CREATE TABLE IF NOT EXISTS router_config_provider_headers (config_set_id TEXT NOT NULL, provider_name TEXT NOT NULL, header_name TEXT NOT NULL, header_value TEXT NOT NULL, PRIMARY KEY (config_set_id, provider_name, header_name), FOREIGN KEY (config_set_id, provider_name) REFERENCES router_config_providers(config_set_id, provider_name))`,
	`CREATE TABLE IF NOT EXISTS router_config_provider_models (config_set_id TEXT NOT NULL, provider_name TEXT NOT NULL, model_ref TEXT NOT NULL, model TEXT NOT NULL, dialect TEXT NOT NULL DEFAULT '', display_name TEXT NOT NULL DEFAULT '', context_tokens BIGINT NOT NULL DEFAULT 0, input_price_per_million_usd DOUBLE PRECISION NOT NULL DEFAULT 0, output_price_per_million_usd DOUBLE PRECISION NOT NULL DEFAULT 0, pricing_source TEXT NOT NULL DEFAULT '', pricing_updated_at TEXT NOT NULL DEFAULT '', pricing_notes TEXT NOT NULL DEFAULT '', PRIMARY KEY (config_set_id, provider_name, model_ref), FOREIGN KEY (config_set_id, provider_name) REFERENCES router_config_providers(config_set_id, provider_name))`,
	`CREATE TABLE IF NOT EXISTS router_config_model_groups (config_set_id TEXT NOT NULL REFERENCES router_config_sets(id), group_name TEXT NOT NULL, strategy TEXT NOT NULL DEFAULT '', attempt_timeout_ms BIGINT NOT NULL DEFAULT 0, PRIMARY KEY (config_set_id, group_name))`,
	`CREATE TABLE IF NOT EXISTS router_config_model_group_targets (config_set_id TEXT NOT NULL, group_name TEXT NOT NULL, sequence BIGINT NOT NULL, provider_name TEXT NOT NULL, model_ref TEXT, model TEXT NOT NULL DEFAULT '', dialect TEXT NOT NULL DEFAULT '', weight BIGINT NOT NULL DEFAULT 0, rpm BIGINT NOT NULL DEFAULT 0, tier TEXT NOT NULL DEFAULT '', cost BIGINT NOT NULL DEFAULT 0, PRIMARY KEY (config_set_id, group_name, sequence), CONSTRAINT router_config_targets_group_fk FOREIGN KEY (config_set_id, group_name) REFERENCES router_config_model_groups(config_set_id, group_name), CONSTRAINT router_config_targets_provider_fk FOREIGN KEY (config_set_id, provider_name) REFERENCES router_config_providers(config_set_id, provider_name), CONSTRAINT router_config_targets_provider_model_fk FOREIGN KEY (config_set_id, provider_name, model_ref) REFERENCES router_config_provider_models(config_set_id, provider_name, model_ref))`,
	`CREATE INDEX IF NOT EXISTS router_config_targets_provider_model ON router_config_model_group_targets(config_set_id, provider_name, model_ref)`,
	`CREATE TABLE IF NOT EXISTS router_config_callers (config_set_id TEXT NOT NULL REFERENCES router_config_sets(id), caller_id TEXT NOT NULL, owner_user TEXT NOT NULL DEFAULT '', project TEXT NOT NULL DEFAULT '', environment TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT '', token_sha256 TEXT NOT NULL DEFAULT '', token_id TEXT NOT NULL DEFAULT '', metrics_admin BOOLEAN NOT NULL DEFAULT FALSE, content_admin BOOLEAN NOT NULL DEFAULT FALSE, rpm BIGINT NOT NULL DEFAULT 0, tpm BIGINT NOT NULL DEFAULT 0, concurrent BIGINT NOT NULL DEFAULT 0, PRIMARY KEY (config_set_id, caller_id))`,
	`CREATE TABLE IF NOT EXISTS router_config_caller_allowed_groups (config_set_id TEXT NOT NULL, caller_id TEXT NOT NULL, group_name TEXT NOT NULL, PRIMARY KEY (config_set_id, caller_id, group_name), FOREIGN KEY (config_set_id, caller_id) REFERENCES router_config_callers(config_set_id, caller_id), FOREIGN KEY (config_set_id, group_name) REFERENCES router_config_model_groups(config_set_id, group_name))`,
}

// configControlPlanePhase4DDL keeps repeated capability values in child rows
// rather than JSON, arrays, or packed text. Every child is scoped to the
// immutable provider-model identity within a versioned configuration set.
var configControlPlanePhase4DDL = []string{
	`CREATE TABLE IF NOT EXISTS router_config_provider_model_capabilities (config_set_id TEXT NOT NULL, provider_name TEXT NOT NULL, model_ref TEXT NOT NULL, image_input_price_per_million_tokens_usd DOUBLE PRECISION NOT NULL DEFAULT 0, image_input_price_per_image_usd DOUBLE PRECISION NOT NULL DEFAULT 0, rpm BIGINT NOT NULL DEFAULT 0, tier TEXT NOT NULL DEFAULT '', cost BIGINT NOT NULL DEFAULT 0, reasoning_supported BOOLEAN NOT NULL DEFAULT FALSE, reasoning_mode TEXT NOT NULL DEFAULT '', reasoning_control TEXT NOT NULL DEFAULT '', reasoning_default_on BOOLEAN NOT NULL DEFAULT FALSE, reasoning_min_budget_tokens BIGINT NOT NULL DEFAULT 0, reasoning_max_budget_tokens BIGINT NOT NULL DEFAULT 0, reasoning_budget_must_be_less_than_max_tokens BOOLEAN NOT NULL DEFAULT FALSE, reasoning_stream_block TEXT NOT NULL DEFAULT '', reasoning_rejects_max_tokens BOOLEAN NOT NULL DEFAULT FALSE, reasoning_rejects_temperature BOOLEAN NOT NULL DEFAULT FALSE, reasoning_rejects_top_p BOOLEAN NOT NULL DEFAULT FALSE, reasoning_supports_summaries BOOLEAN NOT NULL DEFAULT FALSE, honors_max_tokens BOOLEAN, force_store_false BOOLEAN NOT NULL DEFAULT FALSE, output_token_field TEXT NOT NULL DEFAULT '', max_request_bytes BIGINT NOT NULL DEFAULT 0, max_estimated_input_tokens BIGINT NOT NULL DEFAULT 0, min_requested_output_tokens BIGINT NOT NULL DEFAULT 0, max_requested_output_tokens BIGINT NOT NULL DEFAULT 0, max_tool_schema_bytes BIGINT NOT NULL DEFAULT 0, supports_large_coding_agent_payloads BOOLEAN, request_shape_validation_status TEXT NOT NULL DEFAULT '', request_shape_validation_notes TEXT NOT NULL DEFAULT '', responses_to_chat_enabled BOOLEAN NOT NULL DEFAULT FALSE, responses_to_chat_text BOOLEAN NOT NULL DEFAULT FALSE, responses_to_chat_function_tools BOOLEAN NOT NULL DEFAULT FALSE, responses_to_chat_tool_choice BOOLEAN NOT NULL DEFAULT FALSE, responses_to_chat_structured_outputs BOOLEAN NOT NULL DEFAULT FALSE, responses_to_chat_reasoning BOOLEAN NOT NULL DEFAULT FALSE, responses_to_chat_images BOOLEAN NOT NULL DEFAULT FALSE, responses_to_chat_streaming BOOLEAN NOT NULL DEFAULT FALSE, responses_to_chat_validation_status TEXT NOT NULL DEFAULT '', responses_to_chat_validation_notes TEXT NOT NULL DEFAULT '', PRIMARY KEY (config_set_id, provider_name, model_ref), FOREIGN KEY (config_set_id, provider_name, model_ref) REFERENCES router_config_provider_models(config_set_id, provider_name, model_ref))`,
	`CREATE TABLE IF NOT EXISTS router_config_provider_model_tool_support (config_set_id TEXT NOT NULL, provider_name TEXT NOT NULL, model_ref TEXT NOT NULL, api_surface TEXT NOT NULL, capability TEXT NOT NULL, PRIMARY KEY (config_set_id, provider_name, model_ref, api_surface, capability), FOREIGN KEY (config_set_id, provider_name, model_ref) REFERENCES router_config_provider_model_capabilities(config_set_id, provider_name, model_ref))`,
	`CREATE TABLE IF NOT EXISTS router_config_provider_model_modalities (config_set_id TEXT NOT NULL, provider_name TEXT NOT NULL, model_ref TEXT NOT NULL, direction TEXT NOT NULL, modality TEXT NOT NULL, PRIMARY KEY (config_set_id, provider_name, model_ref, direction, modality), FOREIGN KEY (config_set_id, provider_name, model_ref) REFERENCES router_config_provider_model_capabilities(config_set_id, provider_name, model_ref))`,
	`CREATE TABLE IF NOT EXISTS router_config_provider_model_request_inbound_dialects (config_set_id TEXT NOT NULL, provider_name TEXT NOT NULL, model_ref TEXT NOT NULL, inbound_dialect TEXT NOT NULL, PRIMARY KEY (config_set_id, provider_name, model_ref, inbound_dialect), FOREIGN KEY (config_set_id, provider_name, model_ref) REFERENCES router_config_provider_model_capabilities(config_set_id, provider_name, model_ref))`,
	`CREATE TABLE IF NOT EXISTS router_config_provider_model_request_unsupported_features (config_set_id TEXT NOT NULL, provider_name TEXT NOT NULL, model_ref TEXT NOT NULL, feature TEXT NOT NULL, PRIMARY KEY (config_set_id, provider_name, model_ref, feature), FOREIGN KEY (config_set_id, provider_name, model_ref) REFERENCES router_config_provider_model_capabilities(config_set_id, provider_name, model_ref))`,
	`CREATE TABLE IF NOT EXISTS router_config_provider_model_bridges (config_set_id TEXT NOT NULL, provider_name TEXT NOT NULL, model_ref TEXT NOT NULL, direction TEXT NOT NULL, enabled BOOLEAN NOT NULL DEFAULT FALSE, text_enabled BOOLEAN, tools BOOLEAN NOT NULL DEFAULT FALSE, tool_choice BOOLEAN NOT NULL DEFAULT FALSE, parallel_tool_calls BOOLEAN NOT NULL DEFAULT FALSE, structured_outputs BOOLEAN NOT NULL DEFAULT FALSE, images BOOLEAN NOT NULL DEFAULT FALSE, reasoning BOOLEAN NOT NULL DEFAULT FALSE, streaming BOOLEAN NOT NULL DEFAULT FALSE, stateful_sessions_enabled BOOLEAN NOT NULL DEFAULT FALSE, stateful_sessions_backend TEXT NOT NULL DEFAULT '', stateful_sessions_session_header TEXT NOT NULL DEFAULT '', stateful_sessions_ttl_seconds BIGINT NOT NULL DEFAULT 0, stateful_sessions_max_entries BIGINT NOT NULL DEFAULT 0, stateful_sessions_redis_address TEXT NOT NULL DEFAULT '', stateful_sessions_redis_namespace TEXT NOT NULL DEFAULT '', stateful_sessions_redis_db BIGINT NOT NULL DEFAULT 0, stateful_sessions_redis_username_env TEXT NOT NULL DEFAULT '', stateful_sessions_redis_password_env TEXT NOT NULL DEFAULT '', stateful_sessions_redis_tls_enabled BOOLEAN NOT NULL DEFAULT FALSE, stateful_sessions_redis_tls_server_name TEXT NOT NULL DEFAULT '', stateful_sessions_redis_tls_insecure_skip_verify BOOLEAN NOT NULL DEFAULT FALSE, stateful_sessions_redis_connect_timeout_ms BIGINT NOT NULL DEFAULT 0, stateful_sessions_redis_read_timeout_ms BIGINT NOT NULL DEFAULT 0, stateful_sessions_redis_write_timeout_ms BIGINT NOT NULL DEFAULT 0, stateful_sessions_redis_pool_size BIGINT NOT NULL DEFAULT 0, PRIMARY KEY (config_set_id, provider_name, model_ref, direction), FOREIGN KEY (config_set_id, provider_name, model_ref) REFERENCES router_config_provider_model_capabilities(config_set_id, provider_name, model_ref))`,
}

var configControlPlanePostgresComments = []string{
	`COMMENT ON TABLE router_config_sets IS 'Versioned configuration set metadata; exactly one validated active set is expected per runtime scope.'`,
	`COMMENT ON COLUMN router_config_sets.id IS 'Opaque configuration-set identifier.'`,
	`COMMENT ON COLUMN router_config_sets.runtime_scope IS 'Deployment/runtime scope owning the active configuration.'`,
	`COMMENT ON COLUMN router_config_sets.name IS 'Human-readable version name unique in the runtime scope.'`,
	`COMMENT ON COLUMN router_config_sets.status IS 'Draft, active, or archived lifecycle state.'`,
	`COMMENT ON COLUMN router_config_sets.validation_status IS 'Validation state; only valid sets may be read as active.'`,
	`COMMENT ON COLUMN router_config_sets.created_by IS 'Sanitized actor identifier that created the set.'`,
	`COMMENT ON COLUMN router_config_sets.created_at IS 'UTC creation timestamp.'`,
	`COMMENT ON COLUMN router_config_sets.activated_at IS 'UTC activation timestamp when applicable.'`,
	`COMMENT ON TABLE router_config_server IS 'Scalar server settings for one configuration set.'`,
	`COMMENT ON COLUMN router_config_server.config_set_id IS 'Owning configuration-set identifier.'`,
	`COMMENT ON COLUMN router_config_server.listen IS 'Router listener address.'`,
	`COMMENT ON COLUMN router_config_server.default_model_group IS 'Default deployment-defined model group.'`,
	`COMMENT ON COLUMN router_config_server.state_path IS 'Runtime state path, not a secret.'`,
	`COMMENT ON COLUMN router_config_server.license_enabled IS 'Whether license enforcement is enabled for this configuration set.'`,
	`COMMENT ON COLUMN router_config_server.license_path IS 'Deployment-managed path to the signed license file.'`,
	`COMMENT ON COLUMN router_config_server.license_state_path IS 'Deployment-managed path to non-secret license state.'`,
	`COMMENT ON TABLE router_config_providers IS 'Provider endpoint and secret-reference metadata; no raw provider secret is stored.'`,
	`COMMENT ON COLUMN router_config_providers.config_set_id IS 'Owning configuration-set identifier.'`,
	`COMMENT ON COLUMN router_config_providers.provider_name IS 'Deployment-local provider reference.'`,
	`COMMENT ON COLUMN router_config_providers.base_url IS 'Provider API base URL.'`,
	`COMMENT ON COLUMN router_config_providers.dialect IS 'Validated provider API dialect.'`,
	`COMMENT ON COLUMN router_config_providers.api_key_env IS 'Environment-variable name for the provider credential.'`,
	`COMMENT ON COLUMN router_config_providers.key_id IS 'Non-secret provider credential identifier.'`,
	`COMMENT ON COLUMN router_config_providers.auth_scheme IS 'Provider authentication scheme.'`,
	`COMMENT ON TABLE router_config_provider_headers IS 'One deployment-owned upstream header per provider.'`,
	`COMMENT ON COLUMN router_config_provider_headers.config_set_id IS 'Owning configuration-set identifier.'`,
	`COMMENT ON COLUMN router_config_provider_headers.provider_name IS 'Referenced provider name.'`,
	`COMMENT ON COLUMN router_config_provider_headers.header_name IS 'Configured non-secret upstream header name.'`,
	`COMMENT ON COLUMN router_config_provider_headers.header_value IS 'Configured non-secret upstream header value.'`,
	`COMMENT ON TABLE router_config_provider_models IS 'Catalogued provider models with scalar pricing metadata.'`,
	`COMMENT ON COLUMN router_config_provider_models.config_set_id IS 'Owning configuration-set identifier.'`,
	`COMMENT ON COLUMN router_config_provider_models.provider_name IS 'Referenced provider name.'`,
	`COMMENT ON COLUMN router_config_provider_models.model_ref IS 'Deployment-local provider model reference.'`,
	`COMMENT ON COLUMN router_config_provider_models.model IS 'Exact upstream model identifier.'`,
	`COMMENT ON COLUMN router_config_provider_models.dialect IS 'Optional model-specific API dialect.'`,
	`COMMENT ON COLUMN router_config_provider_models.display_name IS 'Non-secret operator display name.'`,
	`COMMENT ON COLUMN router_config_provider_models.context_tokens IS 'Advertised context-window tokens.'`,
	`COMMENT ON COLUMN router_config_provider_models.input_price_per_million_usd IS 'Input price per million tokens in USD.'`,
	`COMMENT ON COLUMN router_config_provider_models.output_price_per_million_usd IS 'Output price per million tokens in USD.'`,
	`COMMENT ON COLUMN router_config_provider_models.pricing_source IS 'Primary pricing evidence location.'`,
	`COMMENT ON COLUMN router_config_provider_models.pricing_updated_at IS 'Pricing evidence date.'`,
	`COMMENT ON COLUMN router_config_provider_models.pricing_notes IS 'Operator pricing caveats.'`,
	`COMMENT ON TABLE router_config_model_groups IS 'Deployment-defined model-group routing metadata.'`,
	`COMMENT ON COLUMN router_config_model_groups.config_set_id IS 'Owning configuration-set identifier.'`,
	`COMMENT ON COLUMN router_config_model_groups.group_name IS 'Deployment-defined router model group.'`,
	`COMMENT ON COLUMN router_config_model_groups.strategy IS 'Configured routing strategy.'`,
	`COMMENT ON COLUMN router_config_model_groups.attempt_timeout_ms IS 'Per-attempt timeout in milliseconds.'`,
	`COMMENT ON TABLE router_config_model_group_targets IS 'Ordered weighted targets for a model group.'`,
	`COMMENT ON COLUMN router_config_model_group_targets.config_set_id IS 'Owning configuration-set identifier.'`,
	`COMMENT ON COLUMN router_config_model_group_targets.group_name IS 'Referenced model group.'`,
	`COMMENT ON COLUMN router_config_model_group_targets.sequence IS 'Stable target ordering value.'`,
	`COMMENT ON COLUMN router_config_model_group_targets.provider_name IS 'Referenced provider name.'`,
	`COMMENT ON COLUMN router_config_model_group_targets.model_ref IS 'Referenced provider-model record when supplied.'`,
	`COMMENT ON COLUMN router_config_model_group_targets.model IS 'Inline exact upstream model identifier when supplied.'`,
	`COMMENT ON COLUMN router_config_model_group_targets.dialect IS 'Optional target-specific API dialect.'`,
	`COMMENT ON COLUMN router_config_model_group_targets.weight IS 'Group-local routing weight.'`,
	`COMMENT ON COLUMN router_config_model_group_targets.rpm IS 'Target rate limit in requests per minute.'`,
	`COMMENT ON COLUMN router_config_model_group_targets.tier IS 'Deployment-local target tier.'`,
	`COMMENT ON COLUMN router_config_model_group_targets.cost IS 'Deployment-local relative cost class.'`,
	`COMMENT ON TABLE router_config_callers IS 'Caller identity and token hash metadata; raw caller tokens are never stored.'`,
	`COMMENT ON COLUMN router_config_callers.config_set_id IS 'Owning configuration-set identifier.'`,
	`COMMENT ON COLUMN router_config_callers.caller_id IS 'Stable caller identity.'`,
	`COMMENT ON COLUMN router_config_callers.owner_user IS 'Normalized owning user identifier.'`,
	`COMMENT ON COLUMN router_config_callers.project IS 'Normalized owning project identifier.'`,
	`COMMENT ON COLUMN router_config_callers.environment IS 'Normalized deployment environment identifier.'`,
	`COMMENT ON COLUMN router_config_callers.status IS 'Caller lifecycle status.'`,
	`COMMENT ON COLUMN router_config_callers.token_sha256 IS 'Write-only SHA-256 hash of the caller token.'`,
	`COMMENT ON COLUMN router_config_callers.token_id IS 'Non-secret public caller-token identifier.'`,
	`COMMENT ON COLUMN router_config_callers.metrics_admin IS 'Whether caller may access global metrics.'`,
	`COMMENT ON COLUMN router_config_callers.content_admin IS 'Whether caller may access governed content administration.'`,
	`COMMENT ON COLUMN router_config_callers.rpm IS 'Caller request rate limit.'`,
	`COMMENT ON COLUMN router_config_callers.tpm IS 'Caller token rate limit.'`,
	`COMMENT ON COLUMN router_config_callers.concurrent IS 'Caller concurrent request limit.'`,
	`COMMENT ON TABLE router_config_caller_allowed_groups IS 'One allowed model group per caller.'`,
	`COMMENT ON COLUMN router_config_caller_allowed_groups.config_set_id IS 'Owning configuration-set identifier.'`,
	`COMMENT ON COLUMN router_config_caller_allowed_groups.caller_id IS 'Referenced caller identifier.'`,
	`COMMENT ON COLUMN router_config_caller_allowed_groups.group_name IS 'Referenced allowed model group.'`,
}

var configControlPlanePhase4PostgresComments = []string{
	`COMMENT ON TABLE router_config_provider_model_capabilities IS 'Scalar capability, request-shape, bridge-compatibility, and non-secret pricing metadata for one provider model.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.config_set_id IS 'Owning configuration-set identifier.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.provider_name IS 'Referenced provider name.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.model_ref IS 'Referenced provider-model catalog reference.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.image_input_price_per_million_tokens_usd IS 'Image input price per million image tokens in USD.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.image_input_price_per_image_usd IS 'Image input price per image in USD.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.rpm IS 'Catalogued provider-model request-rate value.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.tier IS 'Deployment-local provider-model tier label.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.cost IS 'Deployment-local provider-model relative cost class.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.reasoning_supported IS 'Whether the provider model supports declared reasoning controls.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.reasoning_mode IS 'Declared reasoning mode.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.reasoning_control IS 'Declared upstream reasoning control field.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.reasoning_default_on IS 'Whether reasoning defaults on.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.reasoning_min_budget_tokens IS 'Minimum allowed reasoning budget tokens.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.reasoning_max_budget_tokens IS 'Maximum allowed reasoning budget tokens.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.reasoning_budget_must_be_less_than_max_tokens IS 'Whether the reasoning budget must be less than the output cap.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.reasoning_stream_block IS 'Declared reasoning stream-block encoding.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.reasoning_rejects_max_tokens IS 'Whether the upstream rejects a normal max-token field while reasoning is active.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.reasoning_rejects_temperature IS 'Whether the upstream rejects temperature while reasoning is active.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.reasoning_rejects_top_p IS 'Whether the upstream rejects top_p while reasoning is active.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.reasoning_supports_summaries IS 'Whether the upstream supports reasoning summaries.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.honors_max_tokens IS 'Nullable explicit maximum-output-token conformance; null preserves the router default.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.force_store_false IS 'Whether supported OpenAI-compatible calls require store:false.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.output_token_field IS 'Upstream output-cap field, such as max_tokens or max_completion_tokens.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.max_request_bytes IS 'Maximum validated inbound request bytes.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.max_estimated_input_tokens IS 'Maximum validated estimated input tokens.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.min_requested_output_tokens IS 'Minimum validated requested output tokens.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.max_requested_output_tokens IS 'Maximum validated requested output tokens.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.max_tool_schema_bytes IS 'Maximum validated serialized tool-schema bytes.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.supports_large_coding_agent_payloads IS 'Nullable explicit large coding-agent payload support flag.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.request_shape_validation_status IS 'Request-shape validation status.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.request_shape_validation_notes IS 'Sanitized request-shape validation notes.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.responses_to_chat_enabled IS 'Whether the legacy Responses-to-Chat bridge is enabled.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.responses_to_chat_text IS 'Whether the legacy Responses-to-Chat bridge supports text.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.responses_to_chat_function_tools IS 'Whether the legacy Responses-to-Chat bridge supports function tools.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.responses_to_chat_tool_choice IS 'Whether the legacy Responses-to-Chat bridge supports tool choice.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.responses_to_chat_structured_outputs IS 'Whether the legacy Responses-to-Chat bridge supports structured outputs.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.responses_to_chat_reasoning IS 'Whether the legacy Responses-to-Chat bridge supports reasoning.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.responses_to_chat_images IS 'Whether the legacy Responses-to-Chat bridge supports images.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.responses_to_chat_streaming IS 'Whether the legacy Responses-to-Chat bridge supports streaming.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.responses_to_chat_validation_status IS 'Legacy Responses-to-Chat bridge validation status.'`,
	`COMMENT ON COLUMN router_config_provider_model_capabilities.responses_to_chat_validation_notes IS 'Sanitized legacy Responses-to-Chat bridge validation notes.'`,
	`COMMENT ON TABLE router_config_provider_model_tool_support IS 'One validated tool capability per provider-model API surface.'`,
	`COMMENT ON COLUMN router_config_provider_model_tool_support.config_set_id IS 'Owning configuration-set identifier.'`,
	`COMMENT ON COLUMN router_config_provider_model_tool_support.provider_name IS 'Referenced provider name.'`,
	`COMMENT ON COLUMN router_config_provider_model_tool_support.model_ref IS 'Referenced provider-model catalog reference.'`,
	`COMMENT ON COLUMN router_config_provider_model_tool_support.api_surface IS 'Validated API surface such as openai_chat or anthropic_messages.'`,
	`COMMENT ON COLUMN router_config_provider_model_tool_support.capability IS 'One validated tool capability label.'`,
	`COMMENT ON TABLE router_config_provider_model_modalities IS 'One declared validated input or output modality per provider model.'`,
	`COMMENT ON COLUMN router_config_provider_model_modalities.config_set_id IS 'Owning configuration-set identifier.'`,
	`COMMENT ON COLUMN router_config_provider_model_modalities.provider_name IS 'Referenced provider name.'`,
	`COMMENT ON COLUMN router_config_provider_model_modalities.model_ref IS 'Referenced provider-model catalog reference.'`,
	`COMMENT ON COLUMN router_config_provider_model_modalities.direction IS 'Modality direction: input or output.'`,
	`COMMENT ON COLUMN router_config_provider_model_modalities.modality IS 'One validated modality value.'`,
	`COMMENT ON TABLE router_config_provider_model_request_inbound_dialects IS 'One validated inbound dialect supported by a provider model request shape.'`,
	`COMMENT ON COLUMN router_config_provider_model_request_inbound_dialects.config_set_id IS 'Owning configuration-set identifier.'`,
	`COMMENT ON COLUMN router_config_provider_model_request_inbound_dialects.provider_name IS 'Referenced provider name.'`,
	`COMMENT ON COLUMN router_config_provider_model_request_inbound_dialects.model_ref IS 'Referenced provider-model catalog reference.'`,
	`COMMENT ON COLUMN router_config_provider_model_request_inbound_dialects.inbound_dialect IS 'One validated inbound request dialect.'`,
	`COMMENT ON TABLE router_config_provider_model_request_unsupported_features IS 'One bounded request feature intentionally unsupported by a provider model.'`,
	`COMMENT ON COLUMN router_config_provider_model_request_unsupported_features.config_set_id IS 'Owning configuration-set identifier.'`,
	`COMMENT ON COLUMN router_config_provider_model_request_unsupported_features.provider_name IS 'Referenced provider name.'`,
	`COMMENT ON COLUMN router_config_provider_model_request_unsupported_features.model_ref IS 'Referenced provider-model catalog reference.'`,
	`COMMENT ON COLUMN router_config_provider_model_request_unsupported_features.feature IS 'One bounded unsupported request feature label.'`,
	`COMMENT ON TABLE router_config_provider_model_bridges IS 'One explicit dialect-bridge capability record per provider model and bridge direction.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.config_set_id IS 'Owning configuration-set identifier.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.provider_name IS 'Referenced provider name.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.model_ref IS 'Referenced provider-model catalog reference.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.direction IS 'Bridge direction: chat_to_responses or responses_to_chat.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.enabled IS 'Whether the dialect bridge is enabled.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.text_enabled IS 'Nullable explicit text-shape bridge support.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.tools IS 'Whether the dialect bridge supports tools.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.tool_choice IS 'Whether the dialect bridge supports tool choice.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.parallel_tool_calls IS 'Whether the dialect bridge supports parallel tool calls.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.structured_outputs IS 'Whether the dialect bridge supports structured outputs.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.images IS 'Whether the dialect bridge supports images.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.reasoning IS 'Whether the dialect bridge supports reasoning.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.streaming IS 'Whether the dialect bridge supports streaming.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.stateful_sessions_enabled IS 'Whether stateful bridge sessions are enabled.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.stateful_sessions_backend IS 'Stateful bridge-session backend selector.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.stateful_sessions_session_header IS 'Stateful bridge-session correlation header.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.stateful_sessions_ttl_seconds IS 'Stateful bridge-session retention in seconds.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.stateful_sessions_max_entries IS 'Maximum stateful bridge-session entries.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.stateful_sessions_redis_address IS 'Redis host and port without credentials.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.stateful_sessions_redis_namespace IS 'Redis key namespace.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.stateful_sessions_redis_db IS 'Redis database number.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.stateful_sessions_redis_username_env IS 'Environment variable naming Redis username.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.stateful_sessions_redis_password_env IS 'Environment variable naming Redis password.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.stateful_sessions_redis_tls_enabled IS 'Whether Redis transport TLS is enabled.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.stateful_sessions_redis_tls_server_name IS 'Redis TLS server-name override.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.stateful_sessions_redis_tls_insecure_skip_verify IS 'Whether Redis TLS verification is explicitly disabled.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.stateful_sessions_redis_connect_timeout_ms IS 'Redis connection timeout in milliseconds.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.stateful_sessions_redis_read_timeout_ms IS 'Redis read timeout in milliseconds.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.stateful_sessions_redis_write_timeout_ms IS 'Redis write timeout in milliseconds.'`,
	`COMMENT ON COLUMN router_config_provider_model_bridges.stateful_sessions_redis_pool_size IS 'Redis connection pool size.'`,
}

type configSetRow struct {
	ID               string `gorm:"column:id"`
	RuntimeScope     string `gorm:"column:runtime_scope"`
	Status           string `gorm:"column:status"`
	ValidationStatus string `gorm:"column:validation_status"`
}

func (configSetRow) TableName() string { return "router_config_sets" }

type serverConfigRow struct {
	ConfigSetID       string `gorm:"column:config_set_id"`
	Listen            string `gorm:"column:listen"`
	DefaultModelGroup string `gorm:"column:default_model_group"`
	StatePath         string `gorm:"column:state_path"`
	LicenseEnabled    bool   `gorm:"column:license_enabled"`
	LicensePath       string `gorm:"column:license_path"`
	LicenseStatePath  string `gorm:"column:license_state_path"`
}

func (serverConfigRow) TableName() string { return "router_config_server" }

type providerRow struct {
	ConfigSetID  string `gorm:"column:config_set_id"`
	ProviderName string `gorm:"column:provider_name"`
	BaseURL      string `gorm:"column:base_url"`
	Dialect      string `gorm:"column:dialect"`
	APIKeyEnv    string `gorm:"column:api_key_env"`
	KeyID        string `gorm:"column:key_id"`
	AuthScheme   string `gorm:"column:auth_scheme"`
}

func (providerRow) TableName() string { return "router_config_providers" }

type providerHeaderRow struct {
	HeaderName  string `gorm:"column:header_name"`
	HeaderValue string `gorm:"column:header_value"`
}

func (providerHeaderRow) TableName() string { return "router_config_provider_headers" }

type providerModelRow struct {
	ModelRef                      string   `gorm:"column:model_ref"`
	Model                         string   `gorm:"column:model"`
	Dialect                       string   `gorm:"column:dialect"`
	DisplayName                   string   `gorm:"column:display_name"`
	ContextTokens                 int      `gorm:"column:context_tokens"`
	InputPricePerMillionUSD       float64  `gorm:"column:input_price_per_million_usd"`
	OutputPricePerMillionUSD      float64  `gorm:"column:output_price_per_million_usd"`
	CachedInputPricePerMillionUSD *float64 `gorm:"column:cached_input_price_per_million_usd"`
	PricingSource                 string   `gorm:"column:pricing_source"`
	PricingUpdatedAt              string   `gorm:"column:pricing_updated_at"`
	PricingNotes                  string   `gorm:"column:pricing_notes"`
}

func (providerModelRow) TableName() string { return "router_config_provider_models" }

type providerModelCapabilityRow struct {
	ImageInputPricePerMillionTokensUSD     float64      `gorm:"column:image_input_price_per_million_tokens_usd"`
	ImageInputPricePerImageUSD             float64      `gorm:"column:image_input_price_per_image_usd"`
	RPM                                    int          `gorm:"column:rpm"`
	Tier                                   string       `gorm:"column:tier"`
	Cost                                   int          `gorm:"column:cost"`
	ReasoningSupported                     bool         `gorm:"column:reasoning_supported"`
	ReasoningMode                          string       `gorm:"column:reasoning_mode"`
	ReasoningControl                       string       `gorm:"column:reasoning_control"`
	ReasoningDefaultOn                     bool         `gorm:"column:reasoning_default_on"`
	ReasoningMinBudgetTokens               int          `gorm:"column:reasoning_min_budget_tokens"`
	ReasoningMaxBudgetTokens               int          `gorm:"column:reasoning_max_budget_tokens"`
	ReasoningBudgetMustBeLessThanMaxTokens bool         `gorm:"column:reasoning_budget_must_be_less_than_max_tokens"`
	ReasoningStreamBlock                   string       `gorm:"column:reasoning_stream_block"`
	ReasoningRejectsMaxTokens              bool         `gorm:"column:reasoning_rejects_max_tokens"`
	ReasoningRejectsTemperature            bool         `gorm:"column:reasoning_rejects_temperature"`
	ReasoningRejectsTopP                   bool         `gorm:"column:reasoning_rejects_top_p"`
	ReasoningSupportsSummaries             bool         `gorm:"column:reasoning_supports_summaries"`
	HonorsMaxTokens                        sql.NullBool `gorm:"column:honors_max_tokens"`
	ForceStoreFalse                        bool         `gorm:"column:force_store_false"`
	OutputTokenField                       string       `gorm:"column:output_token_field"`
	MaxRequestBytes                        int          `gorm:"column:max_request_bytes"`
	MaxEstimatedInputTokens                int          `gorm:"column:max_estimated_input_tokens"`
	MinRequestedOutputTokens               int          `gorm:"column:min_requested_output_tokens"`
	MaxRequestedOutputTokens               int          `gorm:"column:max_requested_output_tokens"`
	MaxToolSchemaBytes                     int          `gorm:"column:max_tool_schema_bytes"`
	SupportsLargeCodingAgentPayloads       sql.NullBool `gorm:"column:supports_large_coding_agent_payloads"`
	RequestShapeValidationStatus           string       `gorm:"column:request_shape_validation_status"`
	RequestShapeValidationNotes            string       `gorm:"column:request_shape_validation_notes"`
	ResponsesToChatEnabled                 bool         `gorm:"column:responses_to_chat_enabled"`
	ResponsesToChatText                    bool         `gorm:"column:responses_to_chat_text"`
	ResponsesToChatFunctionTools           bool         `gorm:"column:responses_to_chat_function_tools"`
	ResponsesToChatToolChoice              bool         `gorm:"column:responses_to_chat_tool_choice"`
	ResponsesToChatStructuredOutputs       bool         `gorm:"column:responses_to_chat_structured_outputs"`
	ResponsesToChatReasoning               bool         `gorm:"column:responses_to_chat_reasoning"`
	ResponsesToChatImages                  bool         `gorm:"column:responses_to_chat_images"`
	ResponsesToChatStreaming               bool         `gorm:"column:responses_to_chat_streaming"`
	ResponsesToChatValidationStatus        string       `gorm:"column:responses_to_chat_validation_status"`
	ResponsesToChatValidationNotes         string       `gorm:"column:responses_to_chat_validation_notes"`
}

func (providerModelCapabilityRow) TableName() string {
	return "router_config_provider_model_capabilities"
}

type providerModelToolSupportRow struct {
	APISurface string `gorm:"column:api_surface"`
	Capability string `gorm:"column:capability"`
}

func (providerModelToolSupportRow) TableName() string {
	return "router_config_provider_model_tool_support"
}

type providerModelModalityRow struct {
	Direction string `gorm:"column:direction"`
	Modality  string `gorm:"column:modality"`
}

func (providerModelModalityRow) TableName() string {
	return "router_config_provider_model_modalities"
}

type providerModelRequestInboundDialectRow struct {
	InboundDialect string `gorm:"column:inbound_dialect"`
}

func (providerModelRequestInboundDialectRow) TableName() string {
	return "router_config_provider_model_request_inbound_dialects"
}

type providerModelRequestUnsupportedFeatureRow struct {
	Feature string `gorm:"column:feature"`
}

func (providerModelRequestUnsupportedFeatureRow) TableName() string {
	return "router_config_provider_model_request_unsupported_features"
}

type providerModelBridgeRow struct {
	Direction                                  string       `gorm:"column:direction"`
	Enabled                                    bool         `gorm:"column:enabled"`
	TextEnabled                                sql.NullBool `gorm:"column:text_enabled"`
	Tools                                      bool         `gorm:"column:tools"`
	ToolChoice                                 bool         `gorm:"column:tool_choice"`
	ParallelToolCalls                          bool         `gorm:"column:parallel_tool_calls"`
	StructuredOutputs                          bool         `gorm:"column:structured_outputs"`
	Images                                     bool         `gorm:"column:images"`
	Reasoning                                  bool         `gorm:"column:reasoning"`
	Streaming                                  bool         `gorm:"column:streaming"`
	StatefulSessionsEnabled                    bool         `gorm:"column:stateful_sessions_enabled"`
	StatefulSessionsBackend                    string       `gorm:"column:stateful_sessions_backend"`
	StatefulSessionsSessionHeader              string       `gorm:"column:stateful_sessions_session_header"`
	StatefulSessionsTTLSeconds                 int          `gorm:"column:stateful_sessions_ttl_seconds"`
	StatefulSessionsMaxEntries                 int          `gorm:"column:stateful_sessions_max_entries"`
	StatefulSessionsRedisAddress               string       `gorm:"column:stateful_sessions_redis_address"`
	StatefulSessionsRedisNamespace             string       `gorm:"column:stateful_sessions_redis_namespace"`
	StatefulSessionsRedisDB                    int          `gorm:"column:stateful_sessions_redis_db"`
	StatefulSessionsRedisUsernameEnv           string       `gorm:"column:stateful_sessions_redis_username_env"`
	StatefulSessionsRedisPasswordEnv           string       `gorm:"column:stateful_sessions_redis_password_env"`
	StatefulSessionsRedisTLSEnabled            bool         `gorm:"column:stateful_sessions_redis_tls_enabled"`
	StatefulSessionsRedisTLSServerName         string       `gorm:"column:stateful_sessions_redis_tls_server_name"`
	StatefulSessionsRedisTLSInsecureSkipVerify bool         `gorm:"column:stateful_sessions_redis_tls_insecure_skip_verify"`
	StatefulSessionsRedisConnectTimeoutMS      int          `gorm:"column:stateful_sessions_redis_connect_timeout_ms"`
	StatefulSessionsRedisReadTimeoutMS         int          `gorm:"column:stateful_sessions_redis_read_timeout_ms"`
	StatefulSessionsRedisWriteTimeoutMS        int          `gorm:"column:stateful_sessions_redis_write_timeout_ms"`
	StatefulSessionsRedisPoolSize              int          `gorm:"column:stateful_sessions_redis_pool_size"`
}

func (providerModelBridgeRow) TableName() string {
	return "router_config_provider_model_bridges"
}

func (row providerModelBridgeRow) dialectBridgeSupport() DialectBridgeSupport {
	return DialectBridgeSupport{
		Enabled:           row.Enabled,
		Text:              controlPlaneOptionalBool(row.TextEnabled),
		Tools:             row.Tools,
		ToolChoice:        row.ToolChoice,
		ParallelToolCalls: row.ParallelToolCalls,
		StructuredOutputs: row.StructuredOutputs,
		Images:            row.Images,
		Reasoning:         row.Reasoning,
		Streaming:         row.Streaming,
		StatefulSessions: BridgeStatefulSessionsConfig{
			Enabled:       row.StatefulSessionsEnabled,
			Backend:       row.StatefulSessionsBackend,
			SessionHeader: row.StatefulSessionsSessionHeader,
			TTLSeconds:    row.StatefulSessionsTTLSeconds,
			MaxEntries:    row.StatefulSessionsMaxEntries,
			Redis: BridgeStatefulSessionsRedisConfig{
				Address:          row.StatefulSessionsRedisAddress,
				Namespace:        row.StatefulSessionsRedisNamespace,
				DB:               row.StatefulSessionsRedisDB,
				UsernameEnv:      row.StatefulSessionsRedisUsernameEnv,
				PasswordEnv:      row.StatefulSessionsRedisPasswordEnv,
				TLS:              BridgeStatefulSessionsRedisTLSConfig{Enabled: row.StatefulSessionsRedisTLSEnabled, ServerName: row.StatefulSessionsRedisTLSServerName, InsecureSkipVerify: row.StatefulSessionsRedisTLSInsecureSkipVerify},
				ConnectTimeoutMS: row.StatefulSessionsRedisConnectTimeoutMS,
				ReadTimeoutMS:    row.StatefulSessionsRedisReadTimeoutMS,
				WriteTimeoutMS:   row.StatefulSessionsRedisWriteTimeoutMS,
				PoolSize:         row.StatefulSessionsRedisPoolSize,
			},
		},
	}
}

type modelGroupRow struct {
	GroupName        string `gorm:"column:group_name"`
	Strategy         string `gorm:"column:strategy"`
	AttemptTimeoutMS int    `gorm:"column:attempt_timeout_ms"`
}

func (modelGroupRow) TableName() string { return "router_config_model_groups" }

type modelGroupTargetRow struct {
	Sequence     int            `gorm:"column:sequence"`
	ProviderName string         `gorm:"column:provider_name"`
	ModelRef     sql.NullString `gorm:"column:model_ref"`
	Model        string         `gorm:"column:model"`
	Dialect      string         `gorm:"column:dialect"`
	Weight       int            `gorm:"column:weight"`
	RPM          int            `gorm:"column:rpm"`
	Tier         string         `gorm:"column:tier"`
	Cost         int            `gorm:"column:cost"`
}

func (modelGroupTargetRow) TableName() string { return "router_config_model_group_targets" }

type callerRow struct {
	CallerID     string `gorm:"column:caller_id"`
	OwnerUser    string `gorm:"column:owner_user"`
	Project      string `gorm:"column:project"`
	Environment  string `gorm:"column:environment"`
	Status       string `gorm:"column:status"`
	TokenSHA256  string `gorm:"column:token_sha256"`
	TokenID      string `gorm:"column:token_id"`
	MetricsAdmin bool   `gorm:"column:metrics_admin"`
	ContentAdmin bool   `gorm:"column:content_admin"`
	RPM          int    `gorm:"column:rpm"`
	TPM          int    `gorm:"column:tpm"`
	Concurrent   int    `gorm:"column:concurrent"`
}

func (callerRow) TableName() string { return "router_config_callers" }

type callerAllowedGroupRow struct {
	GroupName string `gorm:"column:group_name"`
}

func (callerAllowedGroupRow) TableName() string { return "router_config_caller_allowed_groups" }

// ConfigControlPlaneTableNames returns a sorted copy for schema validation
// tests and operator inspection without exposing configuration values.
func ConfigControlPlaneTableNames() []string {
	names := append([]string(nil), configControlPlaneTables...)
	names = append(names, configControlPlanePhase4Tables...)
	sort.Strings(names)
	return names
}

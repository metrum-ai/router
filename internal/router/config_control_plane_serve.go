// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

// LoadActiveCanonicalConfig loads the active canonical YAML document for
// runtimeScope, expands environment references into memory only, validates,
// and returns the serving Config. The stored document is never rewritten.
func LoadActiveCanonicalConfig(db *gorm.DB, runtimeScope string, identifiers ...IdentifierConfig) (*Config, error) {
	raw, err := LoadActiveCanonicalYAML(db, runtimeScope)
	if err != nil {
		return nil, err
	}
	expanded := os.ExpandEnv(string(raw))
	cleaned, hadDeprecatedLicense, err := discardDeprecatedServerLicenseYAML([]byte(expanded))
	if err != nil {
		return nil, fmt.Errorf("parse active canonical YAML: %w", err)
	}
	if hadDeprecatedLicense {
		return nil, fmt.Errorf("config key server.license is rejected in 4.0.0; remove the server.license block")
	}
	var cfg Config
	if err := yaml.Unmarshal(cleaned, &cfg); err != nil {
		return nil, fmt.Errorf("parse active canonical YAML: %w", err)
	}
	if len(identifiers) > 1 {
		return nil, fmt.Errorf("only one identifier configuration is allowed")
	}
	if len(identifiers) == 1 {
		cfg.Server.Identifiers = identifiers[0]
	}
	cfg.setDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate active canonical config: %w", err)
	}
	return &cfg, nil
}

// ResolveConfigSourceDB builds a UsageDBConfig for the control-plane database
// from ConfigSourceConfig. DSNEnv values are resolved from the process
// environment; unresolved postgres DSNs fail closed.
func ResolveConfigSourceDB(cfg ConfigSourceConfig) (UsageDBConfig, error) {
	driver := strings.ToLower(strings.TrimSpace(cfg.Driver))
	if driver == "" {
		driver = "sqlite"
	}
	out := UsageDBConfig{Driver: driver, Path: strings.TrimSpace(cfg.Path)}
	if envName := strings.TrimSpace(cfg.DSNEnv); envName != "" {
		dsn, ok := os.LookupEnv(envName)
		if !ok || strings.TrimSpace(dsn) == "" {
			return UsageDBConfig{}, fmt.Errorf("config_source dsn_env %q is unset", envName)
		}
		out.DSN = dsn
	}
	return out, nil
}

// LoadConfigForServe loads the bootstrap YAML file, then when
// server.config_source.mode is database replaces it with the active
// canonical document from the control-plane database.
func LoadConfigForServe(path string) (*Config, error) {
	bootstrap, err := LoadConfig(path)
	if err != nil {
		return nil, err
	}
	mode := strings.ToLower(strings.TrimSpace(bootstrap.Server.ConfigSource.Mode))
	if mode == "" || mode == "yaml" {
		return bootstrap, nil
	}
	if mode != "database" {
		return nil, fmt.Errorf("server config_source.mode must be yaml or database")
	}
	dbCfg, err := ResolveConfigSourceDB(bootstrap.Server.ConfigSource)
	if err != nil {
		return nil, err
	}
	db, closer, err := OpenConfigControlPlaneDB(dbCfg)
	if err != nil {
		return nil, fmt.Errorf("open config control-plane database: %w", err)
	}
	defer func() { _ = closer() }()
	cfg, err := LoadActiveCanonicalConfig(db, bootstrap.Server.ConfigSource.RuntimeScope, bootstrap.Server.Identifiers)
	if err != nil {
		return nil, err
	}
	// Preserve the bootstrap config_source block so runtime refresh keeps the
	// same database coordinates without requiring them inside the document.
	cfg.Server.ConfigSource = bootstrap.Server.ConfigSource
	return cfg, nil
}

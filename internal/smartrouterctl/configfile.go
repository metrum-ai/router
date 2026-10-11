// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package smartrouterctl

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/metrum-ai/router/internal/router"
	"gopkg.in/yaml.v3"
)

// LoadConfigRaw reads router YAML without expanding environment expressions so
// ${ENV} placeholders survive file-owned edits.
func LoadConfigRaw(path string) (*router.Config, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("config is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg router.Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Provider == nil {
		cfg.Provider = map[string]router.ProviderConfig{}
	}
	if cfg.Models == nil {
		cfg.Models = map[string]router.ModelGroup{}
	}
	return &cfg, nil
}

// WriteConfigAtomic writes cfg to path after a timestamped sibling backup, then
// validates the written file with router.LoadConfig.
func WriteConfigAtomic(path string, cfg *router.Config) (backupPath string, err error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("config is required")
	}
	if cfg == nil {
		return "", fmt.Errorf("config value is required")
	}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		backupPath = path + ".bak." + time.Now().UTC().Format("20060102T150405Z")
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", readErr
		}
		if err := os.WriteFile(backupPath, existing, 0o600); err != nil {
			return "", err
		}
	}
	tmp := path + ".tmp." + time.Now().UTC().Format("20060102T150405Z")
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return backupPath, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return backupPath, err
	}
	// Re-parse without env expansion so ${ENV} placeholders remain intact.
	// Full router.LoadConfig validation remains the `config validate` gate once
	// adjacent env.json and concrete token hashes are present.
	if _, err := LoadConfigRaw(path); err != nil {
		return backupPath, fmt.Errorf("written config failed parse: %w", err)
	}
	return backupPath, nil
}

// UpsertProvider inserts or replaces a provider entry.
func UpsertProvider(cfg *router.Config, name string, provider router.ProviderConfig) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("provider name is required")
	}
	if strings.TrimSpace(provider.BaseURL) == "" {
		return fmt.Errorf("base_url is required")
	}
	if strings.TrimSpace(provider.Dialect) == "" {
		provider.Dialect = "openai-chat"
	}
	if cfg.Provider == nil {
		cfg.Provider = map[string]router.ProviderConfig{}
	}
	cfg.Provider[name] = provider
	return nil
}

// RemoveProvider deletes a provider by name.
func RemoveProvider(cfg *router.Config, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("provider name is required")
	}
	if _, ok := cfg.Provider[name]; !ok {
		return fmt.Errorf("provider %q not found", name)
	}
	delete(cfg.Provider, name)
	return nil
}

// UpsertModelGroup inserts or replaces a static model group.
func UpsertModelGroup(cfg *router.Config, name, strategy string, targets []router.Target) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("model group name is required")
	}
	if strategy == "" {
		strategy = "static"
	}
	if strategy != "static" && strategy != "weighted" {
		return fmt.Errorf("strategy must be static or weighted for file-owned upsert")
	}
	if len(targets) == 0 {
		return fmt.Errorf("at least one target is required")
	}
	for i, target := range targets {
		if strings.TrimSpace(target.Provider) == "" {
			return fmt.Errorf("targets[%d].provider is required", i)
		}
		if strings.TrimSpace(target.ModelRef) == "" && strings.TrimSpace(target.Model) == "" {
			return fmt.Errorf("targets[%d].model_ref is required", i)
		}
		if target.Weight <= 0 {
			targets[i].Weight = 100
		}
	}
	if cfg.Models == nil {
		cfg.Models = map[string]router.ModelGroup{}
	}
	cfg.Models[name] = router.ModelGroup{Strategy: strategy, Targets: targets}
	return nil
}

// MergeCaller inserts or replaces a caller by ID.
func MergeCaller(cfg *router.Config, caller router.CallerConfig) error {
	if strings.TrimSpace(caller.ID) == "" {
		return fmt.Errorf("caller id is required")
	}
	for i, existing := range cfg.Callers {
		if existing.ID == caller.ID {
			cfg.Callers[i] = caller
			return nil
		}
	}
	cfg.Callers = append(cfg.Callers, caller)
	return nil
}

// RevokeCaller sets caller status to disabled.
func RevokeCaller(cfg *router.Config, callerID string) error {
	callerID = strings.TrimSpace(callerID)
	if callerID == "" {
		return fmt.Errorf("caller id is required")
	}
	for i, existing := range cfg.Callers {
		if existing.ID == callerID {
			cfg.Callers[i].Status = "disabled"
			return nil
		}
	}
	return fmt.Errorf("caller %q not found", callerID)
}

// RotateCallerHash replaces token_sha256 and token_id for an existing caller.
//
// Deprecated: use RotateCallerTokenHash, which writes token_hash.
func RotateCallerHash(cfg *router.Config, callerID, tokenSHA256, tokenID string) error {
	if len(tokenSHA256) != 64 {
		return fmt.Errorf("token_sha256 must be 64 hex characters")
	}
	return rotateCallerVerifier(cfg, callerID, "", tokenSHA256, tokenID)
}

// RotateCallerTokenHash replaces token_hash and token_id for an existing
// caller and clears any deprecated token_sha256.
func RotateCallerTokenHash(cfg *router.Config, callerID, tokenHash, tokenID string) error {
	if err := router.ValidateCallerTokenHash(tokenHash); err != nil {
		return fmt.Errorf("invalid token_hash: %w", err)
	}
	if strings.TrimSpace(tokenID) == "" {
		return fmt.Errorf("token_hash requires token_id")
	}
	return rotateCallerVerifier(cfg, callerID, tokenHash, "", tokenID)
}

func rotateCallerVerifier(cfg *router.Config, callerID, tokenHash, tokenSHA256, tokenID string) error {
	callerID = strings.TrimSpace(callerID)
	if callerID == "" {
		return fmt.Errorf("caller id is required")
	}
	for i, existing := range cfg.Callers {
		if existing.ID == callerID {
			cfg.Callers[i].TokenHash = tokenHash
			cfg.Callers[i].TokenSHA256 = tokenSHA256
			if strings.TrimSpace(tokenID) != "" {
				cfg.Callers[i].TokenID = tokenID
			}
			cfg.Callers[i].Status = "active"
			return nil
		}
	}
	return fmt.Errorf("caller %q not found", callerID)
}

// EnsureAccountDirectory creates minimal user/project/membership rows when missing.
func EnsureAccountDirectory(cfg *router.Config, ownerUser, project string) {
	ownerUser = strings.TrimSpace(ownerUser)
	project = strings.TrimSpace(project)
	if ownerUser == "" || project == "" {
		return
	}
	hasUser := false
	for _, user := range cfg.Users {
		if user.ID == ownerUser {
			hasUser = true
			break
		}
	}
	if !hasUser {
		cfg.Users = append(cfg.Users, router.UserConfig{
			ID: ownerUser, Name: ownerUser, Type: "service_account", Status: "active",
		})
	}
	hasProject := false
	for _, p := range cfg.Projects {
		if p.ID == project {
			hasProject = true
			break
		}
	}
	if !hasProject {
		cfg.Projects = append(cfg.Projects, router.ProjectConfig{
			ID: project, Name: project, Status: "active",
		})
	}
	hasMembership := false
	for _, m := range cfg.ProjectMemberships {
		if m.UserID == ownerUser && m.Project == project {
			hasMembership = true
			break
		}
	}
	if !hasMembership {
		cfg.ProjectMemberships = append(cfg.ProjectMemberships, router.ProjectMembershipConfig{
			UserID: ownerUser, Project: project, Role: "member", Status: "active",
		})
	}
}

// SiblingBackupPath returns a UTC timestamped backup path next to path.
func SiblingBackupPath(path string) string {
	return path + ".bak." + time.Now().UTC().Format("20060102T150405Z")
}

// ConfigDir is a convenience for adjacent env.json discovery.
func ConfigDir(path string) string {
	return filepath.Dir(path)
}

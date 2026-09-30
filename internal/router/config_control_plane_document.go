// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Phase six of the configuration control plane adds canonical YAML document
// storage. The canonical YAML is the single source of truth for a
// configuration set; the relational projection is a derived read model. Raw
// provider keys and raw caller tokens are never stored in the document.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"

	"gopkg.in/yaml.v3"
)

// configControlPlaneDocumentRow is the GORM projection for
// router_config_documents. Canonical YAML is stored unexpanded: environment
// variable references are preserved as-is and raw secrets are never written.
type configControlPlaneDocumentRow struct {
	ConfigSetID   string `gorm:"column:config_set_id;primaryKey"`
	CanonicalYAML string `gorm:"column:canonical_yaml"`
	ContentSHA256 string `gorm:"column:content_sha256"`
	UpdatedAt     string `gorm:"column:updated_at"`
}

func (configControlPlaneDocumentRow) TableName() string { return "router_config_documents" }

// configControlPlaneRuntimeRow is the GORM projection for
// router_config_runtime. Exactly one row exists per runtime scope.
type configControlPlaneRuntimeRow struct {
	RuntimeScope   string `gorm:"column:runtime_scope;primaryKey"`
	ConfigRevision int64  `gorm:"column:config_revision"`
	ActiveSetID    string `gorm:"column:active_set_id"`
}

func (configControlPlaneRuntimeRow) TableName() string { return "router_config_runtime" }

// configControlPlaneChangeEventRow is the GORM projection for
// router_config_change_events. Events are append-only and never updated.
type configControlPlaneChangeEventRow struct {
	EventID        string `gorm:"column:event_id;primaryKey"`
	RuntimeScope   string `gorm:"column:runtime_scope"`
	ConfigSetID    string `gorm:"column:config_set_id"`
	Action         string `gorm:"column:action"`
	Actor          string `gorm:"column:actor"`
	At             string `gorm:"column:at"`
	ConfigRevision int64  `gorm:"column:config_revision"`
}

func (configControlPlaneChangeEventRow) TableName() string { return "router_config_change_events" }

// configControlPlaneSecretPattern matches values that look like raw provider
// keys or tokens. The guard is intentionally conservative: it rejects any
// scalar that matches a known live-secret shape, regardless of YAML key.
var configControlPlaneSecretPattern = regexp.MustCompile(
	`(?i)(sk-[A-Za-z0-9_-]{16,}|sk-or-v1-[A-Za-z0-9_-]{16,}|xai-[A-Za-z0-9_-]{16,}|ghp_[A-Za-z0-9_]{16,}|sk-ant-[A-Za-z0-9_-]{16,})`,
)

// ImportCanonicalYAML parses, validates, and stores a canonical YAML document
// as a new draft configuration set. The YAML is stored unexpanded: callers
// must supply environment-variable references (for example $MY_API_KEY or
// api_key_env) rather than raw credentials. Raw-looking secrets are rejected
// before any storage. The relational projection is populated via the
// ProjectConfigToRelational hook when non-nil.
func ImportCanonicalYAML(db *gorm.DB, runtimeScope, name string, yamlBytes []byte, actor string) (string, error) {
	if db == nil {
		return "", errors.New("config control-plane database is required")
	}
	runtimeScope = strings.TrimSpace(runtimeScope)
	if runtimeScope == "" {
		return "", errors.New("config runtime scope is required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("config set name is required")
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return "", errors.New("config actor is required")
	}
	if len(yamlBytes) == 0 {
		return "", errors.New("canonical YAML document is required")
	}

	// Reject raw-looking secrets before any parsing or storage.
	if err := rejectRawSecretsInYAML(yamlBytes); err != nil {
		return "", err
	}

	// Parse and validate using the same YAML path as LoadConfig, but without
	// environment expansion so that $VAR references are preserved verbatim.
	cleaned, hadDeprecatedLicense, err := discardDeprecatedServerLicenseYAML(yamlBytes)
	if err != nil {
		return "", fmt.Errorf("parse canonical YAML: %w", err)
	}
	if hadDeprecatedLicense {
		return "", fmt.Errorf("config key server.license is rejected in 4.0.0; remove the server.license block")
	}
	var cfg Config
	if err := yaml.Unmarshal(cleaned, &cfg); err != nil {
		return "", fmt.Errorf("parse canonical YAML: %w", err)
	}
	cfg.setDefaults()
	if err := cfg.Validate(); err != nil {
		return "", fmt.Errorf("validate canonical YAML: %w", err)
	}

	// Store the exact input bytes (after license scrub) so that export is a
	// pure read of the stored document.
	canonicalYAML := cleaned
	contentHash := sha256.Sum256(canonicalYAML)
	contentSHA256 := hex.EncodeToString(contentHash[:])
	configSetID := newConfigSetID()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	err = db.Transaction(func(tx *gorm.DB) error {
		// Insert the configuration set as draft + valid.
		if err := tx.Exec(
			`INSERT INTO router_config_sets (id, runtime_scope, name, status, validation_status, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			configSetID, runtimeScope, name, "draft", "valid", actor, now,
		).Error; err != nil {
			return fmt.Errorf("insert config set: %w", err)
		}
		// Insert the canonical document.
		if err := tx.Exec(
			`INSERT INTO router_config_documents (config_set_id, canonical_yaml, content_sha256, updated_at) VALUES (?, ?, ?, ?)`,
			configSetID, string(canonicalYAML), contentSHA256, now,
		).Error; err != nil {
			return fmt.Errorf("insert canonical document: %w", err)
		}
		// Populate the relational projection when the hook is installed.
		if ProjectConfigHook != nil {
			if err := ProjectConfigHook(tx, configSetID, &cfg); err != nil {
				return fmt.Errorf("project config to relational: %w", err)
			}
		}
		// Append the import audit event.
		eventID := newConfigChangeEventID()
		if err := tx.Exec(
			`INSERT INTO router_config_change_events (event_id, runtime_scope, config_set_id, action, actor, at, config_revision) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			eventID, runtimeScope, configSetID, "import", actor, now, 0,
		).Error; err != nil {
			return fmt.Errorf("insert change event: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return configSetID, nil
}

// ExportCanonicalYAML returns the stored canonical YAML bytes for a
// configuration set. It is a pure read of the document store; no relational
// reconstruction is performed.
func ExportCanonicalYAML(db *gorm.DB, configSetID string) ([]byte, error) {
	if db == nil {
		return nil, errors.New("config control-plane database is required")
	}
	configSetID = strings.TrimSpace(configSetID)
	if configSetID == "" {
		return nil, errors.New("config set ID is required")
	}
	var row configControlPlaneDocumentRow
	if err := db.Where("config_set_id = ?", configSetID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("canonical document for config set %q not found", configSetID)
		}
		return nil, fmt.Errorf("load canonical document: %w", err)
	}
	return []byte(row.CanonicalYAML), nil
}

// LoadActiveCanonicalYAML loads the canonical YAML document for the single
// active and valid configuration set in the given runtime scope.
func LoadActiveCanonicalYAML(db *gorm.DB, runtimeScope string) ([]byte, error) {
	if db == nil {
		return nil, errors.New("config control-plane database is required")
	}
	runtimeScope = strings.TrimSpace(runtimeScope)
	if runtimeScope == "" {
		return nil, errors.New("config runtime scope is required")
	}
	var sets []configSetRow
	if err := db.Where(
		"runtime_scope = ? AND status = ? AND validation_status = ?",
		runtimeScope, "active", "valid",
	).Limit(2).Find(&sets).Error; err != nil {
		return nil, fmt.Errorf("load active config set: %w", err)
	}
	if len(sets) == 0 {
		return nil, fmt.Errorf("no validated active config set for runtime scope %q", runtimeScope)
	}
	if len(sets) != 1 {
		return nil, fmt.Errorf("multiple validated active config sets for runtime scope %q", runtimeScope)
	}
	return ExportCanonicalYAML(db, sets[0].ID)
}

// rejectRawSecretsInYAML scans every scalar value in the YAML document and
// rejects any that matches a known live-secret shape. This is a conservative
// guard: it does not attempt to distinguish keys from values, and it does not
// rely on YAML key names.
func rejectRawSecretsInYAML(raw []byte) error {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return fmt.Errorf("parse YAML for secret scan: %w", err)
	}
	if err := walkYAMLForSecrets(&root, ""); err != nil {
		return err
	}
	return nil
}

func walkYAMLForSecrets(node *yaml.Node, path string) error {
	if node == nil {
		return nil
	}
	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			if err := walkYAMLForSecrets(child, path); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i].Value
			valuePath := key
			if path != "" {
				valuePath = path + "." + key
			}
			if err := walkYAMLForSecrets(node.Content[i+1], valuePath); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for i, child := range node.Content {
			childPath := fmt.Sprintf("%s[%d]", path, i)
			if err := walkYAMLForSecrets(child, childPath); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		if configControlPlaneSecretPattern.MatchString(node.Value) {
			return fmt.Errorf("canonical YAML contains a raw-looking secret at %q; use environment variable references (for example $VAR or api_key_env) instead", path)
		}
	case yaml.AliasNode:
		return walkYAMLForSecrets(node.Alias, path)
	}
	return nil
}

// newConfigSetID returns an opaque identifier for a new configuration set.
// The ID is unique within the database and safe to expose in logs.
func newConfigSetID() string {
	return fmt.Sprintf("set-%d", time.Now().UTC().UnixNano())
}

// newConfigChangeEventID returns an opaque identifier for a new change event.
func newConfigChangeEventID() string {
	return fmt.Sprintf("evt-%d", time.Now().UTC().UnixNano())
}

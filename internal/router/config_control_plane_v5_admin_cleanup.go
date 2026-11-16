// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"time"

	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

const v5AdminConfigCleanupMigrationID = 2026111601

// v5AdminConfigCleanupMigration removes v4-only serving-port route settings
// from stored canonical documents before v5 routers load them.
func v5AdminConfigCleanupMigration() MigrationDefinition {
	return FinalizeMigrationDefinition(MigrationDefinition{
		ID:               v5AdminConfigCleanupMigrationID,
		Scope:            configControlPlaneScope,
		Name:             "remove v4 serving-port admin settings from stored configs",
		Release:          "v5.0.0",
		Checksum:         MigrationChecksum("router-config.v5.admin-config-cleanup.v1"),
		SchemaVersion:    13,
		Transactional:    true,
		MaintenanceMode:  "maintenance",
		RollbackClass:    "restore-required",
		HandlerKey:       "router-config.v5.admin-cleanup.apply.v1@applyV5AdminConfigCleanup",
		PostconditionKey: "router-config.v5.admin-cleanup.verify.v1@verifyV5AdminConfigCleanup",
		Dependencies:     []int{adminKeyMigrationID},
		ExecutionMode:    "transactional",
		LockClass:        "maintenance",
		TimeoutClass:     "maintenance",
		Apply:            applyV5AdminConfigCleanup,
		Verify:           verifyV5AdminConfigCleanup,
	})
}

func cleanupRemovedV5AdminKeys(raw []byte) ([]byte, bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, false, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return raw, false, nil
	}
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "admin" || root.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		admin := root.Content[i+1]
		kept := make([]*yaml.Node, 0, len(admin.Content))
		changed := false
		for j := 0; j+1 < len(admin.Content); j += 2 {
			key := admin.Content[j].Value
			if key == "legacy_routes_on_serving_port" || key == "legacy_sunset" {
				changed = true
				continue
			}
			kept = append(kept, admin.Content[j], admin.Content[j+1])
		}
		if !changed {
			return raw, false, nil
		}
		admin.Content = kept
		out, err := yaml.Marshal(&doc)
		return out, true, err
	}
	return raw, false, nil
}

type v5AdminConfigDocument struct {
	ConfigSetID   string `gorm:"column:config_set_id"`
	CanonicalYAML string `gorm:"column:canonical_yaml"`
}

func eachV5AdminConfigDocument(tx *gorm.DB, visit func(v5AdminConfigDocument) error) error {
	last := ""
	for {
		var batch []v5AdminConfigDocument
		if err := tx.Raw(`SELECT config_set_id, canonical_yaml FROM router_config_documents WHERE config_set_id > ? ORDER BY config_set_id LIMIT 100`, last).Scan(&batch).Error; err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		for _, row := range batch {
			if err := visit(row); err != nil {
				return err
			}
			last = row.ConfigSetID
		}
	}
}

func applyV5AdminConfigCleanup(tx *gorm.DB) error {
	if !tx.Migrator().HasTable("router_config_documents") {
		return fmt.Errorf("required control-plane table router_config_documents is missing")
	}
	updatedAt := time.Now().UTC().Format(time.RFC3339Nano)
	return eachV5AdminConfigDocument(tx, func(row v5AdminConfigDocument) error {
		cleaned, changed, err := cleanupRemovedV5AdminKeys([]byte(row.CanonicalYAML))
		if err != nil {
			return fmt.Errorf("parse stored config %q: %w", row.ConfigSetID, err)
		}
		if !changed {
			return nil
		}
		log.Printf("router-config migration %d removed legacy admin settings from config set %q", v5AdminConfigCleanupMigrationID, row.ConfigSetID)
		digest := sha256.Sum256(cleaned)
		if err := tx.Exec(`UPDATE router_config_documents SET canonical_yaml = ?, content_sha256 = ?, updated_at = ? WHERE config_set_id = ?`, string(cleaned), hex.EncodeToString(digest[:]), updatedAt, row.ConfigSetID).Error; err != nil {
			return fmt.Errorf("clean stored config %q: %w", row.ConfigSetID, err)
		}
		return nil
	})
}

func verifyV5AdminConfigCleanup(tx *gorm.DB) error {
	if !tx.Migrator().HasTable("router_config_documents") {
		return fmt.Errorf("required control-plane table router_config_documents is missing")
	}
	return eachV5AdminConfigDocument(tx, func(row v5AdminConfigDocument) error {
		_, changed, err := cleanupRemovedV5AdminKeys([]byte(row.CanonicalYAML))
		if err != nil {
			return err
		}
		if changed {
			return fmt.Errorf("stored config %q still contains removed admin settings", row.ConfigSetID)
		}
		return nil
	})
}

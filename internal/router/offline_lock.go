// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"
)

func usageDBUsesSQLite(cfg UsageDBConfig) bool {
	if cfg.Enable != nil && !*cfg.Enable {
		return false
	}
	driver := strings.ToLower(strings.TrimSpace(cfg.Driver))
	return driver == "" || driver == "sqlite"
}

// AcquirePostgresConfigRuntimeLock holds a session advisory lock until release.
// The key is stable across router and routerctl processes.
func AcquirePostgresConfigRuntimeLock(db *gorm.DB, shared bool) (func() error, error) {
	if db == nil {
		return nil, fmt.Errorf("config database is required for runtime lock")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	conn, err := sqlDB.Conn(context.Background())
	if err != nil {
		return nil, err
	}
	fn := "pg_try_advisory_lock"
	unlock := "pg_advisory_unlock"
	if shared {
		fn = "pg_try_advisory_lock_shared"
		unlock = "pg_advisory_unlock_shared"
	}
	var acquired bool
	if err := conn.QueryRowContext(context.Background(), "SELECT "+fn+"(822, 1)").Scan(&acquired); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !acquired {
		_ = conn.Close()
		return nil, fmt.Errorf("offline operation refused: a router or maintenance command holds the config runtime lock")
	}
	return func() error {
		var released bool
		err := conn.QueryRowContext(context.Background(), "SELECT "+unlock+"(822, 1)").Scan(&released)
		closeErr := conn.Close()
		if err != nil {
			return err
		}
		return closeErr
	}, nil
}

type offlineMaintenanceRecord struct {
	ID       int64  `gorm:"column:id"`
	Kind     string `gorm:"column:kind"`
	Command  string `gorm:"column:command"`
	Revision int64  `gorm:"column:revision"`
	Actor    string `gorm:"column:actor"`
	Scope    string `gorm:"column:runtime_scope"`
	At       string `gorm:"column:at"`
}

func (offlineMaintenanceRecord) TableName() string { return "router_offline_operation_markers" }

const offlineOperationMarkerLimit = 128

// RecordOfflineConfigMaintenance appends provenance while the caller holds the
// exclusive runtime lock. Markers are consumed into the usage audit at startup.
func RecordOfflineConfigMaintenance(db *gorm.DB, command, scope, actor string) error {
	if db == nil || strings.TrimSpace(command) == "" || strings.TrimSpace(scope) == "" || strings.TrimSpace(actor) == "" {
		return fmt.Errorf("offline operation provenance requires a database, command, scope, and actor")
	}
	if !db.Migrator().HasTable(&offlineMaintenanceRecord{}) {
		return fmt.Errorf("offline operation marker table is missing; apply the config-control-plane migration first")
	}
	var count int64
	if err := db.Model(&offlineMaintenanceRecord{}).Count(&count).Error; err != nil {
		return err
	}
	if count >= offlineOperationMarkerLimit {
		return fmt.Errorf("offline provenance marker list is full; start the router to audit and consume existing markers")
	}
	var runtime configControlPlaneRuntimeRow
	result := db.Where("runtime_scope = ?", scope).First(&runtime)
	if result.Error != nil && result.Error != gorm.ErrRecordNotFound {
		return result.Error
	}
	return db.Create(&offlineMaintenanceRecord{Command: adminAuditFieldValue(command), Revision: runtime.ConfigRevision, Actor: adminAuditFieldValue(actor), Scope: adminAuditFieldValue(scope), Kind: "config", At: time.Now().UTC().Format(time.RFC3339Nano)}).Error
}

// RecordOfflineUsageMaintenance appends a marker to the usage database before
// an offline backup/restore operation. The table is installed by the usage
// migration, never created by a report or read-only path.
func RecordOfflineUsageMaintenance(db *gorm.DB, command, actor string) error {
	if db == nil || strings.TrimSpace(command) == "" || strings.TrimSpace(actor) == "" {
		return fmt.Errorf("offline usage provenance requires a database, command, and actor")
	}
	if !db.Migrator().HasTable(&offlineMaintenanceRecord{}) {
		return nil // Preserve backup and restore compatibility with unmigrated v4.8 databases.
	}
	var count int64
	if err := db.Model(&offlineMaintenanceRecord{}).Count(&count).Error; err != nil {
		return err
	}
	if count >= offlineOperationMarkerLimit {
		return fmt.Errorf("offline provenance marker list is full; start the router to audit and consume existing markers")
	}
	return db.Create(&offlineMaintenanceRecord{Command: adminAuditFieldValue(command), Actor: adminAuditFieldValue(actor), Scope: "usage", Kind: "usage", At: time.Now().UTC().Format(time.RFC3339Nano)}).Error
}

// CheckOfflineUsageMarkerCapacity lets an operation refuse before it changes
// the database when the bounded provenance queue cannot accept another row.
func CheckOfflineUsageMarkerCapacity(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(&offlineMaintenanceRecord{}) {
		return nil // v4.8 databases have no marker table; backup and restore remain supported.
	}
	var count int64
	if err := db.Model(&offlineMaintenanceRecord{}).Count(&count).Error; err != nil {
		return err
	}
	if count >= offlineOperationMarkerLimit {
		return fmt.Errorf("offline provenance marker list is full; start the router to audit and consume existing markers")
	}
	return nil
}

func consumeOfflineConfigMaintenance(configDB, usageDB *gorm.DB) error {
	if configDB == nil || !configDB.Migrator().HasTable(&offlineMaintenanceRecord{}) {
		return nil
	}
	var markers []offlineMaintenanceRecord
	if err := configDB.Where("kind = ?", "config").Order("id ASC").Limit(offlineOperationMarkerLimit).Find(&markers).Error; err != nil {
		return err
	}
	if len(markers) == 0 {
		return nil
	}
	if usageDB == nil {
		return configDB.Transaction(func(tx *gorm.DB) error {
			for _, marker := range markers {
				if _, err := time.Parse(time.RFC3339Nano, marker.At); err != nil {
					return fmt.Errorf("offline marker %d has invalid time", marker.ID)
				}
				var setID string
				if err := tx.Table("router_config_sets").Select("id").Where("runtime_scope = ?", marker.Scope).Order("created_at DESC").Limit(1).Scan(&setID).Error; err != nil {
					return err
				}
				outcome := "attempted"
				if setID == "" {
					outcome = "aborted"
					// The control-plane audit row has a required config-set FK. Use
					// one reserved, non-active anchor when an offline command ran
					// before the first config set was created.
					setID = "__offline_audit_anchor__"
					if err := tx.Exec(`INSERT INTO router_config_sets (id, runtime_scope, name, status, validation_status, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`, setID, "__system__", "offline-audit-anchor", "archived", "not_applicable", "router", marker.At).Error; err != nil {
						return err
					}
				}
				eventID := fmt.Sprintf("offline-config-%s-%d", marker.Scope, marker.ID)
				requestID := fmt.Sprintf("offline-config-%s-marker-%d", marker.Scope, marker.ID)
				detail := fmt.Sprintf("command=%s revision=%d actor=%s", marker.Command, marker.Revision, marker.Actor)
				unlock, err := lockAdminAuditChain(tx, "control", string(AdminGlobalDomain))
				if err != nil {
					return err
				}
				defer unlock()
				var existing int64
				if err := tx.Model(&adminAuditControlExportRecord{}).Where("event_id = ?", eventID).Count(&existing).Error; err != nil {
					return err
				}
				if existing == 0 {
					if err := insertChainedConfigChangeEventTx(tx, chainedConfigChangeEvent{
						EventID: eventID, RuntimeScope: marker.Scope, ConfigSetID: setID,
						Action: "offline_config_maintenance", Actor: marker.Actor, At: marker.At,
						ConfigRevision: marker.Revision, Detail: detail, ActorKind: "offline",
						AuthzDomain: string(AdminGlobalDomain), Object: "admin:config", AuthzAction: "maintenance",
						OperationID: "offline_config_maintenance", RequestID: requestID, Outcome: outcome,
					}); err != nil {
						return err
					}
				}
			}
			ids := make([]int64, 0, len(markers))
			for _, marker := range markers {
				ids = append(ids, marker.ID)
			}
			return tx.Where("id IN ?", ids).Delete(&offlineMaintenanceRecord{}).Error
		})
	}
	if err := usageDB.Transaction(func(tx *gorm.DB) error {
		for _, marker := range markers {
			if _, err := time.Parse(time.RFC3339Nano, marker.At); err != nil {
				return fmt.Errorf("offline marker %d has invalid time", marker.ID)
			}
			requestID := fmt.Sprintf("offline-config-%s-marker-%d", marker.Scope, marker.ID)
			var existing int64
			if err := tx.Table(usageAdminAuditTable).Where("operation_id = ? AND request_id = ?", "offline_config_maintenance", requestID).Count(&existing).Error; err != nil {
				return err
			}
			if existing != 0 {
				continue
			}
			markerAt, _ := time.Parse(time.RFC3339Nano, marker.At)
			if err := insertAdminAuditUsageTx(tx, AdminAuditRecord{
				EventID: fmt.Sprintf("offline-config-%s-marker-%d", marker.Scope, marker.ID), At: markerAt, PrincipalSubject: marker.Actor, PrincipalKind: "offline",
				Domain: AdminGlobalDomain, Object: "admin:config", Action: "maintenance",
				OperationID: "offline_config_maintenance", RequestID: requestID,
				Outcome: "attempted", ConfigRevision: marker.Revision,
				Detail: fmt.Sprintf("command=%s revision=%d actor=%s", marker.Command, marker.Revision, marker.Actor),
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	ids := make([]int64, 0, len(markers))
	for _, marker := range markers {
		ids = append(ids, marker.ID)
	}
	return configDB.Where("id IN ?", ids).Delete(&offlineMaintenanceRecord{}).Error
}

func consumeOfflineUsageMaintenance(usageDB *gorm.DB) error {
	if usageDB == nil || !usageDB.Migrator().HasTable(&offlineMaintenanceRecord{}) {
		return nil
	}
	var markers []offlineMaintenanceRecord
	if err := usageDB.Where("kind = ?", "usage").Order("id ASC").Limit(offlineOperationMarkerLimit).Find(&markers).Error; err != nil {
		return err
	}
	if len(markers) == 0 {
		return nil
	}
	return usageDB.Transaction(func(tx *gorm.DB) error {
		for _, marker := range markers {
			if _, err := time.Parse(time.RFC3339Nano, marker.At); err != nil {
				return fmt.Errorf("offline usage marker %d has invalid time", marker.ID)
			}
			requestID := fmt.Sprintf("offline-usage-marker-%d", marker.ID)
			var existing int64
			if err := tx.Table(usageAdminAuditTable).Where("operation_id = ? AND request_id = ?", "offline_usage_maintenance", requestID).Count(&existing).Error; err != nil {
				return err
			}
			if existing != 0 {
				continue
			}
			markerAt, _ := time.Parse(time.RFC3339Nano, marker.At)
			if err := insertAdminAuditUsageTx(tx, AdminAuditRecord{EventID: requestID, At: markerAt, PrincipalSubject: marker.Actor, PrincipalKind: "offline", Domain: AdminGlobalDomain, Object: "admin:config", Action: "maintenance", OperationID: "offline_usage_maintenance", RequestID: requestID, Outcome: "committed", ConfigRevision: marker.Revision, Detail: fmt.Sprintf("command=%s revision=%d actor=%s", marker.Command, marker.Revision, marker.Actor)}); err != nil {
				return err
			}
		}
		ids := make([]int64, 0, len(markers))
		for _, marker := range markers {
			ids = append(ids, marker.ID)
		}
		return tx.Where("id IN ?", ids).Delete(&offlineMaintenanceRecord{}).Error
	})
}

// AcquireUsageRuntimeLock coordinates a live router with offline SQLite
// maintenance commands. Serving processes hold a shared lock; maintenance
// commands request the exclusive lock without waiting.
func AcquireUsageRuntimeLock(path string, exclusive bool) (*os.File, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("SQLite usage path is required for offline lock")
	}
	resolved, err := canonicalRuntimeDBPath(path)
	if err != nil {
		return nil, fmt.Errorf("resolve runtime database path: %w", err)
	}
	lockPath := resolved + ".router-online.lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := acquireUsageFileLock(f, exclusive); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func canonicalRuntimeDBPath(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	parent, base := filepath.Dir(abs), filepath.Base(abs)
	if resolved, e := filepath.EvalSymlinks(parent); e == nil {
		parent = resolved
	}
	joined := filepath.Join(parent, base)
	if resolved, e := filepath.EvalSymlinks(joined); e == nil {
		joined = resolved
	} else if !os.IsNotExist(e) {
		return "", e
	}
	return joined, nil
}

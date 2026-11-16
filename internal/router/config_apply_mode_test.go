// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestConfigApplyModesInventory(t *testing.T) {
	modes := ConfigApplyModes()
	got := map[string]string{}
	for _, item := range modes {
		if item.Mode != "hot_reload" && item.Mode != "restart_required" {
			t.Errorf("admin config schema path %s has no apply-mode classification: %q", item.Path, item.Mode)
		}
		if _, exists := got[item.Path]; exists {
			t.Errorf("admin config schema path %s appears more than once", item.Path)
		}
		got[item.Path] = item.Mode
	}
	for path, want := range map[string]string{
		"server.listen":                   "restart_required",
		"server.usage_db.path":            "restart_required",
		"server.tls.min_version":          "restart_required",
		"admin.tls.mode":                  "restart_required",
		"server.responses.model_identity": "hot_reload",
		"providers.*.api_key_env":         "restart_required",
		"callers.*.id":                    "hot_reload",
		"server.read_timeout_ms":          "restart_required",
		"server.write_timeout_ms":         "restart_required",
		"server.idle_timeout_ms":          "restart_required",
		"server.stream_idle_timeout_ms":   "restart_required",
		"state_path":                      "restart_required",
	} {
		if got[path] != want {
			t.Errorf("apply mode %s = %q, want %q", path, got[path], want)
		}
	}
	if mode, _ := modeForPath("server.unknown_setting"); mode != "unknown" {
		t.Fatalf("unknown config field mode=%q", mode)
	}
	// The Config YAML tags are the canonical admin configuration schema. Every
	// generated leaf must be represented exactly once and resolve to a mode;
	// a newly added field therefore fails CI until it is classified.
	var schemaPaths []string
	collectConfigPaths(reflect.TypeOf(Config{}), "", &schemaPaths)
	if len(schemaPaths) != len(got) {
		t.Fatalf("apply-mode inventory has %d paths; admin config schema has %d", len(got), len(schemaPaths))
	}
}

func TestAdminV1ConfigStatusIncludesApplyModes(t *testing.T) {
	harness := newAdminV1DBHarness(t)
	recorder := harness.do(t, http.MethodGet, "/admin/v1/config/status", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("config status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var status struct {
		ApplyModes []ConfigApplyMode `json:"apply_modes"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.ApplyModes) == 0 {
		t.Fatal("versioned config status omitted apply-mode inventory")
	}
	for _, item := range status.ApplyModes {
		if item.Path == "server.listen" && item.Mode == "restart_required" {
			return
		}
	}
	t.Fatalf("versioned config status omitted server.listen restart requirement: %+v", status.ApplyModes)
}

func TestUsageRuntimeLockExcludesOfflineWriter(t *testing.T) {
	path := t.TempDir() + "/usage.db"
	store, err := newUsageStore(UsageDBConfig{Driver: "sqlite", Path: path, MigrationPolicy: usageDBMigrationPolicyAutoSafe})
	if err != nil {
		t.Fatalf("start serving usage store: %v", err)
	}
	if _, err := AcquireUsageRuntimeLock(path, true); err == nil {
		t.Fatal("offline writer acquired exclusive lock while serving usage store was running")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	exclusive, err := AcquireUsageRuntimeLock(path, true)
	if err != nil {
		t.Fatalf("offline writer could not lock after serving store closed: %v", err)
	}
	if err := exclusive.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConfigSourceRuntimeLockUsesItsOwnDatabasePath(t *testing.T) {
	dir := t.TempDir()
	usage, err := AcquireUsageRuntimeLock(dir+"/usage.db", false)
	if err != nil {
		t.Fatal(err)
	}
	defer usage.Close()
	config, err := AcquireUsageRuntimeLock(dir+"/config.db", false)
	if err != nil {
		t.Fatal(err)
	}
	defer config.Close()
	if offline, err := AcquireUsageRuntimeLock(dir+"/config.db", true); err == nil {
		_ = offline.Close()
		t.Fatal("offline config writer acquired config DB lock while serving")
	}
	offlineUsage, err := AcquireUsageRuntimeLock(dir+"/usage.db", true)
	if err == nil {
		_ = offlineUsage.Close()
		t.Fatal("unexpected usage lock availability")
	}
}

func TestOfflineProvenanceConsumedAtStartup(t *testing.T) {
	dir := t.TempDir()
	store, err := newUsageStore(UsageDBConfig{Driver: "sqlite", Path: dir + "/usage.sqlite", MigrationPolicy: usageDBMigrationPolicyAutoSafe})
	if err != nil {
		t.Fatalf("start serving usage store: %v", err)
	}
	defer store.Close()
	config, closer, err := openMigratedConfigDBForOfflineTest(t, dir+"/config.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer closer()
	seedOfflineConfigRuntime(t, config, "prod")
	if err := RecordOfflineConfigMaintenance(config, "config import", "prod", "operator"); err != nil {
		t.Fatal(err)
	}
	if err := consumeOfflineConfigMaintenance(config, store.db); err != nil {
		t.Fatal(err)
	}
	var rows int64
	if err := store.db.Table(usageAdminAuditTable).Where("operation_id = ?", "offline_config_maintenance").Count(&rows).Error; err != nil || rows != 1 {
		t.Fatalf("startup audit rows=%d err=%v", rows, err)
	}
	var revision int64
	if err := store.db.Table(usageAdminAuditTable).Where("operation_id = ?", "offline_config_maintenance").Select("config_revision").Scan(&revision).Error; err != nil || revision != 7 {
		t.Fatalf("recorded config revision=%d err=%v", revision, err)
	}
	var markers int64
	if err := config.Model(&offlineMaintenanceRecord{}).Count(&markers).Error; err != nil || markers != 0 {
		t.Fatalf("remaining markers=%d err=%v", markers, err)
	}
}

func TestOfflineProvenanceWithoutUsageDBWritesControlAudit(t *testing.T) {
	config, closer, err := openMigratedConfigDBForOfflineTest(t, t.TempDir()+"/config.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer closer()
	seedOfflineConfigRuntime(t, config, "prod")
	if err := RecordOfflineConfigMaintenance(config, "emergency activate", "prod", "operator"); err != nil {
		t.Fatal(err)
	}
	if err := consumeOfflineConfigMaintenance(config, nil); err != nil {
		t.Fatal(err)
	}
	var rows int64
	if err := config.Table("router_config_change_events").Where("request_id LIKE ?", "offline-config-prod-marker-%").Count(&rows).Error; err != nil || rows != 1 {
		t.Fatalf("control-plane audit rows=%d err=%v", rows, err)
	}
}

func TestOfflineMarkerQueueIsMigratedScopedAndBounded(t *testing.T) {
	config, closer, err := openMigratedConfigDBForOfflineTest(t, t.TempDir()+"/config.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer closer()
	if !config.Migrator().HasTable(&offlineMaintenanceRecord{}) {
		t.Fatal("config marker migration did not create its table")
	}
	seedOfflineConfigRuntime(t, config, "prod")
	markers := make([]offlineMaintenanceRecord, offlineOperationMarkerLimit)
	for i := range markers {
		markers[i] = offlineMaintenanceRecord{Command: "prior operation", Actor: "operator", Scope: "prod", At: time.Now().UTC().Format(time.RFC3339Nano)}
	}
	if err := config.Create(&markers).Error; err != nil {
		t.Fatal(err)
	}
	if err := RecordOfflineConfigMaintenance(config, "config activate", "prod", "operator"); err == nil || !strings.Contains(err.Error(), "marker list is full") {
		t.Fatalf("full config marker queue error=%v", err)
	}
	var count int64
	if err := config.Model(&offlineMaintenanceRecord{}).Count(&count).Error; err != nil || count != offlineOperationMarkerLimit {
		t.Fatalf("refused queue count=%d err=%v", count, err)
	}

	usage, err := newUsageStore(UsageDBConfig{Driver: "sqlite", Path: t.TempDir() + "/usage.sqlite", MigrationPolicy: usageDBMigrationPolicyAutoSafe})
	if err != nil {
		t.Fatal(err)
	}
	defer usage.Close()
	if !usage.db.Migrator().HasTable(&offlineMaintenanceRecord{}) {
		t.Fatal("usage marker migration did not create its table")
	}
	if err := usage.db.Create(&markers).Error; err != nil {
		t.Fatal(err)
	}
	if err := CheckOfflineUsageMarkerCapacity(usage.db); err == nil || !strings.Contains(err.Error(), "marker list is full") {
		t.Fatalf("full usage marker queue error=%v", err)
	}
}

func TestOfflineMarkerReadValidationDoesNotAutoMigrate(t *testing.T) {
	path := t.TempDir() + "/usage.sqlite"
	if store, err := OpenUsageStore(UsageDBConfig{Driver: "sqlite", Path: path, MigrationPolicy: usageDBMigrationPolicyValidate}); err == nil {
		_ = store.Close()
		t.Fatal("validate policy accepted a database without its migration ledger")
	}
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { raw, _ := db.DB(); _ = raw.Close() }()
	if db.Migrator().HasTable(&offlineMaintenanceRecord{}) {
		t.Fatal("read/validate open auto-created offline marker table")
	}
}

func seedOfflineConfigRuntime(t *testing.T, db *gorm.DB, scope string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.Exec(`INSERT INTO router_config_sets (id, runtime_scope, name, status, validation_status, created_at) VALUES (?, ?, ?, ?, ?, ?)`, "offline-marker-fixture", scope, "offline-marker-fixture", "active", "valid", now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO router_config_documents (config_set_id, canonical_yaml, content_sha256, updated_at) VALUES (?, ?, ?, ?)`, "offline-marker-fixture", "{}", "fixture", now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO router_config_runtime (runtime_scope, config_revision, active_set_id) VALUES (?, ?, ?)`, scope, 7, "offline-marker-fixture").Error; err != nil {
		t.Fatal(err)
	}
}

func openMigratedConfigDBForOfflineTest(t *testing.T, path string) (*gorm.DB, func() error, error) {
	t.Helper()
	runner, closer, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		return nil, nil, err
	}
	applyConfigControlPlaneMigrationsForTest(t, runner)
	return runner.db, closer, nil
}

func TestUsageRuntimeLockReleasedWhenStoreStartupFails(t *testing.T) {
	path := t.TempDir() + "/usage.db"
	wantErr := errors.New("fixture open failure")
	_, err := newUsageStoreWithOpener(UsageDBConfig{Driver: "sqlite", Path: path}, func(UsageDBConfig) (*usageStore, error) {
		return nil, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("startup error = %v, want %v", err, wantErr)
	}
	lock, err := AcquireUsageRuntimeLock(path, true)
	if err != nil {
		t.Fatalf("startup failure leaked shared lock: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}

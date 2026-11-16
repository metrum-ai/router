// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
		"server.security.events.enabled":  "restart_required",
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
	// Compare the committed path-to-mode contract with the reflected Config
	// schema in both directions, so missing and stale classifications fail CI.
	var schemaPaths []string
	collectConfigPaths(reflect.TypeOf(Config{}), "", &schemaPaths)
	schema := make(map[string]struct{}, len(schemaPaths))
	for _, path := range schemaPaths {
		if _, duplicate := schema[path]; duplicate {
			t.Errorf("admin config schema path %s appears more than once", path)
		}
		schema[path] = struct{}{}
		if _, classified := configApplyModeInventory[path]; !classified {
			t.Errorf("admin config schema path %s is missing from the committed apply-mode inventory", path)
		}
	}
	for path := range configApplyModeInventory {
		if _, exists := schema[path]; !exists {
			t.Errorf("committed apply-mode inventory contains stale path %s", path)
		}
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
	var marker offlineMaintenanceRecord
	if err := config.Where("command = ?", "config import").First(&marker).Error; err != nil {
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
	var recordedAt string
	if err := store.db.Table(usageAdminAuditTable).Where("operation_id = ?", "offline_config_maintenance").Select("at").Scan(&recordedAt).Error; err != nil {
		t.Fatal(err)
	}
	recordedTime, recordedErr := time.Parse(time.RFC3339Nano, recordedAt)
	markerTime, markerErr := time.Parse(time.RFC3339Nano, marker.At)
	if recordedErr != nil || markerErr != nil || !recordedTime.Equal(markerTime) {
		t.Fatalf("audit timestamp=%q, want marker timestamp %q", recordedAt, marker.At)
	}
	var markers int64
	if err := config.Model(&offlineMaintenanceRecord{}).Count(&markers).Error; err != nil || markers != 0 {
		t.Fatalf("remaining markers=%d err=%v", markers, err)
	}
}

func TestOfflineProvenanceWithoutUsageDBWritesControlAudit(t *testing.T) {
	dir := t.TempDir()
	configPath := dir + "/config.sqlite"
	config, closer, err := openMigratedConfigDBForOfflineTest(t, configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordOfflineConfigMaintenance(config, "emergency activate", "prod", "operator"); err != nil {
		t.Fatal(err)
	}
	_ = closer()
	cfg := testConfig(t, "http://127.0.0.1:1", "test-key", dir)
	cfg.Server.ConfigSource = ConfigSourceConfig{Mode: "database", Driver: "sqlite", Path: configPath, RuntimeScope: "prod"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("start service and consume marker without usage DB: %v", err)
	}
	defer svc.Close()
	var rows int64
	if err := svc.configControlPlane.Table("router_config_change_events").Where("request_id LIKE ? AND outcome = ?", "offline-config-prod-marker-%", "aborted").Count(&rows).Error; err != nil || rows != 1 {
		t.Fatalf("control-plane audit rows=%d err=%v", rows, err)
	}
	if err := svc.configControlPlane.Model(&offlineMaintenanceRecord{}).Count(&rows).Error; err != nil || rows != 0 {
		t.Fatalf("unconsumed offline markers=%d err=%v", rows, err)
	}
	export := adminAuditExportHandlers(nil, svc.configControlPlane)["admin.audit.export"]
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/audit-events/export", nil)
	request = request.WithContext(WithAdminAuditMeta(context.Background(), AdminAuditMeta{Subject: "operator", Domain: AdminGlobalDomain}))
	response := httptest.NewRecorder()
	export(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("offline marker control export=%d body=%s", response.Code, response.Body.String())
	}
	if err := VerifyAdminAuditExport(strings.NewReader(response.Body.String())); err != nil {
		t.Fatalf("verify offline marker control export: %v", err)
	}
}

func TestOfflineConsumersSeparateMarkersWhenDatabasesShareDSN(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/shared.sqlite"
	config, closeConfig, err := openMigratedConfigDBForOfflineTest(t, path)
	if err != nil {
		t.Fatal(err)
	}
	defer closeConfig()
	usage, err := newUsageStore(UsageDBConfig{Driver: "sqlite", Path: path, MigrationPolicy: usageDBMigrationPolicyAutoSafe})
	if err != nil {
		t.Fatal(err)
	}
	defer usage.Close()
	seedOfflineConfigRuntime(t, config, "usage")
	if err := RecordOfflineConfigMaintenance(config, "config import", "usage", "operator"); err != nil {
		t.Fatal(err)
	}
	if err := RecordOfflineUsageMaintenance(config, "usage backup", "operator"); err != nil {
		t.Fatal(err)
	}
	if err := consumeOfflineConfigMaintenance(config, usage.db); err != nil {
		t.Fatal(err)
	}
	if err := consumeOfflineUsageMaintenance(usage.db); err != nil {
		t.Fatal(err)
	}
	var configRows, usageRows int64
	if err := usage.db.Table(usageAdminAuditTable).Where("operation_id = ?", "offline_config_maintenance").Count(&configRows).Error; err != nil {
		t.Fatal(err)
	}
	if err := usage.db.Table(usageAdminAuditTable).Where("operation_id = ?", "offline_usage_maintenance").Count(&usageRows).Error; err != nil {
		t.Fatal(err)
	}
	if configRows != 1 || usageRows != 1 {
		t.Fatalf("shared DB audit counts config=%d usage=%d", configRows, usageRows)
	}
	var remaining int64
	if err := config.Model(&offlineMaintenanceRecord{}).Count(&remaining).Error; err != nil || remaining != 0 {
		t.Fatalf("shared DB markers=%d err=%v", remaining, err)
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
		markers[i] = offlineMaintenanceRecord{Kind: "config", Command: "prior operation", Actor: "operator", Scope: "prod", At: time.Now().UTC().Format(time.RFC3339Nano)}
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
	for i := range markers {
		markers[i].Kind = "usage"
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

func TestOfflineMarkerMigrationUsesNextCurrentMainSchemas(t *testing.T) {
	control := offlineOperationMarkerControlMigrationDefinition()
	usage := offlineOperationMarkerUsageMigrationDefinition()
	if control.ID != 2026111627 || usage.ID != 2026111627 {
		t.Fatalf("offline marker migration IDs: config=%d usage=%d", control.ID, usage.ID)
	}
	if control.SchemaVersion != 19 || usage.SchemaVersion != 23 {
		t.Fatalf("offline marker schema versions: config=%d usage=%d", control.SchemaVersion, usage.SchemaVersion)
	}
	if len(control.Dependencies) != 1 || control.Dependencies[0] != configControlPlaneHoneytokenMigrationID {
		t.Fatalf("config migration dependencies=%v", control.Dependencies)
	}
	if len(usage.Dependencies) != 1 || usage.Dependencies[0] != usageHierarchyAuditMigrationID {
		t.Fatalf("usage migration dependencies=%v", usage.Dependencies)
	}
}

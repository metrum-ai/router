// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sqliteDriver "github.com/glebarez/sqlite"
	detect "github.com/metrum-ai/router/internal/security/detect"
	"github.com/metrum-ai/router/pkg/detector"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

type autoContainmentTestDetector struct{}

func (autoContainmentTestDetector) Detect(context.Context, detector.Event) ([]detector.Signal, error) {
	return []detector.Signal{{SchemaVersion: detector.SchemaVersion, DetectorID: "test-detector", Severity: "critical", Score: 1}}, nil
}

type countingAutoContainmentTestDetector struct{ calls *atomic.Int32 }

func (d countingAutoContainmentTestDetector) Detect(context.Context, detector.Event) ([]detector.Signal, error) {
	signals := []detector.Signal{{SchemaVersion: detector.SchemaVersion, DetectorID: "test-detector", Severity: "critical", Score: 1}}
	d.calls.Add(1)
	return signals, nil
}

func openAutomaticContainmentTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqliteDriver.Open(filepath.Join(t.TempDir(), name)), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyUsageAdminAuditMigration(db); err != nil {
		t.Fatal(err)
	}
	if err := applyAutomaticContainmentMigration(db); err != nil {
		t.Fatal(err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return db
}

func TestAutoContainmentConfigValidation(t *testing.T) {
	valid := AutoContainmentConfig{Mode: "enforce", MaxActionsPerMinute: 5, UnlimitedThrottleLimit: 100, ExemptCallers: []string{"emergency"}, Rules: []AutoContainmentRuleConfig{{DetectorID: "honeytoken", Severity: "critical", Action: "suspend", TTLMS: 60000}}}
	if err := validateAutoContainment(valid); err != nil {
		t.Fatalf("valid automatic containment config: %v", err)
	}
	for _, cfg := range []AutoContainmentConfig{
		{Mode: "enforce"},
		{Mode: "enforce", MaxActionsPerMinute: 5, Rules: []AutoContainmentRuleConfig{{DetectorID: "honeytoken", Severity: "critical", Action: "suspend", TTLMS: 0}}},
		{Mode: "enforce", MaxActionsPerMinute: 5, Rules: []AutoContainmentRuleConfig{{DetectorID: "honeytoken", Severity: "critical", Action: "throttle", TTLMS: 60000}}},
		{Mode: "enforce", MaxActionsPerMinute: 5, Rules: []AutoContainmentRuleConfig{{DetectorID: "honeytoken", Severity: "critical", Action: "revoke", TTLMS: 60000}}},
		{Mode: "enforce", MaxActionsPerMinute: 5, Rules: []AutoContainmentRuleConfig{{DetectorID: "honeytoken", Severity: "critical", Action: "throttle", ThrottlePercent: 10, TTLMS: 60000}}},
	} {
		if err := validateAutoContainment(cfg); err == nil {
			t.Fatalf("invalid config accepted: %+v", cfg)
		}
	}
}

func TestAutoContainmentSkipsHoneytokenCallers(t *testing.T) {
	db := openAutomaticContainmentTestDB(t, "containment-honeytoken.sqlite")
	svc := &Service{
		cfg: &Config{Callers: []CallerConfig{{ID: "decoy", Honeytoken: &HoneytokenConfig{Label: "plant"}}}, Server: ServerConfig{AutoContainment: AutoContainmentConfig{
			Mode: "enforce", MaxActionsPerMinute: 5,
			Rules: []AutoContainmentRuleConfig{{DetectorID: "honeytoken", Severity: "critical", Action: "suspend", TTLMS: 60000}},
		}}},
		usage: &usageStore{db: db},
	}
	svc.applyAutomaticContainmentResult(detect.Result{DetectorID: "honeytoken", CallerID: "decoy", Enforce: true, Mode: "enforce", Signals: []detector.Signal{{SchemaVersion: detector.SchemaVersion, DetectorID: "honeytoken", Severity: "critical", Score: 1}}})
	var count int64
	if err := db.Model(&automaticContainmentActionRecord{}).Where("caller_id = ?", "decoy").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("automatic containment actions for honeytoken=%d err=%v", count, err)
	}
}

func TestAutoContainmentEnforceFiltersCapsAndThrottlesUnlimitedCallers(t *testing.T) {
	db, err := gorm.Open(sqliteDriver.Open(filepath.Join(t.TempDir(), "containment-filter.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyUsageAdminAuditMigration(db); err != nil {
		t.Fatal(err)
	}
	if err := applyAutomaticContainmentMigration(db); err != nil {
		t.Fatal(err)
	}
	callers := make(map[string]*callerRuntime, 20)
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("caller-%02d", i)
		callers[id] = &callerRuntime{cfg: CallerConfig{ID: id}}
	}
	svc := &Service{
		cfg: &Config{Server: ServerConfig{AutoContainment: AutoContainmentConfig{
			Mode: "enforce", MaxActionsPerMinute: 3, UnlimitedThrottleLimit: 100,
			ExemptCallers: []string{"caller-00"},
			Rules:         []AutoContainmentRuleConfig{{DetectorID: "test-detector", Severity: "critical", Action: "throttle", ThrottlePercent: 25, TTLMS: 60000}},
		}}},
		usage: &usageStore{db: db}, quota: &quotaStore{callers: callers},
	}
	result := func(callerID string, enforce bool) detect.Result {
		return detect.Result{DetectorID: "test-detector", CallerID: callerID, Enforce: enforce,
			Signals: []detector.Signal{{SchemaVersion: detector.SchemaVersion, DetectorID: "test-detector", Severity: "critical", Score: 1}}}
	}
	svc.applyAutomaticContainmentResult(result("caller-00", true))
	svc.applyAutomaticContainmentResult(result("ignored-caller", false))
	for i := 1; i < 20; i++ {
		svc.applyAutomaticContainmentResult(result(fmt.Sprintf("caller-%02d", i), true))
	}
	var count int64
	if err := db.Model(&automaticContainmentEventRecord{}).Count(&count).Error; err != nil || count != 3 {
		t.Fatalf("storm event rows = %d, err %v; want cap of 3", count, err)
	}
	if got := callers["caller-01"].effectiveRateLimit(0); got != 25 {
		t.Fatalf("unlimited caller throttle = %d, want configured default throttle limit 25", got)
	}
	if got := callers["caller-00"].effectiveRateLimit(0); got != 0 {
		t.Fatalf("exempt caller throttle = %d, want unlimited 0", got)
	}
	var exemptActions, nonEnforceActions int64
	if err := db.Model(&automaticContainmentEventRecord{}).Where("caller_id = ?", "caller-00").Count(&exemptActions).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&automaticContainmentEventRecord{}).Where("caller_id = ?", "ignored-caller").Count(&nonEnforceActions).Error; err != nil {
		t.Fatal(err)
	}
	if exemptActions != 0 || nonEnforceActions != 0 {
		t.Fatalf("exempt actions=%d non-enforce actions=%d; want neither caller actioned", exemptActions, nonEnforceActions)
	}
}

func TestAutomaticContainmentExemptionReleasesActiveThrottleOnRefresh(t *testing.T) {
	db := openAutomaticContainmentTestDB(t, "containment-exempt-refresh.sqlite")
	caller := &callerRuntime{cfg: CallerConfig{ID: "caller-a"}}
	cfg := &Config{Server: ServerConfig{AutoContainment: AutoContainmentConfig{
		Mode: "enforce", MaxActionsPerMinute: 5, UnlimitedThrottleLimit: 100,
		Rules: []AutoContainmentRuleConfig{{DetectorID: "test-detector", Severity: "critical", Action: "throttle", ThrottlePercent: 10, TTLMS: 60000}},
	}}}
	snapshot := NewConfigSnapshot()
	snapshot.Store(cfg)
	svc := &Service{cfg: cfg, configSnapshot: snapshot, quota: &quotaStore{callers: map[string]*callerRuntime{"caller-a": caller}}, usage: &usageStore{db: db}}
	svc.applyAutomaticContainmentResult(detect.Result{DetectorID: "test-detector", CallerID: "caller-a", Enforce: true, Signals: []detector.Signal{{SchemaVersion: detector.SchemaVersion, DetectorID: "test-detector", Severity: "critical", Score: 1}}})
	if got := caller.effectiveRateLimit(100); got != 10 {
		t.Fatalf("throttle limit before exemption=%d, want 10", got)
	}
	reloaded := *cfg
	reloaded.Server.AutoContainment.ExemptCallers = []string{"caller-a"}
	snapshot.Store(&reloaded)
	svc.syncAutomaticContainment()
	if got := caller.effectiveRateLimit(100); got != 100 {
		t.Fatalf("throttle limit after exempt refresh=%d, want base 100", got)
	}
	var rows int64
	if err := db.Model(&automaticContainmentActionRecord{}).Where("caller_id = ?", "caller-a").Count(&rows).Error; err != nil || rows != 1 {
		t.Fatalf("active action row count=%d err=%v; exemption must retain the TTL record", rows, err)
	}
}

func TestAutomaticSuspendFailureRollsBackActionCapForRetry(t *testing.T) {
	db := openAutomaticContainmentTestDB(t, "containment-suspend-rollback.sqlite")
	controlPlane, err := gorm.Open(sqliteDriver.Open(filepath.Join(t.TempDir(), "missing-config-control-plane.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		cfg: &Config{Server: ServerConfig{ConfigSource: ConfigSourceConfig{Mode: "database"}, AutoContainment: AutoContainmentConfig{
			Mode: "enforce", MaxActionsPerMinute: 1,
			Rules: []AutoContainmentRuleConfig{{DetectorID: "test-detector", Severity: "critical", Action: "suspend", TTLMS: 60000}},
		}}},
		configControlPlane: controlPlane,
		usage:              &usageStore{db: db},
	}
	result := detect.Result{DetectorID: "test-detector", CallerID: "caller-a", Enforce: true, Signals: []detector.Signal{{SchemaVersion: detector.SchemaVersion, DetectorID: "test-detector", Severity: "critical", Score: 1}}}
	svc.applyAutomaticContainmentResult(result)
	// A second attempt must get through the one-action cap too. The control-plane
	// request still fails, but it must create a fresh (then rolled-back) attempt.
	svc.applyAutomaticContainmentResult(result)
	var actions, capRows int64
	if err := db.Model(&automaticContainmentActionRecord{}).Count(&actions).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&automaticContainmentEventRecord{}).Count(&capRows).Error; err != nil {
		t.Fatal(err)
	}
	if actions != 0 || capRows != 0 {
		t.Fatalf("failed suspend left actions=%d cap rows=%d; want both zero for retry", actions, capRows)
	}
	var failedAudits int64
	if err := db.Table(usageAdminAuditTable).Where("principal_subject = ? AND outcome = ? AND reason_code = ?", "test-detector", "failed", "automatic_suspend_failed").Count(&failedAudits).Error; err != nil || failedAudits != 2 {
		t.Fatalf("failed suspension audit rows=%d err=%v; want 2 attempts", failedAudits, err)
	}
	controlPlaneSQL, err := controlPlane.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := controlPlaneSQL.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAutomaticContainmentListenerFollowsReloadedMode(t *testing.T) {
	db, err := gorm.Open(sqliteDriver.Open(filepath.Join(t.TempDir(), "containment-reload.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyUsageAdminAuditMigration(db); err != nil {
		t.Fatal(err)
	}
	if err := applyAutomaticContainmentMigration(db); err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		cfg:   &Config{Server: ServerConfig{AutoContainment: AutoContainmentConfig{Mode: "off"}}},
		usage: &usageStore{db: db},
		quota: &quotaStore{callers: map[string]*callerRuntime{}},
	}
	if err := svc.configureAutomaticContainment(context.Background()); err != nil {
		t.Fatal(err)
	}
	if svc.autoContainmentStop != nil {
		t.Fatal("disabled config started listener")
	}
	svc.cfg.Server.AutoContainment = AutoContainmentConfig{Mode: "enforce"}
	if err := svc.configureAutomaticContainment(context.Background()); err != nil {
		t.Fatalf("enable on reload: %v", err)
	}
	if svc.autoContainmentStop == nil {
		t.Fatal("reloaded enabled config did not start listener")
	}
	svc.cfg.Server.AutoContainment.Mode = "off"
	if err := svc.configureAutomaticContainment(context.Background()); err != nil {
		t.Fatalf("disable on reload: %v", err)
	}
	if svc.autoContainmentStop != nil {
		t.Fatal("reloaded disabled config left listener running")
	}
}

func TestAutomaticSuspendUsesContainmentResumePath(t *testing.T) {
	db := callerAdminTestDB(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.Exec(`INSERT INTO router_config_callers (config_set_id, caller_id, status, token_sha256, token_id) VALUES (?, ?, ?, ?, ?)`, "set-1", "alice", accountStatusActive, "verifier", "key-id").Error; err != nil {
		t.Fatal(err)
	}
	canonical := `server:
  listen: ':8080'
  default_model_group: default
callers:
  - id: alice
    token_id: key-id
    status: active
    allow: [default]
`
	hash := sha256.Sum256([]byte(canonical))
	if err := db.Exec(`UPDATE router_config_documents SET canonical_yaml = ?, content_sha256 = ?, updated_at = ? WHERE config_set_id = ?`, canonical, hex.EncodeToString(hash[:]), now, "set-1").Error; err != nil {
		t.Fatal(err)
	}
	svc := &Service{cfg: &Config{Server: ServerConfig{ConfigSource: ConfigSourceConfig{RuntimeScope: "staging"}}}, configControlPlane: db}
	if err := svc.applyAutomaticContainmentSuspension("alice", "test-detector", 60000); err != nil {
		t.Fatalf("automatic suspension: %v", err)
	}
	assertStatus := func(want, source string) {
		t.Helper()
		var stored string
		if err := db.Raw(`SELECT canonical_yaml FROM router_config_documents WHERE config_set_id = ?`, "set-1").Row().Scan(&stored); err != nil {
			t.Fatal(err)
		}
		var cfg Config
		if err := yaml.Unmarshal([]byte(stored), &cfg); err != nil {
			t.Fatal(err)
		}
		if len(cfg.Callers) != 1 || cfg.Callers[0].Status != want || cfg.Callers[0].ContainmentSource != source {
			t.Fatalf("canonical caller containment = %+v; want status %s source %s", cfg.Callers, want, source)
		}
	}
	assertStatus(accountStatusSuspended, "containment")
	callContainment := func(action string, admin bool) *httptest.ResponseRecorder {
		t.Helper()
		body := fmt.Sprintf(`{"caller_id":"alice","action":%q,"reason":"resume_test"}`, action)
		req := httptest.NewRequest(http.MethodPost, "/admin/callers/containment", strings.NewReader(body))
		principal := Principal{Subject: authzRoleContainment, Kind: "containment"}
		if admin {
			principal = Principal{Subject: "admin:incident", Kind: "adminkey"}
		}
		req = req.WithContext(WithPrincipal(req.Context(), principal))
		deps := CallerAdminDeps{DB: db, RuntimeScope: "staging", Authorize: func(*http.Request, string, string) bool { return true }}
		if admin {
			deps.CallerWriteAuthorized = func(*http.Request, string) bool { return true }
		}
		rec := httptest.NewRecorder()
		newCallerAdminHandler(deps).handleContainment(rec, req)
		return rec
	}
	if rec := callContainment("resume", false); rec.Code != http.StatusOK {
		t.Fatalf("containment resume status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertStatus(accountStatusActive, "")
	if err := svc.applyAutomaticContainmentSuspension("alice", "test-detector", 60000); err != nil {
		t.Fatalf("second automatic suspension: %v", err)
	}
	if rec := callContainment("resume", true); rec.Code != http.StatusOK {
		t.Fatalf("admin resume status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertStatus(accountStatusActive, "")
	var actors int64
	if err := db.Model(&configControlPlaneChangeEventRow{}).Where("action = ? AND actor = ?", "caller_containment_suspend", "test-detector").Count(&actors).Error; err != nil || actors != 2 {
		t.Fatalf("detector actor suspension audit rows=%d err=%v; want 2", actors, err)
	}
}

func TestAutoContainmentPersistsCapsAndAppliesRuntimeAction(t *testing.T) {
	db, err := gorm.Open(sqliteDriver.Open(filepath.Join(t.TempDir(), "containment.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyUsageAdminAuditMigration(db); err != nil {
		t.Fatal(err)
	}
	if err := applyAutomaticContainmentMigration(db); err != nil {
		t.Fatal(err)
	}
	store := &usageStore{db: db}
	now := time.Now().UTC()
	first := automaticContainmentActionRecord{CallerID: "cap-only", DetectorID: "honeytoken", Action: "throttle", ThrottlePercent: 10, CreatedAt: formatUsageTime(now), ExpiresAt: formatUsageTime(now.Add(time.Minute))}
	created, err := store.applyAutomaticContainment(first, 1, now)
	if err != nil || !created {
		t.Fatalf("first action = (%t, %v), want created", created, err)
	}
	second := automaticContainmentActionRecord{CallerID: "caller-b", DetectorID: "honeytoken", Action: "suspend", CreatedAt: formatUsageTime(now), ExpiresAt: formatUsageTime(now.Add(time.Minute))}
	created, err = store.applyAutomaticContainment(second, 1, now)
	if err != nil || created {
		t.Fatalf("capped action = (%t, %v), want not created", created, err)
	}

	caller := &callerRuntime{cfg: CallerConfig{ID: "caller-a", Rate: RateConfig{RPM: 100, TPM: 10000, Concurrent: 20}}}
	throttleCaller := &callerRuntime{cfg: CallerConfig{ID: "caller-throttle", Rate: RateConfig{RPM: 100}}}
	svc := &Service{cfg: &Config{}, configSnapshot: NewConfigSnapshot(), usage: store, quota: &quotaStore{callers: map[string]*callerRuntime{"caller-a": caller, "caller-throttle": throttleCaller}}}
	svc.cfg.Server.AutoContainment = AutoContainmentConfig{Mode: "enforce", MaxActionsPerMinute: 5, UnlimitedThrottleLimit: 100, Rules: []AutoContainmentRuleConfig{{DetectorID: "honeytoken", Severity: "critical", Action: "throttle", ThrottlePercent: 10, TTLMS: 60000}}}
	svc.configSnapshot.Store(svc.cfg)
	svc.applyAutomaticContainmentResult(detect.Result{DetectorID: "honeytoken", CallerID: "caller-a", Enforce: true, Mode: "enforce", Signals: []detector.Signal{{SchemaVersion: detector.SchemaVersion, DetectorID: "honeytoken", Severity: "critical", Score: 1}}})
	if got := caller.effectiveRateLimit(caller.cfg.Rate.RPM); got != 10 {
		t.Fatalf("automatic throttle RPM = %d, want 10", got)
	}
	var auditCount int64
	if err := db.Table(usageAdminAuditTable).Where("action = ? AND principal_subject = ?", "throttle", "honeytoken").Count(&auditCount).Error; err != nil || auditCount != 2 {
		t.Fatalf("automatic containment detector audit rows = %d, err %v; want 2", auditCount, err)
	}
	svc.syncAutomaticContainment()
	throttle := automaticContainmentActionRecord{CallerID: "caller-throttle", DetectorID: "honeytoken", Action: "throttle", ThrottlePercent: 10, CreatedAt: formatUsageTime(now), ExpiresAt: formatUsageTime(now.Add(time.Minute))}
	if created, err := store.applyAutomaticContainment(throttle, 5, now); err != nil || !created {
		t.Fatalf("throttle action = (%t, %v), want created", created, err)
	}
	svc.syncAutomaticContainment()
	if got := throttleCaller.effectiveRateLimit(throttleCaller.cfg.Rate.RPM); got != 10 {
		t.Fatalf("persisted throttle RPM = %d, want 10", got)
	}

	// A newly constructed runtime restores the action from the database after restart.
	restartedCaller := &callerRuntime{cfg: throttleCaller.cfg}
	restarted := &Service{usage: store, quota: &quotaStore{callers: map[string]*callerRuntime{"caller-throttle": restartedCaller}}}
	restarted.syncAutomaticContainment()
	if got := restartedCaller.effectiveRateLimit(restartedCaller.cfg.Rate.RPM); got != 10 {
		t.Fatalf("restored throttle RPM = %d, want 10", got)
	}

	// The TTL stops admission effects, then the periodic cleanup removes the row.
	if err := store.deleteExpiredAutomaticContainments(now.Add(2 * time.Minute)); err != nil {
		t.Fatalf("delete expired containment: %v", err)
	}
	restarted.syncAutomaticContainment()
	if got := restartedCaller.effectiveRateLimit(restartedCaller.cfg.Rate.RPM); got != 100 {
		t.Fatalf("expired throttle RPM = %d, want 100", got)
	}
	var expiredCount int64
	if err := db.Model(&automaticContainmentActionRecord{}).Where("caller_id = ?", "caller-throttle").Count(&expiredCount).Error; err != nil || expiredCount != 0 {
		t.Fatalf("expired action rows = %d, err %v; want 0", expiredCount, err)
	}
}

func TestAutoContainmentStorageFailureDoesNotApplyAction(t *testing.T) {
	db, err := gorm.Open(sqliteDriver.Open(filepath.Join(t.TempDir(), "containment-failure.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyUsageAdminAuditMigration(db); err != nil {
		t.Fatal(err)
	}
	if err := applyAutomaticContainmentMigration(db); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	caller := &callerRuntime{cfg: CallerConfig{ID: "caller-a", Rate: RateConfig{RPM: 100}}}
	svc := &Service{
		cfg:   &Config{Server: ServerConfig{AutoContainment: AutoContainmentConfig{Mode: "enforce", MaxActionsPerMinute: 5, UnlimitedThrottleLimit: 100, Rules: []AutoContainmentRuleConfig{{DetectorID: "honeytoken", Severity: "critical", Action: "throttle", TTLMS: 60000, ThrottlePercent: 10}}}}},
		usage: &usageStore{db: db}, quota: &quotaStore{callers: map[string]*callerRuntime{"caller-a": caller}},
	}
	svc.applyAutomaticContainmentResult(detect.Result{DetectorID: "honeytoken", CallerID: "caller-a", Enforce: true, Signals: []detector.Signal{{SchemaVersion: detector.SchemaVersion, DetectorID: "honeytoken", Severity: "critical", Score: 1}}})
	if got := caller.effectiveRateLimit(caller.cfg.Rate.RPM); got != 100 {
		t.Fatalf("storage failure changed rate limit to %d, want 100", got)
	}
}

func TestAutoContainmentSuspendEscalatesExistingThrottle(t *testing.T) {
	db, err := gorm.Open(sqliteDriver.Open(filepath.Join(t.TempDir(), "containment-escalate.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyUsageAdminAuditMigration(db); err != nil {
		t.Fatal(err)
	}
	if err := applyAutomaticContainmentMigration(db); err != nil {
		t.Fatal(err)
	}
	store := &usageStore{db: db}
	now := time.Now().UTC()
	throttle := automaticContainmentActionRecord{CallerID: "alice", DetectorID: "detector-a", Action: "throttle", ThrottlePercent: 10, CreatedAt: formatUsageTime(now), ExpiresAt: formatUsageTime(now.Add(time.Minute))}
	if created, err := store.applyAutomaticContainment(throttle, 5, now); err != nil || !created {
		t.Fatalf("throttle creation = (%t, %v)", created, err)
	}
	suspend := automaticContainmentActionRecord{CallerID: "alice", DetectorID: "detector-b", Action: "suspend", CreatedAt: formatUsageTime(now), ExpiresAt: formatUsageTime(now.Add(time.Minute))}
	if created, err := store.applyAutomaticContainment(suspend, 5, now.Add(time.Second)); err != nil || !created {
		t.Fatalf("suspend escalation = (%t, %v), want created", created, err)
	}
	var current automaticContainmentActionRecord
	if err := db.First(&current, "caller_id = ?", "alice").Error; err != nil {
		t.Fatal(err)
	}
	if current.Action != "suspend" || current.DetectorID != "detector-b" {
		t.Fatalf("escalated action = %+v; want detector-b suspend", current)
	}
}

func TestAutoContainmentShadowDoesNotChangeAdmission(t *testing.T) {
	db, err := gorm.Open(sqliteDriver.Open(filepath.Join(t.TempDir(), "containment-shadow.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyUsageAdminAuditMigration(db); err != nil {
		t.Fatal(err)
	}
	if err := applyAutomaticContainmentMigration(db); err != nil {
		t.Fatal(err)
	}
	caller := &callerRuntime{cfg: CallerConfig{ID: "caller-a", Rate: RateConfig{RPM: 100}}}
	svc := &Service{cfg: &Config{Server: ServerConfig{AutoContainment: AutoContainmentConfig{Mode: "shadow", MaxActionsPerMinute: 5, UnlimitedThrottleLimit: 100, Rules: []AutoContainmentRuleConfig{{DetectorID: "honeytoken", Severity: "critical", Action: "throttle", TTLMS: 60000, ThrottlePercent: 10}}}}}, quota: &quotaStore{callers: map[string]*callerRuntime{"caller-a": caller}}, usage: &usageStore{db: db}}
	svc.applyAutomaticContainmentResult(detect.Result{DetectorID: "honeytoken", CallerID: "caller-a", Enforce: true, Signals: []detector.Signal{{SchemaVersion: detector.SchemaVersion, DetectorID: "honeytoken", Severity: "critical", Score: 1}}})
	if got := caller.effectiveRateLimit(caller.cfg.Rate.RPM); got != 100 {
		t.Fatalf("shadow-mode rate limit = %d, want unchanged 100", got)
	}
	var shadowAudits int64
	if err := db.Table(usageAdminAuditTable).Where("outcome = ?", "shadow").Count(&shadowAudits).Error; err != nil || shadowAudits != 1 {
		t.Fatalf("shadow admin audit rows = %d, err %v; want 1", shadowAudits, err)
	}
}

func TestAutoContainmentEndToEndAppliesAfterCompletedRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"upstream","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	detectorServer := httptest.NewServer(detector.Handler(autoContainmentTestDetector{}))
	defer detectorServer.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.Detectors = []DetectorConfig{{ID: "test-detector", URL: detectorServer.URL + "/v1/detect", AllowHosts: []string{"127.0.0.1"}, AllowHTTP: true, Mode: "enforce", TimeoutMS: 1000, OnError: "fallback", SampleRate: 1}}
	cfg.Server.AutoContainment = AutoContainmentConfig{Mode: "enforce", MaxActionsPerMinute: 5, UnlimitedThrottleLimit: 100, Rules: []AutoContainmentRuleConfig{{DetectorID: "test-detector", Severity: "critical", Action: "throttle", ThrottlePercent: 1, TTLMS: 60000}}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	routerServer := httptest.NewServer(svc.mux)
	defer routerServer.Close()
	client := &http.Client{Timeout: 2 * time.Second}
	request := func() *http.Response {
		req, err := http.NewRequest(http.MethodPost, routerServer.URL+"/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hello"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	first := request()
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("initial request status = %d, want 200", first.StatusCode)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && svc.quota.callers["alice"].effectiveRateLimit(100) == 100 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := svc.quota.callers["alice"].effectiveRateLimit(100); got != 1 {
		t.Fatalf("completed detector action left RPM at %d; want 1", got)
	}
	second := request()
	second.Body.Close()
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("request after detector completion status=%d; want throttled 429", second.StatusCode)
	}
	var auditCount int64
	if err := svc.usage.db.Table(usageAdminAuditTable).Where("action = ? AND principal_subject = ?", "throttle", "test-detector").Count(&auditCount).Error; err != nil || auditCount != 1 {
		t.Fatalf("automatic containment audit rows = %d, err %v; want 1", auditCount, err)
	}
	var events []automaticContainmentEventRecord
	if err := svc.usage.db.Find(&events).Error; err != nil || len(events) != 1 {
		t.Fatalf("automatic containment event rows = %d, err %v; want 1", len(events), err)
	}
	var auditText []struct {
		ReasonCode string
		Detail     string
		RequestID  string
	}
	if err := svc.usage.db.Table(usageAdminAuditTable).Select("reason_code, detail, request_id").Find(&auditText).Error; err != nil {
		t.Fatal(err)
	}
	logBytes, err := os.ReadFile(cfg.Server.Logging.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range append([]string{string(logBytes)}, auditText[0].ReasonCode+auditText[0].Detail+auditText[0].RequestID) {
		if strings.Contains(raw, testToken) || strings.Contains(raw, "hello") {
			t.Fatal("automatic containment logs or audit stored a token or prompt text")
		}
	}
}

func TestAutoContainmentDetectorStormCapsManyCallersEndToEnd(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"upstream","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	var detectorCalls atomic.Int32
	detectorServer := httptest.NewServer(detector.Handler(countingAutoContainmentTestDetector{calls: &detectorCalls}))
	defer detectorServer.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.Detectors = []DetectorConfig{{ID: "test-detector", URL: detectorServer.URL + "/v1/detect", AllowHosts: []string{"127.0.0.1"}, AllowHTTP: true, Mode: "enforce", TimeoutMS: 1000, OnError: "fallback", SampleRate: 1}}
	cfg.Server.AutoContainment = AutoContainmentConfig{Mode: "enforce", MaxActionsPerMinute: 3, UnlimitedThrottleLimit: 100, Rules: []AutoContainmentRuleConfig{{DetectorID: "test-detector", Severity: "critical", Action: "throttle", ThrottlePercent: 100, TTLMS: 60000}}}
	cfg.Callers = nil
	tokens := make([]string, 20)
	for i := range tokens {
		tokenID := fmt.Sprintf("rtr_storm_%02d", i)
		token := tokenID + "_storm-token"
		tokens[i] = token
		cfg.Callers = append(cfg.Callers, CallerConfig{
			ID: fmt.Sprintf("storm-%02d", i), User: "storm-user", Project: "security-test", Environment: "test",
			TokenHash: mustCallerTokenHashForTest(t, token), TokenID: tokenID, Allow: []string{"default"},
			Rate: RateConfig{RPM: 100, TPM: 100000, Concurrent: 4},
		})
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	routerServer := httptest.NewServer(svc.mux)
	defer routerServer.Close()
	client := &http.Client{Timeout: 2 * time.Second}
	for i, token := range tokens {
		req, err := http.NewRequest(http.MethodPost, routerServer.URL+"/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"storm"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("caller %d request: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("caller %d request status=%d, want 200", i, resp.StatusCode)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for detectorCalls.Load() < int32(len(tokens)) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := detectorCalls.Load(); got < int32(len(tokens)) {
		t.Fatalf("completed detector calls=%d; want at least all %d caller calls before checking the cap", got, len(tokens))
	}
	var count int64
	if err := svc.usage.db.Model(&automaticContainmentEventRecord{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("storm action events = %d; want exactly capped 3 of 20 callers", count)
	}
}

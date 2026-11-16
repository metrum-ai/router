// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func waitAdminOperation(t *testing.T, db *gorm.DB, id string, want string) adminOperationRow {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var row adminOperationRow
		if err := db.Where("id = ?", id).First(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.Status == want {
			return row
		}
		time.Sleep(5 * time.Millisecond)
	}
	var row adminOperationRow
	_ = db.Where("id = ?", id).First(&row).Error
	t.Fatalf("operation %s did not reach %s; state=%s error=%s", id, want, row.Status, row.ErrorCode)
	return adminOperationRow{}
}

func TestAdminOperationLifecycleFailureAndTimeout(t *testing.T) {
	db := configAdminTestDB(t)
	started, release := make(chan struct{}), make(chan struct{})
	row, err := startAdminOperation(db, "test-success", time.Now().UTC(), func(context.Context) (any, error) {
		close(started)
		<-release
		return map[string]any{"revision": 9, "restored": true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != adminOperationPending {
		t.Fatalf("initial state=%q, want pending", row.Status)
	}
	<-started
	running := waitAdminOperation(t, db, row.ID, adminOperationRunning)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/admin/v1/operations/"+row.ID, nil)
	request.SetPathValue("operation_id", row.ID)
	adminOperationResourceHandler(db, nil)(recorder, request)
	var state adminOperationResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &state); err != nil || recorder.Code != 200 || state.Status != adminOperationRunning {
		t.Fatalf("running resource status=%d body=%s err=%v", recorder.Code, recorder.Body.String(), err)
	}
	close(release)
	succeeded := waitAdminOperation(t, db, row.ID, adminOperationSucceeded)
	if succeeded.UpdatedAt <= running.UpdatedAt || succeeded.Result == "" || strings.Contains(succeeded.Result, "secret") {
		t.Fatalf("successful operation state is incomplete or contains a secret: %+v", succeeded)
	}

	failed, err := startAdminOperation(db, "test-failure", time.Now().UTC(), func(context.Context) (any, error) {
		return nil, errors.New("secret-bearing internal error")
	})
	if err != nil {
		t.Fatal(err)
	}
	failed = waitAdminOperation(t, db, failed.ID, adminOperationFailed)
	if failed.ErrorCode != "operation_failed" || strings.Contains(failed.ErrorCode+failed.Result, "secret-bearing") {
		t.Fatalf("failure exposed an unsafe error: %+v", failed)
	}

	timedOut, err := startAdminOperationWithTimeout(db, "test-timeout", time.Now().UTC(), 10*time.Millisecond, func(ctx context.Context) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	timedOut = waitAdminOperation(t, db, timedOut.ID, adminOperationFailed)
	if timedOut.ErrorCode != "operation_timeout" {
		t.Fatalf("timeout error code=%q, want operation_timeout", timedOut.ErrorCode)
	}

	startedLate, releaseLate := make(chan struct{}), make(chan struct{})
	late, err := startAdminOperationWithTimeout(db, "test-late-success", time.Now().UTC(), time.Second, func(context.Context) (any, error) {
		close(startedLate)
		<-releaseLate
		return map[string]any{"restored": true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-startedLate
	if err := db.Model(&adminOperationRow{}).Where("id = ?", late.ID).Update("updated_at", time.Now().UTC().Add(-adminOperationStaleTimeout-time.Second).Format(time.RFC3339Nano)).Error; err != nil {
		t.Fatal(err)
	}
	lateResponse := httptest.NewRecorder()
	lateRequest := httptest.NewRequest("GET", "/admin/v1/operations/"+late.ID, nil)
	lateRequest.SetPathValue("operation_id", late.ID)
	adminOperationResourceHandler(db, time.Now)(lateResponse, lateRequest)
	var timedOutState adminOperationResult
	if err := json.Unmarshal(lateResponse.Body.Bytes(), &timedOutState); err != nil || timedOutState.Status != adminOperationFailed {
		t.Fatalf("stale operation read state=%+v body=%s err=%v", timedOutState, lateResponse.Body.String(), err)
	}
	close(releaseLate)
	late = waitAdminOperation(t, db, late.ID, adminOperationSucceeded)
	if !strings.Contains(late.Result, `"restored":true`) {
		t.Fatalf("late success result was lost: %+v", late)
	}

	staleAt := time.Now().UTC().Add(-adminOperationStaleTimeout - time.Second)
	stale := adminOperationRow{ID: "stale-after-restart", Kind: "control-plane-restore", Status: adminOperationRunning,
		CreatedAt: staleAt.Format(time.RFC3339Nano), UpdatedAt: staleAt.Format(time.RFC3339Nano), ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)}
	if err := db.Create(&stale).Error; err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	staleRequest := httptest.NewRequest("GET", "/admin/v1/operations/"+stale.ID, nil)
	staleRequest.SetPathValue("operation_id", stale.ID)
	adminOperationResourceHandler(db, time.Now)(response, staleRequest)
	var staleState adminOperationResult
	if err := json.Unmarshal(response.Body.Bytes(), &staleState); err != nil || staleState.Status != adminOperationFailed || staleState.ErrorCode != "operation_timeout" {
		t.Fatalf("stale operation recovery status=%d body=%s err=%v", response.Code, response.Body.String(), err)
	}
}

func TestAdminOperationAuthorizationIsResourceScoped(t *testing.T) {
	harness := newAdminV1Harness(t)
	harness.authorize.deny[AdminGlobalDomain+"|"+authzObjectAdminBackup+"|write"] = true
	response := harness.do(t, http.MethodPost, "/admin/v1/control-plane/backups", map[string]any{}, nil)
	if response.Code != http.StatusForbidden {
		t.Fatalf("backup without admin:backup:write status=%d body=%s", response.Code, response.Body.String())
	}
	calls := harness.authorize.snapshot()
	if len(calls) != 1 || calls[0].Object != authzObjectAdminBackup || calls[0].Action != "write" || calls[0].Domain != AdminGlobalDomain {
		t.Fatalf("backup authorization decision=%+v", calls)
	}
	harness.authorize.deny[AdminGlobalDomain+"|"+authzObjectAdminOperations+"|read"] = true
	response = harness.do(t, http.MethodGet, "/admin/v1/operations/op-1", nil, nil)
	if response.Code != http.StatusForbidden {
		t.Fatalf("operation without operations:read status=%d body=%s", response.Code, response.Body.String())
	}
	harness.authorize.deny[AdminGlobalDomain+"|"+authzObjectAdminRestore+"|write"] = true
	response = harness.do(t, http.MethodPost, "/admin/v1/control-plane/restores", map[string]any{}, nil)
	if response.Code != http.StatusForbidden {
		t.Fatalf("restore without admin:restore:write status=%d body=%s", response.Code, response.Body.String())
	}
	calls = harness.authorize.snapshot()
	last := calls[len(calls)-1]
	if last.Object != authzObjectAdminRestore || last.Action != "write" {
		t.Fatalf("restore authorization decision=%+v, want separate admin:restore/write", last)
	}
}

func TestControlPlaneBackupRestoreThroughAdminAPI(t *testing.T) {
	runControlPlaneBackupRestoreThroughAdminAPI(t)
}

func runControlPlaneBackupRestoreThroughAdminAPI(t *testing.T) {
	t.Setenv("ALPHA_API_KEY", "test-only-alpha-key")
	t.Setenv("BETA_API_KEY", "test-only-beta-key")
	adminPolicyYAML := strings.Replace(configAdminTestYAML, "  listen: ':8080'\n", "  listen: ':8080'\n  admin_auth:\n    authorization:\n      enabled: true\n      source: static\n      policy:\n        - p, adminkey:operations-test, *, admin:backup, read|write\n        - p, adminkey:operations-test, *, admin:restore, write\n        - p, adminkey:operations-test, *, admin:operations, read\n        - p, adminkey:operations-test, *, admin:config, read\n", 1)
	sourceConfig := configAdminTestDBWithYAML(t, adminPolicyYAML)
	targetConfig := emptyConfigAdminTestDB(t)
	sourceService := adminOperationsTestService(t, sourceConfig, "source")
	targetService := adminOperationsTestService(t, targetConfig, "target")
	sourceUsage := sourceService.usage.db
	targetUsage := targetService.usage.db
	seedBackupPolicyData(t, sourceUsage)
	seedBackupKeyData(t, sourceConfig)

	sourceMux := adminOperationsServiceMux(t, sourceService)
	if err := sourceService.reloadActiveConfigFromDB(); err != nil {
		t.Fatalf("load source serving config: %v", err)
	}
	backupStart := adminOperationsTestRequest(t, sourceMux, http.MethodPost, "/admin/v1/control-plane/backups", map[string]any{})
	if backupStart.Code != http.StatusAccepted || backupStart.Header().Get("Location") == "" {
		t.Fatalf("start backup status=%d body=%s", backupStart.Code, backupStart.Body.String())
	}
	backupOperation := awaitAdminOperationAPI(t, sourceMux, backupStart)
	if backupOperation.Status != adminOperationSucceeded || len(backupOperation.Result) != 0 {
		t.Fatalf("backup operation=%+v", backupOperation)
	}
	backupDownload := adminOperationsTestRequest(t, sourceMux, http.MethodGet, "/admin/v1/control-plane/backups/"+backupOperation.ID, nil)
	if backupDownload.Code != http.StatusOK {
		t.Fatalf("download backup status=%d body=%s", backupDownload.Code, backupDownload.Body.String())
	}
	var backupBody struct {
		Bundle json.RawMessage `json:"bundle"`
	}
	if err := json.Unmarshal(backupDownload.Body.Bytes(), &backupBody); err != nil {
		t.Fatal(err)
	}
	var bundle controlPlaneBackupBundle
	if err := json.Unmarshal(backupBody.Bundle, &bundle); err != nil {
		t.Fatal(err)
	}
	if err := validateControlPlaneBackup(bundle); err != nil {
		t.Fatalf("API backup bundle failed integrity validation: %v", err)
	}

	targetMux := adminOperationsServiceMux(t, targetService)
	loginEvent := AdminCredentialEvent{
		EventID: "restore-target-login", At: time.Now().UTC(), Kind: "adminkey",
		CredentialID: "operations-test", Subject: "adminkey:operations-test", Issuer: "test",
		Outcome: "success", RequestID: "restore-target-login-request", Lifecycle: "authenticate",
	}
	if err := targetConfig.Transaction(func(tx *gorm.DB) error { return insertAdminCredentialEventTx(tx, loginEvent) }); err != nil {
		t.Fatalf("record target's authenticated restore request: %v", err)
	}
	var targetCredentialEvents int64
	if err := targetConfig.Table(adminCredentialEventsTable).Where("event_id = ?", loginEvent.EventID).Count(&targetCredentialEvents).Error; err != nil || targetCredentialEvents != 1 {
		t.Fatalf("target credential event count=%d err=%v, want one before restore", targetCredentialEvents, err)
	}
	restoreStart := adminOperationsTestRequest(t, targetMux, http.MethodPost, "/admin/v1/control-plane/restores", bundle)
	if restoreStart.Code != http.StatusAccepted || restoreStart.Header().Get("Location") == "" {
		t.Fatalf("start restore status=%d body=%s", restoreStart.Code, restoreStart.Body.String())
	}
	restoreOperation := awaitAdminOperationAPI(t, targetMux, restoreStart)
	if restoreOperation.Status != adminOperationSucceeded {
		t.Fatalf("restore operation=%+v", restoreOperation)
	}

	var sourceRevision, targetRevision int64
	if err := sourceConfig.Raw("SELECT config_revision FROM router_config_runtime WHERE runtime_scope = ?", "prod").Scan(&sourceRevision).Error; err != nil {
		t.Fatal(err)
	}
	if err := targetConfig.Raw("SELECT config_revision FROM router_config_runtime WHERE runtime_scope = ?", "prod").Scan(&targetRevision).Error; err != nil {
		t.Fatal(err)
	}
	if sourceRevision != targetRevision {
		t.Fatalf("restored configuration revision=%d, source=%d", targetRevision, sourceRevision)
	}
	assertBackupTableEqual(t, sourceConfig, targetConfig, "router_config_change_events", "event_id")
	assertBackupTableEqual(t, sourceConfig, targetConfig, "router_config_change_event_targets", "event_id, phase, sequence")
	var restoredVerifier string
	if err := targetConfig.Raw("SELECT verifier FROM admin_api_key_generations WHERE key_id = ? AND generation = 1", "backup-ops").Scan(&restoredVerifier).Error; err != nil {
		t.Fatal(err)
	}
	if restoredVerifier != "pbkdf2-sha256$backup-verifier-only" {
		t.Fatalf("API backup did not restore the key verifier")
	}
	assertBackupTableEqual(t, sourceUsage, targetUsage, "authz_policy_audit_events", "id")
	verifyRestoredConfigAuditChain(t, targetConfig, sourceConfig)
	assertOperationAudit(t, sourceUsage, "admin:backup", "committed", auditBundleDigest(bundle.SHA256), "adminkey:operations-test")
	assertOperationAudit(t, targetUsage, "admin:restore", "committed", auditBundleDigest(bundle.SHA256), "adminkey:operations-test")
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer "+configAdminTestToken)
	served := httptest.NewRecorder()
	targetService.Handler().ServeHTTP(served, request)
	if served.Code != http.StatusOK {
		t.Fatalf("serving after restore status=%d body=%s", served.Code, served.Body.String())
	}
	if got := targetService.configSnapshot.Load().Provider["alpha"].BaseURL; got != "https://alpha.example/v1" {
		t.Fatalf("restored live config provider URL=%q", got)
	}

	// Restore is deliberately limited to clean replicas; the second attempt must
	// fail asynchronously and expose only a stable safe error code.
	replay := adminOperationsTestRequest(t, targetMux, http.MethodPost, "/admin/v1/control-plane/restores", bundle)
	if replay.Code != http.StatusAccepted {
		t.Fatalf("start dirty-target restore status=%d body=%s", replay.Code, replay.Body.String())
	}
	failedRestore := awaitAdminOperationAPI(t, targetMux, replay)
	if failedRestore.Status != adminOperationFailed || failedRestore.ErrorCode != "operation_failed" {
		t.Fatalf("dirty-target restore state=%+v", failedRestore)
	}
	assertOperationAudit(t, targetUsage, "admin:restore", "failed", auditBundleDigest(bundle.SHA256), "adminkey:operations-test")
}

func assertBackupTableEqual(t *testing.T, source, target *gorm.DB, table, order string) {
	t.Helper()
	var sourceRows, targetRows []map[string]any
	if err := source.Table(table).Order(order).Find(&sourceRows).Error; err != nil {
		t.Fatalf("read source %s audit rows: %v", table, err)
	}
	if err := target.Table(table).Order(order).Find(&targetRows).Error; err != nil {
		t.Fatalf("read restored %s audit rows: %v", table, err)
	}
	if !reflect.DeepEqual(sourceRows, targetRows) {
		t.Fatalf("restored %s rows differ from source\nsource: %#v\ntarget: %#v", table, sourceRows, targetRows)
	}
}

func emptyConfigAdminTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	runner, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "empty-config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeDB() })
	applyConfigControlPlaneMigrationsForTest(t, runner)
	return runner.db
}

func adminOperationsTestUsageDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), name+"-usage.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&authzPolicySetRecord{}, &authzPolicyRuleRecord{}, &authzRoleLinkRecord{}, &policyMutationRevision{}, &authzPolicyAuditEventRecord{}); err != nil {
		t.Fatal(err)
	}
	if err := applyUsageAdminAuditMigration(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedBackupPolicyData(t *testing.T, db *gorm.DB) {
	t.Helper()
	set := authzPolicySetRecord{ID: "policy-backup", Name: "main", Version: 1, Status: "active", CreatedBy: "test", CreatedAt: "2026-11-16T00:00:00Z", ActivatedAt: "2026-11-16T00:00:00Z"}
	if err := db.Create(&set).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&authzPolicyRuleRecord{PolicySetID: set.ID, Sequence: 1, PType: "p", SubjectOrRole: "ops", Domain: "*", Object: authzObjectAdminOperations, Action: "read|write", Effect: "allow"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&authzRoleLinkRecord{PolicySetID: set.ID, Sequence: 1, Subject: "ops-user", Role: "ops", Domain: "*"}).Error; err != nil {
		t.Fatal(err)
	}
	runtime := policyMutationRevision{ID: 1, Revision: 7, ConfigRevision: 4, ActiveSetID: set.ID}
	if err := db.Model(&policyMutationRevision{}).Where("id = ?", 1).Updates(map[string]any{"revision": runtime.Revision, "config_revision": runtime.ConfigRevision, "active_set_id": runtime.ActiveSetID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ?", 1).First(&runtime).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		if err := db.Create(&policyMutationRevision{ID: 1, Revision: 7, ConfigRevision: 4, ActiveSetID: set.ID}).Error; err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&authzPolicyAuditEventRecord{PolicySetID: set.ID, ActorSubject: "ops-user", Action: "activate", TS: "2026-11-16T00:00:00Z", RequestID: "backup-test", SafeSummary: "activated test policy"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO router_admin_audit_events (event_id, at, outcome) VALUES (?, ?, ?)", "audit-backup", "2026-11-16T00:00:00Z", "committed").Error; err != nil {
		t.Fatal(err)
	}
}

func seedBackupKeyData(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec("INSERT INTO admin_api_key_metadata (id, subject, status, expires_at, created_at, revoked_at, revision) VALUES (?, ?, ?, ?, ?, NULL, 1)", "backup-ops", "adminkey:backup-ops", "active", "2027-11-16T00:00:00Z", "2026-11-16T00:00:00Z").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO admin_api_key_generations (key_id, generation, verifier, created_at, overlap_until) VALUES (?, 1, ?, ?, NULL)", "backup-ops", "pbkdf2-sha256$backup-verifier-only", "2026-11-16T00:00:00Z").Error; err != nil {
		t.Fatal(err)
	}
}

func adminOperationsTestService(t *testing.T, configDB *gorm.DB, name string) *Service {
	t.Helper()
	bootstrapPath := filepath.Join(t.TempDir(), name+"-bootstrap.yaml")
	if err := os.WriteFile(bootstrapPath, []byte(configAdminTestYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(bootstrapPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.ConfigSource.RuntimeScope = "prod"
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), name+"-usage.sqlite"))
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatalf("start %s Service: %v", name, err)
	}
	svc.configControlPlane = configDB
	t.Cleanup(svc.Close)
	return svc
}

func adminOperationsServiceMux(t *testing.T, svc *Service) *http.ServeMux {
	t.Helper()
	principal := Principal{Subject: "adminkey:operations-test", Kind: "adminkey", CredentialID: "operations-test", Issuer: "test"}
	policy, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Source: "inline", Policy: []string{
		"p, adminkey:operations-test, *, admin:backup, read|write",
		"p, adminkey:operations-test, *, admin:restore, write",
		"p, adminkey:operations-test, *, admin:operations, read",
		"p, adminkey:operations-test, *, admin:config, read",
	}}, nil, svc.usage, svc.cfg.Server.AdminAuth)
	if err != nil {
		t.Fatal(err)
	}
	svc.authorizerSnapshot.Store(policy)
	mux := http.NewServeMux()
	if err := svc.MountAdminV1(mux, &fakeAuthenticator{principal: principal}, func(context.Context, Principal, string, string, string) error { return nil }); err != nil {
		t.Fatalf("mount production admin service handlers: %v", err)
	}
	return mux
}

func assertOperationAudit(t *testing.T, db *gorm.DB, object, outcome, digest, actor string) {
	t.Helper()
	var count int64
	if err := db.Table(usageAdminAuditTable).Where("object = ? AND outcome = ? AND before_hash = ?", object, outcome, digest).Count(&count).Error; err != nil {
		t.Fatalf("count %s operation audit rows: %v", object, err)
	}
	if count != 1 {
		t.Fatalf("found %d %s operation audit rows, want exactly one", count, object)
	}
	var row struct {
		PrincipalSubject string `gorm:"column:principal_subject"`
		RequestID        string `gorm:"column:request_id"`
		BeforeHash       string `gorm:"column:before_hash"`
		Outcome          string `gorm:"column:outcome"`
		Object           string `gorm:"column:object"`
		Detail           string `gorm:"column:detail"`
	}
	if err := db.Table(usageAdminAuditTable).Select("principal_subject, request_id, before_hash, outcome, object, detail").Where("object = ? AND outcome = ? AND before_hash = ?", object, outcome, digest).Order("at DESC").First(&row).Error; err != nil {
		t.Fatalf("find %s operation audit row: %v", object, err)
	}
	if row.PrincipalSubject != actor || row.RequestID == "" || !strings.Contains(row.Detail, digest) {
		t.Fatalf("incomplete %s operation audit row: %+v", object, row)
	}
}

func verifyRestoredConfigAuditChain(t *testing.T, target, source *gorm.DB) {
	t.Helper()
	assertBackupTableEqual(t, source, target, "router_config_change_events", "event_id")
	assertBackupTableEqual(t, source, target, "router_config_change_event_targets", "event_id, phase, sequence")
	var events []struct {
		ConfigSetID   string `gorm:"column:config_set_id"`
		PreviousSetID string `gorm:"column:prev_config_set_id"`
		AfterHash     string `gorm:"column:after_hash"`
		RuntimeScope  string `gorm:"column:runtime_scope"`
	}
	if err := target.Table("router_config_change_events").Select("config_set_id, prev_config_set_id, after_hash, runtime_scope").Order("config_revision, event_id").Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		for _, setID := range []string{event.ConfigSetID, event.PreviousSetID} {
			if setID == "" {
				continue
			}
			var count int64
			if err := target.Table("router_config_sets").Where("id = ? AND runtime_scope = ?", setID, event.RuntimeScope).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("audit event references missing config set %q in %q: count=%d err=%v", setID, event.RuntimeScope, count, err)
			}
		}
		if event.AfterHash != "" {
			var canonical string
			if err := target.Table("router_config_documents").Select("canonical_yaml").Where("config_set_id = ?", event.ConfigSetID).Take(&canonical).Error; err != nil {
				t.Fatalf("audit event config document missing for %q: %v", event.ConfigSetID, err)
			}
			if got := adminAuditHash([]byte(canonical)); got != event.AfterHash {
				t.Fatalf("audit after_hash for config set %q = %s, document hash = %s", event.ConfigSetID, event.AfterHash, got)
			}
		}
	}
}

func adminOperationsTestRequest(t *testing.T, mux *http.ServeMux, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Test-Principal", "adminkey:operations-test")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

func awaitAdminOperationAPI(t *testing.T, mux *http.ServeMux, start *httptest.ResponseRecorder) adminOperationResult {
	t.Helper()
	var accepted struct {
		OperationURL string `json:"operation_url"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &accepted); err != nil || accepted.OperationURL == "" {
		t.Fatalf("invalid accepted operation response: %s err=%v", start.Body.String(), err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		response := adminOperationsTestRequest(t, mux, http.MethodGet, accepted.OperationURL, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("operation resource status=%d body=%s", response.Code, response.Body.String())
		}
		var state adminOperationResult
		if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		if state.Status == adminOperationSucceeded || state.Status == adminOperationFailed {
			return state
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("operation API did not reach a terminal state")
	return adminOperationResult{}
}

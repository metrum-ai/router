// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestLeakedKeySelfRevocationLocalhostE2E(t *testing.T) {
	var capturedLog bytes.Buffer
	oldLog := log.Writer()
	log.SetOutput(&capturedLog)
	t.Cleanup(func() { log.SetOutput(oldLog) })
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-test-key", dir)
	generated, err := GenerateCallerToken(TokenGenerateOptions{
		OwnerUser: "alice", Project: "metrum-insights", Environment: "prod", KeySlug: "key1", Allow: []string{"default", "other"},
	})
	if err != nil {
		t.Fatal(err)
	}
	callerToken := generated.Token
	generated.Caller.ID = "alice"
	other, err := GenerateCallerToken(TokenGenerateOptions{OwnerUser: "bob", Project: "metrum-insights", Environment: "prod", KeySlug: "key2", Allow: []string{"default"}})
	if err != nil {
		t.Fatal(err)
	}
	other.Caller.ID = "bob"
	cfg.Callers = []CallerConfig{generated.Caller, other.Caller}
	cfg.Security.KeyHygiene = KeyHygieneConfig{MinEntropyBits: 128, Enforce: true}
	controlDB := filepath.Join(dir, "config.sqlite")
	cfg.Server.ConfigSource = ConfigSourceConfig{Mode: "database", Driver: "sqlite", Path: controlDB, RuntimeScope: "test"}
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	runner, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: controlDB})
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	applyConfigControlPlaneMigrationsForTest(t, runner)
	canonical, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	setID, err := ImportCanonicalYAML(runner.db, "test", "self-revoke", canonical, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := ActivateConfigSet(runner.db, "test", setID, "test"); err != nil {
		t.Fatal(err)
	}
	bootstrapPath := filepath.Join(dir, "bootstrap.yaml")
	if err := os.WriteFile(bootstrapPath, canonical, 0600); err != nil {
		t.Fatal(err)
	}
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	secondReplica, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer secondReplica.Close()
	mux := http.NewServeMux()
	RegisterCallerDirectoryAdmin(mux, CallerAdminDeps{DB: runner.db, RuntimeScope: "test", Authorize: func(r *http.Request, _, _ string) bool { return r.Header.Get("X-Test-Admin") == "yes" }, AfterMutate: func(_ *http.Request, _ string) error { return svc.reloadActiveConfigFromDB() }})
	mux.Handle("/", svc.Handler())
	router := httptest.NewServer(mux)
	defer router.Close()
	post := func(path, body string) (*http.Response, []byte) {
		req, _ := http.NewRequest(http.MethodPost, router.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+callerToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return resp, data
	}
	bad := httptest.NewRequest(http.MethodPost, "/v1/security/leaked-keys", nil)
	bad.Header.Set("Authorization", "Bearer malformed_v2_ABCDEF12")
	badRecorder := httptest.NewRecorder()
	svc.Handler().ServeHTTP(badRecorder, bad)
	if badRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("malformed token status=%d", badRecorder.Code)
	}
	lockTx := runner.db.Begin()
	if lockTx.Error != nil {
		t.Fatal(lockTx.Error)
	}
	if err := lockTx.Exec("UPDATE router_config_callers SET status = status WHERE config_set_id = ? AND caller_id = ?", setID, "alice").Error; err != nil {
		t.Fatal(err)
	}
	timeoutCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	timedOut := httptest.NewRequest(http.MethodPost, "/v1/security/leaked-keys", nil).WithContext(timeoutCtx)
	timedOut.Header.Set("Authorization", "Bearer "+callerToken)
	timedOutRecorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { svc.Handler().ServeHTTP(timedOutRecorder, timedOut); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		_ = lockTx.Rollback().Error
		<-done
		t.Fatal("self-revocation did not honor request timeout while the caller store was locked")
	}
	_ = lockTx.Rollback().Error
	if timedOutRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("timed out revoke status=%d", timedOutRecorder.Code)
	}
	response, body := post("/v1/security/leaked-keys", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", response.StatusCode, body)
	}
	if err := secondReplica.reloadActiveConfigFromDB(); err != nil {
		t.Fatal(err)
	}
	if caller, _, _ := secondReplica.authenticate("Bearer "+callerToken, ""); caller != nil {
		t.Fatalf("second replica still accepts revoked token after config reload: cfg=%+v", secondReplica.cfg.Callers[0])
	}
	var result map[string]string
	if err := json.Unmarshal(body, &result); err != nil || result["status"] != "revoked" || result["token_id"] != cfg.Callers[0].TokenID {
		t.Fatalf("revoke response=%s err=%v", body, err)
	}
	var audit struct {
		ReasonCode string `gorm:"column:reason_code"`
	}
	if err := runner.db.Table(adminCredentialEventsTable).Where("lifecycle = ? AND reason_code = ?", "revoke", "self_revocation").First(&audit).Error; err != nil {
		t.Fatal(err)
	}
	if audit.ReasonCode != "self_revocation" {
		t.Fatalf("unsafe revocation audit: %+v", audit)
	}
	for _, path := range []string{"/v1/models", "/v1/usage", "/metrics", "/v1/security/leaked-keys"} {
		method := http.MethodGet
		if path == "/v1/security/leaked-keys" {
			method = http.MethodPost
		}
		req, _ := http.NewRequest(method, router.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+callerToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("revoked token %s status=%d", path, resp.StatusCode)
		}
	}
	var selfRevocationAudits int64
	if err := runner.db.Table(adminCredentialEventsTable).Where("lifecycle = ? AND reason_code = ?", "revoke", "self_revocation").Count(&selfRevocationAudits).Error; err != nil {
		t.Fatal(err)
	}
	if selfRevocationAudits != 1 {
		t.Fatalf("self-revocation audit rows=%d, want one", selfRevocationAudits)
	}
	revokedConfig, err := LoadActiveConfigFromDB(runner.db, "test", cfg.Server.Identifiers)
	if err != nil {
		t.Fatalf("load revoked config for restart: %v", err)
	}
	if revokedConfig.Callers[0].RevokedTokenHash == "" {
		t.Fatal("revoked verifier did not persist in the caller store")
	}
	restartConfig, err := LoadConfigForServe(bootstrapPath)
	if err != nil {
		t.Fatalf("load database-backed config after restart: %v", err)
	}
	restartedRevoked, err := newServiceWithVerifiedFixture(restartConfig)
	if err != nil {
		t.Fatalf("restart with revoked caller: %v", err)
	}
	if caller, _, _ := restartedRevoked.authenticate("Bearer "+callerToken, ""); caller != nil {
		t.Fatal("restarted router accepts revoked token")
	}
	restartedRevoked.Close()
	otherReq, _ := http.NewRequest(http.MethodGet, router.URL+"/v1/models", nil)
	otherReq.Header.Set("Authorization", "Bearer "+other.Token)
	otherResp, err := http.DefaultClient.Do(otherReq)
	if err != nil {
		t.Fatal(err)
	}
	otherResp.Body.Close()
	if otherResp.StatusCode != http.StatusOK {
		t.Fatalf("unrevoked caller status=%d", otherResp.StatusCode)
	}
	rotateReq, _ := http.NewRequest(http.MethodPost, router.URL+"/admin/callers/alice/rotate", nil)
	rotateReq.Header.Set("X-Test-Admin", "yes")
	rotateResp, err := http.DefaultClient.Do(rotateReq)
	if err != nil {
		t.Fatal(err)
	}
	var rotated callerAdminTokenResponse
	if err := json.NewDecoder(rotateResp.Body).Decode(&rotated); err != nil {
		t.Fatal(err)
	}
	rotateResp.Body.Close()
	if rotateResp.StatusCode != http.StatusOK || rotated.Token == "" {
		t.Fatalf("rotate status=%d", rotateResp.StatusCode)
	}
	newReq, _ := http.NewRequest(http.MethodGet, router.URL+"/v1/models", nil)
	newReq.Header.Set("Authorization", "Bearer "+rotated.Token)
	newResp, err := http.DefaultClient.Do(newReq)
	if err != nil {
		t.Fatal(err)
	}
	newResp.Body.Close()
	if newResp.StatusCode != http.StatusOK {
		t.Fatalf("rotated token status=%d", newResp.StatusCode)
	}
	loaded, err := LoadActiveConfigFromDB(runner.db, "test", cfg.Server.Identifiers)
	if err != nil {
		t.Fatalf("load enforced rotated config: %v", err)
	}
	if loaded.Callers[0].TokenFormatVersion != 2 || loaded.Callers[0].TokenEntropyBits != 256 || loaded.Callers[0].RevokedTokenHash != "" {
		t.Fatalf("rotated token metadata=%+v", loaded.Callers[0])
	}
	restarted, err := newServiceWithVerifiedFixture(loaded)
	if err != nil {
		t.Fatalf("restart from config DB: %v", err)
	}
	defer restarted.Close()
	if caller, _, _ := restarted.authenticate("Bearer "+callerToken, ""); caller != nil {
		t.Fatal("restarted router accepts old token")
	}
	if caller, _, _ := restarted.authenticate("Bearer "+rotated.Token, ""); caller == nil {
		t.Fatal("restarted router rejects rotated token")
	}
	var rows []usageRecord
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := svc.usage.db.Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for revocation usage rows")
		}
		time.Sleep(10 * time.Millisecond)
	}
	serialized, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	serializedEvent, err := json.Marshal(audit)
	if err != nil {
		t.Fatal(err)
	}
	var rotatedHash string
	if err := runner.db.Raw(`SELECT token_sha256 FROM router_config_callers WHERE config_set_id = ? AND caller_id = ?`, setID, "alice").Row().Scan(&rotatedHash); err != nil {
		t.Fatal(err)
	}
	for _, secret := range [][]byte{[]byte(callerToken), []byte(generated.TokenHash), []byte(rotated.Token), []byte(rotatedHash)} {
		if bytes.Contains(capturedLog.Bytes(), secret) || bytes.Contains(serialized, secret) || bytes.Contains(serializedEvent, secret) || bytes.Contains(body, secret) {
			t.Fatal("caller token or verifier appeared in logs, response or persisted records")
		}
	}
}

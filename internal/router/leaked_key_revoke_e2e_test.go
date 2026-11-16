// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLeakedKeySelfRevocationLocalhostE2E(t *testing.T) {
	var capturedLog bytes.Buffer
	oldLog := log.Writer()
	log.SetOutput(&capturedLog)
	t.Cleanup(func() { log.SetOutput(oldLog) })
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-test-key", dir)
	generated, err := GenerateCallerToken(TokenGenerateOptions{
		OwnerUser: "alice", Project: "metrum-insights", Environment: "test", KeySlug: "key1", Allow: []string{"default", "other"},
	})
	if err != nil {
		t.Fatal(err)
	}
	callerToken := generated.Token
	generated.Caller.ID = "alice"
	cfg.Callers[0] = generated.Caller
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	router := httptest.NewServer(svc.Handler())
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
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	timedOutRequest := httptest.NewRequest(http.MethodPost, "/v1/security/leaked-keys", nil).WithContext(canceled)
	timedOutRequest.Header.Set("Authorization", "Bearer "+callerToken)
	timedOutRecorder := httptest.NewRecorder()
	svc.Handler().ServeHTTP(timedOutRecorder, timedOutRequest)
	if timedOutRecorder.Code != http.StatusServiceUnavailable || !strings.Contains(timedOutRecorder.Body.String(), "revocation-unavailable") {
		t.Fatalf("canceled revoke status=%d body=%s", timedOutRecorder.Code, timedOutRecorder.Body.String())
	}
	svc.quota.saveFault = errors.New("synthetic persistence failure")
	failed, failedBody := post("/v1/security/leaked-keys", "")
	if failed.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(failedBody), "revocation-unavailable") {
		t.Fatalf("failed revoke status=%d body=%s", failed.StatusCode, failedBody)
	}
	svc.quota.saveFault = nil
	response, body := post("/v1/security/leaked-keys", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", response.StatusCode, body)
	}
	var result map[string]string
	if err := json.Unmarshal(body, &result); err != nil || result["status"] != "revoked" || result["token_id"] != cfg.Callers[0].TokenID {
		t.Fatalf("revoke response=%s err=%v", body, err)
	}
	var event securityAccessEventRecord
	if err := svc.usage.db.Where("event_type = ?", "caller_token_revoked").First(&event).Error; err != nil {
		t.Fatal(err)
	}
	if event.TokenID != cfg.Callers[0].TokenID || event.ReasonCode != "self_revocation" {
		t.Fatalf("unsafe revocation audit: %+v", event)
	}
	inference, inferenceBody := post("/v1/chat/completions", `{"model":"default","messages":[{"role":"user","content":"hello"}]}`)
	if inference.StatusCode != http.StatusForbidden || !strings.Contains(string(inferenceBody), "key-exhausted") {
		t.Fatalf("revoked token remained active: status=%d body=%s", inference.StatusCode, inferenceBody)
	}
	var rows []usageRecord
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := svc.usage.db.Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) >= 3 {
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
	serializedEvent, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range [][]byte{[]byte(callerToken), []byte(cfg.Callers[0].TokenHash)} {
		if bytes.Contains(capturedLog.Bytes(), secret) || bytes.Contains(serialized, secret) || bytes.Contains(serializedEvent, secret) || bytes.Contains(body, secret) {
			t.Fatal("caller token or verifier appeared in logs, response or persisted records")
		}
	}
}

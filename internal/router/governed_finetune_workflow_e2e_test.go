// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const governedFineTuneOptOutToken = "rtr_governed_optout_testtoken"

func TestGovernedFineTuneCaptureExportExternalGateShadowPromoteRollbackE2E(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("external trainer fixture requires python3")
	}
	t.Setenv("MOCK_API_KEY", "test-only-governed-finetune-provider-key")
	t.Setenv("CONTENT_CAPTURE_LOCAL_KEY", strings.Repeat("11", 32))
	adminKey, err := GenerateAdminAPIKey("e2e-ops", nil, callerTokenHashMinIterations)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROUTER_ADMIN_KEY_E2E_OPS_HASH", adminKey.TokenHash)

	var shadowHits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var request struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if strings.HasPrefix(r.Header.Get("User-Agent"), "metrum-ai-router-shadow/") {
			shadowHits.Add(1)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_1", "choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "synthetic answer"}, "finish_reason": "stop",
			}}, "usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.sqlite")
	runner, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: configPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeDB() })
	applyConfigControlPlaneMigrationsForTest(t, runner)
	canonical := adminSwapE2ECanonicalYAML(t, upstream.URL)
	canonical = strings.Replace(canonical, "callers:\n  - id: alice\n", fmt.Sprintf(`callers:
  - id: optout
    owner_user: optout
    project: search
    environment: dev
    status: active
    token_hash: "%s"
    token_id: rtr_governed_optout
    allow:
      - uncaptured
  - id: alice
`, mustCallerTokenHashForTest(t, governedFineTuneOptOutToken)), 1)
	canonical = strings.Replace(canonical, "        domain: local/test\n", "        domain: '*'\n", 1)
	canonical = strings.Replace(canonical, "          - shadow:write\n", "          - shadow:write\n          - admin:training:export\n          - admin:training:label\n", 1)
	canonical = strings.Replace(canonical, "  default:\n    strategy: static\n", `  default:
    strategy: static
    content_capture:
      enabled: true
      retention_days: 1
      capture_request: true
      capture_response: true
      capture_tool_calls: true
      redact_before_storage: true
      max_capture_bytes: 65536
      encryption:
        enabled: true
        local_key_id: e2e-content-key
`, 1)
	canonical = strings.Replace(canonical, "callers:\n", `  uncaptured:
    strategy: static
    targets:
      - provider: mock
        model_ref: m-one
callers:
`, 1)
	canonical = strings.Replace(canonical, "providers:\n", `    authorization:
      enabled: true
      policy:
        - p, adminkey:e2e-ops, *, content:training, export|label
providers:
`, 1)
	setID, err := ImportCanonicalYAML(runner.db, "prod", "governed-finetune-e2e", []byte(canonical), "test-seed")
	if err != nil {
		t.Fatal(err)
	}
	if err := ActivateConfigSet(runner.db, "prod", setID, "test-seed"); err != nil {
		t.Fatal(err)
	}
	bootstrap := fmt.Sprintf(`server:
  listen: ':0'
  identifiers:
    mode: passthrough
  config_source:
    mode: database
    driver: sqlite
    runtime_scope: prod
    path: %s
providers:
  placeholder:
    base_url: https://placeholder.example/v1
    dialect: openai
    api_key_env: MOCK_API_KEY
models:
  placeholder:
    strategy: static
    targets:
      - provider: placeholder
        model: placeholder
`, configPath)
	bootstrapPath := filepath.Join(dir, "bootstrap.yaml")
	if err := os.WriteFile(bootstrapPath, []byte(bootstrap), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigForServe(bootstrapPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.AdminReports = AdminReportsConfig{Enabled: true, Security: AdminSecurityReportsConfig{Enabled: true, RetentionDays: 1}}
	t.Setenv("TRACE_GOVERNED_ADMIN_HASH", mustBcryptHash(t, "synthetic-e2e-admin-password"))
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{Enabled: true, AllowInsecureHTTP: true,
		Users: []AdminBasicAuthUser{{Username: "governed-test", Subject: "basic:governed-test", Domain: "*", PasswordHashEnv: "TRACE_GOVERNED_ADMIN_HASH"}}}
	cfg.StatePath = filepath.Join(dir, "router-state.json")
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	var adminSequence atomic.Uint64
	adminRequest := func(method, path, body string) *httptest.ResponseRecorder {
		reader := strings.NewReader(body)
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("Authorization", "Bearer "+adminKey.Token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", fmt.Sprintf("governed-finetune-%d", adminSequence.Add(1)))
		if method != http.MethodGet {
			etag, _, err := svc.adminV1CurrentETag(req)
			if err != nil {
				t.Fatalf("read admin ETag: %v", err)
			}
			req.Header.Set("If-Match", etag)
		}
		response := httptest.NewRecorder()
		svc.AdminHandler().ServeHTTP(response, req)
		return response
	}
	chatAs := func(token string, index int) (string, time.Time) {
		model := "default"
		if token == governedFineTuneOptOutToken {
			model = "uncaptured"
		}
		body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"synthetic workflow prompt %d"}]}`, model, index)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		svc.Handler().ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("synthetic route %d failed with status %d", index, response.Code)
		}
		id := response.Header().Get("X-Request-Id")
		if id == "" {
			id = response.Header().Get("X-Request-ID")
		}
		if id == "" {
			t.Fatal("router response omitted request ID")
		}
		flushCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := svc.usage.writer.flush(flushCtx); err != nil {
			cancel()
			t.Fatalf("flush routed usage: %v", err)
		}
		cancel()
		var usage usageRecord
		if err := svc.usage.db.First(&usage, "request_id = ?", id).Error; err != nil {
			t.Fatal("routed usage row was not persisted")
		}
		var captureCount int64
		if err := svc.usage.db.Model(&contentCaptureRecord{}).Where("request_id = ?", id).Count(&captureCount).Error; err != nil {
			t.Fatalf("read capture rows: %v", err)
		}
		if token == governedFineTuneOptOutToken && captureCount != 0 {
			t.Fatalf("caller without opt-in created %d capture rows", captureCount)
		}
		if token != governedFineTuneOptOutToken && captureCount < 2 {
			t.Fatalf("explicitly opted-in request has %d encrypted capture rows (caller=%q group=%q)", captureCount, usage.CallerID, usage.ResolvedGroup)
		}
		capturedAt, err := time.Parse(time.RFC3339Nano, usage.TS)
		if err != nil {
			t.Fatal("usage timestamp was invalid")
		}
		return id, capturedAt
	}
	chat := func(index int) (string, time.Time) { return chatAs(adminSwapE2ECallerToken, index) }
	labelCapture := func(index int, requestID string) {
		label := map[string]any{"version": "router-outcome-v1", "task_hash": strings.Repeat(string(rune('a'+index)), 64),
			"task_success": true, "schema_valid": true, "quality": 1.0, "backend": "acme-support-v1", "seed": index + 1}
		payload, _ := json.Marshal(label)
		deadline := time.Now().Add(5 * time.Second)
		for {
			response := adminRequest(http.MethodPost, "/admin/v1/training/outcomes/"+requestID, string(payload))
			if response.Code == http.StatusOK || response.Code == http.StatusCreated {
				return
			}
			if response.Code != http.StatusNotFound || time.Now().After(deadline) {
				t.Fatalf("label outcome failed with status %d", response.Code)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	// Three requests land on each side of the frozen train/validation/test
	// boundaries. Only this explicitly opted-in caller is captured.
	ids := make([]string, 3)
	stamps := make([]time.Time, 3)
	for i := range ids {
		ids[i], stamps[i] = chat(i)
		labelCapture(i, ids[i])
		time.Sleep(20 * time.Millisecond)
	}
	_, _ = chatAs(governedFineTuneOptOutToken, 20)
	filters := map[string]any{
		"project": "search", "group": "default",
		"from": stamps[0].Add(-time.Second), "validation_from": stamps[0].Add(5 * time.Millisecond),
		"test_from": stamps[1].Add(5 * time.Millisecond), "to": stamps[2].Add(time.Second),
		"success_only": true, "min_quality": 1.0,
		"teacher_terms_decision": map[string]string{"decision": "synthetic fixture", "training_outputs_allowed": "true"},
		"task_source":            map[string]string{"license": "Apache-2.0"},
	}
	filterBytes, _ := json.Marshal(filters)
	export := adminRequest(http.MethodPost, "/admin/v1/training/export", string(filterBytes))
	if export.Code != http.StatusOK || !strings.Contains(export.Header().Get("Content-Type"), "application/zip") {
		t.Fatalf("training export failed with status %d", export.Code)
	}
	archive, err := zip.NewReader(bytes.NewReader(export.Body.Bytes()), int64(export.Body.Len()))
	if err != nil {
		t.Fatalf("export was not a valid ZIP archive: %v", err)
	}
	counts := map[string]int{}
	for _, file := range archive.File {
		if file.Name == "manifest.json" {
			reader, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			var manifest struct {
				Files map[string]struct {
					Count int `json:"count"`
				} `json:"files"`
			}
			if err := json.NewDecoder(reader).Decode(&manifest); err != nil {
				t.Fatal("training manifest was invalid")
			}
			reader.Close()
			for split, info := range manifest.Files {
				counts[split] = info.Count
			}
		}
	}
	if counts["train.jsonl"] != 1 || counts["val.jsonl"] != 1 || counts["test.jsonl"] != 1 {
		var rows []usageRecord
		var captures []contentCaptureRecord
		_ = svc.usage.db.Select("request_id, ts, caller_project, resolved_group, token_id").Find(&rows).Error
		_ = svc.usage.db.Select("request_id, scope, retention_until").Find(&captures).Error
		t.Fatalf("frozen export split counts were unexpected: %+v usage=%+v capture_metadata=%+v", counts, rows, captures)
	}

	// Freeze the gate before invoking the external process. Its output is a
	// synthetic candidate marker, not trained model weights or a quality claim.
	gatePath := filepath.Join(dir, "preregistered-gate.json")
	if err := os.WriteFile(gatePath, []byte(`{"id":"governed-e2e-v1","minimum_train_rows":1,"minimum_train_success_rate":1.0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	exportPath, candidatePath := filepath.Join(dir, "training-export.zip"), filepath.Join(dir, "candidate.json")
	if err := os.WriteFile(exportPath, export.Body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	trainerPath := filepath.Join("..", "..", "examples", "fine-tune-qwen3p8-27b", "router-loop", "external_trainer.py")
	if _, err := os.Stat(trainerPath); err != nil {
		t.Fatalf("external trainer fixture missing: %v", err)
	}
	trainer := exec.Command("python3", trainerPath, "--export-zip", exportPath, "--preregistered-gate", gatePath, "--out", candidatePath)
	if output, err := trainer.CombinedOutput(); err != nil {
		t.Fatalf("external trainer fixture failed (exit %v): %s", err, strings.TrimSpace(string(output)))
	}
	var candidate struct {
		Target string `json:"candidate_target"`
		Gate   struct {
			Passed bool   `json:"passed"`
			ID     string `json:"id"`
		} `json:"gate"`
	}
	candidateBytes, err := os.ReadFile(candidatePath)
	if err != nil || json.Unmarshal(candidateBytes, &candidate) != nil || !candidate.Gate.Passed || candidate.Gate.ID != "governed-e2e-v1" || candidate.Target != "mock/m-two" {
		t.Fatal("external trainer candidate did not pass the frozen gate")
	}

	shadowBody, _ := json.Marshal(map[string]any{"provider": "mock", "model_ref": "m-two", "sample_rate": 1.0,
		"max_requests": 10, "reason": "preregistered gate passed"})
	shadow := adminRequest(http.MethodPost, "/admin/v1/config/groups/default/shadow", string(shadowBody))
	if shadow.Code != http.StatusCreated {
		t.Fatalf("shadow start failed with status %d", shadow.Code)
	}
	chat(3)
	deadline := time.Now().Add(5 * time.Second)
	for shadowHits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if shadowHits.Load() == 0 {
		t.Fatal("gated candidate did not receive shadow traffic")
	}
	promoted := adminRequest(http.MethodPost, "/admin/v1/config/groups/default/shadow/promote", `{"reason":"frozen gate passed"}`)
	if promoted.Code != http.StatusOK {
		t.Fatalf("candidate promotion failed with status %d", promoted.Code)
	}
	postPromoteID, _ := chat(4)
	assertServedModel := func(requestID, want string) {
		t.Helper()
		var row usageRecord
		if err := svc.usage.db.First(&row, "request_id = ?", requestID).Error; err != nil || row.TargetModel != want {
			t.Fatalf("request after config change served model %q, want %q (err=%v)", row.TargetModel, want, err)
		}
	}
	assertServedModel(postPromoteID, "mock-model-2")
	rollback := adminRequest(http.MethodPost, "/admin/v1/config/groups/default/rollback", `{"reason":"one-call recovery after canary"}`)
	if rollback.Code != http.StatusOK {
		t.Fatalf("group rollback failed with status %d", rollback.Code)
	}
	postRollbackID, _ := chat(5)
	assertServedModel(postRollbackID, "mock-model-1")
}

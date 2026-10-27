// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestConfigAdminShadowLifecycle(t *testing.T) {
	db := configAdminTestDB(t)
	mux := configAdminTestMux(t, db, nil)
	admin := adminHeaders("adminkey:ops", "config:read", "config:write", "config:activate", "shadow:write")

	// Missing bounds: 400.
	rr := configAdminRequest(t, mux, "POST", "/admin/config/groups/agent-default/shadow",
		map[string]any{"provider": "beta", "model_ref": "b-one", "sample_rate": 0.5}, admin)
	if rr.Code != http.StatusBadRequest || decodeErrorType(t, rr) != "config-invalid-request" {
		t.Fatalf("unbounded shadow status=%d body=%s", rr.Code, rr.Body.String())
	}
	// Bad sample rate: 400.
	rr = configAdminRequest(t, mux, "POST", "/admin/config/groups/agent-default/shadow",
		map[string]any{"provider": "beta", "model_ref": "b-one", "sample_rate": 1.5, "max_requests": 10}, admin)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad sample_rate status=%d body=%s", rr.Code, rr.Body.String())
	}
	// Ineligible candidate: 422 with the report.
	rr = configAdminRequest(t, mux, "POST", "/admin/config/groups/agent-default/shadow",
		map[string]any{"provider": "ghost", "model_ref": "g-1", "sample_rate": 0.5, "max_requests": 10}, admin)
	if rr.Code != http.StatusUnprocessableEntity || decodeErrorType(t, rr) != "target-ineligible" {
		t.Fatalf("ineligible shadow status=%d body=%s", rr.Code, rr.Body.String())
	}
	// shadow:write missing: 403.
	rr = configAdminRequest(t, mux, "POST", "/admin/config/groups/agent-default/shadow",
		map[string]any{"provider": "beta", "model_ref": "b-one", "sample_rate": 0.5, "max_requests": 10},
		adminHeaders("adminkey:ro", "config:read", "config:write"))
	if rr.Code != http.StatusForbidden || decodeErrorType(t, rr) != "shadow-forbidden" {
		t.Fatalf("shadow without shadow:write status=%d body=%s", rr.Code, rr.Body.String())
	}
	// Start: 201.
	rr = configAdminRequest(t, mux, "POST", "/admin/config/groups/agent-default/shadow",
		map[string]any{"provider": "beta", "model_ref": "b-one", "sample_rate": 0.5, "max_requests": 25, "reason": "candidate eval"}, admin)
	if rr.Code != http.StatusCreated {
		t.Fatalf("start status=%d body=%s", rr.Code, rr.Body.String())
	}
	var startBody struct {
		Shadow ShadowRun `json:"shadow"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &startBody); err != nil {
		t.Fatal(err)
	}
	if startBody.Shadow.RunID == "" || startBody.Shadow.Status != "active" || startBody.Shadow.SampleRate != 0.5 {
		t.Fatalf("start body=%s", rr.Body.String())
	}
	runID := startBody.Shadow.RunID

	// Second active shadow: 409.
	rr = configAdminRequest(t, mux, "POST", "/admin/config/groups/agent-default/shadow",
		map[string]any{"provider": "alpha", "model_ref": "a-two", "sample_rate": 0.1, "max_requests": 5}, admin)
	if rr.Code != http.StatusConflict || decodeErrorType(t, rr) != "shadow-already-active" {
		t.Fatalf("second shadow status=%d body=%s", rr.Code, rr.Body.String())
	}

	// Status/comparison: run visible with zero counters.
	rr = configAdminRequest(t, mux, "GET", "/admin/config/groups/agent-default/shadow", nil, admin)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), runID) {
		t.Fatalf("status status=%d body=%s", rr.Code, rr.Body.String())
	}

	// Stop: status stopped.
	rr = configAdminRequest(t, mux, "DELETE", "/admin/config/groups/agent-default/shadow", map[string]any{"reason": "done"}, admin)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"status":"stopped"`) {
		t.Fatalf("stop status=%d body=%s", rr.Code, rr.Body.String())
	}
	// Stop again: 404.
	rr = configAdminRequest(t, mux, "DELETE", "/admin/config/groups/agent-default/shadow", nil, admin)
	if rr.Code != http.StatusNotFound || decodeErrorType(t, rr) != "shadow-not-found" {
		t.Fatalf("second stop status=%d body=%s", rr.Code, rr.Body.String())
	}

	// Promote the stopped run: candidate becomes the first target, old
	// target stays as fallback, audit links the run.
	rr = configAdminRequest(t, mux, "POST", "/admin/config/groups/agent-default/shadow/promote", map[string]any{"reason": "promote"}, admin)
	if rr.Code != http.StatusOK {
		t.Fatalf("promote status=%d body=%s", rr.Code, rr.Body.String())
	}
	var promoteBody struct {
		SetID       string `json:"set_id"`
		ShadowRunID string `json:"shadow_run_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &promoteBody); err != nil {
		t.Fatal(err)
	}
	if promoteBody.ShadowRunID != runID || promoteBody.SetID == "" {
		t.Fatalf("promote body=%s", rr.Body.String())
	}
	raw, err := LoadActiveCanonicalYAML(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "model_ref: b-one") {
		t.Fatalf("promotion did not add candidate: %s", raw)
	}
	// Audit chain: shadow_promote event links the run.
	var detail string
	if err := db.Table("router_config_change_events").Select("detail").
		Where("action = ? AND group_name = ?", configActionShadowPromote, "agent-default").
		Order("at DESC").Limit(1).Scan(&detail).Error; err != nil {
		t.Fatal(err)
	}
	if detail != "shadow_run="+runID {
		t.Fatalf("promote audit detail=%q", detail)
	}
	// History surfaces shadow_start, shadow_stop, shadow_promote.
	rr = configAdminRequest(t, mux, "GET", "/admin/config/groups/agent-default/history", nil, admin)
	for _, action := range []string{configActionShadowStart, configActionShadowStop, configActionShadowPromote} {
		if !strings.Contains(rr.Body.String(), action) {
			t.Fatalf("history missing %s: %s", action, rr.Body.String())
		}
	}
	// Promote again: idempotent replay.
	rr = configAdminRequest(t, mux, "POST", "/admin/config/groups/agent-default/shadow/promote", map[string]any{"reason": "again"}, admin)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"replayed":true`) {
		t.Fatalf("promote replay status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestConfigAdminShadowFileModeRefused(t *testing.T) {
	var liveCfg Config
	if err := yamlUnmarshalForTest(configAdminTestYAML, &liveCfg); err != nil {
		t.Fatal(err)
	}
	liveCfg.setDefaults()
	mux := configAdminTestMux(t, nil, &liveCfg)
	admin := adminHeaders("adminkey:ops", "config:read", "shadow:write")
	rr := configAdminRequest(t, mux, "POST", "/admin/config/groups/agent-default/shadow",
		map[string]any{"provider": "beta", "model_ref": "b-one", "sample_rate": 0.5, "max_requests": 5}, admin)
	if rr.Code != http.StatusConflict || decodeErrorType(t, rr) != "config_source_mode_file" {
		t.Fatalf("file-mode shadow start status=%d body=%s", rr.Code, rr.Body.String())
	}
}

// TestServeE2EShadowSamplePromote drives a DB-backed service with an active
// shadow at sample_rate 1.0, proves the candidate receives replayed traffic
// while callers are unaffected, and promotes the candidate.
func TestServeE2EShadowSamplePromote(t *testing.T) {
	t.Setenv("MOCK_API_KEY", "test-only-shadow-e2e-provider-key")
	adminKey, err := GenerateAdminAPIKey("e2e-ops", nil, callerTokenHashMinIterations)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROUTER_ADMIN_KEY_E2E_OPS_HASH", adminKey.TokenHash)

	var shadowHits atomic.Int64
	mu := make(chan string, 100)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if strings.HasPrefix(r.Header.Get("User-Agent"), "metrum-ai-router-shadow/") {
			shadowHits.Add(1)
			mu <- req.Model
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_1",
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": "hello"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 7, "total_tokens": 12},
		})
	}))
	defer upstream.Close()

	dbPath := filepath.Join(t.TempDir(), "config.sqlite")
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeDB() })
	applyConfigControlPlaneMigrationsForTest(t, r)
	setID, err := ImportCanonicalYAML(r.db, "prod", "initial", []byte(adminSwapE2ECanonicalYAML(upstream.URL)), "test-seed")
	if err != nil {
		t.Fatal(err)
	}
	if err := ActivateConfigSet(r.db, "prod", setID, "test-seed"); err != nil {
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
`, dbPath)
	bootstrapPath := filepath.Join(t.TempDir(), "bootstrap.yaml")
	if err := os.WriteFile(bootstrapPath, []byte(bootstrap), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigForServe(bootstrapPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	cfg.StatePath = filepath.Join(t.TempDir(), "router-state.json")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	admin := map[string]string{"Authorization": "Bearer " + adminKey.Token}
	adminReq := func(method, path string, body string) *httptest.ResponseRecorder {
		var reader *strings.Reader
		if body != "" {
			reader = strings.NewReader(body)
		} else {
			reader = strings.NewReader("")
		}
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("Authorization", admin["Authorization"])
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		return rr
	}

	// Start the shadow at sample_rate 1.0 on candidate mock-model-2.
	rr := adminReq("POST", "/admin/config/groups/default/shadow",
		`{"provider":"mock","model_ref":"m-two","sample_rate":1.0,"max_requests":50,"reason":"e2e shadow"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("shadow start status=%d body=%s", rr.Code, rr.Body.String())
	}
	var startBody struct {
		Shadow ShadowRun `json:"shadow"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &startBody); err != nil {
		t.Fatal(err)
	}

	// Drive caller traffic; every request must succeed on the served target.
	const calls = 6
	for i := 0; i < calls; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer "+adminSwapE2ECallerToken)
		chatRR := httptest.NewRecorder()
		svc.Handler().ServeHTTP(chatRR, req)
		if chatRR.Code != http.StatusOK {
			t.Fatalf("chat %d status=%d body=%s", i, chatRR.Code, chatRR.Body.String())
		}
	}

	// The shadow candidate received replays and aggregate counters landed.
	deadline := time.Now().Add(15 * time.Second)
	var counters shadowRunRow
	for {
		if err := svc.configControlPlane.Where("run_id = ?", startBody.Shadow.RunID).First(&counters).Error; err != nil {
			t.Fatal(err)
		}
		if counters.Requests >= int64(calls) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("shadow counters did not converge: %+v (upstream hits %d)", counters, shadowHits.Load())
		}
		time.Sleep(100 * time.Millisecond)
	}
	if counters.Errors != 0 || counters.PromptTokens != 5*calls || counters.CompletionTokens != 7*calls {
		t.Fatalf("shadow counters wrong: %+v", counters)
	}
	// Every replay carried the candidate model.
	close(mu)
	for model := range mu {
		if model != "mock-model-2" {
			t.Fatalf("shadow replay used model %q", model)
		}
	}

	// Comparison endpoint reports both sides.
	rr = adminReq("GET", "/admin/config/groups/default/shadow", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("shadow status=%d body=%s", rr.Code, rr.Body.String())
	}
	var comparison struct {
		Run    ShadowRun `json:"run"`
		Served struct {
			Requests int64 `json:"requests"`
		} `json:"served"`
		Shadow shadowAggregateStats `json:"shadow"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &comparison); err != nil {
		t.Fatal(err)
	}
	if comparison.Shadow.Requests != calls || comparison.Shadow.Errors != 0 {
		t.Fatalf("comparison shadow side wrong: %s", rr.Body.String())
	}

	// Promote: candidate becomes primary; callers still see zero errors.
	rr = adminReq("POST", "/admin/config/groups/default/shadow/promote", `{"reason":"promote candidate"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("promote status=%d body=%s", rr.Code, rr.Body.String())
	}
	var promoteBody struct {
		ShadowRunID string `json:"shadow_run_id"`
		SetID       string `json:"set_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &promoteBody); err != nil {
		t.Fatal(err)
	}
	if promoteBody.ShadowRunID != startBody.Shadow.RunID || promoteBody.SetID == "" {
		t.Fatalf("promote body=%s", rr.Body.String())
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+adminSwapE2ECallerToken)
	chatRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(chatRR, req)
	if chatRR.Code != http.StatusOK {
		t.Fatalf("post-promote chat status=%d body=%s", chatRR.Code, chatRR.Body.String())
	}
}

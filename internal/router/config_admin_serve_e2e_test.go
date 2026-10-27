// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Service-level end-to-end test for the Admin API group swap (#395) on a
// DB-backed router: one authenticated PUT swaps the served model behind a
// group with no restart and no caller-visible change, a caller token can
// never drive the Admin API, and the status endpoint reports the revision.

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
)

const adminSwapE2ECallerToken = "rtr_admin_swap_e2e_caller_token"

// adminSwapE2ECanonicalYAML returns the serving document for the swap e2e.
// The admin key travels inside the canonical document (key_hash_env names
// the verifier environment variable; the raw key never appears).
func adminSwapE2ECanonicalYAML(upstreamURL string) string {
	return fmt.Sprintf(`server:
  listen: ':0'
  identifiers:
    mode: passthrough
  admin_auth:
    api_keys:
      - key_id: e2e-ops
        key_hash_env: ROUTER_ADMIN_KEY_E2E_OPS_HASH
        domain: local/test
        permissions:
          - config:read
          - config:write
          - config:activate
          - config:force
          - shadow:write
providers:
  mock:
    base_url: %s/v1
    dialect: openai
    api_key_env: MOCK_API_KEY
    models:
      m-one:
        model: mock-model-1
      m-two:
        model: mock-model-2
models:
  default:
    strategy: static
    targets:
      - provider: mock
        model_ref: m-one
callers:
  - id: alice
    owner_user: alice
    project: search
    environment: dev
    status: active
    token_sha256: "%x"
    token_id: rtr_admin_swap_e2e
    allow:
      - default
`, upstreamURL, sha256HexForTest(adminSwapE2ECallerToken))
}

func TestServeE2EAdminGroupSwapWithoutRestart(t *testing.T) {
	t.Setenv("MOCK_API_KEY", "test-only-swap-e2e-provider-key")
	adminKey, err := GenerateAdminAPIKey("e2e-ops", nil, callerTokenHashMinIterations)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROUTER_ADMIN_KEY_E2E_OPS_HASH", adminKey.TokenHash)

	// Mock upstream records the model it serves.
	var servedModel atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		servedModel.Store(req.Model)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_1",
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": "hello"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 4, "total_tokens": 7},
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

	chat := func() (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer "+adminSwapE2ECallerToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		served, _ := servedModel.Load().(string)
		return rr.Code, served
	}

	// Baseline: served by mock-model-1.
	if code, served := chat(); code != http.StatusOK || served != "mock-model-1" {
		t.Fatalf("baseline chat code=%d served=%q", code, served)
	}

	// The caller token is rejected by the Admin API.
	swapBody := `{"targets":[{"provider":"mock","model_ref":"m-two"}],"reason":"e2e swap"}`
	req := httptest.NewRequest(http.MethodPut, "/admin/config/groups/default/targets", strings.NewReader(swapBody))
	req.Header.Set("Authorization", "Bearer "+adminSwapE2ECallerToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("caller token on admin API status=%d body=%s", rr.Code, rr.Body.String())
	}

	// The admin key performs the one-call swap.
	req = httptest.NewRequest(http.MethodPut, "/admin/config/groups/default/targets", strings.NewReader(swapBody))
	req.Header.Set("Authorization", "Bearer "+adminKey.Token)
	rr = httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("swap status=%d body=%s", rr.Code, rr.Body.String())
	}
	var swapResp struct {
		SetID       string `json:"set_id"`
		Revision    int64  `json:"revision"`
		Propagation struct {
			ReplicaReloaded bool `json:"replica_reloaded"`
		} `json:"propagation"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &swapResp); err != nil {
		t.Fatal(err)
	}
	if swapResp.SetID == "" || swapResp.Revision < 2 || !swapResp.Propagation.ReplicaReloaded {
		t.Fatalf("swap response=%s", rr.Body.String())
	}

	// No restart, no caller change: the same group name now serves the new
	// model with zero client errors.
	for i := 0; i < 3; i++ {
		if code, served := chat(); code != http.StatusOK || served != "mock-model-2" {
			t.Fatalf("post-swap chat %d code=%d served=%q", i, code, served)
		}
	}

	// Status reports the new revision and this replica as current.
	req = httptest.NewRequest(http.MethodGet, "/admin/config/status", nil)
	req.Header.Set("Authorization", "Bearer "+adminKey.Token)
	rr = httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var statusBody struct {
		Revision int64 `json:"revision"`
		Replica  struct {
			LoadedRevision int64 `json:"loaded_revision"`
			Current        bool  `json:"current"`
		} `json:"replica"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &statusBody); err != nil {
		t.Fatal(err)
	}
	if statusBody.Revision != swapResp.Revision || !statusBody.Replica.Current || statusBody.Replica.LoadedRevision != swapResp.Revision {
		t.Fatalf("status body=%s", rr.Body.String())
	}

	// One-call group rollback restores the original target.
	req = httptest.NewRequest(http.MethodPost, "/admin/config/groups/default/rollback", strings.NewReader(`{"reason":"e2e revert"}`))
	req.Header.Set("Authorization", "Bearer "+adminKey.Token)
	rr = httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("rollback status=%d body=%s", rr.Code, rr.Body.String())
	}
	if code, served := chat(); code != http.StatusOK || served != "mock-model-1" {
		t.Fatalf("post-rollback chat code=%d served=%q", code, served)
	}
}

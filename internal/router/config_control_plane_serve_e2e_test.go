// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// End-to-end tests for the database-mode config source of truth.
// The tests use a SQLite temp DB (offline, make test-fast friendly) and
// cover the import → activate → serve auth lifecycle, revoke-without-restart
// fail-closed behavior, and the fail-closed path when no config set is
// active.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestContainmentWebhookMountAdminV1ReloadsAndRefusesCaller(t *testing.T) {
	t.Setenv("MOCK_API_KEY", "test-only-serve-e2e-provider-key")
	secret := strings.Repeat("w", 32)
	t.Setenv("TEST_CONTAINMENT_WEBHOOK_SECRET", secret)
	dbPath := serveAuthE2EDB(t)
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeDB() })
	applyConfigControlPlaneMigrationsForTest(t, r)
	setID, err := ImportCanonicalYAML(r.db, "staging", "initial", []byte(serveAuthCanonicalYAML()), "test-actor")
	if err != nil {
		t.Fatal(err)
	}
	if err := ActivateConfigSet(r.db, "staging", setID, "test-actor"); err != nil {
		t.Fatal(err)
	}
	bootstrapPath := filepath.Join(t.TempDir(), "bootstrap.yaml")
	if err := os.WriteFile(bootstrapPath, []byte(serveAuthBootstrapYAML(t, dbPath, "staging")), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigForServe(bootstrapPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	cfg.StatePath = filepath.Join(t.TempDir(), "router-state.json")
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	containmentPolicy, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, role:containment, *, admin:containment, contain"}}, nil, nil, AdminAuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	svc.authorizerSnapshot.Store(containmentPolicy)
	svc.cfg.Server.ContainmentWebhook = ContainmentWebhookConfig{Enabled: true, SecretEnv: "TEST_CONTAINMENT_WEBHOOK_SECRET", TimestampWindow: "10m"}
	adminMux := http.NewServeMux()
	allow := func(_ context.Context, p Principal, _ string, object, action string) error {
		if p.Subject == authzRoleContainment && object == authzObjectAdminContainment && action == authzActionContain {
			return nil
		}
		return ErrAuthorizationUnavailable
	}
	if err := svc.MountAdminV1(adminMux, testAuthenticator{}, allow); err != nil {
		t.Fatalf("MountAdminV1: %v", err)
	}
	serveRequest := func() int {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+serveAuthToken)
		rec := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	if got := serveRequest(); got < 200 || got >= 300 {
		t.Fatalf("caller request before containment = %d", got)
	}
	body := `{"caller_id":"alice","action":"suspend","reason":"edr-alert"}`
	timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	nonce := "soar-reload-path-0001"
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "\n" + nonce + "\n" + body))
	webhook := httptest.NewRequest(http.MethodPost, containmentWebhookPath, strings.NewReader(body))
	webhook.Header.Set("X-Containment-Timestamp", timestamp)
	webhook.Header.Set("X-Containment-Nonce", nonce)
	webhook.Header.Set("X-Containment-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	webhookRec := httptest.NewRecorder()
	adminMux.ServeHTTP(webhookRec, webhook)
	if webhookRec.Code != http.StatusOK {
		t.Fatalf("mounted webhook status = %d body=%s", webhookRec.Code, webhookRec.Body.String())
	}
	if got := serveRequest(); got != http.StatusUnauthorized && got != http.StatusForbidden {
		t.Fatalf("caller request after live reload = %d, want refused", got)
	}
}

// serveAuthToken is the raw bearer token used by the e2e harness. It lives
// only in this test file and never leaves the test process. Its SHA-256 is
// embedded into the canonical YAML; the raw value flows only through the
// authenticate call.
const serveAuthToken = "rtr_serve_e2e_harness_token"

// serveAuthCanonicalYAML returns the canonical YAML used by the e2e tests.
// The caller block carries a salted verifier for serveAuthToken so the
// relational projection and runtime authenticate map share one source of truth.
func serveAuthCanonicalYAML() string {
	tokenHash, err := HashCallerToken(serveAuthToken, bytes.NewReader(make([]byte, callerTokenHashSaltBytes)), callerTokenHashMinIterations)
	if err != nil {
		panic(err)
	}
	return `server:
  identifiers:
    mode: passthrough
providers:
  mock:
    base_url: https://mock.example/v1
    dialect: openai
    api_key_env: MOCK_API_KEY
models:
  default:
    strategy: static
    targets:
      - provider: mock
        model: mock-model
callers:
  - id: alice
    owner_user: alice
    project: search
    environment: dev
    status: active
    token_hash: "` + tokenHash + `"
    token_id: rtr_serve_e2e_harness
    allow:
      - default
`
}

// serveAuthBootstrapYAML writes a bootstrap config that points the runtime at
// the SQLite control-plane database. It is consumed by LoadConfigForServe.
// The identifiers block mirrors the canonical YAML so the bootstrap does
// not clobber the passthrough mode that the active document declares. The
// bootstrap must also carry at least one provider because LoadConfig
// validates the file before the database source is consulted.
func serveAuthBootstrapYAML(t *testing.T, dbPath, runtimeScope string) string {
	t.Helper()
	return `server:
  listen: ":0"
  identifiers:
    mode: passthrough
  config_source:
    mode: database
    driver: sqlite
    runtime_scope: ` + runtimeScope + `
    path: ` + dbPath + `
providers:
  mock:
    base_url: https://mock.example/v1
    dialect: openai
    api_key_env: MOCK_API_KEY
models:
  default:
    strategy: static
    targets:
      - provider: mock
        model: mock-model
`
}

// serveAuthE2EDB returns the control-plane gorm.DB and the on-disk path for
// the temp SQLite database. The caller is responsible for closing the DB
// (use t.Cleanup).
func serveAuthE2EDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "config.sqlite")
}

// TestServeE2EImportActivateServeAuth covers the happy path: migrate the
// control-plane schema, import a canonical YAML document, activate the
// config set, bootstrap the runtime with mode=database, load the active
// document, build a Service, and prove that the bearer token from the
// canonical YAML authenticates while an unknown token does not.
func TestServeE2EImportActivateServeAuth(t *testing.T) {
	// Provider credential env so LoadActiveCanonicalConfig can expand
	// $MOCK_API_KEY references when the document is served.
	t.Setenv("MOCK_API_KEY", "test-only-serve-e2e-provider-key")

	dbPath := serveAuthE2EDB(t)
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeDB() })
	applyConfigControlPlaneMigrationsForTest(t, r)

	// Import + activate the canonical YAML so the runtime has a valid
	// active set in the chosen runtime_scope.
	configSetID, err := ImportCanonicalYAML(r.db, "staging", "initial", []byte(serveAuthCanonicalYAML()), "test-actor")
	if err != nil {
		t.Fatalf("ImportCanonicalYAML: %v", err)
	}
	if err := ActivateConfigSet(r.db, "staging", configSetID, "test-actor"); err != nil {
		t.Fatalf("ActivateConfigSet: %v", err)
	}

	// Bootstrap config that selects the temp DB as the source of truth.
	bootstrapPath := filepath.Join(t.TempDir(), "bootstrap.yaml")
	if err := os.WriteFile(bootstrapPath, []byte(serveAuthBootstrapYAML(t, dbPath, "staging")), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfigForServe(bootstrapPath)
	if err != nil {
		t.Fatalf("LoadConfigForServe: %v", err)
	}
	if got, want := cfg.Server.ConfigSource.Mode, "database"; got != want {
		t.Fatalf("config_source.mode = %q, want %q", got, want)
	}
	if len(cfg.Callers) != 1 || cfg.Callers[0].ID != "alice" {
		t.Fatalf("active canonical config did not carry the imported caller: %+v", cfg.Callers)
	}

	// Build a service from the loaded config. Usage DB needs an explicit
	// SQLite path so newUsageStore does not reuse the bootstrap one, and the
	// quota state file must not default into the package directory.
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	cfg.StatePath = filepath.Join(t.TempDir(), "router-state.json")
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { svc.Close() })

	// Known token from the canonical YAML succeeds.
	caller, tokenID, err := svc.authenticate("Bearer "+serveAuthToken, "")
	if err != nil {
		t.Fatalf("authenticate with known token: %v", err)
	}
	if caller == nil || caller.cfg.ID != "alice" {
		t.Fatalf("authenticate returned wrong caller: %+v", caller)
	}
	if tokenID == "" {
		t.Fatal("authenticate returned empty token id")
	}

	// Unknown token fails closed.
	if _, tokenID, err := svc.authenticate("Bearer not-a-real-token", ""); err == nil {
		t.Fatal("unknown token must not authenticate")
	} else if tokenID != "invalid-token" {
		t.Fatalf("unknown token id = %q, want invalid-token", tokenID)
	}
}

func TestServeE2EMCPGatewayLoadedFromCanonicalConfig(t *testing.T) {
	t.Setenv("MOCK_API_KEY", "test-only-serve-e2e-provider-key")
	t.Setenv("ROUTER_MCP_SERVER_AUTH", "test-only-mcp-upstream-key")
	var upstreamErrors []string
	upstreamRequests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		upstreamRequests++
		if r.Header.Get("Authorization") != "Bearer test-only-mcp-upstream-key" {
			upstreamErrors = append(upstreamErrors, "static credential missing")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			upstreamErrors = append(upstreamErrors, "invalid JSON-RPC request")
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		switch request.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "mcp-db-session")
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "serverInfo": map[string]string{"name": "fixture", "version": "1"}}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			if r.Header.Get("Mcp-Session-Id") != "mcp-db-session" {
				upstreamErrors = append(upstreamErrors, "MCP session missing")
				http.Error(w, "session missing", http.StatusBadRequest)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"tools": []any{map[string]any{"name": "echo", "inputSchema": map[string]any{"type": "object"}}}}})
		default:
			upstreamErrors = append(upstreamErrors, "unexpected MCP method")
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer upstream.Close()

	dbPath := serveAuthE2EDB(t)
	runner, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeDB() })
	applyConfigControlPlaneMigrationsForTest(t, runner)
	canonical := strings.Replace(serveAuthCanonicalYAML(), "callers:\n", "mcp:\n  enabled: true\n  servers:\n    - name: local\n      url: \""+upstream.URL+"\"\n      allow_hosts: [127.0.0.1]\n      credential_env: ROUTER_MCP_SERVER_AUTH\n  policies:\n    - caller: alice\n      tools: [local/echo]\n      deny_by_default: true\ncallers:\n", 1)
	configSetID, err := ImportCanonicalYAML(runner.db, "staging", "mcp", []byte(canonical), "test-actor")
	if err != nil {
		t.Fatalf("ImportCanonicalYAML: %v", err)
	}
	if err := ActivateConfigSet(runner.db, "staging", configSetID, "test-actor"); err != nil {
		t.Fatalf("ActivateConfigSet: %v", err)
	}
	bootstrapPath := filepath.Join(t.TempDir(), "bootstrap.yaml")
	if err := os.WriteFile(bootstrapPath, []byte(serveAuthBootstrapYAML(t, dbPath, "staging")), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigForServe(bootstrapPath)
	if err != nil {
		t.Fatalf("LoadConfigForServe: %v", err)
	}
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	cfg.StatePath = filepath.Join(t.TempDir(), "router-state.json")
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer "+serveAuthToken)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "local/echo") {
		t.Fatalf("database-loaded MCP tools/list status=%d body=%s", w.Code, w.Body.String())
	}
	if len(upstreamErrors) != 0 {
		t.Fatalf("upstream errors: %v", upstreamErrors)
	}
	beforeDisable := upstreamRequests
	mcpStart := strings.Index(canonical, "mcp:\n")
	if mcpStart < 0 {
		t.Fatal("test canonical config missing MCP block")
	}
	callersStart := strings.Index(canonical[mcpStart:], "callers:\n")
	if callersStart < 0 {
		t.Fatal("test canonical config missing callers block")
	}
	disabledCanonical := canonical[:mcpStart] + canonical[mcpStart+callersStart:]
	disabledSet, err := ImportCanonicalYAML(runner.db, "staging", "mcp-disabled", []byte(disabledCanonical), "test-actor")
	if err != nil {
		t.Fatalf("import disabled config: %v", err)
	}
	if err := ActivateConfigSet(runner.db, "staging", disabledSet, "test-actor"); err != nil {
		t.Fatalf("activate disabled config: %v", err)
	}
	if err := svc.reloadActiveConfigFromDB(); err != nil {
		t.Fatalf("reload disabled MCP config: %v", err)
	}
	disabledRequest := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	disabledRequest.Header.Set("Authorization", "Bearer "+serveAuthToken)
	disabledResponse := httptest.NewRecorder()
	svc.Handler().ServeHTTP(disabledResponse, disabledRequest)
	if disabledResponse.Code != http.StatusNotFound || upstreamRequests != beforeDisable {
		t.Fatalf("disabled MCP route status=%d upstream calls=%d want 404 and no forward", disabledResponse.Code, upstreamRequests-beforeDisable)
	}
	reenabledSet, err := ImportCanonicalYAML(runner.db, "staging", "mcp-reenabled", []byte(canonical), "test-actor")
	if err != nil {
		t.Fatalf("import re-enabled config: %v", err)
	}
	if err := ActivateConfigSet(runner.db, "staging", reenabledSet, "test-actor"); err != nil {
		t.Fatalf("activate re-enabled config: %v", err)
	}
	if err := svc.reloadActiveConfigFromDB(); err != nil {
		t.Fatalf("reload re-enabled MCP config: %v", err)
	}
	reenabledRequest := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`))
	reenabledRequest.Header.Set("Authorization", "Bearer "+serveAuthToken)
	reenabledResponse := httptest.NewRecorder()
	svc.Handler().ServeHTTP(reenabledResponse, reenabledRequest)
	if reenabledResponse.Code != http.StatusOK || upstreamRequests <= beforeDisable {
		t.Fatalf("re-enabled MCP route status=%d upstream calls=%d", reenabledResponse.Code, upstreamRequests-beforeDisable)
	}
}

// TestServeE2ERevokeWithoutRestart covers the revoke-without-restart
// requirement: after a caller has been authenticated, an admin revoke must
// cause the same bearer token to be rejected without restarting the
// service. AfterMutate must trigger reloadActiveConfigFromDB so callersBySum
// reflects the disabled status from the canonical document.
func TestServeE2ERevokeWithoutRestart(t *testing.T) {
	t.Setenv("MOCK_API_KEY", "test-only-serve-e2e-provider-key")

	dbPath := serveAuthE2EDB(t)
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeDB() })
	applyConfigControlPlaneMigrationsForTest(t, r)

	configSetID, err := ImportCanonicalYAML(r.db, "staging", "initial", []byte(serveAuthCanonicalYAML()), "test-actor")
	if err != nil {
		t.Fatalf("ImportCanonicalYAML: %v", err)
	}
	if err := ActivateConfigSet(r.db, "staging", configSetID, "test-actor"); err != nil {
		t.Fatalf("ActivateConfigSet: %v", err)
	}

	bootstrapPath := filepath.Join(t.TempDir(), "bootstrap.yaml")
	if err := os.WriteFile(bootstrapPath, []byte(serveAuthBootstrapYAML(t, dbPath, "staging")), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfigForServe(bootstrapPath)
	if err != nil {
		t.Fatalf("LoadConfigForServe: %v", err)
	}
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	cfg.StatePath = filepath.Join(t.TempDir(), "router-state.json")
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { svc.Close() })

	// Sanity check: the bearer token authenticates before revoke.
	if _, _, err := svc.authenticate("Bearer "+serveAuthToken, ""); err != nil {
		t.Fatalf("pre-revoke authenticate: %v", err)
	}

	// Revoke through the admin caller directory with AfterMutate wired to the
	// same Service reload path used in production DB mode.
	mux := http.NewServeMux()
	RegisterCallerDirectoryAdmin(mux, CallerAdminDeps{
		DB:           r.db,
		RuntimeScope: "staging",
		Authorize: func(r *http.Request, object, action string) bool {
			return r.Header.Get("X-Test-Admin") == "yes" && object == authzObjectAdminCallers
		},
		AfterMutate: func(r *http.Request, callerID string) error {
			_ = r
			_ = callerID
			return svc.reloadActiveConfigFromDB()
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/callers/alice/revoke", nil)
	req.Header.Set("X-Test-Admin", "yes")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke status = %d body = %s", rec.Code, rec.Body.String())
	}

	// The relational projection must reflect the disabled status so a
	// reload-based runtime can read the new state.
	var status string
	if err := r.db.Raw(`SELECT status FROM router_config_callers WHERE config_set_id = ? AND caller_id = ?`, configSetID, "alice").Row().Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != accountStatusDisabled {
		t.Fatalf("relational caller status = %q, want %q", status, accountStatusDisabled)
	}

	// Without a restart, the runtime must now refuse the same bearer
	// token because the caller is disabled.
	caller, tokenID, authErr := svc.authenticate("Bearer "+serveAuthToken, "")
	if authErr == nil {
		t.Fatalf("post-revoke authenticate must fail closed; got caller=%+v tokenID=%q", caller, tokenID)
	}
	if caller == nil || caller.cfg.ID != "alice" || tokenID != "rtr_serve_e2e_harness" {
		t.Fatal("post-revoke authentication must retain safe identity for denial attribution")
	}
	if !strings.Contains(authErr.Error(), "disabled") && !strings.Contains(authErr.Error(), "key-") {
		t.Fatalf("post-revoke authenticate error = %v, want disabled-style failure", authErr)
	}
}

// TestServeE2EFailsClosedWithoutActiveSet covers the fail-closed contract:
// when mode=database points at a control-plane database with no active
// config set for the requested runtime_scope, LoadConfigForServe must
// return an error rather than silently fall back to the bootstrap YAML.
func TestServeE2EFailsClosedWithoutActiveSet(t *testing.T) {
	t.Setenv("MOCK_API_KEY", "test-only-serve-e2e-provider-key")

	dbPath := serveAuthE2EDB(t)
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeDB() })
	applyConfigControlPlaneMigrationsForTest(t, r)

	// No import, no activate — the runtime_scope has no active config set.
	bootstrapPath := filepath.Join(t.TempDir(), "bootstrap.yaml")
	if err := os.WriteFile(bootstrapPath, []byte(serveAuthBootstrapYAML(t, dbPath, "staging")), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = LoadConfigForServe(bootstrapPath)
	if err == nil {
		t.Fatal("LoadConfigForServe must fail closed when no active config set exists")
	}
	if !strings.Contains(err.Error(), "active") {
		t.Fatalf("LoadConfigForServe error must mention the active set: %v", err)
	}
}

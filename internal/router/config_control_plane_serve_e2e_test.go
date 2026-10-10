// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// End-to-end tests for the database-mode config source of truth.
// The tests use a SQLite temp DB (offline, make test-fast friendly) and
// cover the import → activate → serve auth lifecycle, revoke-without-restart
// fail-closed behavior, and the fail-closed path when no config set is
// active.

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// serveAuthToken is the raw bearer token used by the e2e harness. It lives
// only in this test file and never leaves the test process. Its SHA-256 is
// embedded into the canonical YAML; the raw value flows only through the
// authenticate call.
const serveAuthToken = "rtr_serve_e2e_harness_token"

// serveAuthCanonicalYAML returns the canonical YAML used by the e2e tests.
// The caller block carries the SHA-256 of serveAuthToken so the relational
// projection and the runtime authenticate map share one source of truth.
func serveAuthCanonicalYAML() string {
	sum := sha256.Sum256([]byte(serveAuthToken))
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
    token_sha256: "` + hex.EncodeToString(sum[:]) + `"
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
	svc, err := New(cfg)
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
	svc, err := New(cfg)
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
	if caller != nil {
		t.Fatalf("post-revoke authenticate must not return a caller: %+v", caller)
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

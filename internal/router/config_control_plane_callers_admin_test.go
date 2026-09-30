// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

func callerAdminTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeDB() })
	applyConfigControlPlaneMigrationsForTest(t, r)
	db := r.db
	now := time.Now().UTC().Format(time.RFC3339Nano)
	seeds := []struct {
		sql    string
		values []any
	}{
		{`INSERT INTO router_config_sets (id, runtime_scope, name, status, validation_status, created_at) VALUES (?, ?, ?, ?, ?, ?)`, []any{"set-1", "staging", "initial", "active", "valid", now}},
		{`INSERT INTO router_config_model_groups (config_set_id, group_name, strategy) VALUES (?, ?, ?)`, []any{"set-1", "default", "static"}},
		{`INSERT INTO router_config_model_groups (config_set_id, group_name, strategy) VALUES (?, ?, ?)`, []any{"set-1", "premium", "static"}},
	}
	for _, seed := range seeds {
		if err := db.Exec(seed.sql, seed.values...).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Seed a canonical document so caller mutations can sync the YAML.
	canonicalYAML := `server:
  listen: ':8080'
  default_model_group: default
callers: []
`
	hash := sha256.Sum256([]byte(canonicalYAML))
	if err := db.Exec(
		`INSERT INTO router_config_documents (config_set_id, canonical_yaml, content_sha256, updated_at) VALUES (?, ?, ?, ?)`,
		"set-1", canonicalYAML, hex.EncodeToString(hash[:]), now,
	).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func callerAdminTestMux(t *testing.T, db *gorm.DB, mutated *[]string) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	RegisterCallerDirectoryAdmin(mux, CallerAdminDeps{
		DB:           db,
		RuntimeScope: "staging",
		Authorize: func(r *http.Request, object, action string) bool {
			return r.Header.Get("X-Test-Admin") == "yes" && object == authzObjectAdminCallers
		},
		AfterMutate: func(r *http.Request, callerID string) error {
			if mutated != nil {
				*mutated = append(*mutated, callerID)
			}
			return nil
		},
		Now: func() time.Time { return time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC) },
	})
	return mux
}

func callerAdminRequest(t *testing.T, mux *http.ServeMux, method, path string, body any, admin bool) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if admin {
		req.Header.Set("X-Test-Admin", "yes")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestCallerAdminIssueListRotateRevokeLifecycle(t *testing.T) {
	db := callerAdminTestDB(t)
	var mutated []string
	mux := callerAdminTestMux(t, db, &mutated)

	// Issue a new caller.
	issueRec := callerAdminRequest(t, mux, http.MethodPost, "/admin/callers/issue", map[string]any{
		"owner_user": "Alice",
		"project":    "Search",
		"allow":      []string{"default", "premium"},
	}, true)
	if issueRec.Code != http.StatusCreated {
		t.Fatalf("issue status = %d body = %s", issueRec.Code, issueRec.Body.String())
	}
	var issueResp callerAdminIssueResponse
	if err := json.Unmarshal(issueRec.Body.Bytes(), &issueResp); err != nil {
		t.Fatal(err)
	}
	if issueResp.Caller.ID != "alice-search-dev" || issueResp.Caller.Status != accountStatusActive {
		t.Fatalf("unexpected issued caller: %+v", issueResp.Caller)
	}
	if issueResp.Token == "" || !strings.HasPrefix(issueResp.Token, issueResp.TokenID+"_") {
		t.Fatalf("raw token must be returned once with the public token id prefix: %+v", issueResp)
	}
	sum := sha256.Sum256([]byte(issueResp.Token))
	if issueResp.Caller.TokenSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("persisted hash must match the returned raw token")
	}

	// The relational projection stores the hash and allows, never the raw token.
	var storedHash, storedTokenID string
	if err := db.Raw(`SELECT token_sha256, token_id FROM router_config_callers WHERE config_set_id = ? AND caller_id = ?`, "set-1", "alice-search-dev").Row().Scan(&storedHash, &storedTokenID); err != nil {
		t.Fatal(err)
	}
	if storedHash != issueResp.Caller.TokenSHA256 || storedTokenID != issueResp.TokenID {
		t.Fatal("relational caller row mismatch")
	}
	var rawCount int64
	if err := db.Table("router_config_callers").Where("token_sha256 = ? OR token_id = ?", issueResp.Token, issueResp.Token).Count(&rawCount).Error; err != nil {
		t.Fatal(err)
	}
	if rawCount != 0 {
		t.Fatal("raw token must never be persisted")
	}
	var allowCount int64
	if err := db.Table("router_config_caller_allowed_groups").Where("config_set_id = ? AND caller_id = ?", "set-1", "alice-search-dev").Count(&allowCount).Error; err != nil {
		t.Fatal(err)
	}
	if allowCount != 2 {
		t.Fatalf("expected 2 allowed groups, got %d", allowCount)
	}

	// Duplicate issue conflicts.
	dupRec := callerAdminRequest(t, mux, http.MethodPost, "/admin/callers/issue", map[string]any{
		"owner_user": "Alice",
		"project":    "Search",
		"allow":      []string{"default"},
	}, true)
	if dupRec.Code != http.StatusConflict {
		t.Fatalf("duplicate issue status = %d body = %s", dupRec.Code, dupRec.Body.String())
	}

	// List reflects the issued caller without any raw token material.
	listRec := callerAdminRequest(t, mux, http.MethodGet, "/admin/callers", nil, true)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", listRec.Code, listRec.Body.String())
	}
	if strings.Contains(listRec.Body.String(), issueResp.Token) {
		t.Fatal("list response must never contain a raw token")
	}
	var listResp callerAdminListResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	if len(listResp.Callers) != 1 || listResp.Callers[0].ID != "alice-search-dev" || len(listResp.Callers[0].Allow) != 2 {
		t.Fatalf("unexpected list response: %+v", listResp)
	}

	// Rotate replaces the stored hash and returns the new raw token once.
	rotateRec := callerAdminRequest(t, mux, http.MethodPost, "/admin/callers/alice-search-dev/rotate", nil, true)
	if rotateRec.Code != http.StatusOK {
		t.Fatalf("rotate status = %d body = %s", rotateRec.Code, rotateRec.Body.String())
	}
	var rotateResp callerAdminTokenResponse
	if err := json.Unmarshal(rotateRec.Body.Bytes(), &rotateResp); err != nil {
		t.Fatal(err)
	}
	if rotateResp.Token == "" || rotateResp.Token == issueResp.Token {
		t.Fatal("rotate must return a fresh raw token")
	}
	var rotatedHash string
	if err := db.Raw(`SELECT token_sha256 FROM router_config_callers WHERE config_set_id = ? AND caller_id = ?`, "set-1", "alice-search-dev").Row().Scan(&rotatedHash); err != nil {
		t.Fatal(err)
	}
	rotatedSum := sha256.Sum256([]byte(rotateResp.Token))
	if rotatedHash != hex.EncodeToString(rotatedSum[:]) || rotatedHash == storedHash {
		t.Fatal("rotate must replace the stored hash with the new token hash")
	}

	// Revoke disables the caller.
	revokeRec := callerAdminRequest(t, mux, http.MethodPost, "/admin/callers/alice-search-dev/revoke", nil, true)
	if revokeRec.Code != http.StatusOK {
		t.Fatalf("revoke status = %d body = %s", revokeRec.Code, revokeRec.Body.String())
	}
	var status string
	if err := db.Raw(`SELECT status FROM router_config_callers WHERE config_set_id = ? AND caller_id = ?`, "set-1", "alice-search-dev").Row().Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != accountStatusDisabled {
		t.Fatalf("revoke must set status disabled, got %q", status)
	}

	// Every successful mutation bumped the runtime revision hook exactly once.
	if len(mutated) != 3 {
		t.Fatalf("AfterMutate calls = %v, want 3 mutations", mutated)
	}
}

func TestCallerAdminRequiresAuthorization(t *testing.T) {
	db := callerAdminTestDB(t)
	mux := callerAdminTestMux(t, db, nil)
	for _, tc := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/admin/callers", nil},
		{http.MethodPost, "/admin/callers/issue", map[string]any{"owner_user": "a", "project": "b", "allow": []string{"default"}}},
		{http.MethodPost, "/admin/callers/x/rotate", nil},
		{http.MethodPost, "/admin/callers/x/revoke", nil},
	} {
		rec := callerAdminRequest(t, mux, tc.method, tc.path, tc.body, false)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s without authz status = %d, want 403", tc.method, tc.path, rec.Code)
		}
	}
}

func TestCallerAdminRotateRevokeUnknownCaller(t *testing.T) {
	db := callerAdminTestDB(t)
	mux := callerAdminTestMux(t, db, nil)
	if rec := callerAdminRequest(t, mux, http.MethodPost, "/admin/callers/ghost/rotate", nil, true); rec.Code != http.StatusNotFound {
		t.Fatalf("rotate unknown status = %d, want 404", rec.Code)
	}
	if rec := callerAdminRequest(t, mux, http.MethodPost, "/admin/callers/ghost/revoke", nil, true); rec.Code != http.StatusNotFound {
		t.Fatalf("revoke unknown status = %d, want 404", rec.Code)
	}
}

func TestCanonicalCallersPatchMutatesCallersSlice(t *testing.T) {
	raw := []byte("server:\n  listen: ':8080'\ncallers:\n  - id: alice-search-dev\n    owner_user: alice\n    project: search\n    environment: dev\n    status: active\n    token_sha256: deadbeef\n    token_id: rtr_metrum_alice_search_dev_k20260721\n    allow:\n      - default\n    rate:\n      rpm: 120\n      tpm: 200000\n      concurrent: 8\n")
	out, err := PatchCanonicalCallersYAML(raw, func(callers []CallerConfig) ([]CallerConfig, error) {
		if len(callers) != 1 || callers[0].ID != "alice-search-dev" || callers[0].Rate.RPM != 120 {
			t.Fatalf("decoded callers mismatch: %+v", callers)
		}
		callers[0].Status = accountStatusDisabled
		return append(callers, CallerConfig{ID: "bob-ml-prod", OwnerUser: "bob", Project: "ml", Environment: "prod", Status: accountStatusActive, TokenSHA256: "cafe", TokenID: "rtr_metrum_bob_ml_prod_k20260721", Allow: []string{"premium"}}), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Server  map[string]any `yaml:"server"`
		Callers []CallerConfig `yaml:"callers"`
	}
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Server["listen"] != ":8080" {
		t.Fatalf("non-callers document content must be preserved: %v", doc.Server)
	}
	if len(doc.Callers) != 2 || doc.Callers[0].Status != accountStatusDisabled || doc.Callers[1].ID != "bob-ml-prod" {
		t.Fatalf("patched callers mismatch: %+v", doc.Callers)
	}
}

func TestCanonicalCallersPatchHandlesMissingCallersKey(t *testing.T) {
	out, err := PatchCanonicalCallersYAML([]byte("server:\n  listen: ':8080'\n"), func(callers []CallerConfig) ([]CallerConfig, error) {
		if len(callers) != 0 {
			t.Fatalf("missing callers key must decode empty, got %+v", callers)
		}
		return []CallerConfig{{ID: "a-b-dev", OwnerUser: "a", Project: "b", Environment: "dev", Status: accountStatusActive, Allow: []string{"default"}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Callers []CallerConfig `yaml:"callers"`
	}
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Callers) != 1 || doc.Callers[0].ID != "a-b-dev" {
		t.Fatalf("patched callers mismatch: %+v", doc.Callers)
	}
}

func TestCanonicalCallersPatchRejectsInvalidYAML(t *testing.T) {
	if _, err := PatchCanonicalCallersYAML([]byte("callers: [unclosed"), func(callers []CallerConfig) ([]CallerConfig, error) {
		return callers, nil
	}); err == nil {
		t.Fatal("invalid YAML must fail")
	}
	if _, err := PatchCanonicalCallersYAML([]byte("server: {}"), nil); err == nil {
		t.Fatal("nil mutate callback must fail")
	}
}

// TestCallerAdminMutationsSyncCanonicalDocument verifies that issue, rotate,
// and revoke each update the canonical YAML document inside the same
// transaction as the relational mutation. The document must reflect the
// caller directory after every successful mutation.
func TestCallerAdminMutationsSyncCanonicalDocument(t *testing.T) {
	db := callerAdminTestDB(t)
	mux := callerAdminTestMux(t, db, nil)

	// Issue a new caller.
	issueRec := callerAdminRequest(t, mux, http.MethodPost, "/admin/callers/issue", map[string]any{
		"owner_user": "Alice",
		"project":    "Search",
		"allow":      []string{"default"},
	}, true)
	if issueRec.Code != http.StatusCreated {
		t.Fatalf("issue status = %d body = %s", issueRec.Code, issueRec.Body.String())
	}
	var issueResp callerAdminIssueResponse
	if err := json.Unmarshal(issueRec.Body.Bytes(), &issueResp); err != nil {
		t.Fatal(err)
	}

	// The canonical document must contain the new caller with the persisted
	// token hash and public token id.
	var docYAML string
	if err := db.Raw(`SELECT canonical_yaml FROM router_config_documents WHERE config_set_id = ?`, "set-1").Row().Scan(&docYAML); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(docYAML, "alice-search-dev") {
		t.Fatalf("canonical document missing issued caller: %s", docYAML)
	}
	if !strings.Contains(docYAML, issueResp.Caller.TokenSHA256) {
		t.Fatalf("canonical document missing issued caller token hash: %s", docYAML)
	}
	if strings.Contains(docYAML, issueResp.Token) {
		t.Fatal("canonical document must never contain a raw token")
	}

	// Rotate replaces the stored hash in the document.
	rotateRec := callerAdminRequest(t, mux, http.MethodPost, "/admin/callers/alice-search-dev/rotate", nil, true)
	if rotateRec.Code != http.StatusOK {
		t.Fatalf("rotate status = %d body = %s", rotateRec.Code, rotateRec.Body.String())
	}
	var rotateResp callerAdminTokenResponse
	if err := json.Unmarshal(rotateRec.Body.Bytes(), &rotateResp); err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT canonical_yaml FROM router_config_documents WHERE config_set_id = ?`, "set-1").Row().Scan(&docYAML); err != nil {
		t.Fatal(err)
	}
	rotatedSum := sha256.Sum256([]byte(rotateResp.Token))
	if !strings.Contains(docYAML, hex.EncodeToString(rotatedSum[:])) {
		t.Fatalf("canonical document missing rotated token hash: %s", docYAML)
	}
	if strings.Contains(docYAML, rotateResp.Token) {
		t.Fatal("canonical document must never contain a raw token")
	}

	// Revoke marks the caller disabled in the document.
	revokeRec := callerAdminRequest(t, mux, http.MethodPost, "/admin/callers/alice-search-dev/revoke", nil, true)
	if revokeRec.Code != http.StatusOK {
		t.Fatalf("revoke status = %d body = %s", revokeRec.Code, revokeRec.Body.String())
	}
	if err := db.Raw(`SELECT canonical_yaml FROM router_config_documents WHERE config_set_id = ?`, "set-1").Row().Scan(&docYAML); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Callers []CallerConfig `yaml:"callers"`
	}
	if err := yaml.Unmarshal([]byte(docYAML), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Callers) != 1 || doc.Callers[0].Status != accountStatusDisabled {
		t.Fatalf("canonical document caller status = %+v, want disabled", doc.Callers)
	}
}

// TestCallerAdminMutationsBumpRevisionAndAppendEvents verifies that every
// successful caller mutation bumps router_config_runtime.config_revision and
// appends a router_config_change_events row with the expected action.
func TestCallerAdminMutationsBumpRevisionAndAppendEvents(t *testing.T) {
	db := callerAdminTestDB(t)
	mux := callerAdminTestMux(t, db, nil)

	var initialRevision int64
	if err := db.Raw(`SELECT config_revision FROM router_config_runtime WHERE runtime_scope = ?`, "staging").Row().Scan(&initialRevision); err != nil {
		// No runtime row yet; the first mutation will create it.
		initialRevision = 0
	}

	// Issue bumps revision to 1.
	issueRec := callerAdminRequest(t, mux, http.MethodPost, "/admin/callers/issue", map[string]any{
		"owner_user": "Alice",
		"project":    "Search",
		"allow":      []string{"default"},
	}, true)
	if issueRec.Code != http.StatusCreated {
		t.Fatalf("issue status = %d body = %s", issueRec.Code, issueRec.Body.String())
	}
	var rev1 int64
	if err := db.Raw(`SELECT config_revision FROM router_config_runtime WHERE runtime_scope = ?`, "staging").Row().Scan(&rev1); err != nil {
		t.Fatal(err)
	}
	if rev1 != initialRevision+1 {
		t.Fatalf("revision after issue = %d, want %d", rev1, initialRevision+1)
	}

	// Rotate bumps revision to 2.
	rotateRec := callerAdminRequest(t, mux, http.MethodPost, "/admin/callers/alice-search-dev/rotate", nil, true)
	if rotateRec.Code != http.StatusOK {
		t.Fatalf("rotate status = %d body = %s", rotateRec.Code, rotateRec.Body.String())
	}
	var rev2 int64
	if err := db.Raw(`SELECT config_revision FROM router_config_runtime WHERE runtime_scope = ?`, "staging").Row().Scan(&rev2); err != nil {
		t.Fatal(err)
	}
	if rev2 != rev1+1 {
		t.Fatalf("revision after rotate = %d, want %d", rev2, rev1+1)
	}

	// Revoke bumps revision to 3.
	revokeRec := callerAdminRequest(t, mux, http.MethodPost, "/admin/callers/alice-search-dev/revoke", nil, true)
	if revokeRec.Code != http.StatusOK {
		t.Fatalf("revoke status = %d body = %s", revokeRec.Code, revokeRec.Body.String())
	}
	var rev3 int64
	if err := db.Raw(`SELECT config_revision FROM router_config_runtime WHERE runtime_scope = ?`, "staging").Row().Scan(&rev3); err != nil {
		t.Fatal(err)
	}
	if rev3 != rev2+1 {
		t.Fatalf("revision after revoke = %d, want %d", rev3, rev2+1)
	}

	// The change-event log must contain one row per mutation with the
	// expected actions and monotonically increasing revisions.
	var actions []string
	rows, err := db.Raw(`SELECT action FROM router_config_change_events WHERE runtime_scope = ? ORDER BY config_revision ASC`, "staging").Rows()
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, action)
	}
	if len(actions) != 3 {
		t.Fatalf("change events = %v, want 3 actions", actions)
	}
	expected := []string{"caller_issue", "caller_rotate", "caller_revoke"}
	for i, want := range expected {
		if actions[i] != want {
			t.Fatalf("change event %d = %q, want %q", i, actions[i], want)
		}
	}
}

// TestServiceReloadActiveConfigFromDB verifies that the service reload path
// loads the active canonical document, rebuilds caller runtimes, and swaps
// the in-memory configuration. The test exercises the same code path used by
// AfterMutate and the revision poller.
func TestServiceReloadActiveConfigFromDB(t *testing.T) {
	db := callerAdminTestDB(t)

	// Seed a canonical document with one caller.
	tokenHash := strings.Repeat("a", 64)
	canonicalYAML := `server:
  listen: ':8080'
  default_model_group: default
  identifiers:
    mode: passthrough
providers:
  mock:
    base_url: http://127.0.0.1:1/v1
    dialect: openai
    api_key_env: TEST_API_KEY
models:
  default:
    strategy: static
    targets:
      - provider: mock
        model: mock-model
callers:
  - id: alice-search-dev
    owner_user: alice
    project: search
    environment: dev
    status: active
    token_sha256: ` + tokenHash + `
    token_id: rtr_metrum_alice_search_dev_k20260721
    allow:
      - default
    rate:
      rpm: 120
      tpm: 200000
      concurrent: 8
`
	hash := sha256.Sum256([]byte(canonicalYAML))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.Exec(
		`UPDATE router_config_documents SET canonical_yaml = ?, content_sha256 = ?, updated_at = ? WHERE config_set_id = ?`,
		canonicalYAML, hex.EncodeToString(hash[:]), now, "set-1",
	).Error; err != nil {
		t.Fatal(err)
	}
	// Also seed the relational projection so the service can load the active
	// config from the DB.
	if err := db.Exec(
		`INSERT INTO router_config_callers (config_set_id, caller_id, owner_user, project, environment, status, token_sha256, token_id, metrics_admin, content_admin, rpm, tpm, concurrent) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"set-1", "alice-search-dev", "alice", "search", "dev", "active", tokenHash, "rtr_metrum_alice_search_dev_k20260721", false, false, 120, 200000, 8,
	).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(
		`INSERT INTO router_config_caller_allowed_groups (config_set_id, caller_id, group_name) VALUES (?, ?, ?)`,
		"set-1", "alice-search-dev", "default",
	).Error; err != nil {
		t.Fatal(err)
	}

	// Build a minimal service with a DB-backed config source.
	dir := t.TempDir()
	usageDBDisabled := false
	cfg := &Config{
		Server: ServerConfig{
			Identifiers:       IdentifierConfig{Mode: "passthrough"},
			Listen:            ":0",
			DefaultModelGroup: "default",
			UsageDB: UsageDBConfig{
				Enable:          &usageDBDisabled,
				MigrationPolicy: usageDBMigrationPolicyAutoSafe,
				Driver:          "sqlite",
				Path:            filepath.Join(dir, "usage.sqlite"),
			},
			ConfigSource: ConfigSourceConfig{
				Mode:         "database",
				Driver:       "sqlite",
				Path:         filepath.Join(dir, "config.sqlite"),
				RuntimeScope: "staging",
			},
		},
		StatePath: filepath.Join(dir, "state.json"),
		Provider: map[string]ProviderConfig{
			"mock": {BaseURL: "http://127.0.0.1:1/v1", Dialect: "openai", APIKey: "test-key"},
		},
		Models: map[string]ModelGroup{
			"default": {Strategy: "static", Targets: []Target{{Provider: "mock", Model: "mock-model"}}},
		},
		Callers: []CallerConfig{},
	}
	// Point the service at the same SQLite file used by the test DB.
	cfg.Server.ConfigSource.Path = db.Config.Dialector.(*sqlite.Dialector).DSN
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	// The initial service has no callers because the bootstrap YAML had none.
	if len(svc.callersBySum) != 0 {
		t.Fatalf("initial callersBySum = %d, want 0", len(svc.callersBySum))
	}

	// Reload from the DB.
	if err := svc.reloadActiveConfigFromDB(); err != nil {
		t.Fatalf("reloadActiveConfigFromDB: %v", err)
	}

	// The service must now have the caller from the canonical document.
	if len(svc.callersBySum) != 1 {
		t.Fatalf("reloaded callersBySum = %d, want 1", len(svc.callersBySum))
	}
	rt, ok := svc.callersBySum[tokenHash]
	if !ok {
		t.Fatalf("reloaded caller missing from callersBySum: %v", svc.callersBySum)
	}
	if rt.cfg.ID != "alice-search-dev" || rt.cfg.Rate.RPM != 120 {
		t.Fatalf("reloaded caller runtime mismatch: %+v", rt.cfg)
	}
	if svc.configSnapshot.Load() == nil || svc.configSnapshot.Load().Callers[0].ID != "alice-search-dev" {
		t.Fatal("config snapshot must reflect the reloaded configuration")
	}
}

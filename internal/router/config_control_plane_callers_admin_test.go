// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	jose "github.com/go-jose/go-jose/v4"
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
	deps := CallerAdminDeps{
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
	}
	RegisterCallerDirectoryAdmin(mux, deps)
	newCallerAdminHandler(deps).register(func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }, "/admin/v1")
	return mux
}

func TestCallerAdminV1IssueHoneytokenPersistsMetadata(t *testing.T) {
	db := callerAdminTestDB(t)
	mux := callerAdminTestMux(t, db, nil)
	unsafe := callerAdminRequest(t, mux, http.MethodPost, "/admin/v1/callers/issue", map[string]any{
		"owner_user": "security", "project": "decoys", "environment": "prod", "key_slug": "unsafe",
		"allow": []string{"default"}, "honeytoken_label": "invalid real-group grant",
	}, true)
	if unsafe.Code != http.StatusBadRequest {
		t.Fatalf("database honeytoken real-group allow status=%d body=%s", unsafe.Code, unsafe.Body.String())
	}
	issued := callerAdminRequest(t, mux, http.MethodPost, "/admin/v1/callers/issue", map[string]any{
		"owner_user": "security", "project": "decoys", "environment": "prod", "key_slug": "honey",
		"allow": []string{}, "honeytoken_label": "incident response kit",
	}, true)
	if issued.Code != http.StatusCreated {
		t.Fatalf("honeytoken issue status=%d body=%s", issued.Code, issued.Body.String())
	}
	var response callerAdminIssueResponse
	if err := json.Unmarshal(issued.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Token == "" || response.Caller.HoneytokenLabel != "incident response kit" || response.Caller.TokenHash == "" {
		t.Fatalf("incomplete database-mode honeytoken response: %+v", response)
	}
	if strings.Contains(issued.Body.String(), response.Caller.TokenHash) == false {
		t.Fatal("caller response should include the one-time verifier record")
	}
	var label, plantedAt string
	if err := db.Raw(`SELECT honeytoken_label, honeytoken_planted_at FROM router_config_callers WHERE caller_id = ?`, response.Caller.ID).Row().Scan(&label, &plantedAt); err != nil {
		t.Fatal(err)
	}
	if label != "incident response kit" || plantedAt == "" {
		t.Fatalf("stored honeytoken metadata label=%q planted_at=%q", label, plantedAt)
	}
	var unsafeCount int64
	if err := db.Table("router_config_callers").Where("token_sha256 = ? OR token_id = ?", response.Token, response.Token).Count(&unsafeCount).Error; err != nil || unsafeCount != 0 {
		t.Fatalf("raw token persisted count=%d err=%v", unsafeCount, err)
	}
}

func TestCallerAdminV1IssueHoneytokenRegistersDatabaseCanaryGroups(t *testing.T) {
	db := callerAdminTestDB(t)
	if err := db.Exec(`INSERT INTO router_config_model_groups (config_set_id, group_name, strategy) VALUES (?, ?, ?)`, "set-1", "canary", "static").Error; err != nil {
		t.Fatal(err)
	}
	canonical := `server:
  listen: ':8080'
  default_model_group: default
models:
  default:
    strategy: static
  canary:
    strategy: static
    canary:
      callers: []
callers: []
`
	hash := sha256.Sum256([]byte(canonical))
	if err := db.Exec(`UPDATE router_config_documents SET canonical_yaml = ?, content_sha256 = ? WHERE config_set_id = ?`, canonical, hex.EncodeToString(hash[:]), "set-1").Error; err != nil {
		t.Fatal(err)
	}
	mux := callerAdminTestMux(t, db, nil)
	issued := callerAdminRequest(t, mux, http.MethodPost, "/admin/v1/callers/issue", map[string]any{
		"owner_user": "security", "project": "decoys", "environment": "prod", "key_slug": "canary",
		"allow": []string{"canary"}, "honeytoken_label": "db canary decoy",
	}, true)
	if issued.Code != http.StatusCreated {
		t.Fatalf("database honeytoken canary issue status=%d body=%s", issued.Code, issued.Body.String())
	}
	var response callerAdminIssueResponse
	if err := json.Unmarshal(issued.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.Raw(`SELECT canonical_yaml FROM router_config_documents WHERE config_set_id = ?`, "set-1").Row().Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var config Config
	if err := yaml.Unmarshal([]byte(stored), &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Callers) != 1 || len(config.Callers[0].Allow) != 1 || config.Callers[0].Allow[0] != "canary" || !canaryAllowsCaller(config.Models["canary"].Canary, response.Caller.ID) {
		t.Fatalf("DB-mode canary grant did not persist on caller and group: caller=%+v group=%+v", config.Callers, config.Models["canary"])
	}
}

func TestCallerContainmentRefusesHoneytokenCaller(t *testing.T) {
	db := callerAdminTestDB(t)
	mux := callerAdminTestMux(t, db, nil)
	issued := callerAdminRequest(t, mux, http.MethodPost, "/admin/v1/callers/issue", map[string]any{
		"owner_user": "security", "project": "decoys", "environment": "prod", "key_slug": "containment",
		"allow": []string{}, "honeytoken_label": "protected decoy",
	}, true)
	if issued.Code != http.StatusCreated {
		t.Fatalf("honeytoken issue status=%d body=%s", issued.Code, issued.Body.String())
	}
	var response callerAdminIssueResponse
	if err := json.Unmarshal(issued.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	h := newCallerAdminHandler(CallerAdminDeps{DB: db, RuntimeScope: "staging", Authorize: func(*http.Request, string, string) bool { return true }})
	req := httptest.NewRequest(http.MethodPost, "/admin/callers/containment", strings.NewReader(`{"caller_id":"`+response.Caller.ID+`","action":"suspend","reason":"detector_test","ttl":"5m"}`))
	rec := httptest.NewRecorder()
	h.handleContainment(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "containment-honeytoken-forbidden") {
		t.Fatalf("honeytoken containment status=%d body=%s", rec.Code, rec.Body.String())
	}
	var status string
	if err := db.Raw(`SELECT status FROM router_config_callers WHERE caller_id = ?`, response.Caller.ID).Row().Scan(&status); err != nil || status != accountStatusActive {
		t.Fatalf("honeytoken status=%q err=%v; want active", status, err)
	}
}

func TestPatchCanonicalCallersYAMLRejectsRealGroupGrantToHoneytoken(t *testing.T) {
	raw := []byte(`models:
  default:
    strategy: static
callers:
  - id: decoy
    honeytoken:
      label: planted decoy
    allow: []
`)
	_, err := PatchCanonicalCallersYAML(raw, func(callers []CallerConfig) ([]CallerConfig, error) {
		callers[0].Allow = []string{"default"}
		return callers, nil
	})
	if err == nil || !strings.Contains(err.Error(), "only allow canary model groups") {
		t.Fatalf("canonical caller write accepted a real model grant to a honeytoken: %v", err)
	}
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

func TestCallerAdminListKeepsDisabledCallerWithEmptyVerifierManageable(t *testing.T) {
	db := callerAdminTestDB(t)
	if err := db.Exec(`INSERT INTO router_config_callers (config_set_id, caller_id, owner_user, project, environment, status, token_sha256, token_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "set-1", "retired-key", "alice", "search", "dev", accountStatusDisabled, "", "").Error; err != nil {
		t.Fatal(err)
	}
	mux := callerAdminTestMux(t, db, nil)
	response := callerAdminRequest(t, mux, http.MethodGet, "/admin/v1/callers", nil, true)
	if response.Code != http.StatusOK {
		t.Fatalf("list callers status=%d body=%s, want disabled empty-verifier caller to remain manageable", response.Code, response.Body.String())
	}
	var listed callerAdminListResponse
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, caller := range listed.Callers {
		if caller.ID == "retired-key" {
			found = caller.Status == accountStatusDisabled && caller.TokenHash == ""
		}
	}
	if !found {
		t.Fatalf("disabled empty-verifier caller missing from response: %+v", listed.Callers)
	}
	if err := db.Exec(`INSERT INTO router_config_callers (config_set_id, caller_id, owner_user, project, environment, status, token_sha256, token_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "set-1", "active-without-verifier", "alice", "search", "dev", accountStatusActive, "", "").Error; err != nil {
		t.Fatal(err)
	}
	response = callerAdminRequest(t, mux, http.MethodGet, "/admin/v1/callers", nil, true)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "callers-legacy-verifier-present") {
		t.Fatalf("active caller without verifier status=%d body=%s, want unavailable", response.Code, response.Body.String())
	}
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
	if issueResp.Caller.ID != "alice-search-prod" || issueResp.Caller.Status != accountStatusActive {
		t.Fatalf("unexpected issued caller: %+v", issueResp.Caller)
	}
	if issueResp.Token == "" || !strings.HasPrefix(issueResp.Token, issueResp.TokenID+"_") {
		t.Fatalf("raw token must be returned once with the public token id prefix: %+v", issueResp)
	}
	if issueResp.Caller.TokenHash == "" || !callerTokenHashVerifies(t, issueResp.Caller.TokenHash, issueResp.Token) {
		t.Fatal("issued caller must carry a token_hash that verifies the returned raw token")
	}

	// The relational projection stores the hash and allows, never the raw token.
	var storedHash, storedTokenID string
	if err := db.Raw(`SELECT token_sha256, token_id FROM router_config_callers WHERE config_set_id = ? AND caller_id = ?`, "set-1", "alice-search-prod").Row().Scan(&storedHash, &storedTokenID); err != nil {
		t.Fatal(err)
	}
	if storedHash != issueResp.Caller.TokenHash || storedTokenID != issueResp.TokenID || !callerTokenHashVerifies(t, storedHash, issueResp.Token) {
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
	if err := db.Table("router_config_caller_allowed_groups").Where("config_set_id = ? AND caller_id = ?", "set-1", "alice-search-prod").Count(&allowCount).Error; err != nil {
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
	if len(listResp.Callers) != 1 || listResp.Callers[0].ID != "alice-search-prod" || len(listResp.Callers[0].Allow) != 2 {
		t.Fatalf("unexpected list response: %+v", listResp)
	}

	// Rotate replaces the stored hash and returns the new raw token once.
	rotateRec := callerAdminRequest(t, mux, http.MethodPost, "/admin/callers/alice-search-prod/rotate", nil, true)
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
	if err := db.Raw(`SELECT token_sha256 FROM router_config_callers WHERE config_set_id = ? AND caller_id = ?`, "set-1", "alice-search-prod").Row().Scan(&rotatedHash); err != nil {
		t.Fatal(err)
	}
	if !callerTokenHashVerifies(t, rotatedHash, rotateResp.Token) || rotatedHash == storedHash {
		t.Fatal("rotate must replace the stored hash with the new token hash")
	}
	var formatVersion, entropyBits int
	if err := db.Raw(`SELECT token_format_version, token_entropy_bits FROM router_config_callers WHERE config_set_id = ? AND caller_id = ?`, "set-1", "alice-search-prod").Row().Scan(&formatVersion, &entropyBits); err != nil {
		t.Fatal(err)
	}
	if formatVersion != 2 || entropyBits != 256 {
		t.Fatalf("rotated metadata format=%d entropy=%d", formatVersion, entropyBits)
	}

	// Revoke disables the caller.
	revokeRec := callerAdminRequest(t, mux, http.MethodPost, "/admin/callers/alice-search-prod/revoke", nil, true)
	if revokeRec.Code != http.StatusOK {
		t.Fatalf("revoke status = %d body = %s", revokeRec.Code, revokeRec.Body.String())
	}
	var status string
	if err := db.Raw(`SELECT status FROM router_config_callers WHERE config_set_id = ? AND caller_id = ?`, "set-1", "alice-search-prod").Row().Scan(&status); err != nil {
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
	raw := []byte("server:\n  listen: ':8080'\ncallers:\n  - id: alice-search-prod\n    owner_user: alice\n    project: search\n    environment: dev\n    status: active\n    token_hash: \"" + configAdminTestTokenHash() + "\"\n    token_id: rtr_metrum_alice_search_prod_k20260721\n    allow:\n      - default\n    rate:\n      rpm: 120\n      tpm: 200000\n      concurrent: 8\n")
	out, err := PatchCanonicalCallersYAML(raw, func(callers []CallerConfig) ([]CallerConfig, error) {
		if len(callers) != 1 || callers[0].ID != "alice-search-prod" || callers[0].Rate.RPM != 120 {
			t.Fatalf("decoded callers mismatch: %+v", callers)
		}
		callers[0].Status = accountStatusDisabled
		return append(callers, CallerConfig{ID: "bob-ml-prod", OwnerUser: "bob", Project: "ml", Environment: "prod", Status: accountStatusActive, TokenHash: configAdminTestTokenHash(), TokenID: "test-bob-token-id", Allow: []string{"premium"}}), nil
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
	if !strings.Contains(docYAML, "alice-search-prod") {
		t.Fatalf("canonical document missing issued caller: %s", docYAML)
	}
	if !strings.Contains(docYAML, issueResp.Caller.TokenHash) {
		t.Fatalf("canonical document missing issued caller token hash: %s", docYAML)
	}
	if !strings.Contains(docYAML, "token_issued_at:") || !strings.Contains(docYAML, "2026-07-21T12:00:00Z") {
		t.Fatalf("canonical document missing issued caller timestamp: %s", docYAML)
	}
	if strings.Contains(docYAML, issueResp.Token) {
		t.Fatal("canonical document must never contain a raw token")
	}

	// Rotate replaces the stored hash in the document.
	rotateRec := callerAdminRequest(t, mux, http.MethodPost, "/admin/callers/alice-search-prod/rotate", nil, true)
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
	if !canonicalDocumentHasTokenHashFor(t, docYAML, rotateResp.Token) {
		t.Fatalf("canonical document missing rotated token hash: %s", docYAML)
	}
	if !strings.Contains(docYAML, "token_issued_at:") || !strings.Contains(docYAML, "2026-07-21T12:00:00Z") {
		t.Fatalf("canonical document missing rotated caller timestamp: %s", docYAML)
	}
	if strings.Contains(docYAML, rotateResp.Token) {
		t.Fatal("canonical document must never contain a raw token")
	}

	// Revoke marks the caller disabled in the document.
	revokeRec := callerAdminRequest(t, mux, http.MethodPost, "/admin/callers/alice-search-prod/revoke", nil, true)
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
	rotateRec := callerAdminRequest(t, mux, http.MethodPost, "/admin/callers/alice-search-prod/rotate", nil, true)
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
	revokeRec := callerAdminRequest(t, mux, http.MethodPost, "/admin/callers/alice-search-prod/revoke", nil, true)
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
	issuerKey := machineJWTKey(t)
	var discoveryRequests atomic.Int64
	var jwksRequests atomic.Int64
	issuerURL := ""
	issuerServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			discoveryRequests.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuerURL, "jwks_uri": issuerURL + "/keys"})
		case "/keys":
			jwksRequests.Add(1)
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &issuerKey.PublicKey, KeyID: "reload-kid", Algorithm: "ES256", Use: "sig"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(issuerServer.Close)
	issuerURL = issuerServer.URL
	issuerHost := strings.TrimPrefix(issuerURL, "https://")
	tokenExchangeConfig := TokenExchangeConfig{Enabled: true, SigningKeyEnv: "MISSION_KEY", DefaultTTL: "1m", Issuers: []TokenExchangeIssuerConfig{{
		IssuerURL: issuerURL, Audience: "router-exchange", TaskClaim: "task_id", Algorithms: []string{"ES256"}, AllowedJWKSHosts: []string{issuerHost}, AllowPrivateNetwork: true,
		Subjects: []TokenExchangeSubjectGrant{{Claim: "sub", Value: "automation", CallerID: "alice-search-prod", HumanOwner: "alice", AllowedGroups: []string{"default"}, MaxSpendUSD: 1, MaxTokens: 100}},
	}}}
	key, err := GenerateAdminAPIKey("reload-reader", nil, callerTokenHashMinIterations)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_RELOAD_ADMIN_KEY_HASH", key.TokenHash)

	// Seed a canonical document with one caller.
	tokenHash := mustCallerTokenHashForTest(t, "rtr_metrum_alice_search_dev_k20260721_secret")
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
security:
  token_exchange:
    enabled: true
    signing_key_env: MISSION_KEY
    default_ttl: 1m
    issuers:
      - issuer: ` + issuerURL + `
        audience: router-exchange
        task_claim: task_id
        algorithms: [ES256]
        allowed_jwks_hosts: [` + issuerHost + `]
        allow_private_network: true
        subjects:
          - claim: sub
            value: automation
            caller_id: alice-search-prod
            human_owner: alice
            allowed_groups: [default]
            max_spend_usd: 1
            max_tokens: 100
callers:
  - id: alice-search-prod
    owner_user: alice
    project: search
    environment: dev
    status: active
    token_hash: ` + tokenHash + `
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
		"set-1", "alice-search-prod", "alice", "search", "dev", "active", tokenHash, "rtr_metrum_alice_search_dev_k20260721", false, false, 120, 200000, 8,
	).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(
		`INSERT INTO router_config_caller_allowed_groups (config_set_id, caller_id, group_name) VALUES (?, ?, ?)`,
		"set-1", "alice-search-prod", "default",
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
			AdminAuth:         AdminAuthConfig{APIKeys: []AdminAPIKeyConfig{{KeyID: "reload-reader", KeyHashEnv: "TEST_RELOAD_ADMIN_KEY_HASH", Domain: "search/dev", Permissions: []string{"config:read"}}}},
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
	runtime, err := newTokenExchangeRuntime(tokenExchangeConfig, nil, func(name string) (string, bool) {
		if name == "MISSION_KEY" {
			return strings.Repeat("k", 32), true
		}
		return "", false
	}, issuerServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	runtime.quota = svc.quota
	svc.tokenExchange = runtime
	svc.mux.HandleFunc("POST /oauth/token", svc.handleTokenExchange)

	// The initial service has no callers because the bootstrap YAML had none.
	if idx := svc.callerAuth.index.Load(); len(idx.byTokenID) != 0 {
		t.Fatalf("initial caller auth index = %d token_hash, want 0", len(idx.byTokenID))
	}

	// The machine path publishes a complete immutable generation. Builder
	// failure must stop reload before keys/config/authorizer are exposed.
	failMachinePreparation := false
	svc.machineGenerationBuilder = func(candidate *Config, compatible *authorizer) (*adminMachineGeneration, error) {
		if failMachinePreparation {
			return nil, ErrInvalidCredential
		}
		keys, err := newConfiguredAdminKeyCatalog(candidate, func(string) (string, bool) { return key.TokenHash, true })
		if err != nil {
			return nil, err
		}
		auth, err := newConfiguredAdminKeyAuthenticator(func() *adminConfiguredKeyCatalog { return keys })
		if err != nil {
			return nil, err
		}
		return &adminMachineGeneration{auth: NewAuthenticatorChain(auth), authorizer: compatible, keys: keys}, nil
	}
	initialMachine, err := svc.machineGenerationBuilder(cfg, svc.currentAuthorizer())
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.publishAdminMachineGeneration(initialMachine); err != nil {
		t.Fatal(err)
	}

	// Authenticate a real configured key, then ensure an invalid candidate
	// cannot disturb its grants or verification state.
	keySubject, ok := svc.authenticateAdminAPIKey(key.Token)
	if !ok || !svc.authorizeAdminPermission(keySubject, "config:read") {
		t.Fatal("bootstrap key must authenticate and read config")
	}
	// Isolate machine preparation failure before testing policy errors. Both
	// the live directory and persistent quota state must retain bootstrap.
	failMachinePreparation = true
	if err := svc.reloadActiveConfigFromDB(); err == nil {
		t.Fatal("failed machine preparation accepted")
	}
	svc.quota.mu.Lock()
	rejectedCallers, rejectedStates := len(svc.quota.callers), len(svc.quota.state.Callers)
	svc.quota.mu.Unlock()
	if rejectedCallers != 0 || rejectedStates != 0 {
		t.Fatalf("failed machine preparation installed rejected caller state: directory=%d persistent=%d", rejectedCallers, rejectedStates)
	}
	failMachinePreparation = false
	invalidYAML := strings.Replace(canonicalYAML, "  default_model_group: default", "  default_model_group: default\n  admin_auth:\n    authorization:\n      enabled: true\n      policy: ['p, subject, search/dev, admin:*, read']", 1)
	invalidHash := sha256.Sum256([]byte(invalidYAML))
	if err := db.Exec(`UPDATE router_config_documents SET canonical_yaml = ?, content_sha256 = ? WHERE config_set_id = ?`, invalidYAML, hex.EncodeToString(invalidHash[:]), "set-1").Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.reloadActiveConfigFromDB(); err == nil {
		t.Fatal("invalid policy candidate reloaded")
	}
	if _, ok := svc.authenticateAdminAPIKey(key.Token); !ok || !svc.authorizeAdminPermission(keySubject, "config:read") {
		t.Fatal("failed reload disturbed active credential/grant")
	}
	svc.quota.mu.Lock()
	rejectedCallers, rejectedStates = len(svc.quota.callers), len(svc.quota.state.Callers)
	svc.quota.mu.Unlock()
	if rejectedCallers != 0 || rejectedStates != 0 {
		t.Fatal("failed authorizer preparation installed rejected caller state")
	}
	if err := db.Exec(`UPDATE router_config_documents SET canonical_yaml = ?, content_sha256 = ? WHERE config_set_id = ?`, canonicalYAML, hex.EncodeToString(hash[:]), "set-1").Error; err != nil {
		t.Fatal(err)
	}

	failMachinePreparation = true
	if err := svc.reloadActiveConfigFromDB(); err == nil {
		t.Fatal("failed machine preparation accepted")
	}
	if svc.machineGeneration.Load() != initialMachine || svc.currentAuthorizer() != initialMachine.authorizer || svc.cfg != cfg {
		t.Fatal("failed preparation partially published config/grants")
	}
	if _, ok := svc.authenticateAdminAPIKey(key.Token); !ok {
		t.Fatal("failed machine preparation removed old key")
	}
	failMachinePreparation = false

	// A stream change is published in the canonical config but takes effect
	// only after process restart; verify reload logs that operational contract.
	eventsYAML := strings.Replace(canonicalYAML, "  default_model_group: default", "  default_model_group: default\n  security:\n    events:\n      enabled: true\n      version: \"1.4.0\"\n      output: file\n      path: '"+filepath.Join(dir, "events.jsonl")+"'", 1)
	eventsHash := sha256.Sum256([]byte(eventsYAML))
	if err := db.Exec(`UPDATE router_config_documents SET canonical_yaml = ?, content_sha256 = ? WHERE config_set_id = ?`, eventsYAML, hex.EncodeToString(eventsHash[:]), "set-1").Error; err != nil {
		t.Fatal(err)
	}
	oldLogWriter := log.Writer()
	var reloadLogs bytes.Buffer
	log.SetOutput(&reloadLogs)
	defer log.SetOutput(oldLogWriter)
	// Reload from the DB. The canonical document removes the bootstrap key.
	if err := svc.reloadActiveConfigFromDB(); err != nil {
		t.Fatalf("reloadActiveConfigFromDB: %v", err)
	}
	log.SetOutput(oldLogWriter)
	if !strings.Contains(reloadLogs.String(), "server.security.events changed; restart required") {
		t.Fatalf("stream config reload did not explain restart requirement: %s", reloadLogs.String())
	}
	if svc.securityEventStream != nil {
		t.Fatal("reload rebuilt a process-local stream without restart")
	}
	machine := svc.machineGeneration.Load()
	if machine == initialMachine || machine.authorizer != svc.currentAuthorizer() || len(machine.keys.keys) != 0 {
		t.Fatal("machine reload did not publish compatible keys/grants together")
	}

	if _, ok := svc.authenticateAdminAPIKey(key.Token); ok {
		t.Fatal("removed key still authenticates after reload")
	}
	if svc.authorizeAdminPermission(keySubject, "config:read") {
		t.Fatal("compiled key grant survived reload revocation")
	}

	// The service must now have the caller from the canonical document.
	idx := svc.callerAuth.index.Load()
	if len(idx.byTokenID) != 1 {
		t.Fatalf("reloaded callers = %d, want 1", len(idx.byTokenID))
	}
	rt, ok := idx.byTokenID["rtr_metrum_alice_search_dev_k20260721"]
	if !ok {
		t.Fatal("reloaded caller missing from caller auth index")
	}
	if rt.caller.cfg.ID != "alice-search-prod" || rt.caller.cfg.Rate.RPM != 120 {
		t.Fatalf("reloaded caller runtime mismatch: %+v", rt.caller.cfg)
	}
	if svc.configSnapshot.Load() == nil || svc.configSnapshot.Load().Callers[0].ID != "alice-search-prod" {
		t.Fatal("config snapshot must reflect the reloaded configuration")
	}

	// A mission token minted for the active caller must stop working when the
	// real canonical-config reload publishes the caller as suspended.
	subjectClaims := map[string]any{"iss": issuerURL, "aud": "router-exchange", "sub": "automation", "iat": time.Now().Unix() - 1, "exp": time.Now().Unix() + 60, "task_id": "reload-task-1"}
	subjectToken := machineSignedJWTType(t, issuerKey, "reload-kid", jose.ES256, "JWT", subjectClaims)
	form := url.Values{"grant_type": {tokenExchangeGrantType}, "subject_token": {subjectToken}, "subject_token_type": {jwtSubjectTokenType}, "scope": {"default"}}
	exchange := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, req)
		return w
	}
	issued := exchange()
	if issued.Code != http.StatusOK {
		t.Fatalf("pre-suspension exchange status=%d body=%s", issued.Code, issued.Body.String())
	}
	var issuedBody struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &issuedBody); err != nil || issuedBody.AccessToken == "" {
		t.Fatalf("pre-suspension token missing: err=%v", err)
	}
	discoveryBeforeSuspension := discoveryRequests.Load()
	jwksBeforeSuspension := jwksRequests.Load()
	suspendedYAML := strings.Replace(eventsYAML, "status: active", "status: suspended", 1)
	suspendedHash := sha256.Sum256([]byte(suspendedYAML))
	if err := db.Exec(`UPDATE router_config_documents SET canonical_yaml = ?, content_sha256 = ?, updated_at = ? WHERE config_set_id = ?`, suspendedYAML, hex.EncodeToString(suspendedHash[:]), time.Now().UTC().Format(time.RFC3339Nano), "set-1").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`UPDATE router_config_callers SET status = ? WHERE config_set_id = ? AND caller_id = ?`, accountStatusSuspended, "set-1", "alice-search-prod").Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.reloadActiveConfigFromDB(); err != nil {
		t.Fatalf("suspend caller through config reload: %v", err)
	}
	if got := exchange(); got.Code != http.StatusUnauthorized {
		t.Fatalf("exchange after suspension reload status=%d body=%s", got.Code, got.Body.String())
	}
	if discoveryRequests.Load() != discoveryBeforeSuspension || jwksRequests.Load() != jwksBeforeSuspension {
		t.Fatalf("unchanged issuer was rediscovered on caller reload: discovery %d->%d, JWKS %d->%d", discoveryBeforeSuspension, discoveryRequests.Load(), jwksBeforeSuspension, jwksRequests.Load())
	}
	inference := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"x"}]}`))
	inference.Header.Set("Authorization", "Bearer "+issuedBody.AccessToken)
	inferenceResponse := httptest.NewRecorder()
	svc.Handler().ServeHTTP(inferenceResponse, inference)
	if inferenceResponse.Code != http.StatusUnauthorized {
		t.Fatalf("mission inference after suspension reload status=%d body=%s", inferenceResponse.Code, inferenceResponse.Body.String())
	}

	// Restore the caller, mint a new token, then remove only its subject grant
	// through the canonical-config reload path.
	activeHash := sha256.Sum256([]byte(eventsYAML))
	if err := db.Exec(`UPDATE router_config_documents SET canonical_yaml = ?, content_sha256 = ?, updated_at = ? WHERE config_set_id = ?`, eventsYAML, hex.EncodeToString(activeHash[:]), time.Now().UTC().Format(time.RFC3339Nano), "set-1").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`UPDATE router_config_callers SET status = ? WHERE config_set_id = ? AND caller_id = ?`, accountStatusActive, "set-1", "alice-search-prod").Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.reloadActiveConfigFromDB(); err != nil {
		t.Fatalf("restore caller through config reload: %v", err)
	}
	restored := exchange()
	if restored.Code != http.StatusOK {
		t.Fatalf("exchange after restore reload status=%d body=%s", restored.Code, restored.Body.String())
	}
	var restoredBody struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(restored.Body.Bytes(), &restoredBody); err != nil || restoredBody.AccessToken == "" {
		t.Fatalf("restored mission token missing: err=%v", err)
	}
	start := strings.Index(eventsYAML, "\nsecurity:\n  token_exchange:")
	if start < 0 {
		t.Fatal("canonical test document lacks root token exchange section")
	}
	endRel := strings.Index(eventsYAML[start+1:], "\ncallers:\n")
	if endRel < 0 {
		t.Fatal("canonical test document lacks caller section")
	}
	grantRemovedYAML := eventsYAML[:start] + eventsYAML[start+1+endRel:]
	grantRemovedHash := sha256.Sum256([]byte(grantRemovedYAML))
	if err := db.Exec(`UPDATE router_config_documents SET canonical_yaml = ?, content_sha256 = ?, updated_at = ? WHERE config_set_id = ?`, grantRemovedYAML, hex.EncodeToString(grantRemovedHash[:]), time.Now().UTC().Format(time.RFC3339Nano), "set-1").Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.reloadActiveConfigFromDB(); err != nil {
		t.Fatalf("remove mission grant through config reload: %v", err)
	}
	if got := exchange(); got.Code != http.StatusUnauthorized {
		t.Fatalf("exchange after grant removal reload status=%d body=%s", got.Code, got.Body.String())
	}
	inference = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"x"}]}`))
	inference.Header.Set("Authorization", "Bearer "+restoredBody.AccessToken)
	inferenceResponse = httptest.NewRecorder()
	svc.Handler().ServeHTTP(inferenceResponse, inference)
	if inferenceResponse.Code != http.StatusUnauthorized {
		t.Fatalf("mission inference after grant removal reload status=%d body=%s", inferenceResponse.Code, inferenceResponse.Body.String())
	}
}

func callerTokenHashVerifies(t *testing.T, encoded, token string) bool {
	t.Helper()
	ok, err := VerifyCallerTokenHash(encoded, token)
	if err != nil {
		t.Fatalf("token_hash is malformed: %v", err)
	}
	return ok
}

func canonicalDocumentHasTokenHashFor(t *testing.T, docYAML, token string) bool {
	t.Helper()
	var doc struct {
		Callers []CallerConfig `yaml:"callers"`
	}
	if err := yaml.Unmarshal([]byte(docYAML), &doc); err != nil {
		t.Fatalf("parse canonical document: %v", err)
	}
	for _, caller := range doc.Callers {
		if callerTokenHashVerifies(t, caller.TokenHash, token) {
			return true
		}
	}
	return false
}

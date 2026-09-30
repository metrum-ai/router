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
		AfterMutate: func(r *http.Request, callerID string) {
			if mutated != nil {
				*mutated = append(*mutated, callerID)
			}
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

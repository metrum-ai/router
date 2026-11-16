// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

// configAdminTestToken is the raw caller bearer for the Admin API tests;
// only its SHA-256 is embedded in the canonical YAML, matching the
// serve-e2e harness convention.
const configAdminTestToken = "rtr_admin_api_test_harness_token"

// configAdminTestYAML is the canonical document seeded for Admin API tests.
// Two providers with disjoint catalogs exercise cross-provider swaps.
var configAdminTestYAML = `server:
  listen: ':8080'
  identifiers:
    mode: passthrough
providers:
  alpha:
    base_url: https://alpha.example/v1
    dialect: openai
    api_key_env: ALPHA_API_KEY
    models:
      a-one:
        model: alpha-model-1
      a-two:
        model: alpha-model-2
  beta:
    base_url: https://beta.example/v1
    dialect: openai
    api_key_env: BETA_API_KEY
    models:
      b-one:
        model: beta-model-1
models:
  agent-default:
    strategy: static
    targets:
      - provider: alpha
        model_ref: a-one
  contract-group:
    strategy: failover
    contract:
      supported_api_shapes:
        - openai_chat
    targets:
      - provider: alpha
        model_ref: a-two
callers:
  - id: alice
    owner_user: alice
    project: search
    environment: dev
    status: active
    token_sha256: "` + configAdminTestTokenSHA256() + `"
    token_id: rtr_admin_api_test
    allow:
      - agent-default
      - contract-group
`

// configAdminTestDB returns a migrated SQLite control-plane DB with one
// active set seeded from configAdminTestYAML.
func configAdminTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "config.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeDB() })
	applyConfigControlPlaneMigrationsForTest(t, r)
	db := r.db
	setID, err := ImportCanonicalYAML(db, "prod", "seed", []byte(configAdminTestYAML), "test-seed")
	if err != nil {
		t.Fatal(err)
	}
	if err := ActivateConfigSet(db, "prod", setID, "test-seed"); err != nil {
		t.Fatal(err)
	}
	return db
}

// configAdminTestMux registers the Admin API config surface against db (nil
// for file mode) with header-driven auth stubs:
//   - X-Test-Admin-Subject: authenticated subject (empty = unauthenticated)
//   - X-Test-Admin-Perms: comma-separated permissions
func configAdminTestMux(t *testing.T, db *gorm.DB, liveCfg *Config) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	fileMode := db == nil
	RegisterConfigAdminAPI(mux, ConfigAdminDeps{
		DB:           db,
		RuntimeScope: "prod",
		FileMode:     fileMode,
		Driver:       "sqlite",
		InstanceID:   "replica-test",
		Authenticate: func(w http.ResponseWriter, r *http.Request) (adminAuthSubject, bool) {
			subject := strings.TrimSpace(r.Header.Get("X-Test-Admin-Subject"))
			if subject == "" {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"type": "unauthorized", "message": "unauthorized"}})
				return adminAuthSubject{}, false
			}
			perms := map[string]bool{}
			for _, p := range strings.Split(r.Header.Get("X-Test-Admin-Perms"), ",") {
				if strings.TrimSpace(p) != "" {
					perms[strings.TrimSpace(p)] = true
				}
			}
			return adminAuthSubject{subject: subject, domain: "local/test", source: "admin_key", permissions: perms}, true
		},
		Authorize: func(subject adminAuthSubject, permission string) bool {
			return subject.permissions[permission]
		},
		CurrentConfig: func() *Config { return liveCfg },
		Now:           func() time.Time { return time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC) },
	})
	return mux
}

func configAdminRequest(t *testing.T, mux *http.ServeMux, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
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
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func adminHeaders(subject string, perms ...string) map[string]string {
	return map[string]string{
		"X-Test-Admin-Subject": subject,
		"X-Test-Admin-Perms":   strings.Join(perms, ","),
	}
}

func decodeErrorType(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body not JSON: %v: %s", err, rr.Body.String())
	}
	return body.Error.Type
}

func TestConfigAdminListGroupsDBAndFileMode(t *testing.T) {
	db := configAdminTestDB(t)
	mux := configAdminTestMux(t, db, nil)
	rr := configAdminRequest(t, mux, "GET", "/admin/config/groups", nil, adminHeaders("adminkey:ops", "config:read"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Mode     string `json:"mode"`
		SetID    string `json:"set_id"`
		Revision int64  `json:"revision"`
		Groups   []struct {
			Name     string `json:"name"`
			Strategy string `json:"strategy"`
			Targets  []struct {
				Provider string `json:"provider"`
				ModelRef string `json:"model_ref"`
			} `json:"targets"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Mode != "database" || body.SetID == "" || body.Revision < 1 || len(body.Groups) != 2 {
		t.Fatalf("list groups body=%s", rr.Body.String())
	}

	// File mode: reads work from the live config.
	var liveCfg Config
	if err := yamlUnmarshalForTest(configAdminTestYAML, &liveCfg); err != nil {
		t.Fatal(err)
	}
	liveCfg.setDefaults()
	fileMux := configAdminTestMux(t, nil, &liveCfg)
	rr = configAdminRequest(t, fileMux, "GET", "/admin/config/groups", nil, adminHeaders("adminkey:ops", "config:read"))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"mode":"file"`) {
		t.Fatalf("file-mode read status=%d body=%s", rr.Code, rr.Body.String())
	}
	// File mode: every write is 409 config_source_mode_file.
	write := map[string]any{"targets": []map[string]any{{"provider": "alpha", "model_ref": "a-two"}}}
	for _, req := range []struct{ method, path string }{
		{"PUT", "/admin/config/groups/agent-default/targets"},
		{"POST", "/admin/config/groups/agent-default/targets"},
		{"DELETE", "/admin/config/groups/agent-default/targets/alpha/a-one"},
		{"POST", "/admin/config/groups/agent-default/targets:validate"},
		{"POST", "/admin/config/groups/agent-default/rollback"},
		{"POST", "/admin/config/sets"},
	} {
		rr := configAdminRequest(t, fileMux, req.method, req.path, write, adminHeaders("adminkey:ops", "config:write", "config:activate"))
		if rr.Code != http.StatusConflict || decodeErrorType(t, rr) != "config_source_mode_file" {
			t.Fatalf("file-mode write %s %s status=%d body=%s", req.method, req.path, rr.Code, rr.Body.String())
		}
	}
}

func TestConfigAdminGetGroupDetail(t *testing.T) {
	db := configAdminTestDB(t)
	mux := configAdminTestMux(t, db, nil)
	rr := configAdminRequest(t, mux, "GET", "/admin/config/groups/agent-default", nil, adminHeaders("adminkey:ops", "config:read"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"eligible_catalog_models"`) || !strings.Contains(rr.Body.String(), "beta-model-1") {
		t.Fatalf("group detail missing catalog: %s", rr.Body.String())
	}
	rr = configAdminRequest(t, mux, "GET", "/admin/config/groups/nope", nil, adminHeaders("adminkey:ops", "config:read"))
	if rr.Code != http.StatusNotFound || decodeErrorType(t, rr) != "group-not-found" {
		t.Fatalf("unknown group status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestConfigAdminSwapDryRunAndLive(t *testing.T) {
	db := configAdminTestDB(t)
	mux := configAdminTestMux(t, db, nil)
	admin := adminHeaders("adminkey:ops", "config:read", "config:write")

	countSets := func() int64 {
		var count int64
		if err := db.Table("router_config_sets").Where("runtime_scope = ?", "prod").Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		return count
	}
	before := countSets()

	// Dry run: diff + eligibility, nothing changes.
	dryRun := map[string]any{
		"targets": []map[string]any{{"provider": "beta", "model_ref": "b-one"}},
		"reason":  "price cut", "dry_run": true,
	}
	rr := configAdminRequest(t, mux, "PUT", "/admin/config/groups/agent-default/targets", dryRun, admin)
	if rr.Code != http.StatusOK {
		t.Fatalf("dry-run status=%d body=%s", rr.Code, rr.Body.String())
	}
	var dryBody struct {
		DryRun bool `json:"dry_run"`
		Diff   struct {
			Added   []string `json:"added"`
			Removed []string `json:"removed"`
		} `json:"diff"`
		Targets []GroupTargetReport `json:"targets"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &dryBody); err != nil {
		t.Fatal(err)
	}
	if !dryBody.DryRun || len(dryBody.Diff.Added) != 1 || dryBody.Diff.Added[0] != "beta/b-one" || len(dryBody.Diff.Removed) != 1 {
		t.Fatalf("dry-run diff wrong: %s", rr.Body.String())
	}
	if len(dryBody.Targets) != 1 || !dryBody.Targets[0].Eligible {
		t.Fatalf("dry-run reports wrong: %s", rr.Body.String())
	}
	if countSets() != before {
		t.Fatal("dry run created a set")
	}

	// Live swap with If-Match on the current revision.
	status := configAdminRequest(t, mux, "GET", "/admin/config/status", nil, admin)
	var statusBody struct {
		Revision int64 `json:"revision"`
	}
	if err := json.Unmarshal(status.Body.Bytes(), &statusBody); err != nil {
		t.Fatal(err)
	}
	live := map[string]any{
		"targets": []map[string]any{{"provider": "beta", "model_ref": "b-one"}},
		"reason":  "price cut",
	}
	liveHeaders := adminHeaders("adminkey:ops", "config:read", "config:write")
	liveHeaders["If-Match"] = fmt.Sprintf("%d", statusBody.Revision)
	liveHeaders["Idempotency-Key"] = "swap-0001"
	rr = configAdminRequest(t, mux, "PUT", "/admin/config/groups/agent-default/targets", live, liveHeaders)
	if rr.Code != http.StatusOK {
		t.Fatalf("live swap status=%d body=%s", rr.Code, rr.Body.String())
	}
	var liveBody struct {
		SetID         string `json:"set_id"`
		PreviousSetID string `json:"previous_set_id"`
		Revision      int64  `json:"revision"`
		Replayed      bool   `json:"replayed"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &liveBody); err != nil {
		t.Fatal(err)
	}
	if liveBody.SetID == "" || liveBody.PreviousSetID == "" || liveBody.Revision != statusBody.Revision+1 || liveBody.Replayed {
		t.Fatalf("live swap body=%s", rr.Body.String())
	}
	if countSets() != before+1 {
		t.Fatal("live swap must create exactly one new set")
	}

	// The serving document changed.
	raw, err := LoadActiveCanonicalYAML(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "model_ref: b-one") || strings.Contains(string(raw), "model_ref: a-one") {
		t.Fatalf("active document not swapped: %s", raw)
	}

	// Idempotent retry: same key + same body returns the recorded result and
	// creates no second set.
	rr = configAdminRequest(t, mux, "PUT", "/admin/config/groups/agent-default/targets", live, liveHeaders)
	if rr.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", rr.Code, rr.Body.String())
	}
	var replayBody struct {
		SetID    string `json:"set_id"`
		Replayed bool   `json:"replayed"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &replayBody); err != nil {
		t.Fatal(err)
	}
	if !replayBody.Replayed || replayBody.SetID != liveBody.SetID {
		t.Fatalf("replay body=%s", rr.Body.String())
	}
	if countSets() != before+1 {
		t.Fatal("idempotent retry created a second set")
	}

	// Same key, different body: conflict.
	other := map[string]any{"targets": []map[string]any{{"provider": "alpha", "model_ref": "a-two"}}}
	rr = configAdminRequest(t, mux, "PUT", "/admin/config/groups/agent-default/targets", other, liveHeaders)
	if rr.Code != http.StatusConflict || decodeErrorType(t, rr) != "idempotency-key-conflict" {
		t.Fatalf("idempotency conflict status=%d body=%s", rr.Code, rr.Body.String())
	}

	// Stale If-Match: 409 with the current revision.
	stale := adminHeaders("adminkey:ops", "config:read", "config:write")
	stale["If-Match"] = "1"
	rr = configAdminRequest(t, mux, "PUT", "/admin/config/groups/agent-default/targets", other, stale)
	if rr.Code != http.StatusConflict || decodeErrorType(t, rr) != "config-revision-conflict" {
		t.Fatalf("stale If-Match status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"current_revision"`) {
		t.Fatalf("conflict must carry the current revision: %s", rr.Body.String())
	}
}

func TestConfigAdminAddRemoveTarget(t *testing.T) {
	db := configAdminTestDB(t)
	mux := configAdminTestMux(t, db, nil)
	admin := adminHeaders("adminkey:ops", "config:read", "config:write")

	// Add a target at the front.
	add := map[string]any{"target": map[string]any{"provider": "beta", "model_ref": "b-one"}, "position": 0}
	rr := configAdminRequest(t, mux, "POST", "/admin/config/groups/agent-default/targets", add, admin)
	if rr.Code != http.StatusOK {
		t.Fatalf("add status=%d body=%s", rr.Code, rr.Body.String())
	}
	targetsOf := func() []configAdminTargetJSON {
		detail := configAdminRequest(t, mux, "GET", "/admin/config/groups/agent-default", nil, admin)
		var detailBody struct {
			Targets []configAdminTargetJSON `json:"targets"`
		}
		if err := json.Unmarshal(detail.Body.Bytes(), &detailBody); err != nil {
			t.Fatal(err)
		}
		return detailBody.Targets
	}
	targets := targetsOf()
	if len(targets) != 2 || targets[0].Provider != "beta" {
		t.Fatalf("position not honored: %+v", targets)
	}

	// Remove it via the wildcard target id.
	rr = configAdminRequest(t, mux, "DELETE", "/admin/config/groups/agent-default/targets/beta/b-one", nil, admin)
	if rr.Code != http.StatusOK {
		t.Fatalf("remove status=%d body=%s", rr.Code, rr.Body.String())
	}
	targets = targetsOf()
	if len(targets) != 1 || targets[0].Provider != "alpha" {
		t.Fatalf("removed target still present: %+v", targets)
	}

	// Removing the last target is refused.
	rr = configAdminRequest(t, mux, "DELETE", "/admin/config/groups/agent-default/targets/alpha/a-one", nil, admin)
	if rr.Code != http.StatusUnprocessableEntity || decodeErrorType(t, rr) != "group-would-be-empty" {
		t.Fatalf("last-target removal status=%d body=%s", rr.Code, rr.Body.String())
	}

	// Unknown target id: 404.
	rr = configAdminRequest(t, mux, "DELETE", "/admin/config/groups/agent-default/targets/alpha/nope", nil, admin)
	if rr.Code != http.StatusNotFound || decodeErrorType(t, rr) != "target-not-found" {
		t.Fatalf("unknown target status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestConfigAdminIneligibleAndMalformed(t *testing.T) {
	db := configAdminTestDB(t)
	mux := configAdminTestMux(t, db, nil)
	admin := adminHeaders("adminkey:ops", "config:read", "config:write")

	// Unknown provider.
	body := map[string]any{"targets": []map[string]any{{"provider": "ghost", "model_ref": "g-1"}}}
	rr := configAdminRequest(t, mux, "PUT", "/admin/config/groups/agent-default/targets", body, admin)
	if rr.Code != http.StatusUnprocessableEntity || decodeErrorType(t, rr) != "target-ineligible" {
		t.Fatalf("unknown provider status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "provider_exists") {
		t.Fatalf("report missing provider_exists check: %s", rr.Body.String())
	}

	// Unknown model_ref on a known provider.
	body = map[string]any{"targets": []map[string]any{{"provider": "alpha", "model_ref": "nope"}}}
	rr = configAdminRequest(t, mux, "PUT", "/admin/config/groups/agent-default/targets", body, admin)
	if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), "model_in_catalog") {
		t.Fatalf("unknown model_ref status=%d body=%s", rr.Code, rr.Body.String())
	}

	// Explicit model that no catalog entry serves.
	body = map[string]any{"targets": []map[string]any{{"provider": "alpha", "model": "alpha-model-9"}}}
	rr = configAdminRequest(t, mux, "PUT", "/admin/config/groups/agent-default/targets", body, admin)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown explicit model status=%d body=%s", rr.Code, rr.Body.String())
	}

	// Malformed JSON.
	req := httptest.NewRequest("PUT", "/admin/config/groups/agent-default/targets", strings.NewReader("{not json"))
	for k, v := range admin {
		req.Header.Set(k, v)
	}
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest || decodeErrorType(t, rr) != "config-invalid-request" {
		t.Fatalf("malformed status=%d body=%s", rr.Code, rr.Body.String())
	}

	// Unknown JSON field (an ad hoc credential would land here too).
	body = map[string]any{"targets": []map[string]any{{"provider": "alpha", "model_ref": "a-one", "api_key": "value"}}}
	rr = configAdminRequest(t, mux, "PUT", "/admin/config/groups/agent-default/targets", body, admin)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d body=%s", rr.Code, rr.Body.String())
	}

	// Credential-shaped value in a legitimate field: refused and never
	// echoed.
	secret := "sk-" + strings.Repeat("a1b2c3", 8)
	body = map[string]any{"targets": []map[string]any{{"provider": "alpha", "model_ref": "a-one"}}, "reason": secret}
	rr = configAdminRequest(t, mux, "PUT", "/admin/config/groups/agent-default/targets", body, admin)
	if rr.Code != http.StatusBadRequest || decodeErrorType(t, rr) != "config-secret-refused" {
		t.Fatalf("secret payload status=%d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), secret) {
		t.Fatal("secret echoed in response")
	}
	// The active set is unchanged after every failed write above.
	raw, err := LoadActiveCanonicalYAML(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "model_ref: a-one") {
		t.Fatal("active set changed by a refused write")
	}

	// Oversized target list: 400.
	oversized := make([]map[string]any, 0, maxAdminGroupTargets+1)
	for i := 0; i <= maxAdminGroupTargets; i++ {
		oversized = append(oversized, map[string]any{"provider": "alpha", "model_ref": "a-one"})
	}
	rr = configAdminRequest(t, mux, "PUT", "/admin/config/groups/agent-default/targets", map[string]any{"targets": oversized}, admin)
	if rr.Code != http.StatusBadRequest || decodeErrorType(t, rr) != "config-target-list-too-large" {
		t.Fatalf("oversized status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestConfigAdminValidationStatusPolicyAndForce(t *testing.T) {
	db := configAdminTestDB(t)
	mux := http.NewServeMux()
	RegisterConfigAdminAPI(mux, ConfigAdminDeps{
		DB: db, RuntimeScope: "prod", RequireValidationStatus: "passed", InstanceID: "replica-test",
		Authenticate: func(w http.ResponseWriter, r *http.Request) (adminAuthSubject, bool) {
			perms := map[string]bool{}
			for _, p := range strings.Split(r.Header.Get("X-Test-Admin-Perms"), ",") {
				if strings.TrimSpace(p) != "" {
					perms[strings.TrimSpace(p)] = true
				}
			}
			return adminAuthSubject{subject: "adminkey:ops", domain: "local/test", source: "admin_key", permissions: perms}, true
		},
		Authorize: func(subject adminAuthSubject, permission string) bool { return subject.permissions[permission] },
	})
	// The b-one target has no validation record: refused under the policy.
	body := map[string]any{"targets": []map[string]any{{"provider": "beta", "model_ref": "b-one"}}}
	rr := configAdminRequest(t, mux, "PUT", "/admin/config/groups/agent-default/targets", body, adminHeaders("adminkey:ops", "config:write"))
	if rr.Code != http.StatusUnprocessableEntity || decodeErrorType(t, rr) != "target-ineligible" {
		t.Fatalf("unvalidated target status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "validation_status") {
		t.Fatalf("report missing validation_status check: %s", rr.Body.String())
	}
	// force without config:force: 403.
	body["force"] = true
	rr = configAdminRequest(t, mux, "PUT", "/admin/config/groups/agent-default/targets", body, adminHeaders("adminkey:ops", "config:write"))
	if rr.Code != http.StatusForbidden || decodeErrorType(t, rr) != "config-force-required" {
		t.Fatalf("force without permission status=%d body=%s", rr.Code, rr.Body.String())
	}
	// force with config:force: allowed and audited forced=true.
	rr = configAdminRequest(t, mux, "PUT", "/admin/config/groups/agent-default/targets", body, adminHeaders("adminkey:ops", "config:write", "config:force"))
	if rr.Code != http.StatusOK {
		t.Fatalf("forced write status=%d body=%s", rr.Code, rr.Body.String())
	}
	var forced int
	if err := db.Table("router_config_change_events").Select("forced").Where("action = ?", configActionGroupTargetsReplace).Order("at DESC").Limit(1).Scan(&forced).Error; err != nil {
		t.Fatal(err)
	}
	if forced != 1 {
		t.Fatal("forced write not audited as forced")
	}
	// force never bypasses catalog resolution.
	bad := map[string]any{"targets": []map[string]any{{"provider": "ghost", "model_ref": "g-1"}}, "force": true}
	rr = configAdminRequest(t, mux, "PUT", "/admin/config/groups/agent-default/targets", bad, adminHeaders("adminkey:ops", "config:write", "config:force"))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("force must not bypass catalog checks: status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestConfigAdminAuthzRejects(t *testing.T) {
	db := configAdminTestDB(t)
	mux := configAdminTestMux(t, db, nil)
	body := map[string]any{"targets": []map[string]any{{"provider": "beta", "model_ref": "b-one"}}}

	// No credentials: 401.
	rr := configAdminRequest(t, mux, "PUT", "/admin/config/groups/agent-default/targets", body, nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no token status=%d", rr.Code)
	}
	// Read-only admin writing: 403.
	rr = configAdminRequest(t, mux, "PUT", "/admin/config/groups/agent-default/targets", body, adminHeaders("adminkey:ro", "config:read"))
	if rr.Code != http.StatusForbidden || decodeErrorType(t, rr) != "config-forbidden" {
		t.Fatalf("read-only write status=%d body=%s", rr.Code, rr.Body.String())
	}
	// Write-only admin reading: 403.
	rr = configAdminRequest(t, mux, "GET", "/admin/config/groups", nil, adminHeaders("adminkey:wo", "config:write"))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("write-only read status=%d", rr.Code)
	}
	// Rollback needs config:activate, not config:write.
	rr = configAdminRequest(t, mux, "POST", "/admin/config/groups/agent-default/rollback", map[string]any{}, adminHeaders("adminkey:ops", "config:write"))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("rollback with config:write only status=%d", rr.Code)
	}
}

func TestConfigAdminGroupRollbackAndHistory(t *testing.T) {
	db := configAdminTestDB(t)
	mux := configAdminTestMux(t, db, nil)
	admin := adminHeaders("adminkey:ops", "config:read", "config:write", "config:activate")

	swap := map[string]any{"targets": []map[string]any{{"provider": "beta", "model_ref": "b-one"}}, "reason": "swap to beta"}
	rr := configAdminRequest(t, mux, "PUT", "/admin/config/groups/agent-default/targets", swap, admin)
	if rr.Code != http.StatusOK {
		t.Fatalf("swap status=%d body=%s", rr.Code, rr.Body.String())
	}
	var swapBody struct {
		PreviousSetID string `json:"previous_set_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &swapBody); err != nil {
		t.Fatal(err)
	}

	// One-call rollback with the default prior set.
	rr = configAdminRequest(t, mux, "POST", "/admin/config/groups/agent-default/rollback", map[string]any{"reason": "revert"}, admin)
	if rr.Code != http.StatusOK {
		t.Fatalf("rollback status=%d body=%s", rr.Code, rr.Body.String())
	}
	var rollbackBody struct {
		RestoredFromSet string `json:"restored_from_set_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &rollbackBody); err != nil {
		t.Fatal(err)
	}
	if rollbackBody.RestoredFromSet != swapBody.PreviousSetID {
		t.Fatalf("rollback restored_from=%s want %s", rollbackBody.RestoredFromSet, swapBody.PreviousSetID)
	}
	raw, err := LoadActiveCanonicalYAML(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "model_ref: a-one") || strings.Contains(string(raw), "model_ref: b-one") {
		t.Fatalf("rollback did not restore targets: %s", raw)
	}

	// History shows swap and rollback with before/after targets and no
	// secrets.
	rr = configAdminRequest(t, mux, "GET", "/admin/config/groups/agent-default/history", nil, admin)
	if rr.Code != http.StatusOK {
		t.Fatalf("history status=%d body=%s", rr.Code, rr.Body.String())
	}
	text := rr.Body.String()
	if !strings.Contains(text, configActionGroupTargetsReplace) || !strings.Contains(text, configActionGroupRollback) {
		t.Fatalf("history missing actions: %s", text)
	}
	if !strings.Contains(text, "restored_from_set=") {
		t.Fatalf("rollback audit must link the source set: %s", text)
	}
}

func TestConfigAdminSetsEndpoints(t *testing.T) {
	db := configAdminTestDB(t)
	mux := configAdminTestMux(t, db, nil)
	admin := adminHeaders("adminkey:ops", "config:read", "config:activate")

	// Export the active set.
	rr := configAdminRequest(t, mux, "GET", "/admin/config/sets/active", nil, admin)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "agent-default") {
		t.Fatalf("export active status=%d", rr.Code)
	}
	exported := rr.Body.String()

	// Import a modified copy as a draft.
	modified := strings.Replace(exported, "model_ref: a-one", "model_ref: a-two", 1)
	rr = configAdminRequest(t, mux, "POST", "/admin/config/sets", map[string]any{"name": "imported", "yaml": modified}, admin)
	if rr.Code != http.StatusCreated {
		t.Fatalf("import status=%d body=%s", rr.Code, rr.Body.String())
	}
	var importBody struct {
		SetID string `json:"set_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &importBody); err != nil {
		t.Fatal(err)
	}

	// Import does not activate.
	raw, _ := LoadActiveCanonicalYAML(db, "prod")
	if !strings.Contains(string(raw), "model_ref: a-one") {
		t.Fatal("import must not activate")
	}

	// Activate the draft.
	rr = configAdminRequest(t, mux, "POST", "/admin/config/sets/"+importBody.SetID+"/activate", nil, admin)
	if rr.Code != http.StatusOK {
		t.Fatalf("activate status=%d body=%s", rr.Code, rr.Body.String())
	}
	raw, _ = LoadActiveCanonicalYAML(db, "prod")
	if strings.Contains(string(raw), "model_ref: a-one") || !strings.Contains(string(raw), "model_ref: a-two") {
		t.Fatalf("activate did not switch the document: %s", raw)
	}

	// List sets.
	rr = configAdminRequest(t, mux, "GET", "/admin/config/sets", nil, admin)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), importBody.SetID) {
		t.Fatalf("list sets status=%d body=%s", rr.Code, rr.Body.String())
	}

	// Diff between the two sets.
	rr = configAdminRequest(t, mux, "GET", "/admin/config/diff?from=&to=active", nil, admin)
	if rr.Code != http.StatusOK {
		t.Fatalf("diff status=%d body=%s", rr.Code, rr.Body.String())
	}

	// Set rollback to the previous set reverts the document.
	var listBody struct {
		Sets []struct {
			SetID  string `json:"set_id"`
			Status string `json:"status"`
		} `json:"sets"`
	}
	rr = configAdminRequest(t, mux, "GET", "/admin/config/sets", nil, admin)
	if err := json.Unmarshal(rr.Body.Bytes(), &listBody); err != nil {
		t.Fatal(err)
	}
	prior := ""
	for _, set := range listBody.Sets {
		if set.Status == "superseded" {
			prior = set.SetID
		}
	}
	if prior == "" {
		t.Fatal("no superseded set found")
	}
	rr = configAdminRequest(t, mux, "POST", "/admin/config/rollback", map[string]any{"set_id": prior, "reason": "revert import"}, admin)
	if rr.Code != http.StatusOK {
		t.Fatalf("set rollback status=%d body=%s", rr.Code, rr.Body.String())
	}
	raw, _ = LoadActiveCanonicalYAML(db, "prod")
	if !strings.Contains(string(raw), "model_ref: a-one") {
		t.Fatal("set rollback did not restore the prior document")
	}

	// Importing a document with a raw-looking secret is refused.
	secretDoc := strings.Replace(exported, "ALPHA_API_KEY", "sk-"+strings.Repeat("ab12", 8), 1)
	rr = configAdminRequest(t, mux, "POST", "/admin/config/sets", map[string]any{"name": "secret", "yaml": secretDoc}, admin)
	if rr.Code != http.StatusBadRequest || decodeErrorType(t, rr) != "config-secret-refused" {
		t.Fatalf("secret import status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestConfigAdminConcurrentWritesExactlyOneWins(t *testing.T) {
	db := configAdminTestDB(t)
	mux := configAdminTestMux(t, db, nil)
	admin := adminHeaders("adminkey:ops", "config:read", "config:write")
	status := configAdminRequest(t, mux, "GET", "/admin/config/status", nil, admin)
	var statusBody struct {
		Revision int64 `json:"revision"`
	}
	if err := json.Unmarshal(status.Body.Bytes(), &statusBody); err != nil {
		t.Fatal(err)
	}
	withMatch := func() map[string]string {
		h := adminHeaders("adminkey:ops", "config:read", "config:write")
		h["If-Match"] = fmt.Sprintf("%d", statusBody.Revision)
		return h
	}
	var wg sync.WaitGroup
	results := make([]int, 8)
	bodies := make([]string, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			target := "a-two"
			if i%2 == 0 {
				target = "a-one"
			}
			provider := "alpha"
			if i%3 == 0 {
				provider, target = "beta", "b-one"
			}
			rr := configAdminRequest(t, mux, "PUT", "/admin/config/groups/agent-default/targets",
				map[string]any{"targets": []map[string]any{{"provider": provider, "model_ref": target}}}, withMatch())
			results[i] = rr.Code
			bodies[i] = rr.Body.String()
		}(i)
	}
	wg.Wait()
	wins := 0
	for i, code := range results {
		if code == http.StatusOK {
			wins++
		} else if code != http.StatusConflict {
			t.Fatalf("concurrent write %d status=%d body=%s", i, code, bodies[i])
		}
	}
	if wins != 1 {
		t.Fatalf("concurrent writes: %d won, want exactly 1 (codes=%v)", wins, results)
	}
}

func TestConfigAdminStatusEndpoint(t *testing.T) {
	db := configAdminTestDB(t)
	mux := configAdminTestMux(t, db, nil)
	rr := configAdminRequest(t, mux, "GET", "/admin/config/status", nil, adminHeaders("adminkey:ops", "config:read"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Mode        string `json:"mode"`
		Driver      string `json:"driver"`
		ActiveSetID string `json:"active_set_id"`
		Revision    int64  `json:"revision"`
		Replica     struct {
			InstanceID string `json:"instance_id"`
		} `json:"replica"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Mode != "database" || body.Driver != "sqlite" || body.ActiveSetID == "" || body.Revision < 1 || body.Replica.InstanceID != "replica-test" {
		t.Fatalf("status body=%s", rr.Body.String())
	}
}

func TestConfigAdminSwapWithValidationMetadata(t *testing.T) {
	// The API uses snake_case throughout, including nested validation
	// metadata (validated_at, quality_score), which must survive into the
	// canonical document's target entry.
	db := configAdminTestDB(t)
	mux := configAdminTestMux(t, db, nil)
	admin := adminHeaders("adminkey:ops", "config:read", "config:write")
	body := map[string]any{"targets": []map[string]any{{
		"provider": "beta", "model_ref": "b-one",
		"validation": map[string]any{"status": "passed", "workload": "e2e", "validated_at": "2026-10-27", "quality_score": 0.9},
	}}}
	rr := configAdminRequest(t, mux, "PUT", "/admin/config/groups/agent-default/targets", body, admin)
	if rr.Code != http.StatusOK {
		t.Fatalf("swap with validation status=%d body=%s", rr.Code, rr.Body.String())
	}
	raw, err := LoadActiveCanonicalYAML(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "validated_at") || !strings.Contains(string(raw), "quality_score") {
		t.Fatalf("validation metadata lost from canonical document: %s", raw)
	}
	// And the serving config carries it.
	cfg, err := LoadActiveCanonicalConfig(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	target := cfg.Models["agent-default"].Targets[0]
	if target.Validation == nil || target.Validation.Status != "passed" || target.Validation.ValidatedAt != "2026-10-27" {
		t.Fatalf("validation metadata not resolved: %+v", target.Validation)
	}
}

func TestConfigAdminValidateEndpointIsSideEffectFree(t *testing.T) {
	db := configAdminTestDB(t)
	mux := configAdminTestMux(t, db, nil)
	admin := adminHeaders("adminkey:ops", "config:read", "config:write")
	body := map[string]any{"targets": []map[string]any{{"provider": "beta", "model_ref": "b-one"}}}
	rr := configAdminRequest(t, mux, "POST", "/admin/config/groups/agent-default/targets:validate", body, admin)
	if rr.Code != http.StatusOK {
		t.Fatalf("validate status=%d body=%s", rr.Code, rr.Body.String())
	}
	var parsed struct {
		DryRun bool `json:"dry_run"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	if !parsed.DryRun {
		t.Fatal(":validate must report dry_run=true")
	}
	raw, _ := LoadActiveCanonicalYAML(db, "prod")
	if !strings.Contains(string(raw), "model_ref: a-one") {
		t.Fatal(":validate changed the active document")
	}
	// Invalid target through :validate reports 422 with the per-target report.
	rr = configAdminRequest(t, mux, "POST", "/admin/config/groups/agent-default/targets:validate",
		map[string]any{"targets": []map[string]any{{"provider": "ghost", "model_ref": "g-1"}}}, admin)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf(":validate ineligible status=%d body=%s", rr.Code, rr.Body.String())
	}
}

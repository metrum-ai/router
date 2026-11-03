// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Service-level tests for owner-bound stored Responses affinity (#533), first
// slice: create and previous_response_id continuation. The feature is off by
// default; the disabled test proves request and response behavior is unchanged.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	affinityTestToken       = testToken
	affinityTestSecondToken = "test-token-bob"
)

func affinityKeyMaterial() string { return strings.Repeat("ab", 32) }

// affinityUpstream is a unique-id native Responses mock. Each request returns a
// new upstream id and, for streamed requests, a response.created event with
// that id before the terminal event.
type affinityUpstream struct {
	mu      sync.Mutex
	next    int
	bodies  []map[string]any
	streams int
	lastRaw string
}

func (u *affinityUpstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.bodies)
}

func (u *affinityUpstream) lastBody() map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.bodies) == 0 {
		return nil
	}
	return u.bodies[len(u.bodies)-1]
}

func (u *affinityUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	u.mu.Lock()
	u.next++
	id := fmt.Sprintf("resp_up_%d", u.next)
	u.bodies = append(u.bodies, body)
	u.mu.Unlock()
	model := stringValue(body["model"])
	if stream, _ := body["stream"].(bool); stream {
		u.mu.Lock()
		u.streams++
		u.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":%q,\"object\":\"response\",\"model\":%q,\"status\":\"in_progress\",\"output\":[]}}\n\n", id, model)
		fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":%q,\"object\":\"response\",\"model\":%q,\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n", id, model)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	encoded, _ := json.Marshal(map[string]any{
		"id": id, "object": "response", "status": "completed", "model": model,
		"output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "ok"}}}},
		"usage":  map[string]any{"input_tokens": 3, "output_tokens": 1, "total_tokens": 4},
	})
	u.mu.Lock()
	u.lastRaw = string(encoded)
	u.mu.Unlock()
	_, _ = w.Write(append(encoded, '\n'))
}

func affinityCaller(token, id string) CallerConfig {
	sum := sha256.Sum256([]byte(token))
	return CallerConfig{
		ID: id, User: id, Project: "affinity-project", Environment: "test",
		TokenSHA256: hex.EncodeToString(sum[:]), TokenID: "rtr_" + id,
		Allow: []string{"default", "other", "stored"},
		Rate:  RateConfig{RPM: 100, TPM: 100000, Concurrent: 4},
	}
}

func affinityTestService(t *testing.T, upstreamURL, dir string, mutate func(*Config)) (*Service, *Config) {
	t.Helper()
	cfg := testConfig(t, upstreamURL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.Identifiers = IdentifierConfig{Mode: "passthrough"}
	cfg.Provider["native"] = ProviderConfig{BaseURL: upstreamURL + "/v1", Dialect: "openai-responses", APIKey: "provider-key", KeyID: "acct-a"}
	cfg.Provider["native-b"] = ProviderConfig{BaseURL: upstreamURL + "/v1", Dialect: "openai-responses", APIKey: "provider-key-b", KeyID: "acct-b"}
	cfg.Models["stored"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "native", Model: "resp-model"}}}
	cfg.Models["pinned"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "native-b", Model: "resp-model-b", RequestShapeSupport: RequestShapeSupport{MaxRequestBytes: 600}},
		{Provider: "native", Model: "resp-model"},
	}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "stored", "pinned")
	cfg.Callers = append(cfg.Callers, affinityCaller(affinityTestSecondToken, "bob"))
	if mutate != nil {
		mutate(cfg)
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc, cfg
}

func affinityRows(t *testing.T, svc *Service) []responseAffinityRecord {
	t.Helper()
	var rows []responseAffinityRecord
	if err := svc.usage.db.Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func postAffinity(t *testing.T, svc *Service, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	return rr
}

func decodeJSONBody(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return out
}

// TestResponsesAffinityDisabledIsUnchanged proves the default-off contract: no
// affinity row, no ownership lookup, no target pin and the same upstream and
// caller payloads as before this feature existed.
func TestResponsesAffinityDisabledIsUnchanged(t *testing.T) {
	upstream := &affinityUpstream{}
	server := httptest.NewServer(upstream)
	defer server.Close()
	svc, cfg := affinityTestService(t, server.URL, t.TempDir(), func(cfg *Config) {
		cfg.Server.ResponsesAffinity = ResponsesAffinityConfig{}
	})
	if svc.responsesAffinity != nil {
		t.Fatal("affinity runtime must be nil when no group is enabled")
	}
	body := `{"model":"stored","input":"hello","previous_response_id":"resp_up_unknown"}`
	rr := postAffinity(t, svc, affinityTestToken, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := upstream.lastBody(); got == nil || stringValue(got["previous_response_id"]) != "resp_up_unknown" {
		t.Fatalf("upstream body changed: %#v", got)
	}
	response := decodeJSONBody(t, rr.Body.String())
	if response["id"] != "resp_up_1" {
		t.Fatalf("response id changed: %#v", response["id"])
	}
	if rows := affinityRows(t, svc); len(rows) != 0 {
		t.Fatalf("disabled affinity wrote rows: %#v", rows)
	}
	if cfg.Server.ResponsesAffinity.enabled() {
		t.Fatal("config unexpectedly enabled")
	}
}

func TestResponsesAffinityCreateAndContinuationPin(t *testing.T) {
	t.Setenv("TEST_RESPONSES_AFFINITY_KEY", affinityKeyMaterial())
	upstream := &affinityUpstream{}
	server := httptest.NewServer(upstream)
	defer server.Close()
	svc, _ := affinityTestService(t, server.URL, t.TempDir(), func(cfg *Config) {
		cfg.Server.ResponsesAffinity = ResponsesAffinityConfig{
			EnabledGroups: []string{"pinned"},
			KeyEnv:        "TEST_RESPONSES_AFFINITY_KEY",
			KeyID:         "affinity-1",
		}
	})
	// The create request is too large for the first target, so the native
	// target stores it. The follow-up is small enough for the first target, so
	// only the affinity pin can keep it on the original account.
	large := strings.Repeat("x", 700)
	rr := postAffinity(t, svc, affinityTestToken, fmt.Sprintf(`{"model":"pinned","input":%q}`, large))
	if rr.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rr.Code, rr.Body.String())
	}
	created := decodeJSONBody(t, rr.Body.String())
	upstreamID := stringValue(created["id"])
	if upstreamID != "resp_up_1" {
		t.Fatalf("create id=%q", upstreamID)
	}
	rows := affinityRows(t, svc)
	if len(rows) != 1 {
		t.Fatalf("rows=%#v", rows)
	}
	row := rows[0]
	if row.Provider != "native" || row.AccountRef != "acct-a" || row.ModelGroup != "pinned" || row.ProviderDialect != "openai-responses" {
		t.Fatalf("row identity=%#v", row)
	}
	if row.OwnerTokenFingerprint == "" || row.ResponseLookupHash == "" || row.UpstreamIDCiphertext == "" || row.UpstreamIDNonce == "" {
		t.Fatalf("row is missing protected fields: %#v", row)
	}
	if strings.Contains(row.ResponseLookupHash, upstreamID) || strings.Contains(row.UpstreamIDCiphertext, upstreamID) {
		t.Fatal("row exposes the upstream id in plaintext")
	}
	if row.ExpiresAt == "" || row.State != "in_progress" {
		t.Fatalf("row lifecycle=%#v", row)
	}

	rr = postAffinity(t, svc, affinityTestToken, fmt.Sprintf(`{"model":"pinned","input":"follow","previous_response_id":%q}`, upstreamID))
	if rr.Code != http.StatusOK {
		t.Fatalf("continuation status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := upstream.lastBody(); got == nil || stringValue(got["previous_response_id"]) != upstreamID {
		t.Fatalf("continuation upstream body=%#v", got)
	}
	if upstream.count() != 2 {
		t.Fatalf("upstream requests=%d, want 2 (pin must not re-route)", upstream.count())
	}
	if rows := affinityRows(t, svc); len(rows) != 2 {
		t.Fatalf("continuation row count=%d", len(rows))
	}
}

func TestResponsesAffinityRecordsServedFallbackTarget(t *testing.T) {
	t.Setenv("TEST_RESPONSES_AFFINITY_KEY", affinityKeyMaterial())
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom","type":"server_error"}}`))
	}))
	defer failing.Close()
	upstream := &affinityUpstream{}
	serving := httptest.NewServer(upstream)
	defer serving.Close()
	svc, _ := affinityTestService(t, serving.URL, t.TempDir(), func(cfg *Config) {
		cfg.Provider["native"] = ProviderConfig{BaseURL: failing.URL + "/v1", Dialect: "openai-responses", APIKey: "k", KeyID: "acct-a"}
		cfg.Models["stored"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "native", Model: "fails"}, {Provider: "native-b", Model: "serves"}}}
		cfg.Server.ResponsesAffinity = ResponsesAffinityConfig{EnabledGroups: []string{"stored"}, KeyEnv: "TEST_RESPONSES_AFFINITY_KEY", KeyID: "affinity-1"}
	})
	rr := postAffinity(t, svc, affinityTestToken, `{"model":"stored","input":"hello"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	rows := affinityRows(t, svc)
	if len(rows) != 1 || rows[0].Provider != "native-b" || rows[0].AccountRef != "acct-b" || rows[0].Model != "serves" {
		t.Fatalf("fallback row=%#v", rows)
	}
}

func TestResponsesAffinityCrossCallerGetsNotFoundWithoutUpstream(t *testing.T) {
	t.Setenv("TEST_RESPONSES_AFFINITY_KEY", affinityKeyMaterial())
	upstream := &affinityUpstream{}
	server := httptest.NewServer(upstream)
	defer server.Close()
	svc, _ := affinityTestService(t, server.URL, t.TempDir(), func(cfg *Config) {
		cfg.Server.ResponsesAffinity = ResponsesAffinityConfig{EnabledGroups: []string{"stored"}, KeyEnv: "TEST_RESPONSES_AFFINITY_KEY", KeyID: "affinity-1"}
	})
	rr := postAffinity(t, svc, affinityTestToken, `{"model":"stored","input":"hello"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rr.Code, rr.Body.String())
	}
	created := decodeJSONBody(t, rr.Body.String())
	before := upstream.count()
	rr = postAffinity(t, svc, affinityTestSecondToken, fmt.Sprintf(`{"model":"stored","input":"steal","previous_response_id":%q}`, created["id"]))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-caller status=%d body=%s", rr.Code, rr.Body.String())
	}
	if errBody := decodeJSONBody(t, rr.Body.String()); stringValue(errBody["error"].(map[string]any)["code"]) != "response_not_found" {
		t.Fatalf("cross-caller error=%s", rr.Body.String())
	}
	if upstream.count() != before {
		t.Fatalf("cross-caller request reached upstream: %d -> %d", before, upstream.count())
	}
}

func TestResponsesAffinityUnavailableTargetReturns409(t *testing.T) {
	t.Setenv("TEST_RESPONSES_AFFINITY_KEY", affinityKeyMaterial())
	upstream := &affinityUpstream{}
	server := httptest.NewServer(upstream)
	defer server.Close()
	dir := t.TempDir()
	svc, _ := affinityTestService(t, server.URL, dir, func(cfg *Config) {
		cfg.Server.ResponsesAffinity = ResponsesAffinityConfig{EnabledGroups: []string{"stored"}, KeyEnv: "TEST_RESPONSES_AFFINITY_KEY", KeyID: "affinity-1"}
	})
	rr := postAffinity(t, svc, affinityTestToken, `{"model":"stored","input":"hello"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rr.Code, rr.Body.String())
	}
	created := decodeJSONBody(t, rr.Body.String())
	before := upstream.count()
	svc.Close()

	// Reopen the same store with a group whose target has a different account.
	cfg2 := testConfig(t, server.URL, "provider-key", dir)
	cfg2.Server.UsageDB = UsageDBConfig{Driver: "sqlite", Path: filepath.Join(dir, "usage.sqlite"), MigrationPolicy: usageDBMigrationPolicyAutoSafe}
	cfg2.Server.Identifiers = IdentifierConfig{Mode: "passthrough"}
	cfg2.Provider["native-b"] = ProviderConfig{BaseURL: server.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key-b", KeyID: "acct-b"}
	cfg2.Models["stored"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "native-b", Model: "resp-model-b"}}}
	cfg2.Callers[0].Allow = append(cfg2.Callers[0].Allow, "stored")
	cfg2.Server.ResponsesAffinity = ResponsesAffinityConfig{EnabledGroups: []string{"stored"}, KeyEnv: "TEST_RESPONSES_AFFINITY_KEY", KeyID: "affinity-1"}
	svc2, err := New(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	defer svc2.Close()
	rr = postAffinity(t, svc2, affinityTestToken, fmt.Sprintf(`{"model":"stored","input":"follow","previous_response_id":%q}`, created["id"]))
	if rr.Code != http.StatusConflict {
		t.Fatalf("unavailable target status=%d body=%s", rr.Code, rr.Body.String())
	}
	if errBody := decodeJSONBody(t, rr.Body.String()); stringValue(errBody["error"].(map[string]any)["code"]) != "response_target_unavailable" {
		t.Fatalf("unavailable target error=%s", rr.Body.String())
	}
	if upstream.count() != before {
		t.Fatalf("unavailable target request reached upstream")
	}
}

func TestResponsesAffinityStoreFalseCreatesNoRecord(t *testing.T) {
	t.Setenv("TEST_RESPONSES_AFFINITY_KEY", affinityKeyMaterial())
	upstream := &affinityUpstream{}
	server := httptest.NewServer(upstream)
	defer server.Close()
	svc, _ := affinityTestService(t, server.URL, t.TempDir(), func(cfg *Config) {
		cfg.Server.ResponsesAffinity = ResponsesAffinityConfig{EnabledGroups: []string{"stored"}, KeyEnv: "TEST_RESPONSES_AFFINITY_KEY", KeyID: "affinity-1"}
	})
	if rr := postAffinity(t, svc, affinityTestToken, `{"model":"stored","input":"nostore","store":false}`); rr.Code != http.StatusOK {
		t.Fatalf("no-store status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rows := affinityRows(t, svc); len(rows) != 0 {
		t.Fatalf("no-store wrote rows: %#v", rows)
	}
	rr := postAffinity(t, svc, affinityTestToken, `{"model":"stored","input":"hello"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rr.Code, rr.Body.String())
	}
	created := decodeJSONBody(t, rr.Body.String())
	rr = postAffinity(t, svc, affinityTestToken, fmt.Sprintf(`{"model":"stored","input":"follow","store":false,"previous_response_id":%q}`, created["id"]))
	if rr.Code != http.StatusOK {
		t.Fatalf("no-store continuation status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := upstream.lastBody(); got == nil || got["store"] != false || stringValue(got["previous_response_id"]) != stringValue(created["id"]) {
		t.Fatalf("no-store continuation did not forward store:false: %#v", got)
	}
	if rows := affinityRows(t, svc); len(rows) != 1 {
		t.Fatalf("no-store continuation created a row: %d", len(rows))
	}
}

func TestResponsesAffinityRewriteModeRoundTrip(t *testing.T) {
	t.Setenv("TEST_RESPONSES_AFFINITY_KEY", affinityKeyMaterial())
	upstream := &affinityUpstream{}
	server := httptest.NewServer(upstream)
	defer server.Close()
	svc, _ := affinityTestService(t, server.URL, t.TempDir(), func(cfg *Config) {
		cfg.Server.Identifiers = identifierTestConfig()
		cfg.Server.ResponsesAffinity = ResponsesAffinityConfig{EnabledGroups: []string{"stored"}, KeyEnv: "TEST_RESPONSES_AFFINITY_KEY", KeyID: "affinity-1"}
	})
	rr := postAffinity(t, svc, affinityTestToken, `{"model":"stored","input":"hello"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rr.Code, rr.Body.String())
	}
	created := decodeJSONBody(t, rr.Body.String())
	presented := stringValue(created["id"])
	if presented == "" || !strings.HasPrefix(presented, "mr_") {
		t.Fatalf("rewrite create did not encode the response id: %q", presented)
	}
	if got := upstream.lastBody(); got == nil {
		t.Fatal("upstream did not receive the create request")
	}
	rows := affinityRows(t, svc)
	if len(rows) != 1 {
		t.Fatalf("rewrite create rows=%d", len(rows))
	}
	if strings.Contains(rows[0].ResponseLookupHash, presented) || strings.Contains(rows[0].UpstreamIDCiphertext, presented) {
		t.Fatal("rewrite create stored a plaintext id")
	}
	// The unary Responses surface leaves provider ids unencoded today, so both
	// the presented id and the router-encoded form a streamed id would use must
	// resolve to the same owner record.
	for _, previous := range []string{presented, "resp_up_1"} {
		rr = postAffinity(t, svc, affinityTestToken, fmt.Sprintf(`{"model":"stored","input":"follow","previous_response_id":%q}`, previous))
		if rr.Code != http.StatusOK {
			t.Fatalf("rewrite continuation %q status=%d body=%s", previous, rr.Code, rr.Body.String())
		}
		if got := upstream.lastBody(); got == nil || !strings.HasPrefix(stringValue(got["previous_response_id"]), "resp_up_") {
			t.Fatalf("rewrite continuation upstream body=%#v", got)
		}
	}
	if upstream.count() != 3 {
		t.Fatalf("rewrite continuation requests=%d", upstream.count())
	}
}

func TestResponsesAffinityStreamingCreateStoresBeforeExposingID(t *testing.T) {
	t.Setenv("TEST_RESPONSES_AFFINITY_KEY", affinityKeyMaterial())
	upstream := &affinityUpstream{}
	server := httptest.NewServer(upstream)
	defer server.Close()
	svc, _ := affinityTestService(t, server.URL, t.TempDir(), func(cfg *Config) {
		cfg.Server.ResponsesAffinity = ResponsesAffinityConfig{EnabledGroups: []string{"stored"}, KeyEnv: "TEST_RESPONSES_AFFINITY_KEY", KeyID: "affinity-1"}
	})
	rr := postAffinity(t, svc, affinityTestToken, `{"model":"stored","input":"hello","stream":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("stream create status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "response.created") || !strings.Contains(rr.Body.String(), "resp_up_1") {
		t.Fatalf("stream body=%s", rr.Body.String())
	}
	if upstream.streams != 1 {
		t.Fatalf("stream requests=%d", upstream.streams)
	}
	rows := affinityRows(t, svc)
	if len(rows) != 1 || rows[0].UpstreamIDCiphertext == "" {
		t.Fatalf("stream create rows=%#v", rows)
	}
	rr = postAffinity(t, svc, affinityTestToken, `{"model":"stored","input":"follow","previous_response_id":"resp_up_1"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("stream continuation status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstream.count() != 2 {
		t.Fatalf("stream continuation requests=%d", upstream.count())
	}
}

func TestResponsesAffinityStoreFailureFailsClosed(t *testing.T) {
	t.Setenv("TEST_RESPONSES_AFFINITY_KEY", affinityKeyMaterial())
	upstream := &affinityUpstream{}
	server := httptest.NewServer(upstream)
	defer server.Close()
	svc, _ := affinityTestService(t, server.URL, t.TempDir(), func(cfg *Config) {
		cfg.Server.ResponsesAffinity = ResponsesAffinityConfig{EnabledGroups: []string{"stored"}, KeyEnv: "TEST_RESPONSES_AFFINITY_KEY", KeyID: "affinity-1"}
	})
	sqlDB, err := svc.usage.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	rr := postAffinity(t, svc, affinityTestToken, `{"model":"stored","input":"hello"}`)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("store failure status=%d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "resp_up_1") {
		t.Fatalf("fail-closed response exposed the upstream id: %s", rr.Body.String())
	}
}

func TestResponsesAffinityStreamStoreFailureExposesNoID(t *testing.T) {
	t.Setenv("TEST_RESPONSES_AFFINITY_KEY", affinityKeyMaterial())
	upstream := &affinityUpstream{}
	server := httptest.NewServer(upstream)
	defer server.Close()
	svc, _ := affinityTestService(t, server.URL, t.TempDir(), func(cfg *Config) {
		cfg.Server.ResponsesAffinity = ResponsesAffinityConfig{EnabledGroups: []string{"stored"}, KeyEnv: "TEST_RESPONSES_AFFINITY_KEY", KeyID: "affinity-1"}
	})
	sqlDB, err := svc.usage.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	rr := postAffinity(t, svc, affinityTestToken, `{"model":"stored","input":"hello","stream":true}`)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("stream store failure status=%d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "response.created") || strings.Contains(rr.Body.String(), "resp_up_1") {
		t.Fatalf("stream store failure exposed the id: %s", rr.Body.String())
	}
}

func TestResponsesAffinityConfigValidation(t *testing.T) {
	t.Setenv("TEST_RESPONSES_AFFINITY_KEY", affinityKeyMaterial())
	base := func() *Config {
		cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
		cfg.Server.UsageDB = UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "u.sqlite"), MigrationPolicy: usageDBMigrationPolicyAutoSafe}
		cfg.Provider["native"] = ProviderConfig{BaseURL: "http://127.0.0.1:1/v1", Dialect: "openai-responses", APIKey: "k", KeyID: "acct"}
		cfg.Models["stored"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "native", Model: "m"}}}
		return cfg
	}
	for name, tc := range map[string]struct {
		cfg     ResponsesAffinityConfig
		wantErr string
	}{
		"disabled is default": {cfg: ResponsesAffinityConfig{}},
		"missing group":       {cfg: ResponsesAffinityConfig{EnabledGroups: []string{"missing"}, KeyEnv: "TEST_RESPONSES_AFFINITY_KEY", KeyID: "k"}, wantErr: "enabled_groups"},
		"no native target":    {cfg: ResponsesAffinityConfig{EnabledGroups: []string{"default"}, KeyEnv: "TEST_RESPONSES_AFFINITY_KEY", KeyID: "k"}, wantErr: "native Responses"},
		"missing key env":     {cfg: ResponsesAffinityConfig{EnabledGroups: []string{"stored"}, KeyID: "k"}, wantErr: "key_env"},
		"missing key id":      {cfg: ResponsesAffinityConfig{EnabledGroups: []string{"stored"}, KeyEnv: "TEST_RESPONSES_AFFINITY_KEY"}, wantErr: "key_env"},
		"half previous key":   {cfg: ResponsesAffinityConfig{EnabledGroups: []string{"stored"}, KeyEnv: "TEST_RESPONSES_AFFINITY_KEY", KeyID: "k", PreviousKeyEnv: "X"}, wantErr: "previous_key_env"},
		"retention too long":  {cfg: ResponsesAffinityConfig{EnabledGroups: []string{"stored"}, KeyEnv: "TEST_RESPONSES_AFFINITY_KEY", KeyID: "k", RetentionDays: 400}, wantErr: "retention_days"},
		"key without groups":  {cfg: ResponsesAffinityConfig{KeyEnv: "X"}, wantErr: "enabled_groups"},
		"duplicate group":     {cfg: ResponsesAffinityConfig{EnabledGroups: []string{"stored", "stored"}, KeyEnv: "TEST_RESPONSES_AFFINITY_KEY", KeyID: "k"}, wantErr: "enabled_groups"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base()
			cfg.Server.ResponsesAffinity = tc.cfg
			cfg.setDefaults()
			err := cfg.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error=%v, want %q", err, tc.wantErr)
			}
		})
	}
	t.Run("missing secret fails startup", func(t *testing.T) {
		cfg := base()
		cfg.Server.ResponsesAffinity = ResponsesAffinityConfig{EnabledGroups: []string{"stored"}, KeyEnv: "TEST_RESPONSES_AFFINITY_KEY_MISSING", KeyID: "k"}
		if _, err := New(cfg); err == nil {
			t.Fatal("startup must fail without key material")
		}
	})
	t.Run("usage db disabled fails startup", func(t *testing.T) {
		cfg := base()
		disabled := false
		cfg.Server.UsageDB.Enable = &disabled
		cfg.Server.ResponsesAffinity = ResponsesAffinityConfig{EnabledGroups: []string{"stored"}, KeyEnv: "TEST_RESPONSES_AFFINITY_KEY", KeyID: "k"}
		if _, err := New(cfg); err == nil {
			t.Fatal("startup must fail without a durable usage database")
		}
	})
}

func affinityDo(t *testing.T, svc *Service, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	return rr
}

func TestResponsesAffinityKeyRotationKeepsOldRowsReachable(t *testing.T) {
	upstream := &affinityUpstream{}
	server := httptest.NewServer(upstream)
	defer server.Close()
	dir := t.TempDir()
	cfg := func(keyEnv, keyID, prevEnv, prevID string) *Config {
		c := testConfig(t, server.URL, "provider-key", dir)
		c.Server.UsageDB = UsageDBConfig{Driver: "sqlite", Path: filepath.Join(dir, "usage.sqlite"), MigrationPolicy: usageDBMigrationPolicyAutoSafe}
		c.Server.Identifiers = IdentifierConfig{Mode: "passthrough"}
		c.Server.Retention = RetentionConfig{Enabled: true}
		c.Provider["native"] = ProviderConfig{BaseURL: server.URL + "/v1", Dialect: "openai-responses", APIKey: "k", KeyID: "acct-a"}
		c.Models["stored"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "native", Model: "resp-model"}}}
		c.Callers[0].Allow = append(c.Callers[0].Allow, "stored")
		c.Server.ResponsesAffinity = ResponsesAffinityConfig{EnabledGroups: []string{"stored"}, KeyEnv: keyEnv, KeyID: keyID, PreviousKeyEnv: prevEnv, PreviousKeyID: prevID}
		return c
	}
	t.Setenv("TEST_AFFINITY_KEY_1", affinityKeyMaterial())
	t.Setenv("TEST_AFFINITY_KEY_2", strings.Repeat("cd", 32))
	t.Setenv("TEST_AFFINITY_KEY_3", strings.Repeat("ef", 32))
	first, err := New(cfg("TEST_AFFINITY_KEY_1", "epoch-1", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	rr := postAffinity(t, first, affinityTestToken, `{"model":"stored","input":"hello"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rr.Code, rr.Body.String())
	}
	id := stringValue(decodeJSONBody(t, rr.Body.String())["id"])
	first.Close()

	rotated, err := New(cfg("TEST_AFFINITY_KEY_2", "epoch-2", "TEST_AFFINITY_KEY_1", "epoch-1"))
	if err != nil {
		t.Fatal(err)
	}
	rr = postAffinity(t, rotated, affinityTestToken, fmt.Sprintf(`{"model":"stored","input":"follow","previous_response_id":%q}`, id))
	if rr.Code != http.StatusOK {
		t.Fatalf("rotated continuation status=%d body=%s", rr.Code, rr.Body.String())
	}
	rotated.Close()

	withoutPrevious, err := New(cfg("TEST_AFFINITY_KEY_3", "epoch-3", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer withoutPrevious.Close()
	before := upstream.count()
	rr = postAffinity(t, withoutPrevious, affinityTestToken, fmt.Sprintf(`{"model":"stored","input":"follow","previous_response_id":%q}`, id))
	if rr.Code != http.StatusNotFound || upstream.count() != before {
		t.Fatalf("retired epoch continuation status=%d ops=%d->%d body=%s", rr.Code, before, upstream.count(), rr.Body.String())
	}
}

func TestResponsesAffinityRejectsContinuationOnNonEnabledGroup(t *testing.T) {
	t.Setenv("TEST_RESPONSES_AFFINITY_KEY", affinityKeyMaterial())
	upstream := &affinityUpstream{}
	server := httptest.NewServer(upstream)
	defer server.Close()
	svc, _ := affinityTestService(t, server.URL, t.TempDir(), func(cfg *Config) {
		cfg.Provider["native-b"] = ProviderConfig{BaseURL: server.URL + "/v1", Dialect: "openai-responses", APIKey: "k-b", KeyID: "acct-b"}
		cfg.Models["other"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "native-b", Model: "other-model"}}}
		cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "other")
		cfg.Server.ResponsesAffinity = ResponsesAffinityConfig{EnabledGroups: []string{"stored"}, KeyEnv: "TEST_RESPONSES_AFFINITY_KEY", KeyID: "affinity-1"}
	})
	rr := postAffinity(t, svc, affinityTestToken, `{"model":"stored","input":"hello"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rr.Code, rr.Body.String())
	}
	id := stringValue(decodeJSONBody(t, rr.Body.String())["id"])
	before := upstream.count()
	rr = postAffinity(t, svc, affinityTestToken, fmt.Sprintf(`{"model":"other","input":"follow","previous_response_id":%q}`, id))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "stored_response_unsupported") {
		t.Fatalf("non-enabled group status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstream.count() != before {
		t.Fatalf("non-enabled group request reached upstream: %d -> %d", before, upstream.count())
	}
}

func TestResponsesAffinityShadowGuard(t *testing.T) {
	t.Setenv("TEST_RESPONSES_AFFINITY_KEY", affinityKeyMaterial())
	svc, _ := affinityTestService(t, "http://127.0.0.1:1", t.TempDir(), func(cfg *Config) {
		cfg.Server.ResponsesAffinity = ResponsesAffinityConfig{EnabledGroups: []string{"stored"}, KeyEnv: "TEST_RESPONSES_AFFINITY_KEY", KeyID: "affinity-1"}
	})
	if !svc.responsesAffinityBlocksShadow("stored", "openai-responses", &IRRequest{Raw: map[string]any{"input": "hi"}}) {
		t.Fatal("stored create must not be shadow replayed")
	}
	if svc.responsesAffinityBlocksShadow("stored", "openai-responses", &IRRequest{Raw: map[string]any{"store": false}}) {
		t.Fatal("no-store request should keep its previous shadow behavior")
	}
	if !svc.responsesAffinityBlocksShadow("stored", "openai-responses", &IRRequest{Raw: map[string]any{"previous_response_id": "resp_1"}}) {
		t.Fatal("continuation must not be shadow replayed")
	}
	if !svc.responsesAffinityBlocksShadow("other", "openai-responses", &IRRequest{Raw: map[string]any{"previous_response_id": "resp_1"}}) {
		t.Fatal("continuation on a non-enabled group must not be shadow replayed")
	}
	if svc.responsesAffinityBlocksShadow("stored", "openai-chat", &IRRequest{Raw: map[string]any{"previous_response_id": "resp_1"}}) {
		t.Fatal("non-Responses dialect must keep its previous shadow behavior")
	}
}

// Stock rewrite-mode deployments with affinity off must keep their existing
// caller-visible ids: the stored-Responses id encoding is opt-in per group.
func TestResponsesAffinityOffKeepsStockRewriteIDs(t *testing.T) {
	upstream := &affinityUpstream{}
	server := httptest.NewServer(upstream)
	defer server.Close()
	svc, _ := affinityTestService(t, server.URL, t.TempDir(), func(cfg *Config) {
		cfg.Server.Identifiers = identifierTestConfig()
	})
	rr := postAffinity(t, svc, affinityTestToken, `{"model":"stored","input":"hello"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "mr_") {
		t.Fatalf("affinity-off rewrite-mode body encoded ids: %s", rr.Body.String())
	}
	upstream.mu.Lock()
	golden := upstream.lastRaw
	upstream.mu.Unlock()
	if got := strings.TrimSpace(rr.Body.String()); got != strings.TrimSpace(golden) {
		t.Fatalf("affinity-off body changed:\n got %s\nwant %s", got, golden)
	}
}

func TestResponsesAffinityForceStoreFalseCreatesNoRecord(t *testing.T) {
	t.Setenv("TEST_RESPONSES_AFFINITY_KEY", affinityKeyMaterial())
	upstream := &affinityUpstream{}
	server := httptest.NewServer(upstream)
	defer server.Close()
	svc, _ := affinityTestService(t, server.URL, t.TempDir(), func(cfg *Config) {
		cfg.Models["stored"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "native", Model: "resp-model", ForceStoreFalse: true}}}
		cfg.Server.ResponsesAffinity = ResponsesAffinityConfig{EnabledGroups: []string{"stored"}, KeyEnv: "TEST_RESPONSES_AFFINITY_KEY", KeyID: "affinity-1"}
	})
	rr := postAffinity(t, svc, affinityTestToken, `{"model":"stored","input":"hello"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rows := affinityRows(t, svc); len(rows) != 0 {
		t.Fatalf("force_store_false target wrote rows: %#v", rows)
	}
}

func TestStoredResponseBasicAuthWithAPIKey(t *testing.T) {
	t.Setenv("TEST_RESPONSES_AFFINITY_KEY", affinityKeyMaterial())
	upstream := &affinityUpstream{}
	server := httptest.NewServer(upstream)
	defer server.Close()
	svc, _ := affinityTestService(t, server.URL, t.TempDir(), func(cfg *Config) {
		cfg.Server.ResponsesAffinity = ResponsesAffinityConfig{EnabledGroups: []string{"stored"}, KeyEnv: "TEST_RESPONSES_AFFINITY_KEY", KeyID: "affinity-1"}
	})
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"stored","input":"hello"}`))
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	req.Header.Set("X-API-Key", affinityTestToken)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rows := affinityRows(t, svc); len(rows) != 1 {
		t.Fatalf("row not written with X-API-Key auth: %d", len(rows))
	}
}

func TestStoredResponseIsBoundToOwnerToken(t *testing.T) {
	t.Setenv("TEST_RESPONSES_AFFINITY_KEY", affinityKeyMaterial())
	upstream := &affinityUpstream{}
	server := httptest.NewServer(upstream)
	defer server.Close()
	svc, _ := affinityTestService(t, server.URL, t.TempDir(), func(cfg *Config) {
		for i := range cfg.Callers {
			if cfg.Callers[i].ID == "bob" {
				cfg.Callers[i].OwnerUser = "alice"
				cfg.Callers[i].User = "alice"
				cfg.Callers[i].Project = "metrum-insights"
				cfg.Callers[i].Environment = "test"
			}
		}
		cfg.Server.ResponsesAffinity = ResponsesAffinityConfig{EnabledGroups: []string{"stored"}, KeyEnv: "TEST_RESPONSES_AFFINITY_KEY", KeyID: "affinity-1"}
	})
	rr := postAffinity(t, svc, affinityTestToken, `{"model":"stored","input":"hello"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rr.Code, rr.Body.String())
	}
	id := stringValue(decodeJSONBody(t, rr.Body.String())["id"])
	before := upstream.count()
	rr = postAffinity(t, svc, affinityTestSecondToken, fmt.Sprintf(`{"model":"stored","input":"steal","previous_response_id":%q}`, id))
	if rr.Code != http.StatusNotFound || upstream.count() != before {
		t.Fatalf("same-owner other-token continuation status=%d ops=%d->%d body=%s", rr.Code, before, upstream.count(), rr.Body.String())
	}
}

func TestAffinityUpstreamIDAADBindsOwnership(t *testing.T) {
	keys, err := newResponsesAffinityKeySet("k1", affinityKeyMaterial())
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, nonce, err := keys.encryptUpstreamID("resp_1", "owner", "lookup")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keys.decryptUpstreamID(ciphertext, nonce, "owner", "lookup"); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if _, err := keys.decryptUpstreamID(ciphertext, nonce, "other-owner", "lookup"); err == nil {
		t.Fatal("decrypt accepted a different owner fingerprint")
	}
	if _, err := keys.decryptUpstreamID(ciphertext, nonce, "owner", "other-lookup"); err == nil {
		t.Fatal("decrypt accepted a different lookup hash")
	}
}

func TestStoredResponseProjectMismatchIsNotFound(t *testing.T) {
	t.Setenv("TEST_RESPONSES_AFFINITY_KEY", affinityKeyMaterial())
	upstream := &affinityUpstream{}
	server := httptest.NewServer(upstream)
	defer server.Close()
	svc, _ := affinityTestService(t, server.URL, t.TempDir(), func(cfg *Config) {
		for i := range cfg.Callers {
			if cfg.Callers[i].ID == "bob" {
				cfg.Callers[i].OwnerUser = "alice"
				cfg.Callers[i].User = "alice"
				cfg.Callers[i].Project = "another-project"
				cfg.Callers[i].Environment = "test"
			}
		}
		cfg.Server.ResponsesAffinity = ResponsesAffinityConfig{EnabledGroups: []string{"stored"}, KeyEnv: "TEST_RESPONSES_AFFINITY_KEY", KeyID: "affinity-1"}
	})
	rr := postAffinity(t, svc, affinityTestToken, `{"model":"stored","input":"hello"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rr.Code, rr.Body.String())
	}
	id := stringValue(decodeJSONBody(t, rr.Body.String())["id"])
	before := upstream.count()
	rr = postAffinity(t, svc, affinityTestSecondToken, fmt.Sprintf(`{"model":"stored","input":"steal","previous_response_id":%q}`, id))
	if rr.Code != http.StatusNotFound || upstream.count() != before {
		t.Fatalf("project mismatch continuation status=%d ops=%d->%d body=%s", rr.Code, before, upstream.count(), rr.Body.String())
	}
}

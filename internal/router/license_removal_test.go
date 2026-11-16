// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestL001LoadConfigWithoutLicenseBlockEmitsNoWarning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeMinimalYAMLConfig(t, path, "")
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		t.Fatal("expected config")
	}
	if strings.Contains(buf.String(), "server.license") {
		t.Fatalf("unexpected license warning: %s", buf.String())
	}
}

func TestL002LoadConfigWithLegacyLicenseBlockRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	legacy := `
  license:
    enabled: true
    path: ` + filepath.Join(dir, "license.json") + `
    state_path: ` + filepath.Join(dir, "license-state.json") + `
    public_keys:
      - key_id: self-managed
        path: ` + filepath.Join(dir, "license.pub") + `
    revocation:
      mode: off
      path: ` + filepath.Join(dir, "revocations.json") + `
    instance_fingerprint: host-a
    recheck_interval: 1h
    grace_period_on_validation_error: 24h
    fail_open_for_dev: false
`
	writeMinimalYAMLConfig(t, path, legacy)
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected server.license rejection")
	}
	if !strings.Contains(err.Error(), "server.license") || !strings.Contains(err.Error(), "rejected in 4.0.0") {
		t.Fatalf("rejection missing version guidance: %v", err)
	}
	if strings.Contains(buf.String(), "server.license") {
		t.Fatalf("rejection must not also warn: %s", buf.String())
	}
}

func TestL003LicenseJSONPresentIsNeverOpened(t *testing.T) {
	dir := t.TempDir()
	licensePath := filepath.Join(dir, "license.json")
	if err := os.WriteFile(licensePath, []byte(`{"trap":true}`), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(licensePath, 0o600) })
	path := filepath.Join(dir, "config.yaml")
	legacy := `
  license:
    enabled: true
    path: ` + licensePath + `
    state_path: ` + filepath.Join(dir, "license-state.json") + `
    recheck_interval: 1h
`
	writeMinimalYAMLConfig(t, path, legacy)
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "server.license") {
		t.Fatalf("LoadConfig should reject server.license without opening license.json: %v", err)
	}
	writeMinimalYAMLConfig(t, path, "")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("New opened or failed on unreadable license.json: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
}

func TestL004StrategiesServeWithoutLicenseGate(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(upstream.Close)

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "secret", dir)
	cfg.Models = map[string]ModelGroup{
		"default":  {Strategy: "static", Targets: []Target{{Provider: "mock", Model: "m1"}}},
		"static":   {Strategy: "static", Targets: []Target{{Provider: "mock", Model: "m1"}}},
		"weighted": {Strategy: "weighted", Targets: []Target{{Provider: "mock", Model: "m1", Weight: 1}}},
		"failover": {Strategy: "failover", Targets: []Target{{Provider: "mock", Model: "m1"}}},
		"dynamic": {Strategy: "dynamic_score", Targets: []Target{{Provider: "mock", Model: "m1", Weight: 1}}, RoutingPolicy: RoutingPolicyConfig{DynamicScore: DynamicScoreConfig{
			MinObservations: 20,
		}}},
	}
	sum := sha256.Sum256([]byte(testToken))
	cfg.Callers[0].TokenSHA256 = hex.EncodeToString(sum[:])
	cfg.Callers[0].Allow = []string{"default", "static", "weighted", "failover", "dynamic"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })

	for _, model := range []string{"static", "weighted", "failover", "dynamic"} {
		body := `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("model %s status=%d body=%s", model, rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "license-") {
			t.Fatalf("model %s returned license error: %s", model, rr.Body.String())
		}
	}
}

func TestL005PrivateUpstreamPIIAndScriptHTTPUngated(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(upstream.Close)

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "secret", dir)
	// httptest upstream is a private/loopback URL; previously gated as private_upstreams.
	cfg.Models["pii-group"] = ModelGroup{
		Strategy:  "static",
		Targets:   []Target{{Provider: "mock", Model: "mock-model"}},
		PIIFilter: testPIIFilterConfig("redact_only"),
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "pii-group")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })

	body := `{"model":"pii-group","messages":[{"role":"user","content":"email me at a@b.co"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "license-") {
		t.Fatalf("license error returned: %s", rr.Body.String())
	}
}

func TestL007ReadyAdminAndVersionHaveNoLicenseFields(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	svc := newTestService(t, upstream.URL, "secret")
	t.Cleanup(func() { svc.Close() })

	for _, path := range []string{"/readyz", "/version"} {
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, rr.Code, rr.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("%s json: %v body=%s", path, err, rr.Body.String())
		}
		for key := range payload {
			if strings.Contains(strings.ToLower(key), "license") {
				t.Fatalf("%s still exposes %q", path, key)
			}
		}
	}
}

func TestL008NoLicenseErrorCodesReachable(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(upstream.Close)
	svc := newTestService(t, upstream.URL, "secret")
	t.Cleanup(func() { svc.Close() })

	requests := []*http.Request{
		httptest.NewRequest(http.MethodGet, "/readyz", nil),
		httptest.NewRequest(http.MethodGet, "/metrics", nil),
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"missing","messages":[{"role":"user","content":"hi"}]}`)),
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`)),
		httptest.NewRequest(http.MethodGet, "/admin/license/status", nil),
	}
	requests[2].Header.Set("Authorization", "Bearer "+testToken)
	requests[3].Header.Set("Authorization", "Bearer "+testToken)

	for _, req := range requests {
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if strings.Contains(rr.Body.String(), `"license-`) || strings.Contains(rr.Body.String(), "license-missing") || strings.Contains(rr.Body.String(), "license-feature-forbidden") {
			t.Fatalf("%s %s returned license error: %s", req.Method, req.URL.Path, rr.Body.String())
		}
	}
}

func writeMinimalYAMLConfig(t *testing.T, path, serverExtra string) {
	t.Helper()
	sum := sha256.Sum256([]byte(testToken))
	content := `server:
  listen: ":0"
  identifiers:
    mode: passthrough
  cache:
    enabled: false
  usage_db:
    enable: false
    migration_policy: auto-safe
  logging:
    path: ` + filepath.Join(filepath.Dir(path), "requests.jsonl") + `
` + serverExtra + `
state_path: ` + filepath.Join(filepath.Dir(path), "state.json") + `
providers:
  mock:
    base_url: http://127.0.0.1:9/v1
    dialect: openai-chat
    api_key: secret
    api_key_env: MOCK_API_KEY
    key_id: mock-key
models:
  default:
    strategy: static
    targets:
      - provider: mock
        model: mock-model
callers:
  - id: alice
    token_sha256: ` + hex.EncodeToString(sum[:]) + `
    allow: [default]
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

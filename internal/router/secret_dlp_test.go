// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func fakeGitHubToken() string  { return "ghp_" + strings.Repeat("1", 36) }
func fakeAWSAccessKey() string { return "AKIA" + strings.Repeat("1", 16) }
func fakePrivateKeyBlock() string {
	return "-----BEGIN " + "PRIVATE KEY-----\nmaterial\n-----END " + "PRIVATE KEY-----"
}

func TestSecretDLPRequestAndUnaryResponseE2E(t *testing.T) {
	key := fakeGitHubToken()
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		messages := body["messages"].([]any)
		content := messages[0].(map[string]any)["content"].(string)
		if strings.Contains(content, key) || !strings.Contains(content, "[SECRET_GITHUB_1]") {
			t.Errorf("upstream content did not redact credential: %q", content)
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "secret-dlp", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "returned " + key}}}, "usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}})
	}))
	defer upstream.Close()

	workDir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", workDir)
	group := cfg.Models["default"]
	group.PIIFilter.Enabled = true
	group.PIIFilter.SecretsEnabled = true
	cfg.Models["default"] = group
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"credential `+key+`"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, calls.Load(), response.Body.String())
	}
	if strings.Contains(response.Body.String(), key) || !strings.Contains(response.Body.String(), "[SECRET_GITHUB_1]") {
		t.Fatalf("caller response did not redact credential: %s", response.Body.String())
	}
	logData, err := os.ReadFile(cfg.Server.Logging.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logData), key) {
		t.Fatal("request log contains detected credential")
	}
}

func FuzzSecretMatcher(f *testing.F) {
	f.Add(fakeAWSAccessKey())
	f.Add(fakeGitHubToken())
	f.Add("benign ordinary content")
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 1<<20 {
			t.Skip()
		}
		state := &piiRedactionState{rules: rules, limit: 200, result: &piiFilterResult{RuleCounts: map[string]int{}}, placeholderFor: map[string]string{}}
		output := state.redact(input)
		if len(output) > len(input)+200*64 {
			t.Fatalf("matcher output grew without bound: %d -> %d", len(input), len(output))
		}
	})
}

func BenchmarkSecretMatcher(b *testing.B) {
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		b.Fatal(err)
	}
	input := strings.Repeat("ordinary request text with no credentials. ", 64)
	b.SetBytes(int64(len(input)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		state := &piiRedactionState{rules: rules, limit: 200, result: &piiFilterResult{RuleCounts: map[string]int{}}, placeholderFor: map[string]string{}}
		_ = state.redact(input)
	}
}

func TestSecretDLPFailClosedBeforeUpstream(t *testing.T) {
	key := fakeAWSAccessKey()
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	group := cfg.Models["default"]
	group.PIIFilter.Enabled = true
	group.PIIFilter.SecretsEnabled = true
	group.PIIFilter.Mode = "fail_on_match"
	cfg.Models["default"] = group
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"`+key+`"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "pii-filter-blocked") || calls.Load() != 0 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, calls.Load(), response.Body.String())
	}
}

func TestSecretDLPFailClosedOnUnaryResponse(t *testing.T) {
	key := fakeGitHubToken()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"id": "response-secret", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": key}}}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	group := cfg.Models["default"]
	group.PIIFilter.Enabled = true
	group.PIIFilter.SecretsEnabled = true
	group.PIIFilter.Mode = "fail_on_match"
	cfg.Models["default"] = group
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), key) || !strings.Contains(response.Body.String(), "pii-filter-blocked") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSecretDLPStreamFailsClosedUntilInspectionGate(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	group := cfg.Models["default"]
	group.PIIFilter.Enabled = true
	group.PIIFilter.SecretsEnabled = true
	cfg.Models["default"] = group
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "secret-dlp-stream-inspection-required") || calls.Load() != 0 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, calls.Load(), response.Body.String())
	}
}

func TestSecretDLPBuiltinsAndBenign(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"aws", fakeAWSAccessKey(), "[SECRET_AWS_1]"},
		{"github", fakeGitHubToken(), "[SECRET_GITHUB_1]"},
		{"private_key", fakePrivateKeyBlock(), "[SECRET_PRIVATE_KEY_1]"},
		{"benign", "AKIA is an ordinary word and examples are not credentials", "AKIA is an ordinary word and examples are not credentials"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := PIIFilterConfig{Enabled: true, SecretsEnabled: true, Mode: "redact_only"}
			req := &IRRequest{Messages: []IRMessage{{Role: "user", Content: tc.input}}}
			if _, err := applyPIIFilter(req, cfg); err != nil {
				t.Fatal(err)
			}
			if req.Messages[0].Content != tc.want {
				t.Fatalf("got %q want %q", req.Messages[0].Content, tc.want)
			}
		})
	}
}

func TestSecretDLPCaptureAndConfigValidation(t *testing.T) {
	token := fakeGitHubToken()
	text, count := redactBuiltInSecrets(`{"prompt":"` + token + `"}`)
	if count != 1 || strings.Contains(text, token) || !strings.Contains(text, "[SECRET_GITHUB_1]") {
		t.Fatalf("capture redaction count=%d text=%s", count, text)
	}
	if err := validatePIIFilter("test", PIIFilterConfig{SecretsEnabled: true}); err == nil {
		t.Fatal("secrets_enabled without pii_filter.enabled unexpectedly passed")
	}
	if err := validatePIIFilter("test", PIIFilterConfig{Enabled: true, SecretsEnabled: true, Mode: "redact_only"}); err != nil {
		t.Fatalf("built-in-only filter rejected: %v", err)
	}
}

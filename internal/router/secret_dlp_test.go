// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metrum-ai/router/internal/stream"
)

func fakeGitHubToken() string {
	// Detector-shaped but unmistakably synthetic; never use a provider-issued value.
	return "ghp_" + strings.Repeat("fake", 9)
}
func fakeAWSAccessKey() string { return "AKIA" + strings.Repeat("1", 16) }
func fakeJWTToken() string {
	return "eyJhbGciOiJIUzI1NiJ9." + "eyJzdWIiOiIxMjM0NTY3ODkwIn0." + "signaturevalue123456"
}
func fakeLongJWTToken() string {
	return "eyJ" + strings.Repeat("h", 1200) + "." + strings.Repeat("p", 1300) + "." + strings.Repeat("s", 1400)
}
func fakeCredentialAssignment() string {
	return "api_key=" + strings.Repeat("a", 16) + "Q7r9W2x4"
}
func fakePasswordAssignment() string {
	return "password=" + strings.Repeat("p", 24) + "Q7r9W2x4"
}
func fakePrivateKeyBlock() string {
	return "-----BEGIN " + "PRIVATE KEY-----\nmaterial\n-----END " + "PRIVATE KEY-----"
}

func TestSecretDLPRequestAndUnaryResponseE2E(t *testing.T) {
	key := fakeGitHubToken()
	jwt := fakeLongJWTToken()
	toolResultKey := fakeAWSAccessKey()
	toolArgumentCredential := strings.Repeat("c", 20) + "Q7r9W2x4"
	objectToolArgumentCredential := strings.Repeat("z", 20) + "Q7r9W2x4"
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		serialized, _ := json.Marshal(body["messages"])
		if strings.Contains(string(serialized), key) || strings.Contains(string(serialized), jwt) || strings.Contains(string(serialized), toolResultKey) || strings.Contains(string(serialized), toolArgumentCredential) || strings.Contains(string(serialized), objectToolArgumentCredential) ||
			!strings.Contains(string(serialized), "[SECRET_GITHUB_1]") || !strings.Contains(string(serialized), "[SECRET_JWT_1]") || !strings.Contains(string(serialized), "[SECRET_AWS_1]") || !strings.Contains(string(serialized), "[SECRET_CREDENTIAL_1]") {
			t.Error("upstream prompt, tool argument, or tool result did not redact credentials")
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "secret-dlp", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "returned " + key + " " + jwt}}}, "usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}})
	}))
	defer upstream.Close()

	workDir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", workDir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(workDir, "usage.sqlite"))
	group := cfg.Models["default"]
	group.PIIFilter.Enabled = true
	group.PIIFilter.SecretsEnabled = true
	cfg.Models["default"] = group
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	const promptPrefix = "private-prompt-marker-before-"
	const promptSuffix = "-private-prompt-marker-after"
	const toolResultPrefix = "private-tool-result-marker-before-"
	const toolResultSuffix = "-private-tool-result-marker-after"
	toolArgumentsJSON, err := json.Marshal(map[string]string{"api_key": toolArgumentCredential})
	if err != nil {
		t.Fatal(err)
	}
	toolArgumentsField, err := json.Marshal(string(toolArgumentsJSON))
	if err != nil {
		t.Fatal(err)
	}
	body := `{"model":"default","messages":[{"role":"user","content":"` + promptPrefix + key + " " + jwt + promptSuffix + `"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_secret","arguments":` + string(toolArgumentsField) + `}},{"id":"call_2","type":"function","function":{"name":"read_object","arguments":{"api_key":"` + objectToolArgumentCredential + `"}}}]},{"role":"tool","tool_call_id":"call_1","content":"` + toolResultPrefix + toolResultKey + toolResultSuffix + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, calls.Load(), response.Body.String())
	}
	if strings.Contains(response.Body.String(), key) || strings.Contains(response.Body.String(), jwt) || !strings.Contains(response.Body.String(), "[SECRET_GITHUB_1]") || !strings.Contains(response.Body.String(), "[SECRET_JWT_2]") {
		t.Fatalf("caller response did not redact credential: %s", response.Body.String())
	}
	logData, err := os.ReadFile(cfg.Server.Logging.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range []string{key, jwt, toolResultKey, toolArgumentCredential, objectToolArgumentCredential, promptPrefix, promptSuffix, toolResultPrefix, toolResultSuffix} {
		if strings.Contains(string(logData), sensitive) {
			t.Fatalf("request log contains sensitive request data %q", sensitive)
		}
	}
	var rows []usageRecord
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := svc.usage.db.Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(rows) == 0 {
		t.Fatal("usage database did not record the request")
	}
	serializedRows, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range []string{key, jwt, toolResultKey, toolArgumentCredential, objectToolArgumentCredential, promptPrefix, promptSuffix, toolResultPrefix, toolResultSuffix} {
		if strings.Contains(string(serializedRows), sensitive) {
			t.Fatalf("usage database rows contain sensitive request data %q", sensitive)
		}
	}
}

func TestSecretDLPToolArgumentJSONDecodesEscapedSecrets(t *testing.T) {
	openRouterSecret := "sk-or-v1-" + strings.Repeat("a", 64)
	bearer := "Authorization: Bearer " + strings.Repeat("b", 16) + "/" + strings.Repeat("c", 16)
	credential := strings.Repeat("c", 20) + "Q7r9W2x4"
	cases := []struct {
		name        string
		input       string
		secret      string
		placeholder string
	}{
		{
			name:        "object key and value",
			input:       `{"api_key":"` + credential + `"}`,
			secret:      credential,
			placeholder: "[SECRET_CREDENTIAL_1]",
		},
		{
			name:        "unicode escaped hyphen",
			input:       `{"opaque":"sk-or-v1\u002d` + openRouterSecret[len("sk-or-v1-"):] + `"}`,
			secret:      openRouterSecret,
			placeholder: "[SECRET_PROVIDER_1]",
		},
		{
			name:        "escaped slash",
			input:       `{"header":"Authorization: Bearer ` + strings.ReplaceAll(strings.TrimPrefix(bearer, "Authorization: Bearer "), "/", `\/`) + `"}`,
			secret:      bearer,
			placeholder: "[SECRET_BEARER_1]",
		},
		{
			name:        "secret in object key",
			input:       `{"` + fakeGitHubToken() + `":"ordinary"}`,
			secret:      fakeGitHubToken(),
			placeholder: "[SECRET_GITHUB_1]",
		},
		{
			name:        "authorization object key",
			input:       `{"Authorization":"Bearer ` + strings.Repeat("b", 20) + `"}`,
			secret:      "Bearer " + strings.Repeat("b", 20),
			placeholder: "[SECRET_BEARER_1]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rules, err := compilePIIRules(builtinSecretRules())
			if err != nil {
				t.Fatal(err)
			}
			state := &piiRedactionState{rules: rules, limit: 200, result: &piiFilterResult{RuleCounts: map[string]int{}}, placeholderFor: map[string]string{}}
			redacted, changed, err := redactToolArgumentJSON(tc.input, state)
			if err != nil || !changed || strings.Contains(redacted, tc.secret) || !strings.Contains(redacted, tc.placeholder) {
				t.Fatalf("secret argument case %q changed=%t err=%v", tc.name, changed, err)
			}
			var decoded any
			if err := json.Unmarshal([]byte(redacted), &decoded); err != nil {
				t.Fatalf("redacted arguments are invalid JSON: %v", err)
			}
		})
	}
}

func TestSecretDLPMalformedToolArgumentsRedactNormalizedCopy(t *testing.T) {
	credential := strings.Repeat("h", 20) + "Q7r9W2x4"
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if strings.Contains(string(body), credential) || !strings.Contains(string(body), "[SECRET_CREDENTIAL_1]") {
			t.Error("truncated argument secret was not redacted before upstream dispatch")
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "malformed-args"})
	}))
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
	truncated := `{"api_key":"` + credential
	args, err := json.Marshal(truncated)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"model":"default","messages":[{"role":"assistant","tool_calls":[{"type":"function","function":{"name":"read","arguments":` + string(args) + `}}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("malformed redact-mode arguments status=%d calls=%d", response.Code, calls.Load())
	}
}

func TestSecretDLPMalformedAuthorizationToolArguments(t *testing.T) {
	token := strings.Repeat("j", 24) + "/" + strings.Repeat("k", 24)
	for _, tc := range []struct {
		name string
		args string
	}{
		{name: "truncated", args: `{"Authorization":"Bearer ` + token},
		{name: "trailing data", args: `{"Authorization":"Bearer ` + token + `"} trailing`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if strings.Contains(string(body), token) || !strings.Contains(string(body), "[SECRET_BEARER_1]") {
					t.Error("malformed Authorization arguments were not redacted before upstream dispatch")
				}
				writeJSON(w, http.StatusOK, map[string]any{"id": "malformed-header"})
			}))
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
			args, err := json.Marshal(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			body := `{"model":"default","messages":[{"role":"assistant","tool_calls":[{"type":"function","function":{"name":"read","arguments":` + string(args) + `}}]}]}`
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			response := httptest.NewRecorder()
			svc.Handler().ServeHTTP(response, req)
			if response.Code != http.StatusOK || calls.Load() != 1 {
				t.Fatalf("malformed Authorization arguments status=%d calls=%d", response.Code, calls.Load())
			}
		})
	}
}

func TestSecretDLPMalformedMixedCredentialAndAuthorizationArguments(t *testing.T) {
	apiCredential := strings.Repeat("q", 20) + "Q7r9W2x4"
	bearer := strings.Repeat("m", 24) + "/" + strings.Repeat("n", 24)
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if strings.Contains(string(body), apiCredential) || strings.Contains(string(body), bearer) ||
			!strings.Contains(string(body), "[SECRET_CREDENTIAL_1]") || !strings.Contains(string(body), "[SECRET_BEARER_1]") {
			t.Error("mixed malformed tool arguments were not fully redacted before upstream dispatch")
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "mixed-malformed-args"})
	}))
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
	truncated := `{"api_key":"` + apiCredential + `","Authorization":"Bearer ` + bearer
	args, err := json.Marshal(truncated)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"model":"default","messages":[{"role":"assistant","tool_calls":[{"type":"function","function":{"name":"read","arguments":` + string(args) + `}}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("mixed malformed tool arguments status=%d calls=%d", response.Code, calls.Load())
	}
}

func TestSecretDLPResponsesSnapshotRedactsFunctionCallArguments(t *testing.T) {
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		t.Fatal(err)
	}
	dlp := &secretStreamDLP{state: &piiRedactionState{rules: rules, limit: 200, result: &piiFilterResult{RuleCounts: map[string]int{}}, placeholderFor: map[string]string{}}}
	credential := strings.Repeat("c", 20) + "Q7r9W2x4"
	customInputCredential := strings.Repeat("d", 20) + "Q7r9W2x4"
	arguments, err := json.Marshal(map[string]string{"api_key": credential})
	if err != nil {
		t.Fatal(err)
	}
	eventData, err := json.Marshal(map[string]any{
		"type": "response.completed",
		"response": map[string]any{"output": []any{
			map[string]any{
				"type":      "function_call",
				"name":      "read_secret",
				"arguments": string(arguments),
			},
			map[string]any{
				"type":  "custom_tool_call",
				"name":  "run",
				"input": map[string]string{"api_key": customInputCredential},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := dlp.filterEvent(stream.Event{Type: "response.completed", Data: eventData})
	if err != nil || len(out) != 1 {
		t.Fatalf("snapshot events=%d err=%v", len(out), err)
	}
	if strings.Contains(string(out[0].Data), credential) || strings.Contains(string(out[0].Data), customInputCredential) || !strings.Contains(string(out[0].Data), "[SECRET_CREDENTIAL_1]") || !strings.Contains(string(out[0].Data), "[SECRET_CREDENTIAL_2]") {
		t.Fatalf("Responses function-call snapshot leaked or failed to redact (event bytes=%d)", len(out[0].Data))
	}
}

func TestSecretDLPMalformedToolArgumentsFailOnMatch(t *testing.T) {
	credential := strings.Repeat("a", 20) + "Q7r9W2x4"
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"id": "must-not-run"})
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
	truncated := `{"api_key":"` + credential
	args, err := json.Marshal(truncated)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"model":"default","messages":[{"role":"assistant","tool_calls":[{"type":"function","function":{"name":"read","arguments":` + string(args) + `}}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "pii-filter-blocked") || calls.Load() != 0 {
		t.Fatalf("malformed secret arguments status=%d calls=%d", response.Code, calls.Load())
	}
}

func TestSecretDLPUnaryChatToolCallResponse(t *testing.T) {
	credential := strings.Repeat("d", 20) + "Q7r9W2x4"
	arguments, err := json.Marshal(map[string]string{"api_key": credential})
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"id": "chat-tool-response", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"type": "function", "function": map[string]any{"name": "read", "arguments": string(arguments)}}}}}}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
	}))
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
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), credential) || !strings.Contains(response.Body.String(), "[SECRET_CREDENTIAL_1]") {
		t.Fatalf("unary tool-call response status=%d leaked=%t", response.Code, strings.Contains(response.Body.String(), credential))
	}
}

func TestSecretDLPResponsesInputFunctionCallArguments(t *testing.T) {
	credential := strings.Repeat("e", 20) + "Q7r9W2x4"
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if strings.Contains(string(body), credential) || !strings.Contains(string(body), "[SECRET_CREDENTIAL_1]") {
			t.Error("Responses input function-call arguments were not redacted before upstream dispatch")
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "responses-tool-input", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "ok"}}}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	group := cfg.Models["default"]
	group.PIIFilter.Enabled = true
	group.PIIFilter.SecretsEnabled = true
	responsesInput := true
	group.PIIFilter.ApplyTo.ResponsesInput = &responsesInput
	group.Targets[0].ToolSupport = ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}
	group.Targets[0].ResponsesToChat = ResponsesToChatBridge{Enabled: true, Text: true, FunctionTools: true, ToolChoice: true}
	cfg.Models["default"] = group
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	arguments, _ := json.Marshal(map[string]string{"api_key": credential})
	body, _ := json.Marshal(map[string]any{"model": "default", "input": []any{map[string]any{"type": "function_call", "call_id": "call_1", "name": "read", "arguments": string(arguments)}}, "max_output_tokens": 64, "tools": []any{map[string]any{"type": "function", "name": "read", "parameters": map[string]any{"type": "object"}}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("Responses input function-call status=%d calls=%d", response.Code, calls.Load())
	}
}

func TestSecretDLPAnthropicToolUseInputRequestAndResponse(t *testing.T) {
	requestSecret := strings.Repeat("f", 20) + "Q7r9W2x4"
	responseSecret := strings.Repeat("g", 20) + "Q7r9W2x4"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if strings.Contains(string(body), requestSecret) || !strings.Contains(string(body), "[SECRET_CREDENTIAL_1]") {
			t.Error("Anthropic tool_use.input was not redacted before upstream dispatch")
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "msg-tool-use", "type": "message", "role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "toolu_1", "name": "read", "input": map[string]string{"api_key": responseSecret}}}, "stop_reason": "tool_use", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}})
	}))
	defer upstream.Close()
	svc := nativeStreamTestService(t, upstream.URL, "anthropic", []Target{anthropicToolTarget()}, func(cfg *Config) {
		group := cfg.Models["native-stream"]
		group.PIIFilter.Enabled = true
		group.PIIFilter.SecretsEnabled = true
		messagesScope := true
		responsesInputScope := false
		group.PIIFilter.ApplyTo.Messages = &messagesScope
		group.PIIFilter.ApplyTo.ResponsesInput = &responsesInputScope
		cfg.Models["native-stream"] = group
	})
	input := `{"model":"native-stream","max_tokens":32,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"read","input":{"api_key":"` + requestSecret + `"}}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(input))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), responseSecret) || !strings.Contains(response.Body.String(), "[SECRET_CREDENTIAL_") {
		t.Fatalf("Anthropic tool_use input response status=%d leaked=%t placeholder=%t", response.Code, strings.Contains(response.Body.String(), responseSecret), strings.Contains(response.Body.String(), "[SECRET_CREDENTIAL_"))
	}
}

func TestSecretDLPUnaryResponseReplacementLimitFailsClosed(t *testing.T) {
	const replacementLimit = 200
	responseSecret := "ghp_" + strings.Repeat("z", 36)
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"id": "limit-response", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": responseSecret}}}, "usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}})
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	group := cfg.Models["default"]
	group.PIIFilter.Enabled = true
	group.PIIFilter.SecretsEnabled = true
	group.PIIFilter.MaxReplacementsPerRequest = replacementLimit
	cfg.Models["default"] = group
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	var prompt strings.Builder
	for i := 0; i < replacementLimit; i++ {
		if i > 0 {
			prompt.WriteByte(' ')
		}
		prompt.WriteString(fmt.Sprintf("ghp_%036x", i+1))
	}
	body, err := json.Marshal(map[string]any{"model": "default", "messages": []any{map[string]any{"role": "user", "content": prompt.String()}}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusBadGateway || calls.Load() != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, calls.Load(), response.Body.String())
	}
	if strings.Contains(response.Body.String(), responseSecret) || !strings.Contains(response.Body.String(), "pii-filter-blocked") {
		t.Fatalf("replacement-limit response leaked or used wrong error: %s", response.Body.String())
	}
}

func TestSecretDLPResponsesClientRedactsPromptAndToolResult(t *testing.T) {
	githubToken := fakeGitHubToken()
	awsToken := fakeAWSAccessKey()
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		messages, _ := json.Marshal(body["messages"])
		for _, secret := range []string{githubToken, awsToken} {
			if strings.Contains(string(messages), secret) {
				t.Errorf("Responses bridge forwarded a secret: %s", messages)
			}
		}
		if !strings.Contains(string(messages), "[SECRET_GITHUB_1]") || !strings.Contains(string(messages), "[SECRET_AWS_1]") {
			t.Errorf("Responses bridge did not redact prompt and tool result: %s", messages)
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "responses-secret-dlp", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "returned " + githubToken}}}, "usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}})
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	group := cfg.Models["default"]
	group.PIIFilter.Enabled = true
	group.PIIFilter.SecretsEnabled = true
	group.Targets[0].ToolSupport = ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}
	group.Targets[0].ResponsesToChat = ResponsesToChatBridge{Enabled: true, Text: true, FunctionTools: true, ToolChoice: true}
	cfg.Models["default"] = group
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	body := `{"model":"default","input":[{"role":"user","content":"credential ` + githubToken + `"},{"type":"function_call","call_id":"call_1","name":"read_secret","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"tool result ` + awsToken + `"}],"max_output_tokens":128,"store":false,"tools":[{"type":"function","name":"read_secret","parameters":{"type":"object"}}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, calls.Load(), response.Body.String())
	}
	if strings.Contains(response.Body.String(), githubToken) || !strings.Contains(response.Body.String(), "[SECRET_GITHUB_1]") {
		t.Fatalf("Responses client received an unredacted completion: %s", response.Body.String())
	}
}

func FuzzSecretMatcher(f *testing.F) {
	f.Add(fakeAWSAccessKey())
	f.Add(fakeGitHubToken())
	f.Add("benign ordinary content")
	f.Add(strings.Repeat("prefix ", 900) + fakeGitHubToken() + strings.Repeat(" suffix", 900))
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
		// Exercise the real rolling stream window at adversarially small splits,
		// including a credential after enough text to force multiple releases.
		secret := fakeGitHubToken()
		for _, split := range []int{4, 7, 128} {
			if split >= len(secret) {
				split = len(secret) / 2
			}
			streamState := &piiRedactionState{rules: rules, limit: 200, result: &piiFilterResult{RuleCounts: map[string]int{}}, placeholderFor: map[string]string{}}
			pending := ""
			var released strings.Builder
			for _, fragment := range []string{strings.Repeat("bounded prefix ", 500), secret[:split], secret[split:] + " " + input + strings.Repeat(" ordinary suffix", 500)} {
				safe, next, streamErr := redactSecretStreamChunk(streamState, pending, fragment, false)
				if streamErr != nil {
					break // bounded fail-closed is an allowed outcome
				}
				if strings.Contains(safe, secret) {
					t.Fatal("rolling stream window released a split credential")
				}
				released.WriteString(safe)
				pending = next
			}
			if safe, _, streamErr := redactSecretStreamChunk(streamState, pending, "", true); streamErr == nil {
				released.WriteString(safe)
				if strings.Contains(released.String(), secret) {
					t.Fatal("rolling stream window retained a credential")
				}
			}
		}
	})
}

func differentialSecretSamples() []string {
	return []string{
		fakeAWSAccessKey(), fakeGitHubToken(),
		"xoxb-" + strings.Repeat("s", 24),
		"sk_live_" + strings.Repeat("t", 24),
		"sk-proj-" + strings.Repeat("u", 32),
		"sk-" + strings.Repeat("v", 40),
		"gsk_" + strings.Repeat("w", 40),
		"sk-or-v1-" + strings.Repeat("a", 64),
		"sk-ant-api03-" + strings.Repeat("b", 32),
		"AIza" + strings.Repeat("C", 35),
		"rtr_metrum_" + strings.Repeat("d", 20),
		fakeLongJWTToken(),
		"Authorization: Bearer " + strings.Repeat("e", 32),
		fakeCredentialAssignment(), fakePasswordAssignment(), fakePrivateKeyBlock(),
	}
}

func differentialSecretInput(seed []byte) (string, []string, func(int) int) {
	var rng uint64 = 1469598103934665603
	for _, b := range seed {
		rng = (rng ^ uint64(b)) * 1099511628211
	}
	if rng == 0 {
		rng = 1
	}
	next := func() uint64 {
		rng ^= rng << 13
		rng ^= rng >> 7
		rng ^= rng << 17
		return rng
	}
	choose := func(n int) int { return int(next() % uint64(n)) }
	text := func(n int) string {
		const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 ,.;:/_"
		var out strings.Builder
		for i := 0; i < n; i++ {
			out.WriteByte(alphabet[choose(len(alphabet))])
		}
		return out.String()
	}
	secrets := differentialSecretSamples()
	// Vary order and surrounding text while including every built-in family in
	// each generated case, so each iteration exercises the complete detector set.
	shift := choose(len(secrets))
	secrets = append(secrets[shift:], secrets[:shift]...)
	var input strings.Builder
	for _, secret := range secrets {
		input.WriteString(text(choose(40)))
		input.WriteByte('#')
		input.WriteString(secret)
		input.WriteByte('#')
		input.WriteString(text(choose(40)))
	}
	return input.String(), secrets, choose
}

func assertStreamMatchesUnarySecretBoundary(t *testing.T, seed []byte) {
	t.Helper()
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		t.Fatal(err)
	}
	newState := func() *piiRedactionState {
		return &piiRedactionState{rules: rules, limit: 200, result: &piiFilterResult{RuleCounts: map[string]int{}}, placeholderFor: map[string]string{}}
	}
	input, secrets, choose := differentialSecretInput(seed)
	unary := newState().redact(input)
	for index, secret := range secrets {
		if strings.Contains(unary, secret) {
			t.Fatalf("unary redactor did not redact generated secret index %d", index)
		}
	}

	state := newState()
	var pending string
	var released strings.Builder
	failedClosed := false
	for start := 0; start < len(input); {
		end := min(start+1+choose(64), len(input))
		safe, next, streamErr := redactSecretStreamChunk(state, pending, input[start:end], false)
		if streamErr != nil {
			// A bounded fail-closed stream is safe if all previously released
			// bytes satisfy the differential property.
			failedClosed = true
			break
		}
		released.WriteString(safe)
		pending = next
		start = end
	}
	if !failedClosed {
		if safe, _, streamErr := redactSecretStreamChunk(state, pending, "", true); streamErr == nil {
			released.WriteString(safe)
		}
	}
	output := released.String()
	for index, secret := range secrets {
		if strings.Contains(output, secret) {
			t.Fatalf("stream released generated secret index %d detected by unary redaction", index)
		}
	}
}

func TestSecretDLPStreamMatchesUnaryQuickProperty(t *testing.T) {
	for i := 0; i < 8; i++ {
		assertStreamMatchesUnarySecretBoundary(t, []byte{byte(i), 0x51, byte(i * 31)})
	}
}

func FuzzSecretStreamMatchesUnary(f *testing.F) {
	f.Add([]byte("quick seed"))
	f.Add([]byte{0, 1, 2, 3, 4, 5})
	f.Add([]byte(strings.Repeat("nightly differential fuzz seed ", 8)))
	f.Fuzz(func(t *testing.T, seed []byte) {
		if len(seed) > 4096 {
			t.Skip()
		}
		assertStreamMatchesUnarySecretBoundary(t, seed)
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

func BenchmarkSecretStreamWindow(b *testing.B) {
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		b.Fatal(err)
	}
	input := strings.Repeat("ordinary streamed response text ", 1024) + fakeGitHubToken() + strings.Repeat(" tail text", 128)
	totalBytes := len(input)
	fragments := make([]string, 0, len(input)/128+1)
	for len(input) > 128 {
		fragments = append(fragments, input[:128])
		input = input[128:]
	}
	fragments = append(fragments, input)
	b.SetBytes(int64(totalBytes))
	result := &piiFilterResult{RuleCounts: map[string]int{}}
	state := &piiRedactionState{rules: rules, limit: 200, result: result, placeholderFor: map[string]string{}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		state.result = &piiFilterResult{RuleCounts: map[string]int{}}
		state.placeholderFor = map[string]string{}
		var pending string
		for _, fragment := range fragments {
			_, next, err := redactSecretStreamChunk(state, pending, fragment, false)
			if err != nil {
				b.Fatal(err)
			}
			pending = next
		}
		if _, _, err := redactSecretStreamChunk(state, pending, "", true); err != nil {
			b.Fatal(err)
		}
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
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
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
	flushUsageForTest(t, svc)
	var usage []usageRecord
	if err := svc.usage.db.Find(&usage).Error; err != nil || len(usage) != 1 || usage[0].InputTokens != 1 || usage[0].OutputTokens != 1 {
		t.Fatalf("blocked response did not settle upstream token usage: rows=%#v err=%v", usage, err)
	}
	var settlements int64
	if err := svc.usage.db.Model(&requestTraceEventRecord{}).Where("request_id = ? AND event = ?", usage[0].RequestID, "quota_settlement").Count(&settlements).Error; err != nil {
		t.Fatal(err)
	}
	if settlements != 1 {
		t.Fatalf("blocked response wrote %d quota-settlement events, want exactly one", settlements)
	}
}

func TestSecretDLPResponseScopeAndCustomOnlyCompatibility(t *testing.T) {
	secret := fakeGitHubToken()
	for _, tc := range []struct {
		name          string
		secrets       bool
		custom        bool
		messagesScope *bool
		want          string
	}{
		{name: "custom-only-keeps-existing-response", custom: true, want: "private-marker"},
		{name: "built-in-response-scope-disabled", secrets: true, messagesScope: boolPtr(false), want: secret},
		{name: "built-in-response-scope-enabled", secrets: true, messagesScope: boolPtr(true), want: "[SECRET_GITHUB_1]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			responseText := secret
			if tc.custom {
				responseText = "private-marker"
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusOK, map[string]any{"id": "response-scope", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": responseText}}}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
			}))
			defer upstream.Close()
			cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
			group := cfg.Models["default"]
			group.PIIFilter.Enabled = true
			group.PIIFilter.SecretsEnabled = tc.secrets
			if tc.custom {
				group.PIIFilter.Mode = "fail_on_match"
				group.PIIFilter.Rules = []PIIFilterRule{{Name: "custom", Expression: `private-marker`, PlaceholderPrefix: "CUSTOM"}}
			}
			if tc.messagesScope != nil {
				group.PIIFilter.ApplyTo.Messages = tc.messagesScope
			}
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
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), tc.want) {
				t.Fatalf("status=%d body=%s want substring %q", response.Code, response.Body.String(), tc.want)
			}
		})
	}
}

func TestSecretDLPUpstreamTimeoutAfterRedaction(t *testing.T) {
	key := fakeAWSAccessKey()
	const promptMarker = "private-timeout-prompt-marker"
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		messages := valueAsSlice(body["messages"])
		content := stringValue(messages[0].(map[string]any)["content"])
		if strings.Contains(content, key) || !strings.Contains(content, "[SECRET_AWS_1]") {
			t.Errorf("timed-out upstream received unredacted credential: %q", content)
		}
		time.Sleep(100 * time.Millisecond)
		writeJSON(w, http.StatusOK, map[string]any{"id": "late", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "late"}}}})
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	group := cfg.Models["default"]
	group.AttemptTimeoutMS = 10
	group.PIIFilter.Enabled = true
	group.PIIFilter.SecretsEnabled = true
	cfg.Models["default"] = group
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"`+promptMarker+" "+key+`"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusGatewayTimeout || calls.Load() != 1 || strings.Contains(response.Body.String(), key) {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, calls.Load(), response.Body.String())
	}
	flushUsageForTest(t, svc)
	var attempts []requestAttemptRecord
	if err := svc.usage.db.Find(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || !attempts[0].TimedOut || attempts[0].ErrorClass != "upstream_timeout" {
		t.Fatalf("unexpected timeout attempts: %#v", attempts)
	}
	var rows []usageRecord
	if err := svc.usage.db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	serialized, err := json.Marshal(map[string]any{"attempts": attempts, "usage": rows})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), key) || strings.Contains(string(serialized), promptMarker) {
		t.Fatal("timeout usage records contain secret or prompt text")
	}
	logData, err := os.ReadFile(cfg.Server.Logging.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logData), key) || strings.Contains(string(logData), promptMarker) {
		t.Fatal("timeout request log contains secret or prompt text")
	}
}

func TestSecretDLPStreamHoldsAndRedactsToolArguments(t *testing.T) {
	var calls atomic.Int64
	secret := "sk_live_" + strings.Repeat("a", 20)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		writeEvent := func(value any) {
			encoded, _ := json.Marshal(value)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
		}
		writeEvent(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_secret", "type": "function", "function": map[string]any{"name": "lookup", "arguments": `{"token":"` + secret + `"}`}}}}, "finish_reason": nil}}})
		writeEvent(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}})
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	group := cfg.Models["default"]
	group.PIIFilter.Enabled = true
	group.PIIFilter.SecretsEnabled = true
	group.Strategy = "tool_aware"
	group.ToolRouting.Streaming = ToolRoutingStreamingConfig{HoldUntilValidated: true, HoldTimeoutMS: 1000, HoldMaxBytes: 1 << 20}
	group.Targets[0].ToolSupport = ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}
	cfg.Models["default"] = group
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","stream":true,"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK || calls.Load() != 1 || !strings.Contains(response.Body.String(), "[SECRET_STRIPE_1]") || strings.Contains(response.Body.String(), secret) || !strings.Contains(response.Body.String(), "call_secret") {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, calls.Load(), response.Body.String())
	}
}

func TestSecretDLPRedactsHeldToolCallArguments(t *testing.T) {
	secret := fakeGitHubToken()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{\\\"token\\\":\\\"%s\\\"}\"}}]},\"finish_reason\":null}]}\n\n", secret)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	group := cfg.Models["default"]
	group.PIIFilter.Enabled = true
	group.PIIFilter.SecretsEnabled = true
	group.Strategy = "tool_aware"
	group.Targets[0].ToolSupport = ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}
	group.ToolRouting.Streaming = ToolRoutingStreamingConfig{HoldUntilValidated: true, HoldTimeoutMS: 1000, HoldMaxBytes: 1 << 20}
	cfg.Models["default"] = group
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	body := `{"model":"default","stream":true,"messages":[{"role":"user","content":"look up token"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), secret) || !strings.Contains(response.Body.String(), "[SECRET_GITHUB_1]") {
		t.Fatalf("stream leaked tool argument or missed redaction: %s", response.Body.String())
	}
	if used := svc.lineBufferBudget.Used(); used != 0 {
		t.Fatalf("aggregate budget after DLP inspection=%d, want 0", used)
	}
}

func TestSecretDLPToolCallInspectorBlocksFailOnMatch(t *testing.T) {
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		t.Fatal(err)
	}
	state := &piiRedactionState{
		rules: rules, limit: 20, result: &piiFilterResult{RuleCounts: map[string]int{}},
		placeholderFor: map[string]string{},
	}
	inspector := secretDLPToolCallInspector{dlp: &secretStreamDLP{state: state, mode: "fail_on_match"}}
	result, err := inspector.inspectStreamToolCalls([]IRToolCall{{Index: 0, ID: "call_1", Name: "lookup", Arguments: `{"token":"` + fakeGitHubToken() + `"}`}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != toolCallInspectionBlock {
		t.Fatalf("inspector action=%q, want block", result.Action)
	}
}

func TestSecretDLPToolCallInspectorRedactsEscapedArguments(t *testing.T) {
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		t.Fatal(err)
	}
	state := &piiRedactionState{
		rules: rules, limit: 20, result: &piiFilterResult{RuleCounts: map[string]int{}},
		placeholderFor: map[string]string{},
	}
	inspector := secretDLPToolCallInspector{dlp: &secretStreamDLP{state: state, mode: "redact"}}
	secretBody := strings.Repeat("a", 64)
	arguments := `{"api_key":"sk-or-v1\u002d` + secretBody + `"}`
	result, err := inspector.inspectStreamToolCalls([]IRToolCall{{Index: 0, ID: "call_1", Name: "lookup", Arguments: arguments}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != toolCallInspectionRedact || len(result.Calls) != 1 {
		t.Fatalf("inspector action=%q calls=%d, want redaction of escaped argument", result.Action, len(result.Calls))
	}
	if strings.Contains(result.Calls[0].Arguments, secretBody) || !strings.Contains(result.Calls[0].Arguments, "[SECRET_PROVIDER_1]") {
		t.Fatalf("inspector leaked or missed escaped credential body")
	}
}

func TestSecretDLPRedactsHeldResponsesAndAnthropicToolCalls(t *testing.T) {
	secret := fakeGitHubToken()
	for _, tc := range []struct {
		name, dialect, path string
		frames              []string
		request             string
		target              Target
	}{
		{
			name: "responses", dialect: "openai-responses", path: "/v1/responses",
			frames: []string{
				`event: response.output_item.added` + "\n" + `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":""}}`,
				`event: response.function_call_arguments.delta` + "\n" + `data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"token\":\"` + secret[:len(secret)/2] + `"}`,
				`event: response.function_call_arguments.delta` + "\n" + `data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"` + secret[len(secret)/2:] + `\"}"}`,
				`event: response.output_item.done` + "\n" + `data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{\"token\":\"` + secret + `\"}"}}`,
				`event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{\"token\":\"` + secret + `\"}"}]}}`,
			},
			request: `{"model":"native-stream","stream":true,"input":"look up token","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`,
			target:  Target{Provider: "native", Model: "responses-model", ToolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}}},
		},
		{
			name: "anthropic", dialect: "anthropic", path: "/v1/messages",
			frames: []string{
				`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"anthropic-model","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
				`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_1","name":"lookup","input":{}}}`,
				`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"token\":\"` + secret + `\"}"}}`,
				`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
				`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}`,
				`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
			},
			request: `{"model":"native-stream","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"look up token"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`,
			target:  Target{Provider: "native", Model: "anthropic-model", ToolSupport: ToolSupport{AnthropicMessages: []string{"client_tools"}}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, frame := range tc.frames {
					_, _ = io.WriteString(w, frame+"\n\n")
				}
			}))
			defer upstream.Close()
			groupName := "native-stream"
			svc := nativeStreamTestService(t, upstream.URL, tc.dialect, []Target{tc.target}, func(cfg *Config) {
				group := cfg.Models[groupName]
				group.Strategy = "tool_aware"
				group.ToolRouting = ToolRoutingConfig{Mode: "observe", Streaming: ToolRoutingStreamingConfig{HoldUntilValidated: true, HoldTimeoutMS: 1000, HoldMaxBytes: 1 << 20}}
				group.PIIFilter.Enabled = true
				group.PIIFilter.SecretsEnabled = true
				cfg.Models[groupName] = group
			})
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.request))
			req.Header.Set("Authorization", "Bearer "+testToken)
			response := httptest.NewRecorder()
			svc.Handler().ServeHTTP(response, req)
			body := response.Body.String()
			if response.Code != http.StatusOK || strings.Contains(body, secret) || !strings.Contains(body, "[SECRET_GITHUB_1]") {
				t.Fatalf("status=%d leaked or missed redaction in full SSE body: %s", response.Code, body)
			}
			if tc.name == "anthropic" && strings.Index(body, "event: content_block_delta") > strings.Index(body, "event: message_delta") {
				t.Fatalf("tool input delta came after terminal message_delta: %s", body)
			}
			if used := svc.lineBufferBudget.Used(); used != 0 {
				t.Fatalf("aggregate budget after protocol DLP=%d, want 0", used)
			}
		})
	}
}

func TestSecretDLPResponsesInterleavedTextItemsThroughHandler(t *testing.T) {
	secret := fakeGitHubToken()
	frames := []string{
		`event: response.output_item.added` + "\n" + `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_a","role":"assistant","content":[]}}`,
		`event: response.output_item.added` + "\n" + `data: {"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg_b","role":"assistant","content":[]}}`,
		`event: response.output_text.delta` + "\n" + `data: {"type":"response.output_text.delta","item_id":"msg_a","output_index":0,"content_index":0,"delta":"` + secret[:len(secret)/2] + `"}`,
		`event: response.output_text.delta` + "\n" + `data: {"type":"response.output_text.delta","item_id":"msg_b","output_index":1,"content_index":0,"delta":"unrelated safe output"}`,
		`event: response.output_text.delta` + "\n" + `data: {"type":"response.output_text.delta","item_id":"msg_a_changed","output_index":0,"content_index":0,"delta":"` + secret[len(secret)/2:] + `"}`,
		`event: response.output_text.done` + "\n" + `data: {"type":"response.output_text.done","item_id":"msg_a","output_index":0,"content_index":0,"text":"` + secret + `"}`,
		`event: response.output_text.done` + "\n" + `data: {"type":"response.output_text.done","item_id":"msg_b","output_index":1,"content_index":0,"text":"unrelated safe output"}`,
		`event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","id":"msg_a","content":[{"type":"output_text","text":"` + secret + `"}]},{"type":"message","id":"msg_b","content":[{"type":"output_text","text":"unrelated safe output"}]}]}}`,
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, frame := range frames {
			_, _ = io.WriteString(w, frame+"\n\n")
		}
	}))
	defer upstream.Close()
	target := Target{Provider: "native", Model: "responses-model", ToolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}}}
	svc := nativeStreamTestService(t, upstream.URL, "openai-responses", []Target{target}, func(cfg *Config) {
		group := cfg.Models["native-stream"]
		group.PIIFilter.Enabled = true
		group.PIIFilter.SecretsEnabled = true
		cfg.Models["native-stream"] = group
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"native-stream","stream":true,"input":"hello"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	body := response.Body.String()
	if response.Code != http.StatusOK || strings.Contains(body, secret) || !strings.Contains(body, "[SECRET_GITHUB_1]") {
		t.Fatalf("status=%d leaked or missed interleaved Responses text redaction: %s", response.Code, body)
	}
	rebuilt := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		data, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			continue
		}
		if stringValue(payload["type"]) != "response.output_text.delta" {
			continue
		}
		outputIndex, _ := numberAsInt(payload["output_index"])
		delta, _ := payload["delta"].(string)
		rebuilt[fmt.Sprint(outputIndex)] += delta
	}
	if strings.Contains(rebuilt["0"], secret) || !strings.Contains(rebuilt["0"], "[SECRET_GITHUB_1]") || !strings.Contains(rebuilt["1"], "unrelated safe output") {
		t.Fatalf("per-item delta reconstruction is incorrect: %#v", rebuilt)
	}
}

func TestSecretDLPResponsesAddedTextSnapshotsThroughHandler(t *testing.T) {
	secret := fakeGitHubToken()
	for _, tc := range []struct {
		name   string
		frames []string
	}{
		{name: "output item", frames: []string{`event: response.output_item.added` + "\n" + `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_secret","role":"assistant","content":[{"type":"output_text","text":"` + secret + `"}]}}`}},
		{name: "content part", frames: []string{`event: response.content_part.added` + "\n" + `data: {"type":"response.content_part.added","output_index":0,"item_id":"msg_secret","content_index":0,"part":{"type":"output_text","text":"` + secret + `"}}`}},
		{name: "refusal content part", frames: []string{`event: response.content_part.added` + "\n" + `data: {"type":"response.content_part.added","output_index":0,"item_id":"msg_secret","content_index":0,"part":{"type":"refusal","refusal":"` + secret + `"}}`}},
		{name: "reasoning summary part", frames: []string{`event: response.reasoning_summary_part.added` + "\n" + `data: {"type":"response.reasoning_summary_part.added","output_index":0,"item_id":"reasoning_secret","summary_index":0,"part":{"type":"summary_text","text":"` + secret + `"}}`}},
		{name: "split across added part and delta", frames: []string{
			`event: response.content_part.added` + "\n" + `data: {"type":"response.content_part.added","output_index":0,"item_id":"msg_split","content_index":0,"part":{"type":"output_text","text":"` + secret[:len(secret)/2] + `"}}`,
			`event: response.output_text.delta` + "\n" + `data: {"type":"response.output_text.delta","output_index":0,"item_id":"msg_split","content_index":0,"delta":"` + secret[len(secret)/2:] + `"}`,
			`event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"status":"completed"}}`,
		}},
		{name: "split across reasoning part and delta", frames: []string{
			`event: response.reasoning_summary_part.added` + "\n" + `data: {"type":"response.reasoning_summary_part.added","output_index":0,"item_id":"reasoning_split","summary_index":0,"part":{"type":"summary_text","text":"` + secret[:len(secret)/2] + `"}}`,
			`event: response.reasoning_summary_text.delta` + "\n" + `data: {"type":"response.reasoning_summary_text.delta","output_index":0,"item_id":"reasoning_split","summary_index":0,"delta":"` + secret[len(secret)/2:] + `"}`,
			`event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"status":"completed"}}`,
		}},
	} {
		for _, mode := range []string{"redact_only", "fail_on_match"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				frames := append([]string(nil), tc.frames...)
				if !strings.HasPrefix(tc.name, "split across") {
					frames = append(frames, `event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"status":"completed"}}`)
				}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					for _, frame := range frames {
						_, _ = io.WriteString(w, frame+"\n\n")
					}
				}))
				defer upstream.Close()
				target := Target{Provider: "native", Model: "responses-model", ToolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}}}
				svc := nativeStreamTestService(t, upstream.URL, "openai-responses", []Target{target}, func(cfg *Config) {
					group := cfg.Models["native-stream"]
					group.PIIFilter.Enabled = true
					group.PIIFilter.SecretsEnabled = true
					group.PIIFilter.Mode = mode
					cfg.Models["native-stream"] = group
				})
				defer svc.Close()
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"native-stream","stream":true,"input":"hello"}`))
				req.Header.Set("Authorization", "Bearer "+testToken)
				response := httptest.NewRecorder()
				svc.Handler().ServeHTTP(response, req)
				body := response.Body.String()
				if strings.Contains(body, secret) {
					t.Fatalf("added snapshot leaked secret: %s", body)
				}
				if strings.HasPrefix(tc.name, "split across") && strings.Contains(body, secret[:len(secret)/2]) {
					t.Fatalf("added snapshot released a credential prefix before continuation inspection: %s", body)
				}
				if mode == "redact_only" && (response.Code != http.StatusOK || !strings.Contains(body, "[SECRET_GITHUB_1]")) {
					t.Fatalf("status=%d body=%s", response.Code, body)
				}
				if mode == "fail_on_match" {
					// Added events commit stream headers before rolling inspection can
					// establish whether their held suffix contains a match.
					wantStatus := http.StatusOK
					if response.Code != wantStatus {
						t.Fatalf("status=%d want=%d body=%s", response.Code, wantStatus, body)
					}
					if strings.Contains(body, `"type":"response.completed"`) {
						t.Fatalf("fail_on_match completed a stream after finding a secret: %s", body)
					}
					if strings.HasPrefix(tc.name, "split across") && (strings.Contains(body, `"type":"response.output_text.delta"`) || strings.Contains(body, `"type":"response.reasoning_summary_text.delta"`)) {
						t.Fatalf("fail_on_match released a split credential delta: %s", body)
					}
				}
			})
		}
	}
}

func TestSecretDLPResponsesAddedSnapshotTextIsNotReemitted(t *testing.T) {
	const addedText = "safe-added-prefix"
	frames := []string{
		`event: response.content_part.added` + "\n" + `data: {"type":"response.content_part.added","output_index":0,"item_id":"msg_safe","content_index":0,"part":{"type":"output_text","text":"` + addedText + `"}}`,
		`event: response.output_text.delta` + "\n" + `data: {"type":"response.output_text.delta","output_index":0,"item_id":"msg_safe","content_index":0,"delta":"` + strings.Repeat("x", 600) + `"}`,
		`event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"status":"completed"}}`,
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, frame := range frames {
			_, _ = io.WriteString(w, frame+"\n\n")
		}
	}))
	defer upstream.Close()
	target := Target{Provider: "native", Model: "responses-model", ToolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}}}
	svc := nativeStreamTestService(t, upstream.URL, "openai-responses", []Target{target}, func(cfg *Config) {
		group := cfg.Models["native-stream"]
		group.PIIFilter.Enabled = true
		group.PIIFilter.SecretsEnabled = true
		cfg.Models["native-stream"] = group
	})
	defer svc.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"native-stream","stream":true,"input":"hello"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK || strings.Count(response.Body.String(), addedText) != 1 {
		t.Fatalf("status=%d added text count=%d body=%s", response.Code, strings.Count(response.Body.String(), addedText), response.Body.String())
	}
}

func TestSecretDLPResponsesAddedLongCredentialInspectedAcrossDeltas(t *testing.T) {
	secret := "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	split := len("ghp_") + 30
	frames := []string{
		`event: response.content_part.added` + "\n" + `data: {"type":"response.content_part.added","output_index":0,"item_id":"msg_long_split","content_index":0,"part":{"type":"output_text","text":"` + secret[:split] + `"}}`,
		`event: response.output_text.delta` + "\n" + `data: {"type":"response.output_text.delta","output_index":0,"item_id":"msg_long_split","content_index":0,"delta":"` + secret[split:] + ". " + strings.Repeat("x", 600) + `"}`,
		`event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"status":"completed"}}`,
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, frame := range frames {
			_, _ = io.WriteString(w, frame+"\n\n")
		}
	}))
	defer upstream.Close()
	target := Target{Provider: "native", Model: "responses-model", ToolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}}}
	svc := nativeStreamTestService(t, upstream.URL, "openai-responses", []Target{target}, func(cfg *Config) {
		group := cfg.Models["native-stream"]
		group.PIIFilter.Enabled = true
		group.PIIFilter.SecretsEnabled = true
		group.PIIFilter.Mode = "redact_only"
		cfg.Models["native-stream"] = group
	})
	defer svc.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"native-stream","stream":true,"input":"hello"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	body := response.Body.String()
	if response.Code != http.StatusOK || strings.Contains(body, secret) || strings.Contains(body, secret[split:]) || !strings.Contains(body, "[SECRET_GITHUB_1]") {
		t.Fatalf("long split credential leaked from added snapshot into deltas: status=%d body=%s", response.Code, body)
	}
}

func TestSecretDLPResponsesAddedSnapshotShortContinuationFlushesOnce(t *testing.T) {
	const addedText = "safe-added-prefix"
	const continuation = "-tail"
	for _, terminal := range []string{"completed", "failed", "incomplete"} {
		t.Run(terminal, func(t *testing.T) {
			frames := []string{
				`event: response.content_part.added` + "\n" + `data: {"type":"response.content_part.added","sequence_number":0,"output_index":0,"item_id":"msg_safe","content_index":0,"part":{"type":"output_text","text":"` + addedText + `"}}`,
				`event: response.output_text.delta` + "\n" + `data: {"type":"response.output_text.delta","sequence_number":1,"output_index":0,"item_id":"msg_safe","content_index":0,"delta":"` + continuation + `"}`,
				`event: response.` + terminal + "\n" + `data: {"type":"response.` + terminal + `","sequence_number":2,"response":{"status":"` + terminal + `"}}`,
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, frame := range frames {
					_, _ = io.WriteString(w, frame+"\n\n")
				}
			}))
			defer upstream.Close()
			target := Target{Provider: "native", Model: "responses-model", ToolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}}}
			svc := nativeStreamTestService(t, upstream.URL, "openai-responses", []Target{target}, func(cfg *Config) {
				group := cfg.Models["native-stream"]
				group.PIIFilter.Enabled = true
				group.PIIFilter.SecretsEnabled = true
				cfg.Models["native-stream"] = group
			})
			defer svc.Close()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"native-stream","stream":true,"input":"hello"}`))
			req.Header.Set("Authorization", "Bearer "+testToken)
			response := httptest.NewRecorder()
			svc.Handler().ServeHTTP(response, req)
			body := response.Body.String()
			if response.Code != http.StatusOK || strings.Count(body, addedText) != 1 || strings.Count(body, continuation) != 1 {
				t.Fatalf("status=%d added text count=%d continuation count=%d body=%s", response.Code, strings.Count(body, addedText), strings.Count(body, continuation), body)
			}
			deltaStart := strings.Index(body, "event: response.output_text.delta\n")
			if deltaStart < 0 {
				t.Fatalf("terminal flush omitted its synthetic delta: %s", body)
			}
			deltaEnd := strings.Index(body[deltaStart:], "\n\n")
			if deltaEnd < 0 {
				t.Fatalf("synthetic delta frame is incomplete: %s", body)
			}
			deltaFrame := body[deltaStart : deltaStart+deltaEnd]
			if !strings.Contains(deltaFrame, `"sequence_number":2`) || strings.Contains(deltaFrame, `"part":`) || strings.Count(body, `"sequence_number":3`) != 1 {
				t.Fatalf("synthetic delta sequence or payload was not fresh and minimal: %s", body)
			}
		})
	}
}

func TestSecretDLPResponsesTerminalSnapshotProtectsAddedPrefix(t *testing.T) {
	secret := fakeGitHubToken()
	for _, terminal := range []string{"response.output_text.done", "response.completed"} {
		t.Run(terminal, func(t *testing.T) {
			added := `event: response.content_part.added` + "\n" + `data: {"type":"response.content_part.added","output_index":0,"item_id":"msg_terminal","content_index":0,"part":{"type":"output_text","text":"` + secret[:len(secret)/2] + `"}}`
			terminalFrame := ""
			if terminal == "response.output_text.done" {
				terminalFrame = `event: response.output_text.done` + "\n" + `data: {"type":"response.output_text.done","output_index":0,"item_id":"msg_terminal","content_index":0,"text":"` + secret + `"}`
			} else {
				terminalFrame = `event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","id":"msg_terminal","content":[{"type":"output_text","text":"` + secret + `"}]}]}}`
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, added+"\n\n"+terminalFrame+"\n\n")
			}))
			defer upstream.Close()
			target := Target{Provider: "native", Model: "responses-model", ToolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}}}
			svc := nativeStreamTestService(t, upstream.URL, "openai-responses", []Target{target}, func(cfg *Config) {
				group := cfg.Models["native-stream"]
				group.PIIFilter.Enabled = true
				group.PIIFilter.SecretsEnabled = true
				group.PIIFilter.Mode = "redact_only"
				cfg.Models["native-stream"] = group
			})
			defer svc.Close()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"native-stream","stream":true,"input":"hello"}`))
			req.Header.Set("Authorization", "Bearer "+testToken)
			response := httptest.NewRecorder()
			svc.Handler().ServeHTTP(response, req)
			body := response.Body.String()
			if response.Code != http.StatusOK || strings.Contains(body, secret) || strings.Contains(body, secret[:len(secret)/2]) || !strings.Contains(body, "[SECRET_GITHUB_1]") {
				t.Fatalf("terminal snapshot leaked the added credential prefix: status=%d body=%s", response.Code, body)
			}
		})
	}
}

func TestSecretDLPResponsesDuplicateAddedSnapshotsOnce(t *testing.T) {
	secret := fakeGitHubToken()
	frames := []string{
		`event: response.output_item.added` + "\n" + `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_duplicate","role":"assistant","content":[{"type":"output_text","text":"` + secret + `"}]}}`,
		`event: response.content_part.added` + "\n" + `data: {"type":"response.content_part.added","output_index":0,"item_id":"msg_duplicate","content_index":0,"part":{"type":"output_text","text":"` + secret + `"}}`,
		`event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"status":"completed","output":[]}}`,
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, frame := range frames {
			_, _ = io.WriteString(w, frame+"\n\n")
		}
	}))
	defer upstream.Close()
	target := Target{Provider: "native", Model: "responses-model", ToolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}}}
	svc := nativeStreamTestService(t, upstream.URL, "openai-responses", []Target{target}, func(cfg *Config) {
		group := cfg.Models["native-stream"]
		group.PIIFilter.Enabled = true
		group.PIIFilter.SecretsEnabled = true
		cfg.Models["native-stream"] = group
	})
	defer svc.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"native-stream","stream":true,"input":"hello"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	body := response.Body.String()
	if response.Code != http.StatusOK || strings.Contains(body, secret) || strings.Count(body, "[SECRET_GITHUB_1]") != 1 {
		t.Fatalf("duplicate added snapshot was inspected or emitted twice: status=%d body=%s", response.Code, body)
	}
}

func TestResponseTextChannelKeyIncludesSummaryIndex(t *testing.T) {
	first, ok := responseTextChannelKey(map[string]any{"output_index": float64(0), "summary_index": float64(0)})
	if !ok {
		t.Fatal("first summary channel missing")
	}
	second, ok := responseTextChannelKey(map[string]any{"output_index": float64(0), "summary_index": float64(1)})
	if !ok || first == second {
		t.Fatalf("reasoning summaries share a channel: first=%q second=%q", first, second)
	}
}

func TestSecretDLPResponsesFailedAndIncompleteSnapshotsThroughHandler(t *testing.T) {
	secret := fakeGitHubToken()
	for _, terminal := range []string{"failed", "incomplete"} {
		for _, mode := range []string{"redact_only", "fail_on_match"} {
			t.Run(terminal+"/"+mode, func(t *testing.T) {
				frame := `event: response.` + terminal + "\n" + `data: {"type":"response.` + terminal + `","response":{"status":"` + terminal + `","output":[{"type":"message","id":"msg_secret","content":[{"type":"output_text","text":"` + secret + `"}]}]}}`
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, frame+"\n\n")
				}))
				defer upstream.Close()
				target := Target{Provider: "native", Model: "responses-model", ToolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}}}
				svc := nativeStreamTestService(t, upstream.URL, "openai-responses", []Target{target}, func(cfg *Config) {
					group := cfg.Models["native-stream"]
					group.PIIFilter.Enabled = true
					group.PIIFilter.SecretsEnabled = true
					group.PIIFilter.Mode = mode
					cfg.Models["native-stream"] = group
				})
				defer svc.Close()
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"native-stream","stream":true,"input":"hello"}`))
				req.Header.Set("Authorization", "Bearer "+testToken)
				response := httptest.NewRecorder()
				svc.Handler().ServeHTTP(response, req)
				body := response.Body.String()
				if strings.Contains(body, secret) {
					t.Fatalf("terminal snapshot exposed a credential: %s", body)
				}
				if mode == "redact_only" && (response.Code != http.StatusOK || !strings.Contains(body, "[SECRET_GITHUB_1]")) {
					t.Fatalf("terminal snapshot was not redacted: status=%d body=%s", response.Code, body)
				}
				if mode == "fail_on_match" && response.Code != http.StatusBadGateway {
					t.Fatalf("fail_on_match did not reject the terminal snapshot: status=%d body=%s", response.Code, body)
				}
			})
		}
	}
}

func TestSecretDLPResponsesRefusalDeltaAndSnapshotThroughHandler(t *testing.T) {
	secret := fakeGitHubToken()
	frames := []string{
		`event: response.refusal.delta` + "\n" + `data: {"type":"response.refusal.delta","item_id":"refusal_1","output_index":0,"delta":"` + secret[:len(secret)/2] + `"}`,
		`event: response.refusal.delta` + "\n" + `data: {"type":"response.refusal.delta","item_id":"refusal_1","output_index":0,"delta":"` + secret[len(secret)/2:] + `"}`,
		`event: response.failed` + "\n" + `data: {"type":"response.failed","response":{"status":"failed","output":[{"type":"refusal","id":"refusal_1","refusal":"` + secret + `"}]}}`,
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, frame := range frames {
			_, _ = io.WriteString(w, frame+"\n\n")
		}
	}))
	defer upstream.Close()
	target := Target{Provider: "native", Model: "responses-model", ToolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}}}
	svc := nativeStreamTestService(t, upstream.URL, "openai-responses", []Target{target}, func(cfg *Config) {
		group := cfg.Models["native-stream"]
		group.PIIFilter.Enabled = true
		group.PIIFilter.SecretsEnabled = true
		cfg.Models["native-stream"] = group
	})
	defer svc.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"native-stream","stream":true,"input":"hello"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	body := response.Body.String()
	if response.Code != http.StatusOK || strings.Contains(body, secret) || !strings.Contains(body, "[SECRET_GITHUB_1]") {
		t.Fatalf("status=%d refusal delta or snapshot leaked/missed redaction: %s", response.Code, body)
	}
}

func TestSecretDLPResponsesRefusalDoneAndOutputItemDoneFlushChannels(t *testing.T) {
	secret := fakeGitHubToken()
	t.Run("refusal done snapshot", func(t *testing.T) {
		frames := []string{
			`event: response.refusal.delta` + "\n" + `data: {"type":"response.refusal.delta","item_id":"refusal_done","output_index":0,"delta":"` + secret[:len(secret)/2] + `"}`,
			`event: response.refusal.done` + "\n" + `data: {"type":"response.refusal.done","item_id":"refusal_done","output_index":0,"refusal":"` + secret + `"}`,
			`event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"status":"completed","output":[]}}`,
		}
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, frame := range frames {
				_, _ = io.WriteString(w, frame+"\n\n")
			}
		}))
		defer upstream.Close()
		target := Target{Provider: "native", Model: "responses-model", ToolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}}}
		svc := nativeStreamTestService(t, upstream.URL, "openai-responses", []Target{target}, func(cfg *Config) {
			group := cfg.Models["native-stream"]
			group.PIIFilter.Enabled = true
			group.PIIFilter.SecretsEnabled = true
			cfg.Models["native-stream"] = group
		})
		defer svc.Close()
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"native-stream","stream":true,"input":"hello"}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		response := httptest.NewRecorder()
		svc.Handler().ServeHTTP(response, req)
		body := response.Body.String()
		if response.Code != http.StatusOK || strings.Contains(body, secret) || strings.Contains(body, secret[:len(secret)/2]) || !strings.Contains(body, "[SECRET_GITHUB_1]") {
			t.Fatalf("refusal.done did not flush its namespaced held text safely: status=%d body=%s", response.Code, body)
		}
	})
	t.Run("output item done flushes refusal and reasoning", func(t *testing.T) {
		frames := []string{
			`event: response.refusal.delta` + "\n" + `data: {"type":"response.refusal.delta","item_id":"item_done","output_index":0,"delta":"` + secret + `"}`,
			`event: response.reasoning_summary_text.delta` + "\n" + `data: {"type":"response.reasoning_summary_text.delta","item_id":"item_done","output_index":0,"summary_index":0,"delta":"` + secret + `"}`,
			`event: response.output_item.done` + "\n" + `data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"item_done","summary":[]}}`,
		}
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, frame := range frames {
				_, _ = io.WriteString(w, frame+"\n\n")
			}
		}))
		defer upstream.Close()
		target := Target{Provider: "native", Model: "responses-model", ToolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}}}
		svc := nativeStreamTestService(t, upstream.URL, "openai-responses", []Target{target}, func(cfg *Config) {
			group := cfg.Models["native-stream"]
			group.PIIFilter.Enabled = true
			group.PIIFilter.SecretsEnabled = true
			cfg.Models["native-stream"] = group
		})
		defer svc.Close()
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"native-stream","stream":true,"input":"hello"}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		response := httptest.NewRecorder()
		svc.Handler().ServeHTTP(response, req)
		body := response.Body.String()
		if response.Code != http.StatusOK || strings.Contains(body, secret) || strings.Count(body, "[SECRET_GITHUB_") < 2 {
			t.Fatalf("output_item.done did not flush both namespaced channels safely: status=%d body=%s", response.Code, body)
		}
		if !strings.Contains(body, `"type":"response.refusal.delta"`) || !strings.Contains(body, `"type":"response.reasoning_summary_text.delta"`) {
			t.Fatalf("output_item.done omitted a synthetic refusal or reasoning delta: %s", body)
		}
	})
}

func TestSecretDLPRedactsEscapedSecretSplitAcrossToolArgumentDeltas(t *testing.T) {
	secret := "sk-or-v1-" + strings.Repeat("a", 64)
	arguments := `{"api_key":"sk-or-v1\u002d` + strings.Repeat("a", 64) + `"}`
	escape := strings.Index(arguments, `\u002d`)
	if escape < 0 {
		t.Fatal("test arguments have no escaped separator")
	}
	split := escape + 4 // Split the JSON unicode escape after `\u00`.
	frame := func(eventType string, payload map[string]any) string {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return "event: " + eventType + "\n" + "data: " + string(raw) + "\n\n"
	}
	frames := []string{
		frame("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "lookup", "arguments": "", "status": "in_progress"}}),
		frame("response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta", "output_index": 0, "delta": arguments[:split]}),
		frame("response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta", "output_index": 0, "delta": arguments[split:]}),
		frame("response.function_call_arguments.done", map[string]any{"type": "response.function_call_arguments.done", "output_index": 0, "arguments": arguments}),
		frame("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "lookup", "arguments": arguments, "status": "completed"}}),
		frame("response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_1", "status": "completed", "output": []any{map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "lookup", "arguments": arguments}}}}),
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, value := range frames {
			_, _ = io.WriteString(w, value)
		}
	}))
	defer upstream.Close()
	target := Target{Provider: "native", Model: "responses-model", ToolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}}}
	svc := nativeStreamTestService(t, upstream.URL, "openai-responses", []Target{target}, func(cfg *Config) {
		group := cfg.Models["native-stream"]
		group.Strategy = "tool_aware"
		group.ToolRouting = ToolRoutingConfig{Mode: "observe", Streaming: ToolRoutingStreamingConfig{HoldUntilValidated: true, HoldTimeoutMS: 1000, HoldMaxBytes: 1 << 20}}
		group.PIIFilter.Enabled = true
		group.PIIFilter.SecretsEnabled = true
		cfg.Models["native-stream"] = group
	})
	request := `{"model":"native-stream","stream":true,"input":"lookup","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(request))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	body := response.Body.String()
	secretBody := strings.Repeat("a", 64)
	if response.Code != http.StatusOK || strings.Contains(body, secret) || !strings.Contains(body, "[SECRET_PROVIDER_1]") {
		t.Fatalf("status=%d streamed escaped tool argument leaked or missed redaction", response.Code)
	}
	var deltaCount, doneCount int
	for _, frame := range strings.Split(body, "\n\n") {
		if strings.Contains(frame, "event: response.function_call_arguments.delta") {
			deltaCount++
			if strings.Contains(frame, secretBody) {
				t.Fatalf("streamed function_call_arguments.delta leaked the credential body")
			}
		}
		if strings.Contains(frame, "event: response.function_call_arguments.done") {
			doneCount++
			if strings.Contains(frame, secretBody) {
				t.Fatalf("streamed function_call_arguments.done leaked the credential body")
			}
		}
		if strings.Contains(frame, secretBody) {
			t.Fatalf("stream frame leaked the credential body")
		}
	}
	if deltaCount != 2 || doneCount != 1 {
		t.Fatalf("streamed argument frames: deltas=%d done=%d, want 2 deltas and 1 done", deltaCount, doneCount)
	}
}

func TestSecretDLPRedactsPolicySelectedHostedResponsesCallsThroughHandler(t *testing.T) {
	secret := fakeGitHubToken()
	frames := []string{
		`event: response.output_item.added` + "\n" + `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"mcp_call","id":"mcp_1","call_id":"call_mcp","name":"lookup_secret","arguments":{"api_key":"` + secret + `"},"status":"in_progress"}}`,
		`event: response.output_item.done` + "\n" + `data: {"type":"response.output_item.done","output_index":0,"item":{"type":"mcp_call","id":"mcp_1","call_id":"call_mcp","name":"lookup_secret","arguments":{"api_key":"` + secret + `"},"status":"completed"}}`,
		`event: response.output_item.added` + "\n" + `data: {"type":"response.output_item.added","output_index":1,"item":{"type":"code_interpreter_call","id":"code_1","code":"print('` + secret + `')","status":"in_progress"}}`,
		`event: response.output_item.done` + "\n" + `data: {"type":"response.output_item.done","output_index":1,"item":{"type":"code_interpreter_call","id":"code_1","code":"print('` + secret + `')","status":"completed"}}`,
		`event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"mcp_call","id":"mcp_1","call_id":"call_mcp","name":"lookup_secret","arguments":{"api_key":"` + secret + `"},"status":"completed"},{"type":"code_interpreter_call","id":"code_1","code":"print('` + secret + `')","status":"completed"}]}}`,
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, frame := range frames {
			_, _ = io.WriteString(w, frame+"\n\n")
		}
	}))
	defer upstream.Close()
	target := Target{Provider: "native", Model: "responses-model", ToolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}}}
	svc := nativeStreamTestService(t, upstream.URL, "openai-responses", []Target{target}, func(cfg *Config) {
		group := cfg.Models["native-stream"]
		group.Strategy = "tool_aware"
		group.ToolRouting = ToolRoutingConfig{Mode: "observe", Streaming: ToolRoutingStreamingConfig{HoldUntilValidated: true, HoldTimeoutMS: 1000, HoldMaxBytes: 1 << 20}}
		group.PIIFilter.Enabled = true
		group.PIIFilter.SecretsEnabled = true
		cfg.Models["native-stream"] = group
		cfg.Server.ToolFirewall = ToolFirewallConfig{Mode: toolFirewallShadow, Deny: []ToolFirewallDenyRule{{Tool: "lookup_secret"}, {Tool: "code_interpreter_call"}}}
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"native-stream","stream":true,"input":"inspect","tools":[{"type":"function","name":"lookup_secret","parameters":{"type":"object"}}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	recorder := httptest.NewRecorder()
	svc.Handler().ServeHTTP(recorder, req)
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || strings.Contains(body, secret) || strings.Count(body, "[SECRET_GITHUB_") < 2 || !strings.Contains(body, `"status":"in_progress"`) || !strings.Contains(body, `"status":"completed"`) || !strings.Contains(body, `"call_mcp"`) || !strings.Contains(body, `"code_1"`) {
		t.Fatalf("status=%d hosted call payload leaked or was not redacted: %s", recorder.Code, body)
	}
}

func TestSecretDLPStreamFailsClosedForUnboundedCustomRules(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	group := cfg.Models["default"]
	group.PIIFilter.Enabled = true
	group.PIIFilter.SecretsEnabled = true
	group.PIIFilter.Rules = []PIIFilterRule{{Name: "custom", Expression: `secret.{1,}`, PlaceholderPrefix: "CUSTOM"}}
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

func TestSecretDLPChatRefusalDeltaRedactsCredentialSplitAcrossChunks(t *testing.T) {
	secret := fakeGitHubToken()
	for _, mode := range []string{"redact_only", "fail_on_match"} {
		t.Run(mode, func(t *testing.T) {
			safePrefix := strings.Repeat("safe-", 150)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				flusher, _ := w.(http.Flusher)
				writeChunk := func(choices ...map[string]any) {
					payload := map[string]any{
						"id": "chat-refusal-secret", "object": "chat.completion.chunk", "created": 1, "model": "default",
						"choices": make([]any, 0, len(choices)),
					}
					for _, choice := range choices {
						payload["choices"] = append(payload["choices"].([]any), choice)
					}
					raw, _ := json.Marshal(payload)
					_, _ = w.Write(append(append([]byte("data: "), raw...), []byte("\n\n")...))
					if flusher != nil {
						flusher.Flush()
					}
				}
				choice := func(index int, refusal string) map[string]any {
					return map[string]any{"index": index, "delta": map[string]any{"refusal": refusal}, "finish_reason": nil}
				}
				writeChunk(
					choice(0, safePrefix+secret[:len(secret)/2]),
					choice(1, safePrefix+secret[:len(secret)/2]),
				)
				// Reverse choice order to prove buffers are keyed by choice index.
				writeChunk(
					choice(1, secret[len(secret)/2:]+" suffix-one"),
					choice(0, secret[len(secret)/2:]+" suffix-zero"),
				)
				// No finish_reason: the decoder must flush safe tails before [DONE].
				_, _ = w.Write([]byte("data: [DONE]\n\n"))
			}))
			defer upstream.Close()

			dir := t.TempDir()
			cfg := testConfig(t, upstream.URL, "provider-key", dir)
			group := cfg.Models["default"]
			group.PIIFilter.Enabled = true
			group.PIIFilter.SecretsEnabled = true
			group.PIIFilter.Mode = mode
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

			reconstructed := map[int]string{}
			done := false
			for _, line := range strings.Split(response.Body.String(), "\n") {
				data, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
				if !ok {
					continue
				}
				if data == "[DONE]" {
					done = true
					continue
				}
				var payload map[string]any
				if json.Unmarshal([]byte(data), &payload) != nil {
					continue
				}
				choices, _ := payload["choices"].([]any)
				for _, raw := range choices {
					item, _ := raw.(map[string]any)
					index, _ := numberAsInt(item["index"])
					delta, _ := item["delta"].(map[string]any)
					refusal, _ := delta["refusal"].(string)
					reconstructed[index] += refusal
				}
			}
			for index, text := range reconstructed {
				if strings.Contains(text, secret) {
					t.Fatalf("Chat choice %d refusal stream leaked a reconstructed credential", index)
				}
			}

			if mode == "redact_only" {
				if response.Code != http.StatusOK || reconstructed[0] != safePrefix+"[SECRET_GITHUB_1] suffix-zero" || reconstructed[1] != safePrefix+"[SECRET_GITHUB_1] suffix-one" || !done {
					t.Fatalf("redact_only refusal reconstruction mismatch: status=%d choice0=%q choice1=%q done=%v", response.Code, reconstructed[0], reconstructed[1], done)
				}
				return
			}
			if done || strings.Contains(response.Body.String(), "[SECRET_GITHUB_1]") {
				t.Fatalf("fail_on_match forwarded a refusal or clean terminal: status=%d body=%s", response.Code, response.Body.String())
			}
			logData, err := os.ReadFile(cfg.Server.Logging.Path)
			if err != nil {
				t.Fatal(err)
			}
			var record map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(logData), &record); err != nil {
				t.Fatalf("decode request log: %v", err)
			}
			if record["error_class"] != "secret_dlp_stream_filter_error" || record["status"] != float64(http.StatusBadGateway) {
				t.Fatalf("fail_on_match did not record an explicit stream failure: %#v", record)
			}
		})
	}
}

func TestSecretDLPStreamingRedactsCredentialSplitAcrossChunks(t *testing.T) {
	secret := fakeGitHubToken()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		writeChunk := func(delta map[string]any, finish any) {
			payload := map[string]any{"id": "stream-secret", "object": "chat.completion.chunk", "created": 1, "model": "default", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
			raw, _ := json.Marshal(payload)
			_, _ = w.Write(append(append([]byte("data: "), raw...), []byte("\n\n")...))
			if flusher != nil {
				flusher.Flush()
			}
		}
		writeChunk(map[string]any{"role": "assistant", "content": "prefix " + secret[:len(secret)/2]}, nil)
		writeChunk(map[string]any{"content": secret[len(secret)/2:] + " suffix"}, nil)
		writeChunk(map[string]any{}, "stop")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
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
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), secret) || !strings.Contains(response.Body.String(), "[SECRET_GITHUB_1]") {
		t.Fatalf("status=%d response=%s", response.Code, response.Body.String())
	}
	logData, err := os.ReadFile(cfg.Server.Logging.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logData), secret) {
		t.Fatal("stream request log contains the streamed credential")
	}
	flushUsageForTest(t, svc)
	var rows []usageRecord
	if err := svc.usage.db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	serialized, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), secret) {
		t.Fatal("stream usage rows contain the streamed credential")
	}
}

func TestSecretDLPTranslatedResponsesStreamRedactsSplitCredential(t *testing.T) {
	secret := fakeGitHubToken()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		writeChunk := func(content string, finish any) {
			payload := map[string]any{"id": "translated-stream-secret", "object": "chat.completion.chunk", "created": 1, "model": "default", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": content}, "finish_reason": finish}}}
			raw, _ := json.Marshal(payload)
			_, _ = w.Write(append(append([]byte("data: "), raw...), []byte("\n\n")...))
			if flusher != nil {
				flusher.Flush()
			}
		}
		writeChunk("prefix "+secret[:len(secret)/2], nil)
		writeChunk(secret[len(secret)/2:]+" suffix", nil)
		writeChunk("", "stop")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	group := cfg.Models["default"]
	group.PIIFilter.Enabled = true
	group.PIIFilter.SecretsEnabled = true
	group.Targets[0].ResponsesToChat = ResponsesToChatBridge{Enabled: true, Text: true, Streaming: true, ValidationStatus: "passed"}
	cfg.Models["default"] = group
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	requestBody := `{"model":"default","stream":true,"input":"hello","max_output_tokens":32,"store":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(requestBody))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), secret) || !strings.Contains(response.Body.String(), "[SECRET_GITHUB_1]") {
		t.Fatalf("status=%d response=%s", response.Code, response.Body.String())
	}
}

func TestSecretDLPBuiltinsAndBenign(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"aws", fakeAWSAccessKey(), "[SECRET_AWS_1]"},
		{"aws_benign", "AKIA123 is not an AWS access key", "AKIA123 is not an AWS access key"},
		{"github", fakeGitHubToken(), "[SECRET_GITHUB_1]"},
		{"github_benign", "ghp_short is documentation text", "ghp_short is documentation text"},
		{"private_key", fakePrivateKeyBlock(), "[SECRET_PRIVATE_KEY_1]"},
		{"encrypted_private_key", "-----BEGIN ENCRYPTED" + " PRIVATE KEY-----\\nmaterial\\n-----END ENCRYPTED" + " PRIVATE KEY-----", "[SECRET_PRIVATE_KEY_1]"},
		{"private_key_benign", "-----BEGIN PRIVATE KEY----- without matching end marker", "-----BEGIN PRIVATE KEY----- without matching end marker"},
		{"slack", "xoxb-" + strings.Repeat("a", 24), "[SECRET_SLACK_1]"},
		{"slack_benign", "xoxb-short is not a complete Slack token", "xoxb-short is not a complete Slack token"},
		{"stripe", "sk_live_" + strings.Repeat("a", 20), "[SECRET_STRIPE_1]"},
		{"stripe_benign", "pk_live_" + strings.Repeat("a", 24), "pk_live_" + strings.Repeat("a", 24)},
		{"openai_provider", "sk-proj-" + strings.Repeat("a", 32), "[SECRET_PROVIDER_1]"},
		{"openai_benign", "sk-proj-short is not a provider key", "sk-proj-short is not a provider key"},
		{"openai_legacy_provider", "sk-" + strings.Repeat("b", 40), "[SECRET_PROVIDER_1]"},
		{"openai_legacy_benign", "sk-short is not a provider key", "sk-short is not a provider key"},
		{"groq_provider", "gsk_" + strings.Repeat("c", 40), "[SECRET_PROVIDER_1]"},
		{"groq_benign", "gsk_short is not a provider key", "gsk_short is not a provider key"},
		{"openrouter_provider", "sk-or-v1-" + strings.Repeat("d", 64), "[SECRET_PROVIDER_1]"},
		{"openrouter_benign", "sk-or-v1-short is not a provider key", "sk-or-v1-short is not a provider key"},
		{"anthropic_provider", "sk-ant-api03-" + strings.Repeat("a", 32), "[SECRET_PROVIDER_1]"},
		{"anthropic_benign", "sk-ant-api03-short is not a provider key", "sk-ant-api03-short is not a provider key"},
		{"google_provider", "AIza" + strings.Repeat("a", 35), "[SECRET_PROVIDER_1]"},
		{"google_benign", "AIza-short is not a Google API key", "AIza-short is not a Google API key"},
		{"router_token", "rtr_metrum_alice_search_dev_k20260101_secretsecret", "[SECRET_ROUTER_1]"},
		{"router_token_benign", "rtr_metrum_ is only a token prefix", "rtr_metrum_ is only a token prefix"},
		{"jwt", fakeJWTToken(), "[SECRET_JWT_1]"},
		{"jwt_benign", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0"},
		{"authorization_bearer", "Authorization: Bearer fixturefixturefixturefixture", "[SECRET_BEARER_1]"},
		{"authorization_bearer_benign", "Authorization: Basic abcdefghijklmnopqrstuvwxyz123456", "Authorization: Basic abcdefghijklmnopqrstuvwxyz123456"},
		{"credential_assignment", fakeCredentialAssignment(), "[SECRET_CREDENTIAL_1]"},
		{"credential_assignment_benign", "password: supplied during the demo", "password: supplied during the demo"},
		{"password_assignment", fakePasswordAssignment(), "[SECRET_CREDENTIAL_1]"},
		{"sha512_integrity", "sha512-" + strings.Repeat("A", 64), "sha512-" + strings.Repeat("A", 64)},
		{"camel_case_identifier", "LongCamelCaseIdentifierThatIsNotASecretValue", "LongCamelCaseIdentifierThatIsNotASecretValue"},
		{"password_prose", "password: supplied during the demo", "password: supplied during the demo"},
		{"long_base64_prose", strings.Repeat("A", 300), strings.Repeat("A", 300)},
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

func TestSecretDLPStreamWindowLongPrefixSplitOffsets(t *testing.T) {
	secrets := []string{
		fakeAWSAccessKey(), fakeGitHubToken(),
		"xoxb-" + strings.Repeat("a", 24), "sk_live_" + strings.Repeat("a", 20),
		"sk-proj-" + strings.Repeat("a", 32), "sk-" + strings.Repeat("b", 40),
		"gsk_" + strings.Repeat("c", 40), "sk-or-v1-" + strings.Repeat("d", 64), "sk-ant-api03-" + strings.Repeat("a", 32),
		"AIza" + strings.Repeat("a", 35), "rtr_metrum_alice_search_dev_k20260101_secretsecret",
		fakeJWTToken(),
		"Authorization: Bearer fixturefixturefixturefixture",
		fakeCredentialAssignment(),
		fakePasswordAssignment(),
		"-----BEGIN ENCRYPTED" + " PRIVATE KEY-----\\nmaterial\\n-----END ENCRYPTED" + " PRIVATE KEY-----",
	}
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		for _, split := range []int{4, 7, 128} {
			if split >= len(secret) {
				continue
			}
			t.Run(fmt.Sprintf("%T/%d/%d", secret[:1], len(secret), split), func(t *testing.T) {
				state := &piiRedactionState{rules: rules, limit: 200, result: &piiFilterResult{RuleCounts: map[string]int{}}, placeholderFor: map[string]string{}}
				pending := ""
				var output strings.Builder
				feed := func(fragment string) {
					t.Helper()
					safe, next, feedErr := redactSecretStreamChunk(state, pending, fragment, false)
					if feedErr != nil {
						t.Fatal(feedErr)
					}
					if strings.Contains(safe, secret) {
						t.Fatalf("raw secret released before completion %q: %q", secret, safe)
					}
					assertNoSecretFragment(t, safe, secret, 8)
					output.WriteString(safe)
					pending = next
				}
				feed(strings.Repeat("ordinary prefix ", 50))
				feed(secret[:split])
				feed(secret[split:])
				feed(strings.Repeat(" ordinary suffix", 500))
				safe, _, err := redactSecretStreamChunk(state, pending, "", true)
				if err != nil {
					t.Fatal(err)
				}
				output.WriteString(safe)
				if strings.Contains(output.String(), secret) || !strings.Contains(output.String(), "[SECRET_") {
					t.Fatalf("secret survived rolling stream window: %q", output.String())
				}
				assertNoSecretFragment(t, output.String(), secret, 8)
			})
		}
	}
}

func TestSecretDLPStreamReleaseEdgeProperty(t *testing.T) {
	secrets := []struct {
		name  string
		value string
	}{
		{"aws", fakeAWSAccessKey()},
		{"github", fakeGitHubToken()},
		{"slack", "xoxb-" + strings.Repeat("a", 24)},
		{"stripe", "sk_live_" + strings.Repeat("a", 20)},
		{"openai_project", "sk-proj-" + strings.Repeat("a", 32)},
		{"openai_legacy", "sk-" + strings.Repeat("b", 40)},
		{"groq", "gsk_" + strings.Repeat("c", 40)},
		{"openrouter", "sk-or-v1-" + strings.Repeat("d", 64)},
		{"anthropic", "sk-ant-api03-" + strings.Repeat("a", 32)},
		{"google", "AIza" + strings.Repeat("a", 35)},
		{"router", "rtr_metrum_alice_search_dev_k20260101_secretsecret"},
		{"jwt", fakeLongJWTToken()},
		{"bearer", "Authorization: Bearer " + strings.Repeat("q", 120)},
		{"credential", fakeCredentialAssignment()},
		{"password", fakePasswordAssignment()},
		{"private_key", fakePrivateKeyBlock()},
		{"encrypted_private_key", "-----BEGIN ENCRYPTED PRIVATE KEY-----\n" + strings.Repeat("E", 128) + "\n-----END ENCRYPTED PRIVATE KEY-----"},
	}
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range secrets {
		for _, edge := range []int{-4, 0, 4} {
			t.Run(fmt.Sprintf("%s/edge_%d", tc.name, edge), func(t *testing.T) {
				state := &piiRedactionState{rules: rules, limit: 200, result: &piiFilterResult{RuleCounts: map[string]int{}}, placeholderFor: map[string]string{}}
				prefix := strings.Repeat("P", secretStreamTailBytes+edge-1) + " "
				var pending string
				var output strings.Builder
				feed := func(fragment string) {
					t.Helper()
					safe, next, feedErr := redactSecretStreamChunk(state, pending, fragment, false)
					if feedErr != nil {
						t.Fatal(feedErr)
					}
					assertNoSecretFragment(t, safe, tc.value, 12)
					output.WriteString(safe)
					pending = next
				}
				feed(prefix)
				split := len(tc.value) / 2
				feed(tc.value[:split])
				feed(tc.value[split:])
				safe, _, err := redactSecretStreamChunk(state, pending, "", true)
				if err != nil {
					t.Fatal(err)
				}
				output.WriteString(safe)
				if strings.Contains(output.String(), tc.value) || !strings.Contains(output.String(), "[SECRET_") {
					t.Fatalf("release-edge stream retained secret %s: output tail=%q", tc.name, output.String()[max(0, len(output.String())-200):])
				}
				assertNoSecretFragment(t, output.String(), tc.value, 12)
			})
		}
	}
}

func TestSecretDLPStreamHyphenatedJWTReleaseEdges(t *testing.T) {
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		t.Fatal(err)
	}
	prefixLengths := []int{0, 1, 64, 300, 500, 508, 512, 516, 600, 1024}
	for _, payloadLength := range []int{300, 900, 1500} {
		token := "eyJ" + strings.Repeat("h", 64) + "." + strings.Repeat("p", payloadLength) + "." + strings.Repeat("s", 128)
		for _, prefixLength := range prefixLengths {
			t.Run(fmt.Sprintf("payload_%d/prefix_%d", payloadLength, prefixLength), func(t *testing.T) {
				state := &piiRedactionState{rules: rules, limit: 200, result: &piiFilterResult{RuleCounts: map[string]int{}}, placeholderFor: map[string]string{}}
				input := strings.Repeat("P", prefixLength) + "-" + token + strings.Repeat("Z", 600)
				var pending string
				var output strings.Builder
				for start := 0; start < len(input); start += 16 {
					end := min(start+16, len(input))
					safe, next, feedErr := redactSecretStreamChunk(state, pending, input[start:end], false)
					if feedErr != nil {
						t.Fatalf("feed [%d:%d]: %v", start, end, feedErr)
					}
					if strings.Contains(safe, token[:12]) {
						t.Fatalf("feed [%d:%d] released JWT header; pending=%d safe=%q", start, end, len(pending), safe[max(0, strings.Index(safe, token[:12])-12):])
					}
					assertNoSecretFragment(t, safe, token, 12)
					output.WriteString(safe)
					pending = next
				}
				safe, _, err := redactSecretStreamChunk(state, pending, "", true)
				if err != nil {
					t.Fatal(err)
				}
				output.WriteString(safe)
				if strings.Contains(output.String(), token) || !strings.Contains(output.String(), "[SECRET_JWT_1]") {
					t.Fatalf("hyphenated JWT was leaked or not redacted; payload=%d prefix=%d tail=%q", payloadLength, prefixLength, output.String()[max(0, output.Len()-200):])
				}
				assertNoSecretFragment(t, output.String(), token, 12)
			})
		}
	}
}

func TestSecretDLPStreamJWTResumesInsideInvalidCandidate(t *testing.T) {
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		t.Fatal(err)
	}
	// The malformed eyJab.cd- candidate must not make the scanner skip the
	// real JWT immediately following its hyphen.
	token := "eyJ" + strings.Repeat("h", 64) + "." + strings.Repeat("p", 600) + "." + strings.Repeat("s", 64)
	input := "see eyJab.cd-" + token + " " + strings.Repeat("Z", 600)
	initState := func() *piiRedactionState {
		return &piiRedactionState{rules: rules, limit: 200, result: &piiFilterResult{RuleCounts: map[string]int{}}, placeholderFor: map[string]string{}}
	}
	unary := initState().redact(input)
	if strings.Contains(unary, token) {
		t.Fatal("unary redactor did not identify the regression token")
	}
	for _, chunkSize := range []int{1, 7, 16, 31, 64} {
		t.Run(fmt.Sprintf("chunk_%d", chunkSize), func(t *testing.T) {
			state := initState()
			var pending string
			var released strings.Builder
			for start := 0; start < len(input); {
				end := min(start+chunkSize, len(input))
				safe, next, err := redactSecretStreamChunk(state, pending, input[start:end], false)
				if err != nil {
					t.Fatal(err)
				}
				released.WriteString(safe)
				pending = next
				start = end
			}
			safe, _, err := redactSecretStreamChunk(state, pending, "", true)
			if err != nil {
				t.Fatal(err)
			}
			released.WriteString(safe)
			if strings.Contains(released.String(), token) {
				t.Fatalf("stream released JWT after malformed candidate (released bytes=%d)", released.Len())
			}
			assertNoSecretFragment(t, released.String(), token, 12)
		})
	}
}

func TestSecretDLPStreamKeywordPaddingIsHeld(t *testing.T) {
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name        string
		input       string
		secret      string
		placeholder string
	}{
		{name: "assignment", input: "api_key" + strings.Repeat(" ", 600) + "=" + strings.Repeat("a", 48) + " tail" + strings.Repeat("z", 600), secret: "api_key" + strings.Repeat(" ", 600) + "=" + strings.Repeat("a", 48), placeholder: "[SECRET_CREDENTIAL_1]"},
		{name: "authorization_bearer", input: "Authorization: Bearer" + strings.Repeat(" ", 600) + strings.Repeat("b", 64) + " tail" + strings.Repeat("z", 600), secret: "Authorization: Bearer" + strings.Repeat(" ", 600) + strings.Repeat("b", 64), placeholder: "[SECRET_BEARER_1]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := &piiRedactionState{rules: rules, limit: 200, result: &piiFilterResult{RuleCounts: map[string]int{}}, placeholderFor: map[string]string{}}
			var pending string
			var output strings.Builder
			for start := 0; start < len(tc.input); start += 16 {
				end := min(start+16, len(tc.input))
				safe, next, feedErr := redactSecretStreamChunk(state, pending, tc.input[start:end], false)
				if feedErr != nil {
					t.Fatalf("feed [%d:%d]: %v", start, end, feedErr)
				}
				assertNoSecretFragment(t, safe, tc.secret, 12)
				output.WriteString(safe)
				pending = next
			}
			safe, _, err := redactSecretStreamChunk(state, pending, "", true)
			if err != nil {
				t.Fatal(err)
			}
			output.WriteString(safe)
			if strings.Contains(output.String(), tc.secret) || !strings.Contains(output.String(), tc.placeholder) {
				t.Fatalf("padded keyword candidate leaked or was not redacted: %q", output.String()[max(0, output.Len()-200):])
			}
			assertNoSecretFragment(t, output.String(), tc.secret, 12)
		})
	}
}

func TestSecretDLPPEMEndMarkersThroughChatHandler(t *testing.T) {
	const safePrefix = "safe prelude "
	const safeSuffix = " safe suffix"
	privateBody := strings.Repeat("K", 700)
	begin := testPEMMarker("BEGIN", "RSA PRIVATE KEY") + "\n"
	mismatchedEnd := "\n" + testPEMMarker("END", "EC PRIVATE KEY") + "\n"
	matchingEnd := "\n" + testPEMMarker("END", "RSA PRIVATE KEY")
	padding := strings.Repeat("ordinary safe text ", 40)

	for _, tc := range []struct {
		name          string
		mode          string
		matchingEnd   bool
		wantRedaction bool
		wantFailure   bool
	}{
		{name: "redact after matching end", mode: "redact_only", matchingEnd: true, wantRedaction: true},
		{name: "fail on matching end", mode: "fail_on_match", matchingEnd: true, wantFailure: true},
		{name: "fail closed at terminal with mismatched end", mode: "redact_only", wantFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var frames []string
			first := safePrefix + begin + privateBody + mismatchedEnd + padding
			if tc.matchingEnd {
				first += matchingEnd[:strings.Index(matchingEnd, "-----END ")]
			}
			frames = append(frames, chatContentChunk(first, nil))
			if tc.matchingEnd {
				frames = append(frames, chatContentChunk(matchingEnd[strings.Index(matchingEnd, "-----END "):]+safeSuffix, nil))
			}
			frames = append(frames, chatContentChunk("", "stop"), "data: [DONE]")
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, frame := range frames {
					_, _ = io.WriteString(w, frame+"\n\n")
					if flusher, ok := w.(http.Flusher); ok {
						flusher.Flush()
					}
				}
			}))
			defer upstream.Close()
			dir := t.TempDir()
			cfg := testConfig(t, upstream.URL, "provider-key", dir)
			group := cfg.Models["default"]
			group.PIIFilter.Enabled = true
			group.PIIFilter.SecretsEnabled = true
			group.PIIFilter.Mode = tc.mode
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
			var rebuilt strings.Builder
			done := false
			for _, line := range strings.Split(response.Body.String(), "\n") {
				data, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
				if !ok {
					continue
				}
				if data == "[DONE]" {
					done = true
					continue
				}
				var payload map[string]any
				if json.Unmarshal([]byte(data), &payload) != nil {
					continue
				}
				choices, _ := payload["choices"].([]any)
				for _, raw := range choices {
					choice, _ := raw.(map[string]any)
					delta, _ := choice["delta"].(map[string]any)
					content, _ := delta["content"].(string)
					rebuilt.WriteString(content)
				}
			}
			output := rebuilt.String()
			if strings.Contains(output, privateBody[:24]) || strings.Contains(output, begin) {
				t.Fatal("Chat stream released private-key material")
			}
			if tc.wantRedaction {
				if response.Code != http.StatusOK || !strings.Contains(output, "[SECRET_PRIVATE_KEY_1]") || !strings.Contains(output, safeSuffix) || !done {
					t.Fatalf("matching PEM END was not redacted and flushed: status=%d len=%d done=%v", response.Code, len(output), done)
				}
				return
			}
			if done || strings.Contains(output, "[SECRET_PRIVATE_KEY_1]") {
				t.Fatalf("blocked PEM stream had a clean terminal or placeholder: status=%d len=%d", response.Code, len(output))
			}
			logData, err := os.ReadFile(cfg.Server.Logging.Path)
			if err != nil {
				t.Fatal(err)
			}
			var record map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(logData), &record); err != nil {
				t.Fatalf("decode request log: %v", err)
			}
			if record["error_class"] != "secret_dlp_stream_filter_error" || record["status"] != float64(http.StatusBadGateway) {
				t.Fatalf("unresolved PEM did not fail closed: class=%v status=%v", record["error_class"], record["status"])
			}
		})
	}
}

func testPEMMarker(kind, label string) string {
	return strings.Repeat("-", 5) + kind + " " + label + strings.Repeat("-", 5)
}

func chatContentChunk(content string, finish any) string {
	payload := map[string]any{"id": "pem-stream", "object": "chat.completion.chunk", "created": 1, "model": "default", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": content}, "finish_reason": finish}}}
	raw, _ := json.Marshal(payload)
	return "data: " + string(raw)
}

func TestSecretDLPTruncatedPEMStreamsFailClosed(t *testing.T) {
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		t.Fatal(err)
	}
	newDLP := func() *secretStreamDLP {
		return &secretStreamDLP{state: &piiRedactionState{rules: rules, limit: 200, result: &piiFilterResult{RuleCounts: map[string]int{}}, placeholderFor: map[string]string{}}}
	}
	truncatedKey := "-----BEGIN RSA PRIVATE KEY-----\n" + strings.Repeat("K", 700)

	t.Run("responses_done_snapshot", func(t *testing.T) {
		dlp := newDLP()
		delta, _ := json.Marshal(map[string]any{"type": "response.output_text.delta", "delta": truncatedKey, "sequence_number": 0})
		out, err := dlp.filterEvent(stream.Event{Type: "response.output_text.delta", Data: delta})
		if err != nil || len(out) != 0 {
			t.Fatalf("Responses delta leaked before done: events=%#v err=%v", out, err)
		}
		done, _ := json.Marshal(map[string]any{"type": "response.output_text.done", "text": truncatedKey, "item_id": "msg_truncated", "sequence_number": 1})
		out, err = dlp.filterEvent(stream.Event{Type: "response.output_text.done", Data: done})
		if !errors.Is(err, errSecretStreamWindowExceeded) || len(out) != 0 {
			t.Fatalf("Responses done snapshot did not fail closed: events=%#v err=%v", out, err)
		}
	})

	t.Run("anthropic_message_stop", func(t *testing.T) {
		dlp := newDLP()
		delta, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": truncatedKey}})
		out, err := dlp.filterEvent(stream.Event{Name: "content_block_delta", Type: "content_block_delta", Data: delta})
		if err != nil || len(out) != 0 {
			t.Fatalf("Anthropic delta leaked before stop: events=%#v err=%v", out, err)
		}
		stop, _ := json.Marshal(map[string]any{"type": "message_stop"})
		out, err = dlp.filterEvent(stream.Event{Name: "message_stop", Type: "message_stop", Data: stop})
		if !errors.Is(err, errSecretStreamWindowExceeded) || len(out) != 0 {
			t.Fatalf("Anthropic message_stop did not fail closed: events=%#v err=%v", out, err)
		}
	})
}

func TestSecretDLPStreamCertificateThenRSA4096KeyIn16ByteChunks(t *testing.T) {
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		t.Fatal(err)
	}
	state := &piiRedactionState{rules: rules, limit: 200, result: &piiFilterResult{RuleCounts: map[string]int{}}, placeholderFor: map[string]string{}}
	certificate := "-----BEGIN CERTIFICATE-----\n" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("certificate", 20))) + "\n-----END CERTIFICATE-----\n"
	rsaKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		t.Fatal(err)
	}
	key := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)}))
	if len(key) < 3000 || len(key) > 4000 {
		t.Fatalf("generated 4096-bit RSA PEM size=%d, want about 3.3 KiB", len(key))
	}
	input := "before\n" + certificate + key + "\nafter"
	var pending string
	var output strings.Builder
	for start := 0; start < len(input); start += 16 {
		end := min(start+16, len(input))
		safe, next, err := redactSecretStreamChunk(state, pending, input[start:end], false)
		if err != nil {
			t.Fatalf("feed bytes [%d:%d]: %v", start, end, err)
		}
		assertNoSecretFragment(t, safe, key, 16)
		output.WriteString(safe)
		pending = next
	}
	safe, _, err := redactSecretStreamChunk(state, pending, "", true)
	if err != nil {
		t.Fatal(err)
	}
	output.WriteString(safe)
	if strings.Contains(output.String(), key) || strings.Contains(output.String(), "BEGIN RSA PRIVATE KEY") || !strings.Contains(output.String(), "[SECRET_PRIVATE_KEY_1]") {
		t.Fatalf("certificate-then-key stream leaked the RSA key: output len=%d tail=%q", output.Len(), output.String()[max(0, output.Len()-250):])
	}
	assertNoSecretFragment(t, output.String(), key, 16)
}

func assertNoSecretFragment(t testing.TB, output, secret string, fragmentBytes int) {
	t.Helper()
	if fragmentBytes < 1 {
		return
	}
	for offset := 0; offset+fragmentBytes <= len(secret); offset++ {
		if strings.Contains(output, secret[offset:offset+fragmentBytes]) {
			t.Fatalf("stream leaked a raw secret fragment %q", secret[offset:offset+fragmentBytes])
		}
	}
}

func TestSecretDLPStreamWindowRedactsSplitCredentials(t *testing.T) {
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		t.Fatal(err)
	}
	result := &piiFilterResult{RuleCounts: map[string]int{}}
	state := &piiRedactionState{rules: rules, limit: 200, result: result, placeholderFor: map[string]string{}}
	for _, secret := range []string{fakeGitHubToken(), fakeAWSAccessKey()} {
		t.Run(secret[:4], func(t *testing.T) {
			var pending string
			var output strings.Builder
			for _, fragment := range []string{"prefix ", secret[:len(secret)/2], secret[len(secret)/2:] + " suffix"} {
				safe, next, err := redactSecretStreamChunk(state, pending, fragment, false)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(safe, secret) {
					t.Fatalf("stream window released a split credential: %q", safe)
				}
				output.WriteString(safe)
				pending = next
			}
			safe, pending, err := redactSecretStreamChunk(state, pending, "", true)
			if err != nil {
				t.Fatal(err)
			}
			output.WriteString(safe)
			output.WriteString(pending)
			if strings.Contains(output.String(), secret) || !strings.Contains(output.String(), "[SECRET_") {
				t.Fatalf("split stream output was not redacted: %q", output.String())
			}
		})
	}
}

func TestSecretDLPStreamWindowHoldsAndBoundsPrivateKey(t *testing.T) {
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		t.Fatal(err)
	}
	state := &piiRedactionState{rules: rules, limit: 200, result: &piiFilterResult{RuleCounts: map[string]int{}}, placeholderFor: map[string]string{}}
	prefix := "before " + strings.Repeat("x", secretStreamTailBytes+32)
	first, pending, err := redactSecretStreamChunk(state, "", prefix+"-----BEGIN PRIVATE KEY-----\n"+strings.Repeat("k", 800), false)
	if err != nil || len(first) == 0 || strings.Contains(first, "BEGIN") || len(pending) < 800 {
		t.Fatalf("private-key block was not held: first=%q pending=%d err=%v", first, len(pending), err)
	}
	if _, _, err := redactSecretStreamChunk(state, pending, strings.Repeat("k", secretStreamPEMMax), false); !errors.Is(err, errSecretStreamWindowExceeded) {
		t.Fatalf("oversized unterminated key error=%v", err)
	}
	if _, _, err := redactSecretStreamChunk(state, "", "-----BEGIN PRIVATE KEY-----\nunfinished", true); !errors.Is(err, errSecretStreamWindowExceeded) {
		t.Fatalf("unterminated key at stream finish error=%v", err)
	}
	if _, _, err := redactSecretStreamChunk(state, "", "ghp_"+strings.Repeat("x", 257), true); !errors.Is(err, errSecretStreamWindowExceeded) {
		t.Fatalf("overlong provider token candidate error=%v", err)
	}
	if _, _, err := redactSecretStreamChunk(state, "", strings.Repeat("A", 300), true); err != nil {
		t.Fatalf("benign long base64-like input unexpectedly failed closed: %v", err)
	}
	state.result = &piiFilterResult{RuleCounts: map[string]int{}}
	state.placeholderFor = map[string]string{}
	var output strings.Builder
	pending = ""
	for _, fragment := range []string{"before ", "-----BEGIN PRIVATE KEY-----\nmaterial\n", "-----END PRIVATE KEY-----", " after"} {
		safe, next, err := redactSecretStreamChunk(state, pending, fragment, false)
		if err != nil {
			t.Fatal(err)
		}
		output.WriteString(safe)
		pending = next
	}
	safe, _, err := redactSecretStreamChunk(state, pending, "", true)
	if err != nil {
		t.Fatal(err)
	}
	output.WriteString(safe)
	if strings.Contains(output.String(), "BEGIN PRIVATE KEY") || !strings.Contains(output.String(), "[SECRET_PRIVATE_KEY_1]") {
		t.Fatalf("split private key was not redacted: %q", output.String())
	}
}

func TestSecretDLPStreamCandidateUsesConfiguredAction(t *testing.T) {
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		t.Fatal(err)
	}
	newState := func() *piiRedactionState {
		return &piiRedactionState{rules: rules, limit: 200, result: &piiFilterResult{RuleCounts: map[string]int{}}, placeholderFor: map[string]string{}}
	}
	secret := fakeGitHubToken()
	redacted, err := (&secretStreamDLP{state: newState(), mode: "redact_only"}).redactCandidate(`{"query":"` + secret + `"}`)
	if err != nil || strings.Contains(redacted, secret) || !strings.Contains(redacted, "[SECRET_GITHUB_1]") {
		t.Fatalf("redact-only candidate=%q err=%v", redacted, err)
	}
	blocked, err := (&secretStreamDLP{state: newState(), mode: "fail_on_match"}).redactCandidate(secret)
	if err == nil || blocked != "" {
		t.Fatalf("fail-on-match candidate=%q err=%v", blocked, err)
	}
	oversized, err := (&secretStreamDLP{state: newState(), mode: "redact_only"}).redactCandidate("ghp_" + strings.Repeat("x", 257))
	if err == nil || oversized != "" {
		t.Fatalf("oversized candidate=%q err=%v", oversized, err)
	}
}

func TestSecretDLPStreamEventFailsOnSplitMatch(t *testing.T) {
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		t.Fatal(err)
	}
	dlp := &secretStreamDLP{state: &piiRedactionState{rules: rules, limit: 200, result: &piiFilterResult{RuleCounts: map[string]int{}}, placeholderFor: map[string]string{}}, mode: "fail_on_match"}
	secret := fakeGitHubToken()
	for _, content := range []string{secret[:len(secret)/2], secret[len(secret)/2:]} {
		payload := map[string]any{"object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": content}, "finish_reason": nil}}}
		data, _ := json.Marshal(payload)
		if _, err := dlp.filterEvent(stream.Event{Data: data}); err != nil {
			t.Fatalf("partial secret unexpectedly failed before completion: %v", err)
		}
	}
	payload := map[string]any{"object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}}
	data, _ := json.Marshal(payload)
	if _, err := dlp.filterEvent(stream.Event{Data: data}); err == nil {
		t.Fatal("fail_on_match streamed credential was not blocked")
	}
}

func TestSecretDLPChatChoicesAndReasoningChannelsAreInspectedIndependently(t *testing.T) {
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		t.Fatal(err)
	}
	dlp := &secretStreamDLP{state: &piiRedactionState{rules: rules, limit: 200, result: &piiFilterResult{RuleCounts: map[string]int{}}, placeholderFor: map[string]string{}}, mode: "redact_only"}
	github := fakeGitHubToken()
	aws := fakeAWSAccessKey()
	jwt := fakeLongJWTToken()
	bearer := "Authorization: Bearer " + strings.Repeat("q", 120)
	makeChatEvent := func(choices ...map[string]any) stream.Event {
		data, marshalErr := json.Marshal(map[string]any{"object": "chat.completion.chunk", "choices": choices})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return stream.Event{Data: data}
	}
	first, err := dlp.filterEvent(makeChatEvent(
		map[string]any{"index": 0, "delta": map[string]any{"content": github[:len(github)/2], "reasoning_content": jwt[:len(jwt)/2]}, "finish_reason": nil},
		map[string]any{"index": 1, "delta": map[string]any{"content": aws[:len(aws)/2], "reasoning_content": bearer[:len(bearer)/2]}, "finish_reason": nil},
	))
	if err != nil || len(first) != 0 {
		t.Fatalf("partial multi-choice secrets emitted early: %#v err=%v", first, err)
	}
	last, err := dlp.filterEvent(makeChatEvent(
		map[string]any{"index": 0, "delta": map[string]any{"content": github[len(github)/2:], "reasoning_content": jwt[len(jwt)/2:]}, "finish_reason": "stop"},
		map[string]any{"index": 1, "delta": map[string]any{"content": aws[len(aws)/2:], "reasoning_content": bearer[len(bearer)/2:]}, "finish_reason": "stop"},
	))
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	for _, event := range append(first, last...) {
		var payload map[string]any
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			t.Fatal(err)
		}
		choices, _ := payload["choices"].([]any)
		if len(choices) != 1 {
			t.Fatalf("expected independent one-choice event, got %#v", payload["choices"])
		}
		output.Write(event.Data)
	}
	for _, secret := range []string{github, aws, jwt, bearer} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("Chat stream leaked secret %q", secret[:min(24, len(secret))])
		}
	}
	for _, placeholder := range []string{"[SECRET_GITHUB_1]", "[SECRET_AWS_1]", "[SECRET_JWT_1]", "[SECRET_BEARER_1]"} {
		if !strings.Contains(output.String(), placeholder) {
			t.Fatalf("Chat stream did not redact a choice/reasoning secret %s: %s", placeholder, output.String())
		}
	}
}

func TestSecretDLPAnthropicThinkingDeltaIsInspected(t *testing.T) {
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		t.Fatal(err)
	}
	dlp := &secretStreamDLP{state: &piiRedactionState{rules: rules, limit: 200, result: &piiFilterResult{RuleCounts: map[string]int{}}, placeholderFor: map[string]string{}}}
	secret := fakeGitHubToken()
	for _, fragment := range []string{secret[:len(secret)/2], secret[len(secret)/2:]} {
		data, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": 3, "delta": map[string]any{"type": "thinking_delta", "thinking": fragment}})
		out, err := dlp.filterEvent(stream.Event{Name: "content_block_delta", Data: data})
		if err != nil || len(out) != 0 {
			t.Fatalf("Anthropic thinking leaked before block stop: %#v err=%v", out, err)
		}
	}
	stop, _ := json.Marshal(map[string]any{"type": "content_block_stop", "index": 3})
	out, err := dlp.filterEvent(stream.Event{Name: "content_block_stop", Data: stop})
	if err != nil || len(out) != 2 {
		t.Fatalf("thinking flush=%#v err=%v", out, err)
	}
	if strings.Contains(string(out[0].Data), secret) || !strings.Contains(string(out[0].Data), "[SECRET_GITHUB_1]") || out[1].Name != "content_block_stop" {
		t.Fatalf("thinking output leaked or reordered: %#v", out)
	}
}

func TestSecretDLPTranslatedStreamRedactsAndSequencesFlush(t *testing.T) {
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		t.Fatal(err)
	}
	dlp := &secretStreamDLP{state: &piiRedactionState{rules: rules, limit: 200, result: &piiFilterResult{RuleCounts: map[string]int{}}, placeholderFor: map[string]string{}}}
	secret := fakeGitHubToken()
	makeEvent := func(eventType string, seq int, payload map[string]any) stream.Event {
		payload["type"] = eventType
		payload["sequence_number"] = seq
		data, _ := json.Marshal(payload)
		return stream.Event{Type: eventType, Data: data}
	}
	for _, fragment := range []string{secret[:len(secret)/2], secret[len(secret)/2:]} {
		out, err := dlp.filterEvent(makeEvent("response.output_text.delta", 5, map[string]any{"delta": fragment, "item_id": "msg_1"}))
		if err != nil || len(out) != 0 {
			t.Fatalf("split delta emitted early: events=%#v err=%v", out, err)
		}
	}
	flushed, err := dlp.filterEvent(makeEvent("response.output_text.done", 6, map[string]any{"text": secret, "item_id": "msg_1"}))
	if err != nil || len(flushed) != 2 {
		t.Fatalf("snapshot flush events=%d err=%v", len(flushed), err)
	}
	completed, err := dlp.filterEvent(makeEvent("response.completed", 7, map[string]any{"response": map[string]any{"output": []any{map[string]any{"content": []any{map[string]any{"type": "output_text", "text": secret}}}}}}))
	if err != nil || len(completed) != 1 {
		t.Fatalf("completion events=%d err=%v", len(completed), err)
	}
	all := append(flushed, completed...)
	lastSequence := 0
	var serialized strings.Builder
	for _, event := range all {
		var payload map[string]any
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			t.Fatal(err)
		}
		sequence, ok := numberAsInt(payload["sequence_number"])
		if !ok || sequence <= lastSequence {
			t.Fatalf("non-monotonic sequence number %v after %d", payload["sequence_number"], lastSequence)
		}
		lastSequence = sequence
		serialized.Write(event.Data)
	}
	if strings.Contains(serialized.String(), secret) || !strings.Contains(serialized.String(), "[SECRET_GITHUB_1]") {
		t.Fatalf("translated stream retained split secret: %s", serialized.String())
	}
}

func TestSecretDLPResponsesTextChannelsStayIndependent(t *testing.T) {
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		t.Fatal(err)
	}
	dlp := &secretStreamDLP{state: &piiRedactionState{rules: rules, limit: 200, result: &piiFilterResult{RuleCounts: map[string]int{}}, placeholderFor: map[string]string{}}}
	secret := fakeGitHubToken()
	half := len(secret) / 2
	makeEvent := func(item, delta string, sequence, outputIndex int) stream.Event {
		data, _ := json.Marshal(map[string]any{"type": "response.output_text.delta", "item_id": item, "output_index": outputIndex, "content_index": 0, "delta": delta, "sequence_number": sequence})
		return stream.Event{Type: "response.output_text.delta", Data: data}
	}
	var output strings.Builder
	for _, event := range []stream.Event{
		makeEvent("msg_a", secret[:half], 0, 0),
		makeEvent("msg_b", "unrelated safe output", 1, 1),
		makeEvent("msg_a_changed", secret[half:], 2, 0),
	} {
		filtered, err := dlp.filterEvent(event)
		if err != nil {
			t.Fatal(err)
		}
		for _, frame := range filtered {
			output.Write(frame.Data)
		}
	}
	for _, item := range []struct {
		id    string
		index int
	}{{"msg_a", 0}, {"msg_b", 1}} {
		text := "unrelated safe output"
		if item.id == "msg_a" {
			text = secret
		}
		data, _ := json.Marshal(map[string]any{"type": "response.output_text.done", "item_id": item.id, "output_index": item.index, "content_index": 0, "text": text, "sequence_number": 3})
		filtered, err := dlp.filterEvent(stream.Event{Type: "response.output_text.done", Data: data})
		if err != nil {
			t.Fatal(err)
		}
		for _, frame := range filtered {
			output.Write(frame.Data)
		}
	}
	if strings.Contains(output.String(), secret) || !strings.Contains(output.String(), "[SECRET_GITHUB_1]") {
		t.Fatalf("interleaved Responses channels leaked secret or missed redaction: %s", output.String())
	}
}

func TestSecretDLPResponsesTextChannelLimitFailsClosed(t *testing.T) {
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		t.Fatal(err)
	}
	dlp := &secretStreamDLP{state: &piiRedactionState{rules: rules, limit: 200, result: &piiFilterResult{RuleCounts: map[string]int{}}, placeholderFor: map[string]string{}}}
	for index := 0; index < maxResponsesTextChannels; index++ {
		data, _ := json.Marshal(map[string]any{"type": "response.output_text.delta", "output_index": index, "content_index": 0, "delta": "safe"})
		if _, err := dlp.filterEvent(stream.Event{Type: "response.output_text.delta", Data: data}); err != nil {
			t.Fatalf("channel %d failed before bound: %v", index, err)
		}
	}
	data, _ := json.Marshal(map[string]any{"type": "response.output_text.delta", "output_index": maxResponsesTextChannels, "content_index": 0, "delta": "unsafe"})
	if _, err := dlp.filterEvent(stream.Event{Type: "response.output_text.delta", Data: data}); !errors.Is(err, errSecretStreamWindowExceeded) {
		t.Fatalf("channel limit error=%v, want %v", err, errSecretStreamWindowExceeded)
	}
}

func TestSecretDLPNativeStreamPreservesSSEMetadataWhenRewriting(t *testing.T) {
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		t.Fatal(err)
	}
	dlp := &secretStreamDLP{state: &piiRedactionState{rules: rules, limit: 200, result: &piiFilterResult{RuleCounts: map[string]int{}}, placeholderFor: map[string]string{}}}
	payload := map[string]any{"object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": strings.Repeat("safe text ", 1800)}, "finish_reason": nil}}}
	data, _ := json.Marshal(payload)
	frame := append([]byte("id: event-17\nretry: 1000\nevent: message\ndata: "), data...)
	frame = append(frame, []byte("\n\n")...)
	frames, err := filterNativeSecretStreamFrame(frame, dlp)
	if err != nil || len(frames) != 1 {
		t.Fatalf("rewritten frames=%d err=%v", len(frames), err)
	}
	for _, metadata := range []string{"id: event-17", "retry: 1000", "event: message"} {
		if !strings.Contains(string(frames[0]), metadata) {
			t.Fatalf("rewritten frame dropped %q: %q", metadata, frames[0])
		}
	}
}

func TestSecretDLPStreamFlushesTextBeforeChatFinishAndAnthropicBlockStop(t *testing.T) {
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		t.Fatal(err)
	}
	newDLP := func() *secretStreamDLP {
		return &secretStreamDLP{state: &piiRedactionState{rules: rules, limit: 200, result: &piiFilterResult{RuleCounts: map[string]int{}}, placeholderFor: map[string]string{}}}
	}
	secret := fakeGitHubToken()
	chat := newDLP()
	chatPayload := map[string]any{"object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": secret}, "finish_reason": "stop"}}}
	chatData, _ := json.Marshal(chatPayload)
	chatEvents, err := chat.filterEvent(stream.Event{Data: chatData})
	if err != nil || len(chatEvents) != 2 {
		t.Fatalf("chat events=%#v err=%v", chatEvents, err)
	}
	var chatContent, chatFinish map[string]any
	_ = json.Unmarshal(chatEvents[0].Data, &chatContent)
	_ = json.Unmarshal(chatEvents[1].Data, &chatFinish)
	if secretStreamChatTerminal(chatContent) || !secretStreamChatTerminal(chatFinish) || strings.Contains(string(chatEvents[0].Data), secret) || strings.Contains(string(chatEvents[1].Data), secret) || !strings.Contains(string(chatEvents[0].Data), "[SECRET_GITHUB_1]") {
		t.Fatalf("chat terminal ordering/content invalid: first=%s second=%s", chatEvents[0].Data, chatEvents[1].Data)
	}

	anthropic := newDLP()
	delta, _ := json.Marshal(map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": secret}})
	if out, err := anthropic.filterEvent(stream.Event{Name: "content_block_delta", Data: delta}); err != nil || len(out) != 0 {
		t.Fatalf("Anthropic text delta emitted before held tail: %#v err=%v", out, err)
	}
	stop, _ := json.Marshal(map[string]any{"type": "content_block_stop", "index": 0})
	flushed, err := anthropic.filterEvent(stream.Event{Name: "content_block_stop", Data: stop})
	if err != nil || len(flushed) != 2 || flushed[0].Name != "content_block_delta" || flushed[1].Name != "content_block_stop" || strings.Contains(string(flushed[0].Data), secret) || !strings.Contains(string(flushed[0].Data), "[SECRET_GITHUB_1]") {
		t.Fatalf("Anthropic block stop order/content invalid: events=%#v err=%v", flushed, err)
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

func TestSecretDLPAnthropicInitialTextThroughHandler(t *testing.T) {
	secret := fakeGitHubToken()
	frames := []string{
		`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"anthropic-model","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"` + secret[:len(secret)/2] + `"}}`,
		`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + secret[len(secret)/2:] + `"}}`,
		`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
		`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, frame := range frames {
			_, _ = io.WriteString(w, frame+"\n\n")
		}
	}))
	defer upstream.Close()
	target := Target{Provider: "native", Model: "anthropic-model", ToolSupport: ToolSupport{AnthropicMessages: []string{"client_tools"}}}
	svc := nativeStreamTestService(t, upstream.URL, "anthropic", []Target{target}, func(cfg *Config) {
		group := cfg.Models["native-stream"]
		group.PIIFilter.Enabled = true
		group.PIIFilter.SecretsEnabled = true
		cfg.Models["native-stream"] = group
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"native-stream","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	body := response.Body.String()
	if response.Code != http.StatusOK || strings.Contains(body, secret) || !strings.Contains(body, "[SECRET_GITHUB_1]") {
		t.Fatalf("status=%d leaked or missed redaction in streamed body: %s", response.Code, body)
	}
	if !strings.Contains(body, "event: content_block_start") {
		t.Fatalf("text block start was dropped while holding a partial secret: %s", body)
	}
}

func TestSecretDLPAnthropicInitialThinkingThroughHandler(t *testing.T) {
	secret := fakeGitHubToken()
	frames := []string{
		`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"anthropic-model","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"` + secret[:len(secret)/2] + `"}}`,
		`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"` + secret[len(secret)/2:] + `"}}`,
		`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
		`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, frame := range frames {
			_, _ = io.WriteString(w, frame+"\n\n")
		}
	}))
	defer upstream.Close()
	target := Target{Provider: "native", Model: "anthropic-model", ToolSupport: ToolSupport{AnthropicMessages: []string{"client_tools"}}}
	svc := nativeStreamTestService(t, upstream.URL, "anthropic", []Target{target}, func(cfg *Config) {
		group := cfg.Models["native-stream"]
		group.PIIFilter.Enabled = true
		group.PIIFilter.SecretsEnabled = true
		cfg.Models["native-stream"] = group
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"native-stream","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	body := response.Body.String()
	if response.Code != http.StatusOK || strings.Contains(body, secret) || !strings.Contains(body, "[SECRET_GITHUB_1]") {
		t.Fatalf("status=%d leaked or missed redaction in streamed body: %s", response.Code, body)
	}
	if !strings.Contains(body, "event: content_block_start") {
		t.Fatalf("thinking block start was dropped while holding a partial secret: %s", body)
	}
}

func TestSecretDLPAnthropicStartWithoutDeltaFlushesDeltaThroughHandler(t *testing.T) {
	secretPrefix := fakeGitHubToken()[:20]
	for _, tc := range []struct {
		name, blockType, field, deltaType, deltaField string
	}{
		{name: "text", blockType: "text", field: "text", deltaType: "text_delta", deltaField: "text"},
		{name: "thinking", blockType: "thinking", field: "thinking", deltaType: "thinking_delta", deltaField: "thinking"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frames := []string{
				`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"anthropic-model","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
				fmt.Sprintf(`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":%q,%q:%q}}`, tc.blockType, tc.field, secretPrefix),
				`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
				`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, frame := range frames {
					_, _ = io.WriteString(w, frame+"\n\n")
				}
			}))
			defer upstream.Close()
			target := Target{Provider: "native", Model: "anthropic-model", ToolSupport: ToolSupport{AnthropicMessages: []string{"client_tools"}}}
			svc := nativeStreamTestService(t, upstream.URL, "anthropic", []Target{target}, func(cfg *Config) {
				group := cfg.Models["native-stream"]
				group.PIIFilter.Enabled = true
				group.PIIFilter.SecretsEnabled = true
				cfg.Models["native-stream"] = group
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"native-stream","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`))
			req.Header.Set("Authorization", "Bearer "+testToken)
			response := httptest.NewRecorder()
			svc.Handler().ServeHTTP(response, req)
			body := response.Body.String()
			if response.Code != http.StatusOK || strings.Count(body, "event: content_block_start") != 1 {
				t.Fatalf("status=%d content_block_start count=%d: %s", response.Code, strings.Count(body, "event: content_block_start"), body)
			}
			if strings.Count(body, "event: content_block_delta") != 1 || !strings.Contains(body, `"type":"`+tc.deltaType+`"`) || !strings.Contains(body, `"`+tc.deltaField+`":"`+secretPrefix+`"`) {
				t.Fatalf("held start suffix was not emitted as one %s: %s", tc.deltaType, body)
			}
		})
	}
}

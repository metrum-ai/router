// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"errors"
	"fmt"
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

func fakeGitHubToken() string  { return "ghp_" + strings.Repeat("1", 36) }
func fakeAWSAccessKey() string { return "AKIA" + strings.Repeat("1", 16) }
func fakeJWTToken() string {
	return "eyJhbGciOiJIUzI1NiJ9." + "eyJzdWIiOiIxMjM0NTY3ODkwIn0." + "signaturevalue123456"
}
func fakeCredentialAssignment() string {
	return "api_key=" + strings.Repeat("a", 16) + "Q7r9W2x4"
}
func fakePrivateKeyBlock() string {
	return "-----BEGIN " + "PRIVATE KEY-----\nmaterial\n-----END " + "PRIVATE KEY-----"
}

func TestSecretDLPRequestAndUnaryResponseE2E(t *testing.T) {
	key := fakeGitHubToken()
	toolResultKey := fakeAWSAccessKey()
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		serialized, _ := json.Marshal(body["messages"])
		if strings.Contains(string(serialized), key) || strings.Contains(string(serialized), toolResultKey) ||
			!strings.Contains(string(serialized), "[SECRET_GITHUB_1]") || !strings.Contains(string(serialized), "[SECRET_AWS_1]") {
			t.Errorf("upstream prompt or tool result did not redact credentials: %s", serialized)
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "secret-dlp", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "returned " + key}}}, "usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}})
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
	body := `{"model":"default","messages":[{"role":"user","content":"` + promptPrefix + key + promptSuffix + `"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_secret","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"` + toolResultPrefix + toolResultKey + toolResultSuffix + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
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
	for _, sensitive := range []string{key, toolResultKey, promptPrefix, promptSuffix, toolResultPrefix, toolResultSuffix} {
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
	for _, sensitive := range []string{key, toolResultKey, promptPrefix, promptSuffix, toolResultPrefix, toolResultSuffix} {
		if strings.Contains(string(serializedRows), sensitive) {
			t.Fatalf("usage database rows contain sensitive request data %q", sensitive)
		}
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
	input := strings.Repeat("ordinary streamed response text ", 80) + fakeGitHubToken() + strings.Repeat(" tail text", 80)
	fragments := make([]string, 0, len(input)/128+1)
	for len(input) > 128 {
		fragments = append(fragments, input[:128])
		input = input[128:]
	}
	fragments = append(fragments, input)
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
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","stream":true,"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "secret-dlp-stream-inspection-required") || calls.Load() != 0 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, calls.Load(), response.Body.String())
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
		{"authorization_bearer", "Authorization: Bearer abcdefghijklmnopqrstuvwxyz123456", "[SECRET_BEARER_1]"},
		{"authorization_bearer_benign", "Authorization: Basic abcdefghijklmnopqrstuvwxyz123456", "Authorization: Basic abcdefghijklmnopqrstuvwxyz123456"},
		{"credential_assignment", fakeCredentialAssignment(), "[SECRET_CREDENTIAL_1]"},
		{"credential_assignment_benign", "password: supplied during the demo", "password: supplied during the demo"},
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
		"Authorization: Bearer abcdefghijklmnopqrstuvwxyz123456",
		fakeCredentialAssignment(),
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
						t.Fatalf("raw secret released before completion: %q", safe)
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
	first, pending, err := redactSecretStreamChunk(state, "", "before "+"-----BEGIN PRIVATE KEY-----\n"+strings.Repeat("k", 800), false)
	if err != nil || first != "before " || strings.Contains(first, "BEGIN") || len(pending) < 800 {
		t.Fatalf("private-key block was not held: first=%q pending=%d err=%v", first, len(pending), err)
	}
	if _, _, err := redactSecretStreamChunk(state, pending, strings.Repeat("k", secretStreamPEMMax), false); !errors.Is(err, errSecretStreamWindowExceeded) {
		t.Fatalf("oversized unterminated key error=%v", err)
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

func TestSecretDLPNativeStreamPreservesSSEMetadataWhenRewriting(t *testing.T) {
	rules, err := compilePIIRules(builtinSecretRules())
	if err != nil {
		t.Fatal(err)
	}
	dlp := &secretStreamDLP{state: &piiRedactionState{rules: rules, limit: 200, result: &piiFilterResult{RuleCounts: map[string]int{}}, placeholderFor: map[string]string{}}}
	payload := map[string]any{"object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": strings.Repeat("safe text ", 500)}, "finish_reason": nil}}}
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

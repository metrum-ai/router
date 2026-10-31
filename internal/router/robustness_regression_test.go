// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFallbackOrdering429RetryAfterTelemetryAndSecretRedaction(t *testing.T) {
	const providerKey = "sk-provider-robustness-secret"
	const tokenHash = "sha256-secret-token-hash"
	var selected []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		model := stringValue(body["model"])
		selected = append(selected, model)
		switch model {
		case "rate-limited-model":
			w.Header().Set("Retry-After", "2")
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error": map[string]any{
					"message":          "provider throttled account with key " + providerKey,
					"provider_api_key": providerKey,
					"token_hash":       tokenHash,
				},
			})
		case "bad-gateway-model":
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": map[string]any{"message": "temporary upstream failure " + providerKey},
			})
		case "healthy-model":
			writeJSON(w, http.StatusOK, map[string]any{
				"id": "robust_fallback_ok",
				"choices": []map[string]any{{
					"message":       map[string]any{"role": "assistant", "content": "fallback recovered"},
					"finish_reason": "stop",
				}},
				"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5},
			})
		default:
			t.Fatalf("unexpected model %q", model)
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	enabled := true
	honorRetryAfter := true
	cfg := testConfig(t, upstream.URL, providerKey, dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	cfg.Provider["mock"] = ProviderConfig{
		BaseURL: upstream.URL + "/v1",
		Dialect: "openai-chat",
		APIKey:  providerKey,
		TrafficShape: TrafficShapeConfig{
			Enabled: &enabled,
			Upstream429Backoff: TrafficBackoffConfig{
				Enabled:         &enabled,
				MinBackoffMS:    100,
				MaxBackoffMS:    1000,
				Multiplier:      2,
				HonorRetryAfter: &honorRetryAfter,
			},
		},
	}
	cfg.Provider["fallback"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: providerKey}
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "mock", Model: "rate-limited-model"},
		{Provider: "fallback", Model: "bad-gateway-model"},
		{Provider: "fallback", Model: "healthy-model"},
	}}
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	rr := performChatRequest(t, svc, `{"model":"default","messages":[{"role":"user","content":"retry safely"}]}`)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "fallback recovered") {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got, want := strings.Join(selected, ","), "rate-limited-model,bad-gateway-model,healthy-model"; got != want {
		t.Fatalf("fallback order=%s, want %s", got, want)
	}

	var attempts []requestAttemptRecord
	flushUsageForTest(t, svc)
	if err := svc.usage.db.Order("attempt_index ASC").Find(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 3 {
		t.Fatalf("attempt rows=%d: %#v", len(attempts), attempts)
	}
	if attempts[0].StatusCode != http.StatusTooManyRequests || attempts[0].ErrorClass != "upstream_rate_limited" ||
		!attempts[0].Retryable || attempts[0].RetryAfterMS <= 0 || attempts[0].FallbackReason != "upstream_rate_limited" {
		t.Fatalf("unexpected 429 attempt telemetry: %#v", attempts[0])
	}
	if attempts[1].StatusCode != http.StatusBadGateway || attempts[1].ErrorClass != "upstream_status_5xx" ||
		!attempts[1].Retryable || attempts[1].FallbackReason != "upstream_status_5xx" {
		t.Fatalf("unexpected 5xx attempt telemetry: %#v", attempts[1])
	}
	if !attempts[2].Selected || attempts[2].Model != "healthy-model" || attempts[2].ErrorClass != "" {
		t.Fatalf("unexpected selected attempt telemetry: %#v", attempts[2])
	}

	var transitions []fallbackTransitionRecord
	flushUsageForTest(t, svc)
	if err := svc.usage.db.Order("seq ASC").Find(&transitions).Error; err != nil {
		t.Fatal(err)
	}
	if len(transitions) != 2 {
		t.Fatalf("fallback transitions=%d: %#v", len(transitions), transitions)
	}
	if transitions[0].FallbackReason != "upstream_rate_limited" || transitions[0].FallbackCandidateIndex != 1 || transitions[0].FallbackSucceeded {
		t.Fatalf("unexpected first transition: %#v", transitions[0])
	}
	if transitions[1].FallbackReason != "upstream_status_5xx" || transitions[1].FallbackCandidateIndex != 2 || !transitions[1].FallbackSucceeded {
		t.Fatalf("unexpected second transition: %#v", transitions[1])
	}

	var cooldown requestUpstreamShapeEventRecord
	flushUsageForTest(t, svc)
	if err := svc.usage.db.Where("decision = ?", shapeDecisionCooldownStarted).First(&cooldown).Error; err != nil {
		t.Fatal(err)
	}
	if cooldown.BackoffReason != "adaptive-backoff-provider-429" || cooldown.RetryAfterMS <= 0 || cooldown.RetryAfterMS > 1000 {
		t.Fatalf("unexpected cooldown telemetry: %#v", cooldown)
	}

	assertNoSecretInRobustnessArtifacts(t, svc, dir, []string{providerKey, tokenHash, testToken})
}

func TestFallbackDoesNotCrossToolEligibility(t *testing.T) {
	var plainCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		switch stringValue(body["model"]) {
		case "tool-model":
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"message": "temporary tool target failure"}})
		case "plain-model":
			plainCalls.Add(1)
			writeChatTestResponse(w, "plain should not receive tool request")
		default:
			t.Fatalf("unexpected model %v", body["model"])
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Provider["mock"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "mock", Model: "tool-model", ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}},
		{Provider: "mock", Model: "plain-model"},
	}}
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"default","messages":[{"role":"user","content":"call a tool"}],"tools":[{"type":"function","function":{"name":"lookup","description":"lookup","parameters":{"type":"object","properties":{},"additionalProperties":false}}}],"tool_choice":"auto"}`
	rr := performChatRequest(t, svc, body)
	if rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), `"type":"upstream-failed"`) {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if plainCalls.Load() != 0 {
		t.Fatalf("plain fallback target received %d tool request(s)", plainCalls.Load())
	}
	var attempts []requestAttemptRecord
	flushUsageForTest(t, svc)
	if err := svc.usage.db.Find(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Model != "tool-model" || attempts[0].FallbackReason != "" {
		t.Fatalf("unexpected attempts: %#v", attempts)
	}
	var transitions []fallbackTransitionRecord
	flushUsageForTest(t, svc)
	if err := svc.usage.db.Find(&transitions).Error; err != nil {
		t.Fatal(err)
	}
	if len(transitions) != 0 {
		t.Fatalf("unexpected fallback transitions: %#v", transitions)
	}
}

func TestCallerRPMErrorIsSafeAndSkipsUpstream(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeChatTestResponse(w, "ok")
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Callers[0].Rate.RPM = 1
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	first := performChatRequest(t, svc, `{"model":"default","messages":[{"role":"user","content":"one"}]}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	second := performChatRequest(t, svc, `{"model":"default","messages":[{"role":"user","content":"two"}]}`)
	if second.Code != http.StatusTooManyRequests || !strings.Contains(second.Body.String(), `"type":"rpm-exceeded"`) {
		t.Fatalf("second status=%d body=%s", second.Code, second.Body.String())
	}
	if second.Header().Get("Retry-After") != "60" {
		t.Fatalf("Retry-After=%q, want 60", second.Header().Get("Retry-After"))
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls=%d, want only the admitted request", calls.Load())
	}
	if strings.Contains(second.Body.String(), "provider-key") || strings.Contains(second.Body.String(), testToken) {
		t.Fatalf("caller rate-limit response leaked secret material: %s", second.Body.String())
	}
	var errors []requestErrorRecord
	flushUsageForTest(t, svc)
	if err := svc.usage.db.Where("error_type = ?", "rpm-exceeded").Find(&errors).Error; err != nil {
		t.Fatal(err)
	}
	if len(errors) != 1 || errors[0].Status != http.StatusTooManyRequests || !errors[0].Retryable || errors[0].Attempts != 0 {
		t.Fatalf("unexpected rpm error telemetry: %#v", errors)
	}
}

func assertNoSecretInRobustnessArtifacts(t *testing.T, svc *Service, dir string, forbidden []string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, "requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range forbidden {
		if strings.Contains(string(body), secret) {
			t.Fatalf("request log leaked %q: %s", secret, body)
		}
	}
	var attempts []requestAttemptRecord
	flushUsageForTest(t, svc)
	if err := svc.usage.db.Find(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	for _, attempt := range attempts {
		for _, secret := range forbidden {
			if strings.Contains(attempt.ErrorMessage, secret) {
				t.Fatalf("attempt row leaked %q: %#v", secret, attempt)
			}
		}
	}
}

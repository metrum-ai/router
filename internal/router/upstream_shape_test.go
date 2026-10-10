// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTrafficShapeValidation(t *testing.T) {
	cfg := minimalConfig(t)
	provider := cfg.Provider["mock"]
	provider.TrafficShape = TrafficShapeConfig{RequestStartPerSec: 1}
	cfg.Provider["mock"] = provider
	cfg.setDefaults()
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "request_burst is required") {
		t.Fatalf("Validate error=%v, want request_burst requirement", err)
	}

	provider.TrafficShape.RequestBurst = 2
	provider.Models = map[string]ProviderModel{
		"mock": {
			Model: "mock-model",
			TrafficShape: TrafficShapeConfig{
				InputTokensPerSec: 10,
				InputTokenBurst:   20,
			},
		},
	}
	cfg.Provider["mock"] = provider
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider: "mock",
		ModelRef: "mock",
		TrafficShape: TrafficShapeConfig{
			TotalReservedTokensPerSec: 10,
			TotalReservedTokenBurst:   50,
		},
	}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate returned error for valid traffic_shape: %v", err)
	}
}

func TestTrafficShapeManagerConcurrentAdmission(t *testing.T) {
	m := newUpstreamShapeManager()
	fixed := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return fixed }
	scope := shapeScope{
		Scope:    shapeScopeProvider,
		Key:      "provider|mock|openai-chat",
		Config:   TrafficShapeConfig{RequestStartPerSec: 1, RequestBurst: 1},
		Provider: "mock",
		Model:    "mock-model",
		Dialect:  "openai-chat",
	}
	var admitted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if m.Admit([]shapeScope{scope}, shapeReservationInput{EstimatedInputTokens: 1, TotalReservedTokens: 1}).OK {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := admitted.Load(); got != 1 {
		t.Fatalf("admitted=%d, want exactly one burst admission", got)
	}
}

func TestProviderTrafficShapeRoutesAroundThrottledTarget(t *testing.T) {
	var calls atomic.Int64
	var fallbackCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		model, _ := body["model"].(string)
		if model == "fallback-model" {
			fallbackCalls.Add(1)
		}
		writeChatTestResponse(w, model+" ok")
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	enableUpstreamShapeUsageDB(t, cfg)
	cfg.Server.Cache.Enabled = false
	cfg.Provider["mock"] = ProviderConfig{
		BaseURL: upstream.URL + "/v1",
		Dialect: "openai-chat",
		APIKey:  "provider-key",
		TrafficShape: TrafficShapeConfig{
			RequestStartPerSec: 0.1,
			RequestBurst:       1,
		},
	}
	cfg.Provider["fallback"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "mock", Model: "mock-model"},
		{Provider: "fallback", Model: "fallback-model"},
	}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	first := performChatRequest(t, svc, `{"model":"default","messages":[{"role":"user","content":"one"}]}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	second := performChatRequest(t, svc, `{"model":"default","messages":[{"role":"user","content":"two"}]}`)
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), "fallback-model ok") {
		t.Fatalf("second status/body=%d %s, want fallback success", second.Code, second.Body.String())
	}
	if fallbackCalls.Load() != 1 {
		t.Fatalf("fallback calls=%d, want 1", fallbackCalls.Load())
	}
	var events []requestUpstreamShapeEventRecord
	if err := svc.usage.db.Where("decision = ?", shapeDecisionSkipped).Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[0].BackoffReason != "provider-shape-throttled" {
		t.Fatalf("shape events=%#v, want provider-shape-throttled skip", events)
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls=%d, want first mock plus fallback", calls.Load())
	}
}

func TestProviderTrafficShapeAllTargetsThrottled(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeChatTestResponse(w, "ok")
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	enableUpstreamShapeUsageDB(t, cfg)
	cfg.Server.Cache.Enabled = false
	cfg.Provider["mock"] = ProviderConfig{
		BaseURL: upstream.URL + "/v1",
		Dialect: "openai-chat",
		APIKey:  "provider-key",
		TrafficShape: TrafficShapeConfig{
			RequestStartPerSec: 0.1,
			RequestBurst:       1,
		},
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	if rr := performChatRequest(t, svc, `{"model":"default","messages":[{"role":"user","content":"one"}]}`); rr.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", rr.Code, rr.Body.String())
	}
	rr := performChatRequest(t, svc, `{"model":"default","messages":[{"role":"user","content":"two"}]}`)
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), `"type":"upstream-capacity-throttled"`) {
		t.Fatalf("second status/body=%d %s, want capacity throttle", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Fatalf("Retry-After header missing on capacity throttle")
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls=%d, want no second upstream call", calls.Load())
	}
	var reqErr requestErrorRecord
	if err := svc.usage.db.Where("error_type = ?", "upstream-capacity-throttled").First(&reqErr).Error; err != nil {
		t.Fatal(err)
	}
	if !reqErr.Retryable {
		t.Fatalf("retryable=%v, want capacity throttle request error marked retryable", reqErr.Retryable)
	}
}

func TestProviderTrafficShapeCacheHitBypassesCapacity(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeChatTestResponse(w, "cached ok")
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	enableUpstreamShapeUsageDB(t, cfg)
	cfg.Server.Cache.Enabled = true
	cfg.Provider["mock"] = ProviderConfig{
		BaseURL: upstream.URL + "/v1",
		Dialect: "openai-chat",
		APIKey:  "provider-key",
		TrafficShape: TrafficShapeConfig{
			RequestStartPerSec: 0.1,
			RequestBurst:       1,
		},
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"default","temperature":0,"messages":[{"role":"user","content":"same deterministic prompt"}]}`
	first := performChatRequest(t, svc, body)
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	second := performChatRequest(t, svc, body)
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), "cached ok") {
		t.Fatalf("second status/body=%d %s, want cache hit bypassing shape capacity", second.Code, second.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls=%d, want second request served from cache", calls.Load())
	}
	var skipped int64
	if err := svc.usage.db.Model(&requestUpstreamShapeEventRecord{}).Where("decision = ?", shapeDecisionSkipped).Count(&skipped).Error; err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Fatalf("shape skipped events=%d, want cache hit to bypass capacity shaping", skipped)
	}
	var rows []usageRecord
	if err := svc.usage.db.Order("ts asc").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Cache != "miss" || rows[1].Cache != "hit" {
		t.Fatalf("cache states=%#v, want miss then hit", rows)
	}
}

func TestAdaptiveBackoffHonorsBoundedRetryAfter(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "120")
		http.Error(w, `{"error":{"message":"rate limited"}}`, http.StatusTooManyRequests)
	}))
	defer upstream.Close()

	enabled := true
	honor := true
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	enableUpstreamShapeUsageDB(t, cfg)
	cfg.Server.Cache.Enabled = false
	cfg.Provider["mock"] = ProviderConfig{
		BaseURL: upstream.URL + "/v1",
		Dialect: "openai-chat",
		APIKey:  "provider-key",
		TrafficShape: TrafficShapeConfig{
			Enabled: &enabled,
			Upstream429Backoff: TrafficBackoffConfig{
				Enabled:         &enabled,
				MinBackoffMS:    100,
				MaxBackoffMS:    1000,
				Multiplier:      2,
				HonorRetryAfter: &honor,
			},
		},
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	// Pin the shaping clock so the 1s cooldown cannot lapse in wall-clock time
	// while a loaded host is still writing usage rows between requests.
	base := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	var offset atomic.Int64
	svc.shaping.now = func() time.Time { return base.Add(time.Duration(offset.Load())) }

	first := performChatRequest(t, svc, `{"model":"default","messages":[{"role":"user","content":"one"}]}`)
	if first.Code != http.StatusServiceUnavailable || !strings.Contains(first.Body.String(), `"type":"upstream-rate-limited"`) {
		t.Fatalf("first status/body=%d %s, want upstream rate limited", first.Code, first.Body.String())
	}
	second := performChatRequest(t, svc, `{"model":"default","messages":[{"role":"user","content":"two"}]}`)
	if second.Code != http.StatusServiceUnavailable || !strings.Contains(second.Body.String(), `"type":"upstream-capacity-throttled"`) {
		t.Fatalf("second status/body=%d %s, want capacity throttle", second.Code, second.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls=%d, want second request skipped by adaptive backoff", calls.Load())
	}
	var cooldown requestUpstreamShapeEventRecord
	if err := svc.usage.db.Where("decision = ?", shapeDecisionCooldownStarted).First(&cooldown).Error; err != nil {
		t.Fatal(err)
	}
	if cooldown.BackoffReason != "adaptive-backoff-provider-429" || cooldown.RetryAfterMS != 1000 {
		t.Fatalf("cooldown=%#v, want Retry-After 120s bounded to max_backoff_ms 1000", cooldown)
	}

	offset.Store(int64(999 * time.Millisecond))
	third := performChatRequest(t, svc, `{"model":"default","messages":[{"role":"user","content":"three"}]}`)
	if third.Code != http.StatusServiceUnavailable || !strings.Contains(third.Body.String(), `"type":"upstream-capacity-throttled"`) || calls.Load() != 1 {
		t.Fatalf("third status/body/calls=%d %s %d, want still throttled just before the bound", third.Code, third.Body.String(), calls.Load())
	}
	offset.Store(int64(time.Second))
	fourth := performChatRequest(t, svc, `{"model":"default","messages":[{"role":"user","content":"four"}]}`)
	if fourth.Code != http.StatusServiceUnavailable || !strings.Contains(fourth.Body.String(), `"type":"upstream-rate-limited"`) || calls.Load() != 2 {
		t.Fatalf("fourth status/body/calls=%d %s %d, want upstream retried once the bounded cooldown expires", fourth.Code, fourth.Body.String(), calls.Load())
	}
}

func performChatRequest(t *testing.T, svc *Service, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	return rr
}

func enableUpstreamShapeUsageDB(t *testing.T, cfg *Config) {
	t.Helper()
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
}

func writeChatTestResponse(w http.ResponseWriter, content string) {
	writeJSON(w, http.StatusOK, map[string]any{
		"id": "chatcmpl_test",
		"choices": []map[string]any{{
			"message": map[string]any{"role": "assistant", "content": content},
		}},
		"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	})
}

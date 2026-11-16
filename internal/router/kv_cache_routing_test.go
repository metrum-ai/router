// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRecomputedInputTokensPreservesUnknownAndClamps(t *testing.T) {
	cached := 40
	if got := recomputedInputTokens(100, &cached); got == nil || *got != 60 {
		t.Fatalf("recomputed=%v, want 60", got)
	}
	tooMany := 120
	if got := recomputedInputTokens(100, &tooMany); got == nil || *got != 0 {
		t.Fatalf("recomputed=%v, want clamped zero", got)
	}
	negative := -5
	if got := recomputedInputTokens(100, &negative); got == nil || *got != 100 {
		t.Fatalf("recomputed=%v, want input clamped to 100", got)
	}
	if got := recomputedInputTokens(100, nil); got != nil {
		t.Fatalf("missing cache evidence became known recompute: %v", *got)
	}
}

func TestSessionAffinityKeyIgnoresChangingPrefixWithinSession(t *testing.T) {
	caller := &callerRuntime{cfg: CallerConfig{ID: "caller-a"}}
	first := &IRRequest{Metadata: map[string]string{"session_id": "session-a"}, Messages: []IRMessage{{Role: "user", Content: "first turn"}}}
	second := &IRRequest{Metadata: map[string]string{"session_id": "session-a"}, Messages: []IRMessage{{Role: "user", Content: "second turn"}}}
	other := &IRRequest{Metadata: map[string]string{"session_id": "session-b"}, Messages: []IRMessage{{Role: "user", Content: "second turn"}}}
	firstKey := dynamicAffinityKey(caller, "group", "openai-chat", first)
	if firstKey == "" || firstKey != dynamicAffinityKey(caller, "group", "openai-chat", second) {
		t.Fatal("same session did not keep one affinity key")
	}
	if firstKey == dynamicAffinityKey(caller, "group", "openai-chat", other) {
		t.Fatal("different session shared affinity key")
	}
	otherCaller := &callerRuntime{cfg: CallerConfig{ID: "caller-b"}}
	if firstKey == dynamicAffinityKey(otherCaller, "group", "openai-chat", first) {
		t.Fatal("same session shared affinity key across callers")
	}
	otherProject := &callerRuntime{cfg: CallerConfig{ID: "caller-a", Project: "project-b"}}
	caller.cfg.Project = "project-a"
	firstKey = dynamicAffinityKey(caller, "group", "openai-chat", first)
	if firstKey == dynamicAffinityKey(otherProject, "group", "openai-chat", first) {
		t.Fatal("same caller and session shared affinity key across projects")
	}
}

func TestTieredKVTargetPreferenceRequiresLongEligibleRequest(t *testing.T) {
	plain := Target{Provider: "mock", Model: "plain"}
	tiered := Target{Provider: "mock", Model: "tiered", TieredKVCache: true}
	short := &IRRequest{Input: "short"}
	service := &Service{}
	if got := service.preferTieredKVTargets("default", []Target{plain, tiered}, short); got[0].Model != "plain" {
		t.Fatalf("short request target=%q", got[0].Model)
	}
	long := &IRRequest{Input: string(make([]byte, 4*longContextKVCacheThresholdTokens))}
	got := service.preferTieredKVTargets("default", []Target{plain, tiered}, long)
	if got[0].Model != "tiered" || got[1].Model != "plain" {
		t.Fatalf("long request order=%q,%q", got[0].Model, got[1].Model)
	}
	if got := service.preferTieredKVTargets("default", []Target{plain}, long); len(got) != 1 || got[0].Model != "plain" {
		t.Fatalf("single eligible target changed: %#v", got)
	}
}

func TestTieredKVTargetPreferenceDemotesUnhealthyTarget(t *testing.T) {
	plain := Target{Provider: "mock", Model: "plain"}
	tiered := Target{Provider: "mock", Model: "tiered", TieredKVCache: true}
	cfg := testConfig(t, "http://127.0.0.1:1", "mock-key", t.TempDir())
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	now := time.Now().UTC()
	for range tieredKVUnhealthyMinObservations {
		svc.observations.add(dynamicObservationKey("default", tiered.Provider, tiered.Model), dynamicObservation{TS: now, Status: http.StatusBadGateway}, defaultDynamicObservationWindow)
	}
	long := &IRRequest{Input: strings.Repeat("x", 4*longContextKVCacheThresholdTokens)}
	got := svc.preferTieredKVTargets("default", []Target{plain, tiered}, long)
	if len(got) != 2 || got[0].Model != "plain" || got[1].Model != "tiered" {
		t.Fatalf("unhealthy tiered target was not demoted: %q,%q", got[0].Model, got[1].Model)
	}
}

func TestTieredKVTargetPreferenceHTTPFallbackAndHealthDemotion(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	var tieredCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream request: %v", err)
		}
		mu.Lock()
		seen = append(seen, body.Model)
		if body.Model == "tiered" {
			tieredCalls++
			if tieredCalls == 1 {
				mu.Unlock()
				writeJSON(w, http.StatusBadGateway, map[string]any{"error": "synthetic upstream failure"})
				return
			}
			if tieredCalls == 2 {
				mu.Unlock()
				time.Sleep(40 * time.Millisecond)
				return
			}
		}
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"id": "kv", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "mock-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	group := cfg.Models["default"]
	plain := group.Targets[0]
	plain.Model = "plain"
	tiered := plain
	tiered.Model = "tiered"
	tiered.TieredKVCache = true
	tiered.TimeoutMS = 10
	group.Strategy = "static"
	group.Targets = []Target{plain, tiered}
	cfg.Models["default"] = group
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(svc.Handler())
	defer server.Close()
	client := server.Client()
	post := func(input string) int {
		t.Helper()
		payload := fmt.Sprintf(`{"model":"default","messages":[{"role":"user","content":%q}]}`, input)
		req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	if status := post("short prompt"); status != http.StatusOK {
		t.Fatalf("short request status=%d", status)
	}
	longPrompt := strings.Repeat("long context ", 11000)
	if status := post(longPrompt); status != http.StatusOK {
		t.Fatalf("upstream error fallback status=%d", status)
	}
	if status := post(longPrompt); status != http.StatusOK {
		t.Fatalf("timeout fallback status=%d", status)
	}
	if status := post(longPrompt); status != http.StatusOK {
		t.Fatalf("unhealthy tiered target demotion status=%d", status)
	}
	mu.Lock()
	got := append([]string(nil), seen...)
	mu.Unlock()
	if len(got) != 6 || got[0] != "plain" || got[1] != "tiered" || got[2] != "plain" || got[3] != "tiered" || got[4] != "plain" || got[5] != "plain" {
		t.Fatalf("upstream target sequence=%v", got)
	}
}

func TestDynamicScoreAffinityHTTPUpstreamErrorPinsTheSuccessfulFallback(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream request: %v", err)
		}
		mu.Lock()
		seen = append(seen, body.Model)
		first := len(seen) == 1
		mu.Unlock()
		if first {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": "synthetic upstream failure"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "fallback", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "mock-key", t.TempDir())
	group := cfg.Models["default"]
	first := group.Targets[0]
	first.Model = "first"
	second := first
	second.Model = "second"
	group.Strategy = "dynamic_score"
	group.Targets = []Target{first, second}
	group.RoutingPolicy.DynamicScore.Affinity = DynamicScoreAffinityConfig{TTLSeconds: 60, MaxEntries: 10}
	cfg.Models["default"] = group
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(svc.Handler())
	defer server.Close()
	post := func(content string) {
		t.Helper()
		payload := fmt.Sprintf(`{"model":"default","metadata":{"session_id":"synthetic-session"},"messages":[{"role":"user","content":%q}]}`, content)
		req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d", resp.StatusCode)
		}
	}
	post("synthetic prompt first turn")
	post("synthetic prompt follow-up")
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 || seen[0] != "first" || seen[1] != "second" || seen[2] != "second" {
		t.Fatalf("successful fallback was not pinned for the next turn: %v", seen)
	}
}

func TestDynamicScoreSessionAffinityHTTPWithChangedPrompt(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream request: %v", err)
		}
		mu.Lock()
		seen = append(seen, body.Model)
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"id": "session", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "mock-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	group := cfg.Models["default"]
	plain := group.Targets[0]
	plain.Model, plain.Weight = "plain", 100
	plain.InputPricePerMillionUSD, plain.OutputPricePerMillionUSD = 10, 10
	cheap := plain
	cheap.Model, cheap.Weight = "cheap", 1
	cheap.InputPricePerMillionUSD, cheap.OutputPricePerMillionUSD = .1, .1
	group.Strategy = "dynamic_score"
	group.Targets = []Target{plain, cheap}
	group.RoutingPolicy.DynamicScore = DynamicScoreConfig{
		MinObservations:           1,
		MaxScoreAdjustmentPercent: 100,
		Affinity:                  DynamicScoreAffinityConfig{TTLSeconds: 600, MaxEntries: 100},
		ScoreTerms:                []DynamicScoreTerm{{Name: "cheap", Expression: "1.0 * cost_score"}},
	}
	cfg.Models["default"] = group
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(svc.Handler())
	defer server.Close()
	client := server.Client()
	const sessionMarker = "SESSION_SECRET_SENTINEL_438"
	const promptMarker = "PROMPT_SECRET_SENTINEL_438"
	post := func(content string) {
		t.Helper()
		payload := fmt.Sprintf(`{"model":"default","metadata":{"session_id":%q},"messages":[{"role":"user","content":%q}]}`, sessionMarker, content)
		req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("status=%d body=%s", resp.StatusCode, body)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	post(promptMarker + " first turn")
	now := time.Now().UTC()
	for _, model := range []string{"plain", "cheap"} {
		svc.observations.add(dynamicObservationKey("default", plain.Provider, model), dynamicObservation{TS: now, Status: http.StatusOK, LatencyMS: 10}, defaultDynamicObservationWindow)
	}
	post(promptMarker + " second turn with changed prefix")
	mu.Lock()
	got := append([]string(nil), seen...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "plain" || got[1] != "plain" {
		t.Fatalf("session moved between turns: %v", got)
	}
	svc.Close()
	for _, path := range []string{cfg.Server.Logging.Path, cfg.Server.UsageDB.Path} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read privacy artifact %s: %v", path, err)
		}
		if strings.Contains(string(contents), promptMarker) || strings.Contains(string(contents), sessionMarker) {
			t.Fatalf("prompt or session marker persisted in %s", path)
		}
	}
}

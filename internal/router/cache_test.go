// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeChatComplexResponsesBypassIRCache(t *testing.T) {
	for _, raw := range []string{
		`{"choices":[{"index":0,"message":{"content":null,"refusal":"no"}}]}`,
		`{"choices":[{"index":0,"message":{"content":"a"}},{"index":1,"message":{"content":"b"}}]}`,
		`{"choices":[{"index":0,"message":{"content":"a"},"logprobs":{"content":[]}}]}`,
	} {
		resp, err := decodeChatPassthrough([]byte(raw), "model")
		if err != nil {
			t.Fatal(err)
		}
		cache := newCache(CacheConfig{Enabled: true, MaxBytes: 4096, DefaultTTL: time.Minute})
		cache.Put("key", resp)
		if _, hit := cache.Get("key"); hit {
			t.Fatalf("lossy IR cache accepted native response: %s", raw)
		}
	}
}

func TestCacheStats(t *testing.T) {
	cache := newCache(CacheConfig{Enabled: true, MaxBytes: 4096, DefaultTTL: time.Minute})
	stats := cache.Stats()
	if !stats.Enabled || stats.Items != 0 || stats.Bytes != 0 || stats.MaxBytes != 4096 || stats.OccupancyPct != 0 {
		t.Fatalf("empty stats = %#v", stats)
	}

	cache.Put("key", &IRResponse{Model: "model", Text: "cached", Usage: Usage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}})
	stats = cache.Stats()
	if !stats.Enabled || stats.Items != 1 || stats.Bytes <= 0 || stats.MaxBytes != 4096 || stats.OccupancyPct <= 0 {
		t.Fatalf("populated stats = %#v", stats)
	}
}

func TestImageRequestsAreNotCacheable(t *testing.T) {
	req := &IRRequest{Messages: []IRMessage{{
		Role:    "user",
		Content: "Read the receipt.",
		Parts: []IRContentPart{
			{Type: "text", Text: "Read the receipt."},
			{Type: "image", ImageURL: receiptImageURL},
		},
	}}}
	if cacheable(req) {
		t.Fatal("image request should bypass cache")
	}
}

func TestCacheKeyDistinguishesOpenAIChatCapField(t *testing.T) {
	maxTokensReq, err := decodeRequest("openai-chat", []byte(`{
		"model": "default",
		"max_tokens": 1,
		"messages": [{"role": "user", "content": "write a long essay"}]
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	maxCompletionTokensReq, err := decodeRequest("openai-chat", []byte(`{
		"model": "default",
		"max_completion_tokens": 1,
		"messages": [{"role": "user", "content": "write a long essay"}]
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	if maxTokensReq.MaxTokens != maxCompletionTokensReq.MaxTokens {
		t.Fatalf("test setup produced different canonical caps: %d vs %d", maxTokensReq.MaxTokens, maxCompletionTokensReq.MaxTokens)
	}

	target := Target{Provider: "openai_chat", Model: "chat-model"}
	maxTokensKey := cacheKey(maxTokensReq, target, "caller-a", "proj", "")
	maxCompletionTokensKey := cacheKey(maxCompletionTokensReq, target, "caller-a", "proj", "")
	if maxTokensKey == maxCompletionTokensKey {
		t.Fatalf("cache key reused across cap fields: %s", maxTokensKey)
	}
}

func TestCacheKeyIncludesCallerAndSamplingFields(t *testing.T) {
	zero := 0.0
	base := &IRRequest{
		Model:       "default",
		Messages:    []IRMessage{{Role: "user", Content: "hi"}},
		Temperature: &zero,
		Raw:         map[string]any{},
	}
	target := Target{Provider: "mock", Model: "m"}
	a := cacheKey(base, target, "caller-a", "proj-1", "")
	b := cacheKey(base, target, "caller-b", "proj-1", "")
	if a == b {
		t.Fatal("cache key must include caller id")
	}
	withTopP := &IRRequest{
		Model:       base.Model,
		Messages:    base.Messages,
		Temperature: &zero,
		Raw:         map[string]any{"top_p": 0.9},
	}
	if cacheKey(base, target, "caller-a", "proj-1", "") == cacheKey(withTopP, target, "caller-a", "proj-1", "") {
		t.Fatal("cache key must include top_p")
	}
	withSeed := &IRRequest{
		Model:       base.Model,
		Messages:    base.Messages,
		Temperature: &zero,
		Raw:         map[string]any{"seed": 7},
	}
	if cacheKey(base, target, "caller-a", "proj-1", "") == cacheKey(withSeed, target, "caller-a", "proj-1", "") {
		t.Fatal("cache key must include seed")
	}
	withPrev := &IRRequest{
		Model:       base.Model,
		Messages:    base.Messages,
		Temperature: &zero,
		Raw:         map[string]any{"previous_response_id": "resp_1"},
	}
	if cacheKey(base, target, "caller-a", "proj-1", "") == cacheKey(withPrev, target, "caller-a", "proj-1", "") {
		t.Fatal("cache key must include previous_response_id")
	}
	withThinking := &IRRequest{
		Model:       base.Model,
		Messages:    base.Messages,
		Temperature: &zero,
		Thinking:    map[string]any{"type": "enabled", "budget_tokens": 256},
		Raw:         map[string]any{},
	}
	if cacheKey(base, target, "caller-a", "proj-1", "") == cacheKey(withThinking, target, "caller-a", "proj-1", "") {
		t.Fatal("cache key must include thinking")
	}
	withFreq := &IRRequest{
		Model:       base.Model,
		Messages:    base.Messages,
		Temperature: &zero,
		Raw:         map[string]any{"frequency_penalty": 0.5},
	}
	if cacheKey(base, target, "caller-a", "proj-1", "") == cacheKey(withFreq, target, "caller-a", "proj-1", "") {
		t.Fatal("cache key must include frequency_penalty")
	}
	withPresence := &IRRequest{
		Model:       base.Model,
		Messages:    base.Messages,
		Temperature: &zero,
		Raw:         map[string]any{"presence_penalty": 0.25},
	}
	if cacheKey(base, target, "caller-a", "proj-1", "") == cacheKey(withPresence, target, "caller-a", "proj-1", "") {
		t.Fatal("cache key must include presence_penalty")
	}
	withBias := &IRRequest{
		Model:       base.Model,
		Messages:    base.Messages,
		Temperature: &zero,
		Raw:         map[string]any{"logit_bias": map[string]any{"42": 1}},
	}
	if cacheKey(base, target, "caller-a", "proj-1", "") == cacheKey(withBias, target, "caller-a", "proj-1", "") {
		t.Fatal("cache key must include logit_bias")
	}
	withChoices := &IRRequest{
		Model:       base.Model,
		Messages:    base.Messages,
		Temperature: &zero,
		Raw:         map[string]any{"n": 2},
	}
	if cacheKey(base, target, "caller-a", "proj-1", "") == cacheKey(withChoices, target, "caller-a", "proj-1", "") {
		t.Fatal("cache key must include n")
	}
}

func TestTenantCacheScopeSharingAndSalt(t *testing.T) {
	base := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	base.Projects = []ProjectConfig{{ID: "alpha"}, {ID: "beta"}}
	callerA := &callerRuntime{cfg: CallerConfig{ID: "caller-a", Project: "alpha"}}
	callerB := &callerRuntime{cfg: CallerConfig{ID: "caller-b", Project: "alpha"}}
	callerOther := &callerRuntime{cfg: CallerConfig{ID: "caller-c", Project: "beta"}}
	req := &IRRequest{Model: "g", Input: "same prompt"}
	rcA, rcB := &requestContext{caller: callerA}, &requestContext{caller: callerB}
	idA, projectA := cacheCallerScope(rcA, base)
	idB, projectB := cacheCallerScope(rcB, base)
	if idA == idB || projectA != projectB {
		t.Fatal("default response cache scope is not caller partitioned")
	}
	keyA := cacheKey(req, Target{Provider: "p", Model: "m"}, idA, projectA, "")
	keyB := cacheKey(req, Target{Provider: "p", Model: "m"}, idB, projectB, "")
	if keyA == keyB {
		t.Fatal("default cache keys crossed callers")
	}
	base.Projects[0].ShareCacheWithinProject = true
	idA, projectA = cacheCallerScope(rcA, base)
	idB, projectB = cacheCallerScope(rcB, base)
	if idA != "" || idB != "" || projectA != "alpha" || projectB != "alpha" {
		t.Fatal("project sharing did not use project-only scope")
	}
	keyA = cacheKey(req, Target{Provider: "p", Model: "m"}, idA, projectA, "")
	keyB = cacheKey(req, Target{Provider: "p", Model: "m"}, idB, projectB, "")
	if keyA != keyB {
		t.Fatal("project sharing did not reuse within project")
	}
	idOther, projectOther := cacheCallerScope(&requestContext{caller: callerOther}, base)
	keyOther := cacheKey(req, Target{Provider: "p", Model: "m"}, idOther, projectOther, "")
	if keyOther == keyA {
		t.Fatal("project sharing crossed tenant boundary")
	}
	secret := []byte(strings.Repeat("s", 32))
	saltA, err := deriveTenantCacheSalt(secret, callerA)
	if err != nil {
		t.Fatal(err)
	}
	saltB, err := deriveTenantCacheSalt(secret, callerB)
	if err != nil {
		t.Fatal(err)
	}
	saltOther, err := deriveTenantCacheSalt(secret, callerOther)
	if err != nil {
		t.Fatal(err)
	}
	if saltA != saltB || saltA == saltOther || strings.Contains(saltA, "alpha") {
		t.Fatal("cache salts are not stable, tenant scoped opaque values")
	}
}

func TestTenantCacheSaltInjectionOverridesClientValue(t *testing.T) {
	body, err := injectTenantCacheSalt([]byte(`{"model":"g","cache_salt":"attacker"}`), ProviderConfig{UpstreamKind: "vllm"}, &callerRuntime{cfg: CallerConfig{ID: "caller", Project: "alpha"}}, []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["cache_salt"] == "attacker" || decoded["cache_salt"] == "" {
		t.Fatal("tenant salt failed to replace caller-controlled value")
	}
	if _, err := injectTenantCacheSalt([]byte(`{}`), ProviderConfig{UpstreamKind: "sglang"}, &callerRuntime{cfg: CallerConfig{ID: "caller"}}, nil); err == nil {
		t.Fatal("missing key allowed unsalted upstream request")
	}
}

func TestTenantCacheSaltKeyConfiguration(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Provider["mock"] = ProviderConfig{BaseURL: "http://127.0.0.1:1", Dialect: "openai", UpstreamKind: "vllm"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("vLLM salt provider accepted without environment key reference")
	}
	cfg.Server.Cache.UpstreamSaltKeyEnv = "ROUTER_PREFIX_CACHE_KEY"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid vLLM salt config rejected: %v", err)
	}
	for _, tc := range []struct {
		name    string
		lookup  func(string) (string, bool)
		wantErr bool
	}{
		{"missing", func(string) (string, bool) { return "", false }, true},
		{"short", func(string) (string, bool) { return strings.Repeat("s", 31), true }, true},
		{"valid", func(string) (string, bool) { return strings.Repeat("s", 32), true }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveCacheSaltKey(cfg.Server.Cache, tc.lookup)
			if (err != nil) != tc.wantErr {
				t.Fatalf("resolve error=%v", err)
			}
		})
	}
}

func TestCacheBypassesUnknownBehaviorChangingRawFields(t *testing.T) {
	zero := 0.0
	req := &IRRequest{
		Model:       "default",
		Messages:    []IRMessage{{Role: "user", Content: "hi"}},
		Temperature: &zero,
		Raw:         map[string]any{"unknown_sampler": true},
	}
	if cacheable(req) {
		t.Fatal("unknown behavior-changing Raw fields must bypass cache")
	}
}

func TestOmittedTemperatureIsNotCacheable(t *testing.T) {
	req := &IRRequest{Messages: []IRMessage{{Role: "user", Content: "hi"}}}
	if cacheable(req) {
		t.Fatal("omitted temperature must not be cacheable")
	}
	zero := 0.0
	req.Temperature = &zero
	if !cacheable(req) {
		t.Fatal("explicit temperature 0 should be cacheable")
	}
	hot := 0.7
	req.Temperature = &hot
	if cacheable(req) {
		t.Fatal("temperature > 0 must not be cacheable")
	}
}

func TestExampleConfigCacheDisabledByDefaultGC8(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "cache:") {
		t.Fatal("config.example.yaml missing cache section")
	}
	// GC-8: sample cache stays off until operators enable caller-scoped caching.
	idx := strings.Index(string(raw), "\n  cache:")
	if idx < 0 {
		t.Fatal("server cache section not found")
	}
	section := string(raw)[idx : idx+200]
	if !strings.Contains(section, "enabled: false") {
		t.Fatalf("example cache default should be false; section=%q", section)
	}
}

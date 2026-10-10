// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	maxTokensKey := cacheKey(maxTokensReq, target, "caller-a", "proj")
	maxCompletionTokensKey := cacheKey(maxCompletionTokensReq, target, "caller-a", "proj")
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
	a := cacheKey(base, target, "caller-a", "proj-1")
	b := cacheKey(base, target, "caller-b", "proj-1")
	if a == b {
		t.Fatal("cache key must include caller id")
	}
	withTopP := &IRRequest{
		Model:       base.Model,
		Messages:    base.Messages,
		Temperature: &zero,
		Raw:         map[string]any{"top_p": 0.9},
	}
	if cacheKey(base, target, "caller-a", "proj-1") == cacheKey(withTopP, target, "caller-a", "proj-1") {
		t.Fatal("cache key must include top_p")
	}
	withSeed := &IRRequest{
		Model:       base.Model,
		Messages:    base.Messages,
		Temperature: &zero,
		Raw:         map[string]any{"seed": 7},
	}
	if cacheKey(base, target, "caller-a", "proj-1") == cacheKey(withSeed, target, "caller-a", "proj-1") {
		t.Fatal("cache key must include seed")
	}
	withPrev := &IRRequest{
		Model:       base.Model,
		Messages:    base.Messages,
		Temperature: &zero,
		Raw:         map[string]any{"previous_response_id": "resp_1"},
	}
	if cacheKey(base, target, "caller-a", "proj-1") == cacheKey(withPrev, target, "caller-a", "proj-1") {
		t.Fatal("cache key must include previous_response_id")
	}
	withThinking := &IRRequest{
		Model:       base.Model,
		Messages:    base.Messages,
		Temperature: &zero,
		Thinking:    map[string]any{"type": "enabled", "budget_tokens": 256},
		Raw:         map[string]any{},
	}
	if cacheKey(base, target, "caller-a", "proj-1") == cacheKey(withThinking, target, "caller-a", "proj-1") {
		t.Fatal("cache key must include thinking")
	}
	withFreq := &IRRequest{
		Model:       base.Model,
		Messages:    base.Messages,
		Temperature: &zero,
		Raw:         map[string]any{"frequency_penalty": 0.5},
	}
	if cacheKey(base, target, "caller-a", "proj-1") == cacheKey(withFreq, target, "caller-a", "proj-1") {
		t.Fatal("cache key must include frequency_penalty")
	}
	withPresence := &IRRequest{
		Model:       base.Model,
		Messages:    base.Messages,
		Temperature: &zero,
		Raw:         map[string]any{"presence_penalty": 0.25},
	}
	if cacheKey(base, target, "caller-a", "proj-1") == cacheKey(withPresence, target, "caller-a", "proj-1") {
		t.Fatal("cache key must include presence_penalty")
	}
	withBias := &IRRequest{
		Model:       base.Model,
		Messages:    base.Messages,
		Temperature: &zero,
		Raw:         map[string]any{"logit_bias": map[string]any{"42": 1}},
	}
	if cacheKey(base, target, "caller-a", "proj-1") == cacheKey(withBias, target, "caller-a", "proj-1") {
		t.Fatal("cache key must include logit_bias")
	}
	withChoices := &IRRequest{
		Model:       base.Model,
		Messages:    base.Messages,
		Temperature: &zero,
		Raw:         map[string]any{"n": 2},
	}
	if cacheKey(base, target, "caller-a", "proj-1") == cacheKey(withChoices, target, "caller-a", "proj-1") {
		t.Fatal("cache key must include n")
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

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"github.com/metrum-ai/router/internal/stream"
	"testing"
)

func TestAnthropicCacheUsageParity(t *testing.T) {
	for _, creation := range []string{`"cache_creation_input_tokens":300`, `"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":200}`} {
		raw := []byte(`{"id":"msg","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":25,"output_tokens":5,"cache_read_input_tokens":2000,` + creation + `}}`)
		unary, err := decodeUpstreamResponse("anthropic", raw, "claude")
		if err != nil {
			t.Fatal(err)
		}
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		native := &IRResponse{}
		start := map[string]any{"type": "message_start", "message": map[string]any{"id": "msg", "usage": p["usage"]}}
		startRaw, _ := json.Marshal(start)
		accumulateNativeSSE(native, "anthropic", append([]byte("data: "), startRaw...))
		bridge := &stream.AnthropicUpstreamToChatCaller{}
		if _, err := bridge.Next(stream.Event{Data: startRaw}); err != nil {
			t.Fatal(err)
		}
		for name, u := range map[string]Usage{"unary": unary.Usage, "native": native.Usage, "bridge": {InputTokens: bridge.UsageSnapshot().InputTokens, CachedInputTokens: bridge.UsageSnapshot().CachedInputTokens, CacheWriteInputTokens: bridge.UsageSnapshot().CacheWriteInputTokens}} {
			if u.InputTokens != 2325 || u.CachedInputTokens == nil || *u.CachedInputTokens != 2000 || u.CacheWriteInputTokens == nil || *u.CacheWriteInputTokens != 300 {
				t.Fatalf("%s usage=%+v", name, u)
			}
		}
		// Cumulative delta fields replace, never add twice; output-only deltas retain input.
		delta := []byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":25,"cache_read_input_tokens":2000,"cache_creation_input_tokens":300,"output_tokens":7}}`)
		accumulateNativeSSE(native, "anthropic", append([]byte("data: "), delta...))
		if _, err := bridge.Next(stream.Event{Data: delta}); err != nil {
			t.Fatal(err)
		}
		if native.Usage.InputTokens != 2325 || native.Usage.TotalTokens != 2332 || bridge.UsageSnapshot().InputTokens != 2325 {
			t.Fatal("cumulative cache usage double counted")
		}
		emitted := anthropicUsageMap(unary.Usage)
		if emitted["input_tokens"] != 25 || emitted["cache_read_input_tokens"] != 2000 || emitted["cache_creation_input_tokens"] != 300 {
			t.Fatalf("emission=%v", emitted)
		}
	}
	read, write := 2000, 300
	cachedPrice, writePrice := 1.0, 12.5
	rec := logRecord{Usage: Usage{InputTokens: 2325, CachedInputTokens: &read, CacheWriteInputTokens: &write}, InputPricePerMillionUSD: 10, CachedInputPricePerMillionUSD: &cachedPrice, CacheWritePricePerMillionUSD: &writePrice}
	populateCosts(&rec)
	if rec.InputCostUSD != 0.006 {
		t.Fatalf("three-way cost=%g", rec.InputCostUSD)
	}
	rec.CacheWritePricePerMillionUSD = nil
	populateCosts(&rec)
	if rec.InputCostUSD != 0.00525 {
		t.Fatalf("fallback cost=%g", rec.InputCostUSD)
	}
	found := false
	for _, w := range rec.Warnings {
		found = found || w == "missing-cache-write-pricing-metadata"
	}
	if !found {
		t.Fatal("missing fallback warning")
	}
	reverse := anthropicUsageMap(Usage{InputTokens: 2325, CachedInputTokens: &read})
	if reverse["input_tokens"] != 325 || reverse["cache_read_input_tokens"] != 2000 {
		t.Fatalf("reverse=%v", reverse)
	}
}

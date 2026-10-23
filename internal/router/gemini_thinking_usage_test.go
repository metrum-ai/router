// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"os"
	"testing"
)

func TestGeminiThinkingUsage(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/gemini/thinking-response.json")
	if err != nil {
		t.Fatal(err)
	}
	response, err := decodeUpstreamResponse("gemini-generate-content", raw, "gemini-thinking")
	if err != nil {
		t.Fatal(err)
	}
	u := response.Usage
	if u.InputTokens != 100 || u.OutputTokens != 520 || u.TotalTokens != 620 || u.ReasoningTokens == nil || *u.ReasoningTokens != 500 || *u.ReasoningTokens > u.OutputTokens {
		t.Fatalf("usage=%+v", u)
	}
	rec := logRecord{Usage: u, InputPricePerMillionUSD: 10, OutputPricePerMillionUSD: 20}
	populateCosts(&rec)
	if rec.OutputCostUSD != 0.0104 || rec.TotalCostUSD != 0.0114 {
		t.Fatalf("cost output=%g total=%g", rec.OutputCostUSD, rec.TotalCostUSD)
	}
	for _, body := range []string{`{"promptTokenCount":100,"candidatesTokenCount":20}`, `{"promptTokenCount":100,"candidatesTokenCount":20,"thoughtsTokenCount":0}`} {
		var m map[string]any
		_ = json.Unmarshal([]byte(body), &m)
		got := geminiUsageFromMap(m)
		_, present := m["thoughtsTokenCount"]
		if (got.ReasoningTokens != nil) != present || got.TotalTokens != 120 {
			t.Fatalf("presence %s: %+v", body, got)
		}
	}
	got := geminiUsageFromMap(map[string]any{"promptTokenCount": json.Number("100"), "toolUsePromptTokenCount": 10, "candidatesTokenCount": 20, "thoughtsTokenCount": 500, "totalTokenCount": 650, "promptTokensDetails": []any{map[string]any{"modality": "TEXT", "tokenCount": 70}, map[string]any{"modality": "IMAGE", "tokenCount": 30}}})
	if got.InputTokens != 110 || got.OutputTokens != 520 || got.TotalTokens != 650 || got.InputImageTokens != 30 {
		t.Fatalf("details=%+v", got)
	}
	fallback := geminiUsageFromMap(map[string]any{"promptTokenCount": 100, "candidatesTokenCount": 20, "thoughtsTokenCount": 500})
	if fallback.TotalTokens != 620 {
		t.Fatalf("fallback=%+v", fallback)
	}
}

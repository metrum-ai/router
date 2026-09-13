// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestUsageFromMapCachedInputTokensOpenAIChat(t *testing.T) {
	reported := usageFromMap(map[string]any{
		"prompt_tokens":     100.0,
		"completion_tokens": 10.0,
		"prompt_tokens_details": map[string]any{
			"cached_tokens": 40.0,
		},
	})
	if reported.CachedInputTokens == nil || *reported.CachedInputTokens != 40 || reported.InputTokens != 100 {
		t.Fatalf("openai chat cached decode: %#v", reported)
	}

	zero := usageFromMap(map[string]any{
		"prompt_tokens":     12.0,
		"completion_tokens": 3.0,
		"prompt_tokens_details": map[string]any{
			"cached_tokens": 0.0,
		},
	})
	if zero.CachedInputTokens == nil || *zero.CachedInputTokens != 0 {
		t.Fatalf("reported zero must remain distinct: %#v", zero)
	}

	missing := usageFromMap(map[string]any{"prompt_tokens": 12.0, "completion_tokens": 3.0})
	if missing.CachedInputTokens != nil {
		t.Fatalf("absent cached tokens must stay nil: %#v", missing)
	}
}

func TestUsageFromMapCachedInputTokensResponses(t *testing.T) {
	got := usageFromMap(map[string]any{
		"input_tokens":  80.0,
		"output_tokens": 5.0,
		"input_tokens_details": map[string]any{
			"cached_tokens": 25.0,
		},
	})
	if got.CachedInputTokens == nil || *got.CachedInputTokens != 25 || got.InputTokens != 80 {
		t.Fatalf("responses cached decode: %#v", got)
	}
}

func TestUsageFromMapCachedInputTokensAnthropicNormalization(t *testing.T) {
	got := usageFromMap(map[string]any{
		"input_tokens":                100.0,
		"cache_creation_input_tokens": 50.0,
		"cache_read_input_tokens":     200.0,
		"output_tokens":               10.0,
	})
	if got.InputTokens != 350 {
		t.Fatalf("anthropic input total want 350 got %d", got.InputTokens)
	}
	if got.CachedInputTokens == nil || *got.CachedInputTokens != 200 {
		t.Fatalf("anthropic cache read: %#v", got)
	}
}

func TestUsageFromMapRejectsInvalidCachedCounts(t *testing.T) {
	over := usageFromMap(map[string]any{
		"prompt_tokens": 10.0,
		"prompt_tokens_details": map[string]any{
			"cached_tokens": 11.0,
		},
	})
	if over.CachedInputTokens != nil {
		t.Fatalf("cached > input must be rejected: %#v", over)
	}

	negative := usageFromMap(map[string]any{
		"prompt_tokens": 10.0,
		"prompt_tokens_details": map[string]any{
			"cached_tokens": -1.0,
		},
	})
	if negative.CachedInputTokens != nil {
		t.Fatalf("negative cached must be rejected: %#v", negative)
	}
}

func TestGeminiUsageFromMapCachedContentTokenCount(t *testing.T) {
	got := geminiUsageFromMap(map[string]any{
		"promptTokenCount":        90.0,
		"candidatesTokenCount":    5.0,
		"cachedContentTokenCount": 30.0,
	})
	if got.CachedInputTokens == nil || *got.CachedInputTokens != 30 || got.InputTokens != 90 {
		t.Fatalf("gemini cached decode: %#v", got)
	}

	invalid := geminiUsageFromMap(map[string]any{
		"promptTokenCount":        10.0,
		"cachedContentTokenCount": 11.0,
	})
	if invalid.CachedInputTokens != nil {
		t.Fatalf("gemini cached > prompt must be rejected: %#v", invalid)
	}
}

func TestChatAndResponsesUsageMapsPreserveCachedTokens(t *testing.T) {
	cached := 7
	usage := Usage{InputTokens: 20, OutputTokens: 3, TotalTokens: 23, CachedInputTokens: &cached}
	chat := chatUsageMap(usage)
	details, _ := chat["prompt_tokens_details"].(map[string]any)
	if details["cached_tokens"] != 7 {
		t.Fatalf("chat usage map missing cached tokens: %#v", chat)
	}
	responses := responsesUsageMap(usage)
	inDetails, _ := responses["input_tokens_details"].(map[string]any)
	if inDetails["cached_tokens"] != 7 {
		t.Fatalf("responses usage map missing cached tokens: %#v", responses)
	}
}

func TestMergeStreamUsagePreservesCachedTokensIncludingZero(t *testing.T) {
	dst := Usage{InputTokens: 1}
	zero := 0
	mergeStreamUsage(&dst, Usage{InputTokens: 10, OutputTokens: 2, CachedInputTokens: &zero})
	if dst.CachedInputTokens == nil || *dst.CachedInputTokens != 0 {
		t.Fatalf("stream merge must preserve reported zero: %#v", dst)
	}
}

func TestPopulateCostsUsesCachedInputPriceWhenEvidencePresent(t *testing.T) {
	cachedTokens := 40
	cachedPrice := 0.5
	rec := &logRecord{
		Usage:                         Usage{InputTokens: 100, OutputTokens: 10},
		InputPricePerMillionUSD:       2,
		CachedInputPricePerMillionUSD: &cachedPrice,
		OutputPricePerMillionUSD:      8,
	}
	rec.Usage.CachedInputTokens = &cachedTokens
	populateCosts(rec)
	// (60*2 + 40*0.5) / 1e6 = 140/1e6 = 0.00014
	if rec.InputCostUSD != 0.00014 {
		t.Fatalf("cached cost want 0.00014 got %v", rec.InputCostUSD)
	}

	missingPrice := &logRecord{
		Usage:                    Usage{InputTokens: 100, OutputTokens: 10, CachedInputTokens: &cachedTokens},
		InputPricePerMillionUSD:  2,
		OutputPricePerMillionUSD: 8,
	}
	populateCosts(missingPrice)
	if missingPrice.InputCostUSD != 0.0002 {
		t.Fatalf("missing cached price must keep ordinary accounting: %v", missingPrice.InputCostUSD)
	}

	invalid := &logRecord{
		Usage:                         Usage{InputTokens: 10, OutputTokens: 1, CachedInputTokens: intPtr(11)},
		InputPricePerMillionUSD:       2,
		CachedInputPricePerMillionUSD: &cachedPrice,
		OutputPricePerMillionUSD:      8,
	}
	populateCosts(invalid)
	if invalid.InputCostUSD != 0.00002 {
		t.Fatalf("invalid cached count must keep ordinary accounting: %v", invalid.InputCostUSD)
	}
}

func TestCachedInputPriceKnownFreeUsesZeroPrice(t *testing.T) {
	cachedTokens := 50
	free := 0.0
	rec := &logRecord{
		Usage:                         Usage{InputTokens: 100, CachedInputTokens: &cachedTokens},
		InputPricePerMillionUSD:       2,
		CachedInputPricePerMillionUSD: &free,
	}
	populateCosts(rec)
	// (50*2 + 50*0) / 1e6 = 0.0001
	if rec.InputCostUSD != 0.0001 {
		t.Fatalf("known-free cached price: %v", rec.InputCostUSD)
	}
}

func TestCachedInputStoreRoundTripJSONLAndRelational(t *testing.T) {
	cachedTokens := 15
	cachedPrice := 0.25
	zeroTokens := 0
	freePrice := 0.0

	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	positive := logRecord{
		TS:                            "2026-09-13T12:00:00.000Z",
		RequestID:                     "cached-positive",
		CallerID:                      "alice",
		ResolvedGroup:                 "default",
		TargetProvider:                "openai",
		TargetModel:                   "gpt-test",
		Status:                        200,
		Usage:                         Usage{InputTokens: 100, OutputTokens: 5, TotalTokens: 105, CachedInputTokens: &cachedTokens},
		InputPricePerMillionUSD:       2,
		CachedInputPricePerMillionUSD: &cachedPrice,
		OutputPricePerMillionUSD:      8,
		Cache:                         "miss",
		Warnings:                      []string{},
	}
	zero := logRecord{
		TS:                            "2026-09-13T12:01:00.000Z",
		RequestID:                     "cached-zero",
		CallerID:                      "alice",
		ResolvedGroup:                 "default",
		TargetProvider:                "openai",
		TargetModel:                   "gpt-test",
		Status:                        200,
		Usage:                         Usage{InputTokens: 20, OutputTokens: 1, TotalTokens: 21, CachedInputTokens: &zeroTokens},
		InputPricePerMillionUSD:       2,
		CachedInputPricePerMillionUSD: &freePrice,
		OutputPricePerMillionUSD:      8,
		Cache:                         "miss",
		Warnings:                      []string{},
	}
	absent := logRecord{
		TS:                       "2026-09-13T12:02:00.000Z",
		RequestID:                "cached-absent",
		CallerID:                 "alice",
		ResolvedGroup:            "default",
		TargetProvider:           "openai",
		TargetModel:              "gpt-test",
		Status:                   200,
		Usage:                    Usage{InputTokens: 20, OutputTokens: 1, TotalTokens: 21},
		InputPricePerMillionUSD:  2,
		OutputPricePerMillionUSD: 8,
		Cache:                    "miss",
		Warnings:                 []string{},
	}

	var jsonl strings.Builder
	for _, rec := range []logRecord{positive, zero, absent} {
		line, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		jsonl.Write(line)
		jsonl.WriteByte('\n')
		store.Emit(rec)
	}

	lines := strings.Split(strings.TrimSpace(jsonl.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("jsonl lines=%d", len(lines))
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	usage, _ := first["usage"].(map[string]any)
	if usage["cached_input_tokens"] != float64(15) {
		t.Fatalf("jsonl cached tokens: %#v", usage)
	}
	if first["cached_input_price_per_million_usd"] != 0.25 {
		t.Fatalf("jsonl cached price: %#v", first["cached_input_price_per_million_usd"])
	}
	var second map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatal(err)
	}
	secondUsage, _ := second["usage"].(map[string]any)
	if secondUsage["cached_input_tokens"] != float64(0) {
		t.Fatalf("jsonl zero cached tokens: %#v", secondUsage)
	}
	if second["cached_input_price_per_million_usd"] != float64(0) {
		t.Fatalf("jsonl known-free price must serialize as 0: %#v", second["cached_input_price_per_million_usd"])
	}
	var third map[string]any
	if err := json.Unmarshal([]byte(lines[2]), &third); err != nil {
		t.Fatal(err)
	}
	thirdUsage, _ := third["usage"].(map[string]any)
	if _, ok := thirdUsage["cached_input_tokens"]; ok {
		t.Fatalf("absent cached tokens must omit from jsonl: %#v", thirdUsage)
	}
	if _, ok := third["cached_input_price_per_million_usd"]; ok {
		t.Fatalf("absent cached price must omit from jsonl: %#v", third)
	}

	var rows []usageRecord
	if err := store.db.Order("request_id asc").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows=%d", len(rows))
	}
	byID := map[string]usageRecord{}
	for _, row := range rows {
		byID[row.RequestID] = row
	}
	if byID["cached-positive"].CachedInputTokens == nil || *byID["cached-positive"].CachedInputTokens != 15 {
		t.Fatalf("relational positive tokens: %#v", byID["cached-positive"].CachedInputTokens)
	}
	if byID["cached-positive"].CachedInputPricePerMillionUSD == nil || *byID["cached-positive"].CachedInputPricePerMillionUSD != 0.25 {
		t.Fatalf("relational positive price: %#v", byID["cached-positive"].CachedInputPricePerMillionUSD)
	}
	if byID["cached-zero"].CachedInputTokens == nil || *byID["cached-zero"].CachedInputTokens != 0 {
		t.Fatalf("relational zero tokens: %#v", byID["cached-zero"].CachedInputTokens)
	}
	if byID["cached-zero"].CachedInputPricePerMillionUSD == nil || *byID["cached-zero"].CachedInputPricePerMillionUSD != 0 {
		t.Fatalf("relational free price: %#v", byID["cached-zero"].CachedInputPricePerMillionUSD)
	}
	if byID["cached-absent"].CachedInputTokens != nil || byID["cached-absent"].CachedInputPricePerMillionUSD != nil {
		t.Fatalf("relational absent must stay null: tokens=%#v price=%#v", byID["cached-absent"].CachedInputTokens, byID["cached-absent"].CachedInputPricePerMillionUSD)
	}
}

func TestCachedInputPricingMigrationPreservesHistoricalNulls(t *testing.T) {
	db, err := openUsageDB(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "cached-migrate.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	if err := applyUsageExplicitBaseline(db); err != nil {
		t.Fatal(err)
	}
	if err := applyUsageReasoningTelemetryMigration(db); err != nil {
		t.Fatal(err)
	}
	if err := applyUsageContentCaptureEncryptionMigration(db); err != nil {
		t.Fatal(err)
	}
	if err := applyUsageTargetRegionDiagnosticsMigration(db); err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasColumn(&usageRecord{}, "CachedInputTokens") {
		t.Fatal("cached columns must not exist before migration")
	}
	if err := db.Omit("CachedInputTokens", "CachedInputPricePerMillionUSD").Create(&usageRecord{RequestID: "hist", TS: "2026-09-01T00:00:00Z"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := applyUsageCachedInputPricingMigration(db); err != nil {
		t.Fatal(err)
	}
	var row usageRecord
	if err := db.Where("request_id = ?", "hist").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.CachedInputTokens != nil || row.CachedInputPricePerMillionUSD != nil {
		t.Fatalf("historical row must keep null cache evidence: %#v %#v", row.CachedInputTokens, row.CachedInputPricePerMillionUSD)
	}
}

func TestPricingCatalogFingerprintIncludesCachedInputPrice(t *testing.T) {
	cfg := &Config{Provider: map[string]ProviderConfig{
		"mock": {Models: map[string]ProviderModel{"a": {Model: "a", InputPricePerMillionUSD: 1}}},
	}}
	before := pricingCatalogFingerprint(cfg)
	price := 0.1
	changed := &Config{Provider: map[string]ProviderConfig{
		"mock": {Models: map[string]ProviderModel{"a": {Model: "a", InputPricePerMillionUSD: 1, CachedInputPricePerMillionUSD: &price}}},
	}}
	after := pricingCatalogFingerprint(changed)
	if before == after {
		t.Fatal("pricing fingerprint must change when cached input price is set")
	}
	free := 0.0
	freeCfg := &Config{Provider: map[string]ProviderConfig{
		"mock": {Models: map[string]ProviderModel{"a": {Model: "a", InputPricePerMillionUSD: 1, CachedInputPricePerMillionUSD: &free}}},
	}}
	if pricingCatalogFingerprint(freeCfg) == after {
		t.Fatal("known-free cached price must fingerprint differently from non-zero cached price")
	}
}

func TestResolveTargetInheritsCachedInputPrice(t *testing.T) {
	price := 0.2
	cfg := &Config{Provider: map[string]ProviderConfig{
		"mock": {Models: map[string]ProviderModel{
			"small": {Model: "mock-small", CachedInputPricePerMillionUSD: &price},
		}},
	}}
	resolved, err := cfg.resolveTarget("group", Target{Provider: "mock", ModelRef: "small"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.CachedInputPricePerMillionUSD == nil || *resolved.CachedInputPricePerMillionUSD != 0.2 {
		t.Fatalf("inherit cached price: %#v", resolved.CachedInputPricePerMillionUSD)
	}
	override := 0.0
	resolved, err = cfg.resolveTarget("group", Target{Provider: "mock", ModelRef: "small", CachedInputPricePerMillionUSD: &override})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.CachedInputPricePerMillionUSD == nil || *resolved.CachedInputPricePerMillionUSD != 0 {
		t.Fatalf("explicit known-free must not inherit: %#v", resolved.CachedInputPricePerMillionUSD)
	}
}

func TestBuildScriptTargetsCarryCachedInputPrice(t *testing.T) {
	price := 0.3
	targets := buildScriptTargets([]Target{{
		Provider: "mock", Model: "m", Weight: 1, CachedInputPricePerMillionUSD: &price,
	}}, map[string]ProviderConfig{"mock": {BaseURL: "https://example.test", Dialect: "openai-chat"}})
	if len(targets) != 1 || targets[0].CachedInputPricePerMillionUSD == nil || *targets[0].CachedInputPricePerMillionUSD != 0.3 {
		t.Fatalf("script target cached price: %#v", targets)
	}
}

func intPtr(v int) *int { return &v }

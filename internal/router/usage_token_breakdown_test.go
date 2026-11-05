// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTokenBreakdownSavingsAttribution(t *testing.T) {
	price := 2.0
	cached := 60
	row := usageRow{InputTokens: 100, OutputTokens: 40, CachedInputTokens: &cached, CachedInputPricePerMillionUSD: &price, InputPricePerMillionUSD: 10, OutputPricePerMillionUSD: 20, CostListUSD: .00132, TotalCostUSD: .00132}
	list := undiscountedUsageListCost(row)
	split := savingsAttribution(.002, list, row.CostListUSD, row.TotalCostUSD)
	if diff := split.RoutingSavingsUSD - .0002; diff < -1e-12 || diff > 1e-12 {
		t.Fatalf("routing=%g", split.RoutingSavingsUSD)
	}
	if diff := split.PromptCacheSavingsUSD - .00048; diff < -1e-12 || diff > 1e-12 {
		t.Fatalf("cache=%g", split.PromptCacheSavingsUSD)
	}
	if split.ResponseCacheSavingsUSD != 0 {
		t.Fatal("provider cache is not a response-cache hit")
	}
	split = savingsAttribution(.002, list, row.CostListUSD, 0)
	if split.ResponseCacheSavingsUSD != row.CostListUSD {
		t.Fatal("lost free response-cache savings")
	}
}

func TestTokenBreakdownMetricsComponents(t *testing.T) {
	reasoning, cached, price := 20, 60, 2.0
	write, writePrice := 20, 12.5
	rec := logRecord{CallerID: "alice", UsageSource: "reported", Usage: Usage{InputTokens: 100, OutputTokens: 40, TotalTokens: 140, ReasoningTokens: &reasoning, CachedInputTokens: &cached, CacheWriteInputTokens: &write}, InputPricePerMillionUSD: 10, OutputPricePerMillionUSD: 20, CachedInputPricePerMillionUSD: &price, CacheWritePricePerMillionUSD: &writePrice}
	populateCosts(&rec)
	metrics := newMetricsStore()
	metrics.Observe(rec)
	rec.Cache = "hit"
	metrics.Observe(rec)
	text := metrics.Prometheus(nil, MigrationStatus{})
	for _, want := range []string{`component="input"} 0.000200`, `component="cache_write"} 0.000250`, `metrum_ai_router_cache_write_input_tokens_total`, `component="cached_input"} 0.000120`, `component="output"} 0.000800`, `metrum_ai_router_reasoning_tokens_total`, `metrum_ai_router_cached_input_tokens_total`} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s in %s", want, text)
		}
	}
}

func TestTokenBreakdownCoverageAndRollups(t *testing.T) {
	zero, reasoning, cached := 0, 20, 60
	rows := []usageRow{
		{TS: time.Now(), InputTokens: 100, OutputTokens: 40, ReasoningTokens: &reasoning, CachedInputTokens: &cached},
		{TS: time.Now(), InputTokens: 100, OutputTokens: 40, ReasoningTokens: &zero, CachedInputTokens: &zero},
		{TS: time.Now(), InputTokens: 100, OutputTokens: 40},
	}
	var total agg
	for _, row := range rows {
		total.add(row)
	}
	want := UsageTokenBreakdown{CostComponentReportedRequests: 3, ReasoningTokens: 20, ReasoningReportedRequests: 2, CachedInputTokens: 60, CachedInputReportedRequests: 2}
	if total.UsageTokenBreakdown != want {
		t.Fatalf("aggregate=%+v", total.UsageTokenBreakdown)
	}
	daily := dailyRollupRecords(rows, "finalized", "2026-10-23", "2026-10-24", usageRollupBaseline{})
	if len(daily) != 1 || daily[0].UsageTokenBreakdown != want {
		t.Fatalf("daily=%+v", daily)
	}
	hourly := aggregateRollupRecordsFromDaily(daily, "hourly")
	if len(hourly) != 1 || hourly[0].UsageTokenBreakdown != want {
		t.Fatalf("hourly=%+v", hourly)
	}
	if adminSummaryFromAgg(&total).UsageTokenBreakdown != want || adminSeriesFromAggValue("test", total, 0, false).UsageTokenBreakdown != want {
		t.Fatal("lost API telemetry")
	}
	store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for i, row := range rows {
		rec := recordFromRow(row)
		rec.RequestID = string(rune('a' + i))
		if err := store.db.Create(rec).Error; err != nil {
			t.Fatal(err)
		}
	}
	var result tokenScalarAggRecord
	if err := store.db.Model(&usageRecord{}).Select(adminScalarAggSQLSelectExpr(adminSavingsBaselineDTO{})).Scan(&result).Error; err != nil {
		t.Fatal(err)
	}
	if result.UsageTokenBreakdown != want {
		t.Fatalf("SQL aggregate=%+v", result.UsageTokenBreakdown)
	}
	for _, present := range []bool{false, true} {
		filtered, err := store.rows(UsageReportOptions{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour), CachePresent: &present})
		if err != nil {
			t.Fatal(err)
		}
		count := 1
		if present {
			count = 2
		}
		if len(filtered) != count {
			t.Fatalf("cache_present=%v rows=%d", present, len(filtered))
		}
	}
}

// Issue #777: the "by Provider / Model" reasoning-coverage table rendered the
// NUL-joined rollup key, so provider and model ran together ("local-tinylocal-tiny").
func TestReasoningCoverageRendersProviderAndModelSeparately(t *testing.T) {
	from := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 5, 1, 0, 0, 0, time.UTC)
	rows := []usageRow{
		{
			TS:             from.Add(10 * time.Minute),
			RequestID:      "req_provider_model",
			RequestedModel: "local-chat",
			ResolvedGroup:  "local-chat",
			TargetProvider: "local-tiny",
			TargetModel:    "local-tiny",
			Status:         200,
		},
		{
			TS:             from.Add(20 * time.Minute),
			RequestID:      "req_provider_model_2",
			RequestedModel: "local-chat",
			ResolvedGroup:  "local-chat",
			TargetProvider: "escalate",
			TargetModel:    "gpt-5.4",
			Status:         200,
		},
	}
	md := renderUsageMarkdown(from, to, rows, decisionTelemetrySummary{}, nil)

	for _, want := range []string{
		"## Reasoning and Cache Coverage by Provider / Model",
		"| local-tiny / local-tiny |",
		"| escalate / gpt-5.4 |",
		// The group table keeps its single-value dimension.
		"## Reasoning and Cache Coverage by Model Group",
		"| local-chat |",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("usage report missing %q:\n%s", want, md)
		}
	}
	for _, notWant := range []string{"local-tinylocal-tiny", "escalategpt-5.4", "\x00"} {
		if strings.Contains(md, notWant) {
			t.Fatalf("usage report contains %q:\n%s", notWant, md)
		}
	}
}

func TestProviderModelDimensionLabel(t *testing.T) {
	cases := []struct{ key, want string }{
		{joinKey("local-tiny", "local-tiny"), "local-tiny / local-tiny"},
		{joinKey("openai", "gpt-5.4-nano"), "openai / gpt-5.4-nano"},
		{joinKey("local", ""), "local"},
		{joinKey("", "model"), " / model"},
		// Table cells stay escaped: a pipe in either field cannot break the row.
		{joinKey("pro|vider", "mo|del"), `pro\|vider / mo\|del`},
	}
	for _, tc := range cases {
		if got := providerModelDimensionLabel(tc.key); got != tc.want {
			t.Fatalf("providerModelDimensionLabel(%q)=%q, want %q", tc.key, got, tc.want)
		}
	}
	if got := dimensionLabel("local-chat"); got != "local-chat" {
		t.Fatalf("dimensionLabel=%q", got)
	}
}

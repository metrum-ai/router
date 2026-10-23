// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"math"
	"strings"
)

// UsageTokenBreakdown preserves both token sums and upstream reporting coverage.
// A reported zero contributes to coverage; an omitted metric does not.
type UsageTokenBreakdown struct {
	CostComponentReportedRequests   int64   `json:"costComponentReportedRequests" gorm:"column:cost_component_reported_request_count;not null;default:0"`
	ProviderListCostUSD             float64 `json:"providerListCostUsd" gorm:"column:provider_list_cost_usd;not null;default:0"`
	RoutingListCostUSD              float64 `json:"routingListCostUsd" gorm:"column:routing_list_cost_usd;not null;default:0"`
	UsageCostComponents             `gorm:"embedded"`
	CacheWriteInputTokens           int64 `json:"cacheWriteInputTokens" gorm:"column:cache_write_input_tokens;not null;default:0"`
	CacheWriteReportedRequests      int64 `json:"cacheWriteReportedRequests" gorm:"column:cache_write_reported_request_count;not null;default:0"`
	ReasoningAttemptCount           int64 `json:"reasoningAttemptCount" gorm:"column:reasoning_attempt_count;not null;default:0"`
	ReasoningSuccessfulAttemptCount int64 `json:"reasoningSuccessfulAttemptCount" gorm:"column:reasoning_successful_attempt_count;not null;default:0"`
	ReasoningReportedAttemptCount   int64 `json:"reasoningReportedAttemptCount" gorm:"column:reasoning_reported_attempt_count;not null;default:0"`
	ReasoningTokens                 int64 `json:"reasoningTokens" gorm:"column:reasoning_tokens;not null;default:0"`
	ReasoningReportedRequests       int64 `json:"reasoningReportedRequests" gorm:"column:reasoning_reported_request_count;not null;default:0"`
	CachedInputTokens               int64 `json:"cachedInputTokens" gorm:"column:cached_input_tokens;not null;default:0"`
	CachedInputReportedRequests     int64 `json:"cachedInputReportedRequests" gorm:"column:cached_input_reported_request_count;not null;default:0"`
}

func (b *UsageTokenBreakdown) addUsage(row usageRow) {
	b.UsageCostComponents.add(usageCostComponents(row))
	if row.Cache == "hit" || math.Abs(row.InputCostUSD+row.OutputCostUSD+row.ImageCostUSD-row.TotalCostUSD) < 1e-9 {
		b.CostComponentReportedRequests++
	}
	b.RoutingListCostUSD += undiscountedUsageListCost(row)
	b.ProviderListCostUSD += providerUsageListCost(row)
	if row.CacheWriteInputTokens != nil {
		b.CacheWriteInputTokens += int64(*row.CacheWriteInputTokens)
		b.CacheWriteReportedRequests++
	}
	b.ReasoningAttemptCount += int64(row.ReasoningAttemptCount)
	b.ReasoningSuccessfulAttemptCount += int64(row.ReasoningSuccessfulAttemptCount)
	b.ReasoningReportedAttemptCount += int64(row.ReasoningReportedAttemptCount)
	if row.ReasoningTokens != nil {
		b.ReasoningTokens += int64(*row.ReasoningTokens)
		b.ReasoningReportedRequests++
	}
	if row.CachedInputTokens != nil {
		b.CachedInputTokens += int64(*row.CachedInputTokens)
		b.CachedInputReportedRequests++
	}
}

func (b *UsageTokenBreakdown) add(other UsageTokenBreakdown) {
	b.UsageCostComponents.add(other.UsageCostComponents)
	b.CostComponentReportedRequests += other.CostComponentReportedRequests
	b.ProviderListCostUSD += other.ProviderListCostUSD
	b.RoutingListCostUSD += other.RoutingListCostUSD
	b.CacheWriteInputTokens += other.CacheWriteInputTokens
	b.CacheWriteReportedRequests += other.CacheWriteReportedRequests
	b.ReasoningAttemptCount += other.ReasoningAttemptCount
	b.ReasoningSuccessfulAttemptCount += other.ReasoningSuccessfulAttemptCount
	b.ReasoningReportedAttemptCount += other.ReasoningReportedAttemptCount
	b.ReasoningTokens += other.ReasoningTokens
	b.ReasoningReportedRequests += other.ReasoningReportedRequests
	b.CachedInputTokens += other.CachedInputTokens
	b.CachedInputReportedRequests += other.CachedInputReportedRequests
}

func writeTokenBreakdownTable(b *strings.Builder, title string, rows map[string]*agg) {
	fmt.Fprintf(b, "\n## %s\n\n", title)
	fmt.Fprintln(b, "| Dimension | Reasoning Tokens | Reasoning Reported Requests | Cached Input Tokens | Cache Reported Requests | Cache Write Tokens | Cache Write Reported Requests | Reasoning Attempts | Successful Reasoning Attempts | Reported Reasoning Attempts |")
	fmt.Fprintln(b, "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, key := range sortedAggKeys(rows) {
		a := rows[key]
		fmt.Fprintf(b, "| %s | %d | %d | %d | %d | %d | %d | %d | %d | %d |\n", esc(key), a.ReasoningTokens, a.ReasoningReportedRequests, a.CachedInputTokens, a.CachedInputReportedRequests, a.CacheWriteInputTokens, a.CacheWriteReportedRequests, a.ReasoningAttemptCount, a.ReasoningSuccessfulAttemptCount, a.ReasoningReportedAttemptCount)
	}
}

var usageTokenBreakdownSQL = usageCostComponentsSQL() + "SUM(" + undiscountedUsageListCostSQL + ") AS routing_list_cost_usd, SUM(" + providerUsageListCostSQL + ") AS provider_list_cost_usd," + `SUM(CASE WHEN cache = 'hit' OR ABS(input_cost_usd + output_cost_usd + image_cost_usd - total_cost_usd) < 0.000000001 THEN 1 ELSE 0 END) AS cost_component_reported_request_count,
 SUM(COALESCE(cache_write_input_tokens,0)) AS cache_write_input_tokens,
 SUM(CASE WHEN cache_write_input_tokens IS NOT NULL THEN 1 ELSE 0 END) AS cache_write_reported_request_count,
 SUM(reasoning_attempt_count) AS reasoning_attempt_count,
 SUM(reasoning_successful_attempt_count) AS reasoning_successful_attempt_count,
 SUM(reasoning_reported_attempt_count) AS reasoning_reported_attempt_count,
 SUM(COALESCE(reasoning_tokens,0)) AS reasoning_tokens,
 SUM(CASE WHEN reasoning_tokens IS NOT NULL THEN 1 ELSE 0 END) AS reasoning_reported_request_count,
 SUM(COALESCE(cached_input_tokens,0)) AS cached_input_tokens,
 SUM(CASE WHEN cached_input_tokens IS NOT NULL THEN 1 ELSE 0 END) AS cached_input_reported_request_count,`

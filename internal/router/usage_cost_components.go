// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "math"

// UsageCostComponents partitions billed cost without counting reasoning twice.
type UsageCostComponents struct {
	UncachedInputCostUSD   float64 `json:"uncachedInputCostUsd" gorm:"column:uncached_input_cost_usd;not null;default:0"`
	CachedInputCostUSD     float64 `json:"cachedInputCostUsd" gorm:"column:cached_input_cost_usd;not null;default:0"`
	CacheWriteCostUSD      float64 `json:"cacheWriteCostUsd" gorm:"column:cache_write_cost_usd;not null;default:0"`
	VisibleOutputCostUSD   float64 `json:"visibleOutputCostUsd" gorm:"column:visible_output_cost_usd;not null;default:0"`
	ReasoningOutputCostUSD float64 `json:"reasoningOutputCostUsd" gorm:"column:reasoning_output_cost_usd;not null;default:0"`
}

func usageCostComponents(row usageRow) UsageCostComponents {
	if row.Cache == "hit" {
		return UsageCostComponents{}
	}
	out := UsageCostComponents{UncachedInputCostUSD: row.InputCostUSD, VisibleOutputCostUSD: row.OutputCostUSD}
	priceCost := func(tokens *int, price *float64, remaining float64) float64 {
		if tokens == nil || *tokens < 0 || *tokens > row.InputTokens {
			return 0
		}
		p := row.InputPricePerMillionUSD
		if price != nil {
			p = *price
		}
		return math.Max(0, math.Min(remaining, roundUSD(float64(*tokens)*p/1e6)))
	}
	out.CachedInputCostUSD = priceCost(row.CachedInputTokens, row.CachedInputPricePerMillionUSD, out.UncachedInputCostUSD)
	out.UncachedInputCostUSD -= out.CachedInputCostUSD
	out.CacheWriteCostUSD = priceCost(row.CacheWriteInputTokens, row.CacheWritePricePerMillionUSD, out.UncachedInputCostUSD)
	out.UncachedInputCostUSD -= out.CacheWriteCostUSD
	if row.ReasoningTokens != nil && row.OutputTokens > 0 {
		reasoning := math.Max(0, math.Min(float64(row.OutputTokens), float64(*row.ReasoningTokens)))
		out.ReasoningOutputCostUSD = roundUSD(row.OutputCostUSD * reasoning / float64(row.OutputTokens))
		out.VisibleOutputCostUSD -= out.ReasoningOutputCostUSD
	}
	return out
}
func (c *UsageCostComponents) add(other UsageCostComponents) {
	c.UncachedInputCostUSD += other.UncachedInputCostUSD
	c.CachedInputCostUSD += other.CachedInputCostUSD
	c.CacheWriteCostUSD += other.CacheWriteCostUSD
	c.VisibleOutputCostUSD += other.VisibleOutputCostUSD
	c.ReasoningOutputCostUSD += other.ReasoningOutputCostUSD
}
func usageCostComponentsSQL() string {
	bounded := func(value, limit string) string {
		return "CASE WHEN (" + value + ") < 0 THEN 0 WHEN (" + value + ") > (" + limit + ") THEN (" + limit + ") ELSE (" + value + ") END"
	}
	cost := func(tokens, price, remaining string) string {
		return "CASE WHEN " + tokens + " IS NULL OR " + tokens + " < 0 OR " + tokens + " > input_tokens THEN 0 ELSE " + bounded("ROUND(CAST("+tokens+" * COALESCE("+price+",input_price_per_million_usd) / 1000000.0 AS NUMERIC),6)", remaining) + " END"
	}
	cached := cost("cached_input_tokens", "cached_input_price_per_million_usd", "input_cost_usd")
	writes := cost("cache_write_input_tokens", "cache_write_price_per_million_usd", "input_cost_usd - ("+cached+")")
	reasoning := "CASE WHEN reasoning_tokens IS NULL OR output_tokens <= 0 THEN 0 ELSE ROUND(CAST(output_cost_usd * (" + bounded("reasoning_tokens", "output_tokens") + ") / output_tokens AS NUMERIC),6) END"
	// NUMERIC makes two-argument ROUND portable across PostgreSQL and SQLite.
	pieces := map[string]string{"uncached_input_cost_usd": "input_cost_usd - (" + cached + ") - (" + writes + ")", "cached_input_cost_usd": cached, "cache_write_cost_usd": writes, "visible_output_cost_usd": "output_cost_usd - (" + reasoning + ")", "reasoning_output_cost_usd": reasoning}
	result := ""
	for _, name := range []string{"uncached_input_cost_usd", "cached_input_cost_usd", "cache_write_cost_usd", "visible_output_cost_usd", "reasoning_output_cost_usd"} {
		result += "SUM(CASE WHEN cache = 'hit' THEN 0 ELSE (" + pieces[name] + ") END) AS " + name + ",\n"
	}
	return result
}

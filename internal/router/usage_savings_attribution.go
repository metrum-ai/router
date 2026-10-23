// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// SavingsAttribution separates model/routing price differences, provider prompt
// caching discounts (negative for write premiums), and free router-cache hits.
type SavingsAttribution struct {
	SavingsAttributionComplete bool    `json:"savingsAttributionComplete"`
	RoutingSavingsUSD          float64 `json:"routingSavingsUsd"`
	PromptCacheSavingsUSD      float64 `json:"promptCacheSavingsUsd"`
	ResponseCacheSavingsUSD    float64 `json:"responseCacheSavingsUsd"`
}

func savingsAttribution(baseline, undiscountedList, providerList, billed float64) SavingsAttribution {
	return SavingsAttribution{SavingsAttributionComplete: true, RoutingSavingsUSD: baseline - undiscountedList, PromptCacheSavingsUSD: undiscountedList - providerList, ResponseCacheSavingsUSD: providerList - billed}
}

func savingsAttributionForUsage(b UsageTokenBreakdown, requests int64, baseline, billed float64) SavingsAttribution {
	split := savingsAttribution(baseline, b.RoutingListCostUSD, b.ProviderListCostUSD, billed)
	split.SavingsAttributionComplete = requests > 0 && b.CostComponentReportedRequests >= requests
	return split
}

func providerUsageListCost(row usageRow) float64 {
	if row.CostListUSD > 0 {
		return row.CostListUSD
	}
	return row.TotalCostUSD
}

const providerUsageListCostSQL = `CASE WHEN cost_list_usd > 0 THEN cost_list_usd ELSE total_cost_usd END`

func undiscountedUsageListCost(row usageRow) float64 {
	if row.InputPricePerMillionUSD == 0 && row.OutputPricePerMillionUSD == 0 {
		return providerUsageListCost(row)
	}
	input := row.InputTokens
	if row.ImageCostUSD > 0 {
		input -= row.InputImageTokens
		if input < 0 {
			input = 0
		}
	}
	return float64(input)*row.InputPricePerMillionUSD/1_000_000 + float64(row.OutputTokens)*row.OutputPricePerMillionUSD/1_000_000 + row.ImageCostUSD
}

const undiscountedUsageListCostSQL = `CASE WHEN input_price_per_million_usd = 0 AND output_price_per_million_usd = 0 THEN (` + providerUsageListCostSQL + `) ELSE
 ((CASE WHEN image_cost_usd > 0 THEN CASE WHEN input_tokens > input_image_tokens THEN input_tokens-input_image_tokens ELSE 0 END ELSE input_tokens END) * input_price_per_million_usd / 1000000.0)
 + output_tokens * output_price_per_million_usd / 1000000.0 + image_cost_usd END`

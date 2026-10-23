// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "gorm.io/gorm"

var tokenBreakdownFields = map[string]string{
	"CostComponentReportedRequests": "cost_component_reported_request_count",
	"ProviderListCostUSD":           "provider_list_cost_usd", "RoutingListCostUSD": "routing_list_cost_usd",
	"UncachedInputCostUSD": "uncached_input_cost_usd", "CachedInputCostUSD": "cached_input_cost_usd", "CacheWriteCostUSD": "cache_write_cost_usd", "VisibleOutputCostUSD": "visible_output_cost_usd", "ReasoningOutputCostUSD": "reasoning_output_cost_usd",
	"ReasoningTokens": "reasoning_tokens", "ReasoningReportedRequests": "reasoning_reported_request_count",
	"CachedInputTokens": "cached_input_tokens", "CachedInputReportedRequests": "cached_input_reported_request_count",
	"CacheWriteInputTokens": "cache_write_input_tokens", "CacheWriteReportedRequests": "cache_write_reported_request_count",
	"ReasoningAttemptCount": "reasoning_attempt_count", "ReasoningSuccessfulAttemptCount": "reasoning_successful_attempt_count", "ReasoningReportedAttemptCount": "reasoning_reported_attempt_count",
}

var usageTokenBreakdownColumns = func() map[string]map[string]struct{} {
	sets := map[string]map[string]struct{}{}
	for _, table := range []string{"usage_rollup_daily", "usage_rollup_hourly", "usage_rollup_monthly_billing"} {
		sets[table] = map[string]struct{}{}
		for _, column := range tokenBreakdownFields {
			sets[table][column] = struct{}{}
		}
	}
	return sets
}()

func applyUsageTokenBreakdownMigration(db *gorm.DB) error {
	for _, model := range []any{&usageRollupDailyRecord{}, &usageRollupHourlyRecord{}, &usageRollupMonthlyBillingRecord{}} {
		for field := range tokenBreakdownFields {
			if !db.Migrator().HasColumn(model, field) {
				if err := db.Migrator().AddColumn(model, field); err != nil {
					return err
				}
			}
		}
	}
	// Historic aggregates have no recoverable NULL/zero evidence after retention.
	// Preserve zero known evidence; only draft windows may be regenerated from raw rows.
	return verifyUsageTokenBreakdownMigration(db)
}
func verifyUsageTokenBreakdownMigration(db *gorm.DB) error { return ensureUsageRelationalSchema(db) }

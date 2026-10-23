// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"gorm.io/gorm"
	"unicode/utf8"
)

var usageProvenanceColumns = map[string]map[string]struct{}{
	"request_usage":                {"usage_source": {}},
	"usage_rollup_daily":           {"estimated_request_count": {}, "estimated_input_tokens": {}, "estimated_output_tokens": {}},
	"usage_rollup_hourly":          {"estimated_request_count": {}, "estimated_input_tokens": {}, "estimated_output_tokens": {}},
	"usage_rollup_monthly_billing": {"estimated_request_count": {}, "estimated_input_tokens": {}, "estimated_output_tokens": {}},
}

func estimatedTokenShare(a agg) float64 {
	if a.TotalTokens <= 0 {
		return 0
	}
	return 100 * float64(a.EstimatedInputTokens+a.EstimatedOutputTokens) / float64(a.TotalTokens)
}

func containsUsageEstimate(warnings []string) bool {
	for _, warning := range warnings {
		if warning == "usage-estimated" {
			return true
		}
	}
	return false
}

// Estimate only observed output, never the requested output ceiling.
func estimatedStreamUsage(req *IRRequest, text string) Usage {
	input := estimateTokens(req)
	output := (utf8.RuneCountInString(text) + 3) / 4
	return Usage{InputTokens: input, OutputTokens: output, TotalTokens: input + output}
}

func applyUsageProvenanceMigration(db *gorm.DB) error {
	for _, model := range []any{&usageRecord{}, &usageRollupDailyRecord{}, &usageRollupHourlyRecord{}, &usageRollupMonthlyBillingRecord{}} {
		fields := []string{"EstimatedRequestCount", "EstimatedInputTokens", "EstimatedOutputTokens"}
		if _, ok := model.(*usageRecord); ok {
			fields = []string{"UsageSource"}
		}
		for _, field := range fields {
			if !db.Migrator().HasColumn(model, field) {
				if err := db.Migrator().AddColumn(model, field); err != nil {
					return err
				}
			}
		}
	}
	// Existing rows deliberately remain unknown (''). Warnings were not durably
	// stored, so classifying historic totals as reported would invent provenance.
	return verifyUsageProvenanceMigration(db)
}

func verifyUsageProvenanceMigration(db *gorm.DB) error {
	for _, model := range []any{&usageRecord{}, &usageRollupDailyRecord{}, &usageRollupHourlyRecord{}, &usageRollupMonthlyBillingRecord{}} {
		fields := []string{"EstimatedRequestCount", "EstimatedInputTokens", "EstimatedOutputTokens"}
		if _, ok := model.(*usageRecord); ok {
			fields = []string{"UsageSource"}
		}
		for _, field := range fields {
			if !db.Migrator().HasColumn(model, field) {
				return fmt.Errorf("missing usage provenance column %s", field)
			}
		}
	}
	return ensureUsageRelationalSchemaExcept(db, usageCacheWriteColumns)
}

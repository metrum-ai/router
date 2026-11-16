// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"math"
	"os"
	"strings"
)

func validateProviderUsageReconciliation(cfg ProviderUsageReconciliationConfig, providers map[string]ProviderConfig, usage UsageDBConfig, securityEventsEnabled, requireEnv bool) error {
	if !cfg.Enabled {
		if cfg.ThresholdUSD != 0 || cfg.TimeoutMS != 0 || cfg.MaxPages != 0 || cfg.ScheduleHourUTC != nil || cfg.MaxCatchUpDays != 0 || len(cfg.Providers) != 0 {
			return fmt.Errorf("server.provider_usage_reconciliation fields require enabled")
		}
		return nil
	}
	if usage.Enable != nil && !*usage.Enable {
		return fmt.Errorf("server.provider_usage_reconciliation requires usage_db enabled")
	}
	if !securityEventsEnabled {
		return fmt.Errorf("server.provider_usage_reconciliation requires admin_reports.security.enabled")
	}
	if math.IsNaN(cfg.ThresholdUSD) || math.IsInf(cfg.ThresholdUSD, 0) || cfg.ThresholdUSD < 0 {
		return fmt.Errorf("server.provider_usage_reconciliation.threshold_usd must be finite and non-negative")
	}
	if cfg.TimeoutMS < 0 || cfg.TimeoutMS > 60000 {
		return fmt.Errorf("server.provider_usage_reconciliation.timeout_ms must be zero or between 1 and 60000")
	}
	if cfg.MaxPages < 0 || cfg.MaxPages > 100 {
		return fmt.Errorf("server.provider_usage_reconciliation.max_pages must be zero or between 1 and 100")
	}
	if cfg.ScheduleHourUTC != nil && (*cfg.ScheduleHourUTC < 0 || *cfg.ScheduleHourUTC > 23) {
		return fmt.Errorf("server.provider_usage_reconciliation.schedule_hour_utc must be between 0 and 23")
	}
	if cfg.MaxCatchUpDays < 0 || cfg.MaxCatchUpDays > 30 {
		return fmt.Errorf("server.provider_usage_reconciliation.max_catch_up_days must be zero or between 1 and 30")
	}
	if len(cfg.Providers) == 0 || len(cfg.Providers) > 100 {
		return fmt.Errorf("server.provider_usage_reconciliation.providers must contain between 1 and 100 entries")
	}
	seen := make(map[string]struct{}, len(cfg.Providers))
	for i, entry := range cfg.Providers {
		name := strings.TrimSpace(entry.Provider)
		if name == "" || providers[name].BaseURL == "" {
			return fmt.Errorf("server.provider_usage_reconciliation.providers[%d] references an unknown provider", i)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("server.provider_usage_reconciliation provider %q is duplicated", name)
		}
		seen[name] = struct{}{}
		kind := strings.ToLower(strings.TrimSpace(entry.Kind))
		switch kind {
		case "openai", "anthropic":
			env := strings.TrimSpace(entry.CredentialEnv)
			if !validEnvName(env) || !strings.HasPrefix(env, "ROUTER_PROVIDER_USAGE_") || strings.TrimSpace(entry.ProviderKeyID) == "" {
				return fmt.Errorf("server.provider_usage_reconciliation provider %q requires a ROUTER_PROVIDER_USAGE_ credential_env and provider_key_id", name)
			}
			if kind == "anthropic" && !strings.HasPrefix(strings.TrimSpace(entry.ProviderKeyID), "apikey_") {
				return fmt.Errorf("server.provider_usage_reconciliation Anthropic provider %q requires an apikey_ provider_key_id", name)
			}
			if value, ok := os.LookupEnv(env); requireEnv && (!ok || strings.TrimSpace(value) == "") {
				return fmt.Errorf("server.provider_usage_reconciliation credential_env %q is unset", env)
			}
		case "bedrock":
			if !validAWSRegion(strings.TrimSpace(entry.AWSRegion)) {
				return fmt.Errorf("server.provider_usage_reconciliation provider %q requires a valid aws_region", name)
			}
			if entry.AWSRoleARN != "" && !validAWSRoleARN(strings.TrimSpace(entry.AWSRoleARN)) {
				return fmt.Errorf("server.provider_usage_reconciliation provider %q has an invalid aws_role_arn", name)
			}
			if entry.CredentialEnv != "" || entry.ProviderKeyID != "" {
				return fmt.Errorf("server.provider_usage_reconciliation Bedrock provider %q uses its scoped AWS credential chain, not credential_env or provider_key_id", name)
			}
			if len(providers[name].Models) == 0 || len(providers[name].Models) > 50 {
				return fmt.Errorf("server.provider_usage_reconciliation Bedrock provider %q requires 1 to 50 configured models", name)
			}
		default:
			return fmt.Errorf("server.provider_usage_reconciliation provider %q kind must be openai, anthropic, or bedrock", name)
		}
	}
	return nil
}

func validAWSRoleARN(value string) bool {
	parts := strings.Split(value, ":")
	if len(parts) != 6 || parts[0] != "arn" || (parts[1] != "aws" && parts[1] != "aws-us-gov" && parts[1] != "aws-cn") || parts[2] != "iam" || parts[3] != "" || len(parts[4]) != 12 || !strings.HasPrefix(parts[5], "role/") || len(parts[5]) <= len("role/") {
		return false
	}
	for _, ch := range parts[4] {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return strings.TrimSpace(value) == value
}

func validAWSRegion(region string) bool {
	if len(region) < 8 || len(region) > 32 || strings.TrimSpace(region) != region {
		return false
	}
	for _, ch := range region {
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' {
			return false
		}
	}
	return true
}

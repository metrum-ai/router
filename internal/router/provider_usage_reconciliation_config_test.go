// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"strings"
	"testing"
)

func TestValidateProviderUsageReconciliation(t *testing.T) {
	enabled := true
	providers := map[string]ProviderConfig{"primary": {BaseURL: "https://provider.example.invalid"}}
	usage := UsageDBConfig{Enable: &enabled}
	valid := ProviderUsageReconciliationConfig{
		Enabled:      true,
		ThresholdUSD: 1,
		TimeoutMS:    1000,
		MaxPages:     4,
		Providers: []ProviderUsageReconciliationEntry{{
			Provider: "primary", Kind: "openai", CredentialEnv: "ROUTER_PROVIDER_USAGE_OPENAI_KEY", ProviderKeyID: "key-123",
		}},
	}
	t.Setenv("ROUTER_PROVIDER_USAGE_OPENAI_KEY", "secret-value-for-test")
	if err := validateProviderUsageReconciliation(valid, providers, usage, true, true); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ProviderUsageReconciliationConfig, *UsageDBConfig)
		want   string
	}{
		{"usage database disabled", func(_ *ProviderUsageReconciliationConfig, db *UsageDBConfig) {
			disabled := false
			db.Enable = &disabled
		}, "usage_db enabled"},
		{"negative threshold", func(cfg *ProviderUsageReconciliationConfig, _ *UsageDBConfig) { cfg.ThresholdUSD = -1 }, "threshold_usd"},
		{"duplicate provider", func(cfg *ProviderUsageReconciliationConfig, _ *UsageDBConfig) {
			cfg.Providers = append(cfg.Providers, cfg.Providers[0])
		}, "duplicated"},
		{"missing provider key id", func(cfg *ProviderUsageReconciliationConfig, _ *UsageDBConfig) { cfg.Providers[0].ProviderKeyID = "" }, "provider_key_id"},
		{"non-dedicated credential environment", func(cfg *ProviderUsageReconciliationConfig, _ *UsageDBConfig) {
			cfg.Providers[0].CredentialEnv = "OPENAI_API_KEY"
		}, "ROUTER_PROVIDER_USAGE_"},
		{"unset credential at serve time", func(cfg *ProviderUsageReconciliationConfig, _ *UsageDBConfig) {
			cfg.Providers[0].CredentialEnv = "ROUTER_PROVIDER_USAGE_OPENAI_KEY_UNSET"
		}, "is unset"},
		{"unsupported provider kind", func(cfg *ProviderUsageReconciliationConfig, _ *UsageDBConfig) { cfg.Providers[0].Kind = "custom" }, "kind must be"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid
			cfg.Providers = append([]ProviderUsageReconciliationEntry(nil), valid.Providers...)
			db := usage
			tc.mutate(&cfg, &db)
			err := validateProviderUsageReconciliation(cfg, providers, db, true, true)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "secret-value-for-test") {
				t.Fatalf("configuration validation leaked credential: %v", err)
			}
		})
	}
}

func TestValidateProviderUsageReconciliationBedrockIdentity(t *testing.T) {
	enabled := true
	base := ProviderUsageReconciliationConfig{
		Enabled:   true,
		Providers: []ProviderUsageReconciliationEntry{{Provider: "primary", Kind: "bedrock", AWSRegion: "us-east-1", AWSRoleARN: "arn:aws:iam::123456789012:role/reconciliation"}},
	}
	providers := map[string]ProviderConfig{"primary": {BaseURL: "https://provider.example.invalid", Models: map[string]ProviderModel{"claude": {Model: "anthropic.claude-3", InputPricePerMillionUSD: 1, OutputPricePerMillionUSD: 2}}}}
	if err := validateProviderUsageReconciliation(base, providers, UsageDBConfig{Enable: &enabled}, true, true); err != nil {
		t.Fatalf("valid Bedrock config rejected: %v", err)
	}
	base.Providers[0].ProviderKeyID = "not-supported-for-bedrock"
	if err := validateProviderUsageReconciliation(base, providers, UsageDBConfig{Enable: &enabled}, true, true); err == nil || !strings.Contains(err.Error(), "credential chain") {
		t.Fatalf("Bedrock key identity error = %v", err)
	}
	base.Providers[0].ProviderKeyID = ""
	base.Providers[0].AWSRoleARN = "arn:aws:iam::bad:role/reconciliation"
	if err := validateProviderUsageReconciliation(base, providers, UsageDBConfig{Enable: &enabled}, true, true); err == nil || !strings.Contains(err.Error(), "aws_role_arn") {
		t.Fatalf("Bedrock role ARN error = %v", err)
	}
	base.Providers[0].AWSRoleARN = ""
	providerModels := make(map[string]ProviderModel, 51)
	for i := 0; i < 51; i++ {
		providerModels[fmt.Sprintf("model-%d", i)] = ProviderModel{Model: fmt.Sprintf("model-%d", i)}
	}
	providers["primary"] = ProviderConfig{BaseURL: "https://provider.example.invalid", Models: providerModels}
	if err := validateProviderUsageReconciliation(base, providers, UsageDBConfig{Enable: &enabled}, true, true); err == nil || !strings.Contains(err.Error(), "50 configured models") {
		t.Fatalf("Bedrock model query bound error = %v", err)
	}
}

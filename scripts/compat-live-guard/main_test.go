// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/metrum-ai/router/internal/router"
)

func fixture(t *testing.T) *router.Config {
	t.Helper()
	dir := t.TempDir()
	enabled := true
	return &router.Config{
		Server:    router.ServerConfig{UsageDB: router.UsageDBConfig{Enable: &enabled, Driver: "sqlite", Path: filepath.Join(dir, "usage.sqlite")}},
		StatePath: filepath.Join(dir, "state.json"),
		Callers:   []router.CallerConfig{{Quota: router.QuotaConfig{Day: router.BudgetConfig{Tokens: 200000}, Month: router.BudgetConfig{Tokens: 200000}}, Key: router.KeyConfig{LifetimeTokens: 200000}}},
		Provider: map[string]router.ProviderConfig{
			"openai":    {BaseURL: "https://api.openai.com/v1"},
			"anthropic": {BaseURL: "https://api.anthropic.com"},
		},
		Models: map[string]router.ModelGroup{
			"gpt":    {Targets: []router.Target{{Provider: "openai", Dialect: "openai-chat", PricingSource: "test", ForceStoreFalse: true, InputPricePerMillionUSD: 0.05, OutputPricePerMillionUSD: 0.4, RequestShapeSupport: router.RequestShapeSupport{ValidationStatus: "limited"}}}},
			"claude": {Targets: []router.Target{{Provider: "anthropic", Dialect: "anthropic", PricingSource: "test", InputPricePerMillionUSD: 1, OutputPricePerMillionUSD: 5, RequestShapeSupport: router.RequestShapeSupport{ValidationStatus: "limited"}}}},
		},
	}
}

func TestValidateLiveSafety(t *testing.T) {
	tests := []struct {
		name   string
		change func(*router.Config)
	}{
		{"unlimited quota", func(c *router.Config) { c.Callers[0].Key.LifetimeTokens = 0 }},
		{"too large quota", func(c *router.Config) { c.Callers[0].Quota.Day.Tokens = 200001 }},
		{"missing prices", func(c *router.Config) { c.Models["claude"].Targets[0].OutputPricePerMillionUSD = 0 }},
		{"overstated validation", func(c *router.Config) { c.Models["gpt"].Targets[0].RequestShapeSupport.ValidationStatus = "passed" }},
		{"database source", func(c *router.Config) { c.Server.ConfigSource.Mode = "database" }},
		{"fallback provider", func(c *router.Config) {
			g := c.Models["claude"]
			g.Targets[0].Provider = "openai"
			c.Models["claude"] = g
		}},
		{"content capture", func(c *router.Config) { c.Server.ContentCapture.Enabled = true }},
		{"upstream storage not disabled", func(c *router.Config) { c.Models["gpt"].Targets[0].ForceStoreFalse = false }},
		{"existing state", func(c *router.Config) {
			if err := os.WriteFile(c.StatePath, []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	if err := validate(fixture(t)); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := fixture(t)
			tc.change(c)
			if err := validate(c); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "testing"

// configureDocsSmokeCapacity is an infrastructure-only allowance for localhost
// documentation proofs, not a production quota recommendation. Match the local
// coding-agent profile; admission-limit tests must explicitly override and label
// the dimension under test. ProjectConfig and ModelGroup have no rate/quota
// fields; these caller allowances apply across their allowed projects/groups.
func configureDocsSmokeCapacity(cfg *Config) {
	for i := range cfg.Callers {
		caller := &cfg.Callers[i]
		caller.Rate = RateConfig{RPM: 2400, TPM: 4_000_000, Concurrent: 160}
		caller.Quota = QuotaConfig{Day: BudgetConfig{Requests: 100_000, Tokens: 400_000_000}, Month: BudgetConfig{Tokens: 8_000_000_000}, SoftPct: 80}
		caller.Key = KeyConfig{LifetimeTokens: 40_000_000_000, SoftPct: 90, OnExhaust: "disable"}
	}
}

func TestDocsSmokeCapacityEveryCaller(t *testing.T) {
	cfg := &Config{Callers: []CallerConfig{{ID: "inference"}, {ID: "metrics"}}}
	configureDocsSmokeCapacity(cfg)
	for _, caller := range cfg.Callers {
		if caller.Rate.RPM != 2400 || caller.Rate.TPM != 4_000_000 || caller.Rate.Concurrent != 160 || caller.Quota.Day.Requests != 100_000 || caller.Quota.Day.Tokens != 400_000_000 || caller.Quota.Month.Tokens != 8_000_000_000 || caller.Key.LifetimeTokens != 40_000_000_000 {
			t.Fatalf("caller %s lacks the documented high-capacity smoke profile", caller.ID)
		}
	}
}

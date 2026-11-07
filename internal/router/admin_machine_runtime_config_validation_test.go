// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"testing"
	"time"
)

func TestRuntimeMachinePreparedSettingsFreezeProfiles(t *testing.T) {
	disabled := false
	input := AdminMachineAuthenticationConfig{APIKeys: AdminMachineAPIKeysConfig{Enabled: &disabled}, JWKS: AdminMachineJWKSConfig{CacheTTL: "2s", RefreshMinInterval: "1s", RequestTimeout: "750ms"}, Issuers: []AdminMachineIssuerConfig{{IssuerURL: "https://issuer.example", Audience: "router-admin", ClockSkew: "0s", Algorithms: []string{"ES256"}, AllowedJWKSHosts: []string{"keys.example"}, Subjects: []AdminMachineJWTSubjectConfig{{Claim: "sub", Value: "automation", Subject: "workload:automation"}}}}}
	settings, err := prepareAdminMachineRuntimeSettings(input, "")
	if err != nil {
		t.Fatal(err)
	}
	p := settings.issuers[0].profile
	if settings.apiKeysEnabled || p.JWKSCacheTTL != 2*time.Second || p.JWKSRefreshMinInterval != time.Second || p.JWKSRequestTimeout != 750*time.Millisecond || p.ClockSkew != 0 || !p.clockSkewExplicit {
		t.Fatal("prepared profile lost explicit settings/default distinction")
	}
	input.Issuers[0].Algorithms[0] = "HS256"
	input.Issuers[0].AllowedJWKSHosts[0] = "evil.example"
	input.Issuers[0].Subjects[0].Value = "other"
	disabled = true
	if p.Algorithms[0] != "ES256" || p.AllowedJWKSHosts[0] != "keys.example" || p.Subjects[0].Value != "automation" || settings.apiKeysEnabled {
		t.Fatal("prepared profile borrowed mutable input slices/pointer")
	}
}

func TestRuntimeMachineAuthenticationStartupSettings(t *testing.T) {
	issuer := func() AdminMachineIssuerConfig {
		return AdminMachineIssuerConfig{IssuerURL: "https://issuer.example", Audience: "router-admin", Kind: "federated", Algorithms: []string{"RS256"}, Subjects: []AdminMachineJWTSubjectConfig{{Claim: "sub", Value: "system:serviceaccount:ops:router", Subject: "workload:router"}}}
	}
	cases := []struct {
		name    string
		mutate  func(*AdminMachineAuthenticationConfig)
		invalid bool
	}{
		{name: "empty default"},
		{name: "valid issuer", mutate: func(c *AdminMachineAuthenticationConfig) { c.Issuers = []AdminMachineIssuerConfig{issuer()} }},
		{name: "invalid JWKS timeout", mutate: func(c *AdminMachineAuthenticationConfig) { c.JWKS.RequestTimeout = "garbage" }, invalid: true},
		{name: "zero JWKS TTL", mutate: func(c *AdminMachineAuthenticationConfig) { c.JWKS.CacheTTL = "0s" }, invalid: true},
		{name: "negative refresh interval", mutate: func(c *AdminMachineAuthenticationConfig) { c.JWKS.RefreshMinInterval = "-1s" }, invalid: true},
		{name: "invalid proof age", mutate: func(c *AdminMachineAuthenticationConfig) { c.DPoP.ProofMaxAge = "-1s" }, invalid: true},
		{name: "invalid replay capacity", mutate: func(c *AdminMachineAuthenticationConfig) { c.DPoP.ReplayCapacity = -1 }, invalid: true},
		{name: "HTTP issuer", mutate: func(c *AdminMachineAuthenticationConfig) {
			i := issuer()
			i.IssuerURL = "http://issuer.example"
			c.Issuers = []AdminMachineIssuerConfig{i}
		}, invalid: true},
		{name: "issuer query", mutate: func(c *AdminMachineAuthenticationConfig) {
			i := issuer()
			i.IssuerURL += "?secret=forbidden"
			c.Issuers = []AdminMachineIssuerConfig{i}
		}, invalid: true},
		{name: "empty audience", mutate: func(c *AdminMachineAuthenticationConfig) {
			i := issuer()
			i.Audience = ""
			c.Issuers = []AdminMachineIssuerConfig{i}
		}, invalid: true},
		{name: "unsupported algorithm", mutate: func(c *AdminMachineAuthenticationConfig) {
			i := issuer()
			i.Algorithms = []string{"HS256"}
			c.Issuers = []AdminMachineIssuerConfig{i}
		}, invalid: true},
		{name: "excessive skew", mutate: func(c *AdminMachineAuthenticationConfig) {
			i := issuer()
			i.ClockSkew = "2m"
			c.Issuers = []AdminMachineIssuerConfig{i}
		}, invalid: true},
		{name: "duplicate issuer", mutate: func(c *AdminMachineAuthenticationConfig) { i := issuer(); c.Issuers = []AdminMachineIssuerConfig{i, i} }, invalid: true},
		{name: "unknown minimum sender", mutate: func(c *AdminMachineAuthenticationConfig) {
			i := issuer()
			i.SenderConstraint = "magic"
			c.Issuers = []AdminMachineIssuerConfig{i}
		}, invalid: true},
		{name: "mutable identity claim", mutate: func(c *AdminMachineAuthenticationConfig) {
			i := issuer()
			i.Subjects[0].Claim = "email"
			c.Issuers = []AdminMachineIssuerConfig{i}
		}, invalid: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := AdminPlaneConfig{Enabled: true, Listen: "127.0.0.1:0"}
			defaultAdminPlaneConfig(&cfg)
			if tc.mutate != nil {
				tc.mutate(&cfg.Authentication)
			}
			err := validateAdminPlane(&cfg)
			if (err != nil) != tc.invalid {
				t.Fatalf("startup settings error=%v want invalid=%v", err, tc.invalid)
			}
		})
	}
}

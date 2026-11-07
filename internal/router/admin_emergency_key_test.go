// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAdminEmergencyKeyProofAndLifetime(t *testing.T) {
	generated, err := GenerateAdminAPIKey("emergency-test", nil, CallerTokenHashDefaultIterations)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	cfg := adminEmergencyKeyVerifierConfig{id: generated.KeyID, subject: "emergency:rescue", issuer: "local", tokenHash: generated.TokenHash, issuedAt: now.Add(-time.Minute), expiresAt: now.Add(30 * time.Minute)}
	v, err := newAdminEmergencyKeyVerifier(cfg, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	proof, err := v.Verify(context.Background(), generated.Token)
	if err != nil {
		t.Fatal(err)
	}
	if proof.credentialID != cfg.id || proof.subject != cfg.subject || proof.issuer != cfg.issuer || len(proof.bindingDigest) != 64 {
		t.Fatal("incorrect verified metadata")
	}
	encoded, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), generated.Token) || strings.Contains(string(encoded), generated.TokenHash) || strings.Contains(string(encoded), proof.bindingDigest) {
		t.Fatal("protected proof material serialized")
	}
	for _, bad := range []string{"", "rtr_metrum_caller_secret", "provider-key", strings.Replace(generated.Token, "emergency-test", "other-key", 1), generated.Token + "x", strings.Repeat("x", maxCallerTokenBytes+1), " " + generated.Token} {
		if _, err := v.Verify(context.Background(), bad); err == nil {
			t.Fatal("invalid token accepted")
		} else if strings.Contains(err.Error(), generated.Token) || strings.Contains(err.Error(), generated.TokenHash) {
			t.Fatal("error leaked credential")
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := v.Verify(cancelled, generated.Token); err == nil {
		t.Fatal("cancelled proof accepted")
	}
	now = cfg.issuedAt.Add(-time.Nanosecond)
	if _, err := v.Verify(context.Background(), generated.Token); err == nil {
		t.Fatal("future-issued credential accepted")
	}
	now = cfg.expiresAt
	if _, err := v.Verify(context.Background(), generated.Token); err == nil {
		t.Fatal("credential accepted at expiry")
	}
	for _, tc := range []struct {
		name   string
		change func(*adminEmergencyKeyVerifierConfig)
	}{
		{"missing-issued-at", func(c *adminEmergencyKeyVerifierConfig) { c.issuedAt = time.Time{} }},
		{"missing-expiry", func(c *adminEmergencyKeyVerifierConfig) { c.expiresAt = time.Time{} }},
		{"reversed-times", func(c *adminEmergencyKeyVerifierConfig) { c.expiresAt = c.issuedAt }},
		{"overlong-issued-lifetime", func(c *adminEmergencyKeyVerifierConfig) { c.expiresAt = c.issuedAt.Add(time.Hour + time.Nanosecond) }},
		{"bad-verifier", func(c *adminEmergencyKeyVerifierConfig) { c.tokenHash = generated.Token }},
		{"bad-id", func(c *adminEmergencyKeyVerifierConfig) { c.id = "ambiguous_id" }},
		{"missing-subject", func(c *adminEmergencyKeyVerifierConfig) { c.subject = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := cfg
			tc.change(&c)
			if _, err := newAdminEmergencyKeyVerifier(c, time.Now); err == nil {
				t.Fatal("invalid configuration accepted")
			} else if strings.Contains(err.Error(), generated.Token) || strings.Contains(err.Error(), generated.TokenHash) {
				t.Fatal("configuration error leaked material")
			}
		})
	}
}

func TestAdminEmergencyKeyBindingAndPostKDFExpiry(t *testing.T) {
	generated, err := GenerateAdminAPIKey("emergency-test", nil, CallerTokenHashDefaultIterations)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	cfg := adminEmergencyKeyVerifierConfig{id: generated.KeyID, subject: "emergency:rescue", issuer: "local", tokenHash: generated.TokenHash, issuedAt: now.Add(-time.Minute), expiresAt: now.Add(30 * time.Minute)}
	getProof := func(c adminEmergencyKeyVerifierConfig) adminEmergencyKeyProof {
		t.Helper()
		v, err := newAdminEmergencyKeyVerifier(c, func() time.Time { return now })
		if err != nil {
			t.Fatal(err)
		}
		p, err := v.Verify(context.Background(), generated.Token)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	original := getProof(cfg)
	for _, change := range []func(*adminEmergencyKeyVerifierConfig){
		func(c *adminEmergencyKeyVerifierConfig) { c.subject = "emergency:replacement" },
		func(c *adminEmergencyKeyVerifierConfig) { c.issuer = "other-issuer" },
		func(c *adminEmergencyKeyVerifierConfig) { c.issuedAt = c.issuedAt.Add(-time.Second) },
		func(c *adminEmergencyKeyVerifierConfig) { c.expiresAt = c.expiresAt.Add(time.Second) },
	} {
		c := cfg
		change(&c)
		if getProof(c).bindingDigest == original.bindingDigest {
			t.Fatal("credential metadata replacement preserved binding")
		}
	}
	alternate, err := HashCallerToken(generated.Token, nil, CallerTokenHashDefaultIterations)
	if err != nil {
		t.Fatal(err)
	}
	changed := cfg
	changed.tokenHash = alternate
	if getProof(changed).bindingDigest == original.bindingDigest {
		t.Fatal("verifier replacement preserved binding")
	}
	calls := 0
	v, err := newAdminEmergencyKeyVerifier(cfg, func() time.Time {
		calls++
		if calls == 1 {
			return now
		}
		return cfg.expiresAt
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), generated.Token); err == nil {
		t.Fatal("credential expired during KDF accepted")
	}
	if calls < 2 {
		t.Fatal("expiry rejection did not exercise post-KDF clock")
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// provenanceTestMaterial is deliberately low-entropy test material, not a credential.
const provenanceTestMaterial = "0123456789abcdef0123456789abcdef"

func provenanceTestKey(t *testing.T, keyID string) provenanceKey {
	t.Helper()
	key, err := newReasoningProvenanceKey(keyID, provenanceTestMaterial)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func provenanceTestScope() reasoningScope {
	return reasoningScope{Dialect: "openai-responses", Provider: "openai", KeyID: "acct-a", Model: "gpt-5.5", Group: "frontier"}
}

func TestReasoningEnvelopeRoundTrip(t *testing.T) {
	key := provenanceTestKey(t, "k1")
	now := time.Unix(1_800_000_000, 0)
	scope := provenanceTestScope()
	opaque := "gAAAAABopaque-provider-ciphertext"
	envelope, err := key.wrap(scope, reasoningKindEncryptedContent, opaque, now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(envelope, reasoningEnvelopePrefix+".k1.") {
		t.Fatalf("unexpected envelope prefix: %q", envelope[:12])
	}
	if strings.Contains(envelope, opaque) {
		t.Fatal("envelope must not contain the raw value")
	}
	got, err := parseReasoningEnvelope(key, envelope, scope, reasoningKindEncryptedContent, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got != opaque {
		t.Fatalf("round trip mismatch: %q", got)
	}
	if _, err := parseReasoningEnvelope(key, envelope, scope, reasoningKindEncryptedContent, now.Add(25*time.Hour)); !errors.Is(err, errEnvelopeExpired) {
		t.Fatalf("expired envelope accepted: %v", err)
	}
}

func TestReasoningEnvelopeRejectsForeignScope(t *testing.T) {
	key := provenanceTestKey(t, "k1")
	now := time.Unix(1_800_000_000, 0)
	scope := provenanceTestScope()
	envelope, err := key.wrap(scope, reasoningKindThinkingSig, "sig-value", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(reasoningScope) reasoningScope{
		"cross-provider": func(s reasoningScope) reasoningScope { s.Provider = "azure-openai"; return s },
		"cross-account":  func(s reasoningScope) reasoningScope { s.KeyID = "acct-b"; return s },
		"cross-model":    func(s reasoningScope) reasoningScope { s.Model = "gpt-5.5-mini"; return s },
		"cross-dialect":  func(s reasoningScope) reasoningScope { s.Dialect = "anthropic"; return s },
		"cross-group":    func(s reasoningScope) reasoningScope { s.Group = "cheap"; return s },
	} {
		if _, err := parseReasoningEnvelope(key, envelope, mutate(scope), reasoningKindThinkingSig, now); !errors.Is(err, errEnvelopeScope) {
			t.Fatalf("%s: expected scope mismatch, got %v", name, err)
		}
	}
	if _, err := parseReasoningEnvelope(key, envelope, scope, reasoningKindRedactedThinking, now); !errors.Is(err, errEnvelopeKind) {
		t.Fatalf("kind mismatch accepted: %v", err)
	}
}

func TestReasoningEnvelopeRejectsTampering(t *testing.T) {
	key := provenanceTestKey(t, "k1")
	now := time.Unix(1_800_000_000, 0)
	scope := provenanceTestScope()
	envelope, err := key.wrap(scope, reasoningKindEncryptedContent, "value", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(envelope, ".")
	if len(parts) != 4 {
		t.Fatalf("unexpected envelope shape: %d parts", len(parts))
	}
	tampered := []string{
		parts[0] + "." + parts[1] + "." + parts[2] + "A." + parts[3],
		parts[0] + "." + parts[1] + "." + parts[2] + "." + parts[3] + "A",
		parts[0] + "." + "k2." + parts[2] + "." + parts[3],
		"rtr_rz1." + parts[1] + "." + parts[2],
		"rtr_rz2." + parts[1] + "." + parts[2] + "." + parts[3],
		"rtr_rz1." + parts[1] + "." + parts[2] + "." + parts[3] + ".extra",
	}
	for _, candidate := range tampered {
		if _, err := parseReasoningEnvelope(key, candidate, scope, reasoningKindEncryptedContent, now); err == nil {
			t.Fatalf("tampered envelope accepted: %q", candidate[:min(len(candidate), 24)])
		}
	}
}

func TestReasoningEnvelopeRejectsKeyMismatch(t *testing.T) {
	first := provenanceTestKey(t, "k1")
	second, err := newReasoningProvenanceKey("k2", provenanceTestMaterial)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	envelope, err := first.wrap(provenanceTestScope(), reasoningKindEncryptedContent, "value", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseReasoningEnvelope(second, envelope, provenanceTestScope(), reasoningKindEncryptedContent, now); !errors.Is(err, errEnvelopeKeyID) {
		t.Fatalf("expected key id mismatch, got %v", err)
	}
	if reason := reasoningProvenanceReason(errEnvelopeKeyID); reason != "unknown-key" {
		t.Fatalf("unexpected trace class: %s", reason)
	}
}

func TestReasoningEnvelopeBounds(t *testing.T) {
	key := provenanceTestKey(t, "k1")
	now := time.Unix(1_800_000_000, 0)
	scope := provenanceTestScope()
	if _, err := key.wrap(scope, reasoningKindEncryptedContent, strings.Repeat("a", maxReasoningOpaqueBytes+1), now, time.Hour); !errors.Is(err, errEnvelopeOpaqueLarge) {
		t.Fatalf("oversized value accepted: %v", err)
	}
	if _, err := key.wrap(scope, reasoningKindEncryptedContent, "value", now, reasoningProvenanceMaxTTL+time.Second); !errors.Is(err, errEnvelopeExpiry) {
		t.Fatalf("oversized ttl accepted: %v", err)
	}
	if _, err := key.wrap(scope, reasoningKindEncryptedContent, "value", now, 0); !errors.Is(err, errEnvelopeExpiry) {
		t.Fatalf("zero ttl accepted: %v", err)
	}
	envelope, err := key.wrap(scope, reasoningKindEncryptedContent, "value", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseReasoningEnvelope(key, envelope+strings.Repeat("A", maxReasoningEnvelopeBytes), scope, reasoningKindEncryptedContent, now); !errors.Is(err, errEnvelopeTooLarge) {
		t.Fatalf("oversized envelope accepted: %v", err)
	}
	if reason := reasoningProvenanceReason(errEnvelopeTooLarge); reason != "oversized" {
		t.Fatalf("unexpected trace class: %s", reason)
	}
}

func TestReasoningProvenanceKeyValidation(t *testing.T) {
	for name, tc := range map[string]struct{ keyID, secret string }{
		"empty key id":      {"", provenanceTestMaterial},
		"uppercase key id":  {"K1", provenanceTestMaterial},
		"space in key id":   {"k 1", provenanceTestMaterial},
		"dotted key id ok":  {"deploy-2026.11_a", provenanceTestMaterial},
		"short secret":      {"k1", "short"},
		"whitespace secret": {"k1", " " + provenanceTestMaterial},
	} {
		_, err := newReasoningProvenanceKey(tc.keyID, tc.secret)
		if name == "dotted key id ok" {
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			continue
		}
		if !errors.Is(err, errEnvelopeKey) {
			t.Fatalf("%s: expected key validation error, got %v", name, err)
		}
	}
}

func TestReasoningProvenanceReasonClasses(t *testing.T) {
	for err, want := range map[error]string{
		nil:                     "verified",
		errEnvelopeExpired:      "expired",
		errEnvelopeScope:        "scope-mismatch",
		errEnvelopeKind:         "kind-mismatch",
		errEnvelopeKeyID:        "unknown-key",
		errEnvelopeMAC:          "invalid-envelope",
		errEnvelopeFormat:       "invalid-envelope",
		errEnvelopePayloadLarge: "oversized",
		errEnvelopeExpiry:       "invalid-expiry",
		fmt.Errorf("wrapped: %w", errEnvelopeEmpty): "invalid-envelope",
	} {
		if got := reasoningProvenanceReason(err); got != want {
			t.Fatalf("reason(%v)=%s want %s", err, got, want)
		}
	}
}

func TestReasoningScopeDigestIsUnambiguous(t *testing.T) {
	a := reasoningScope{Dialect: "ab", Provider: "c", KeyID: "d", Model: "e", Group: "f"}
	b := reasoningScope{Dialect: "a", Provider: "bc", KeyID: "d", Model: "e", Group: "f"}
	if a.digest() == b.digest() {
		t.Fatal("scope digests collide across field boundaries")
	}
	if a.digest() != a.digest() {
		t.Fatal("scope digest is not stable")
	}
}

func FuzzParseReasoningEnvelope(f *testing.F) {
	key, err := newReasoningProvenanceKey("k1", provenanceTestMaterial)
	if err != nil {
		f.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	seed, err := key.wrap(provenanceTestScope(), reasoningKindEncryptedContent, "seed-value", now, time.Hour)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Add("")
	f.Add("rtr_rz1.")
	f.Add("rtr_rz1.k1.")
	f.Add("rtr_rz1.k1....")
	f.Add(strings.Repeat("rtr_rz1.k1.", 40))
	f.Fuzz(func(t *testing.T, envelope string) {
		// The parser must never panic and must never return a value for an
		// envelope it did not authenticate.
		value, err := parseReasoningEnvelope(key, envelope, provenanceTestScope(), reasoningKindEncryptedContent, now)
		if err == nil && envelope != seed {
			t.Fatalf("unauthenticated envelope accepted: %q", envelope[:min(len(envelope), 32)])
		}
		if err != nil && value != "" {
			t.Fatal("failed parse returned a value")
		}
		if class := reasoningProvenanceReason(err); class == "" {
			t.Fatal("missing trace class")
		}
	})
}

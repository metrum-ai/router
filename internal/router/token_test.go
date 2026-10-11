// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestGenerateCallerTokenStructuredMetrumPrefix(t *testing.T) {
	generated, err := GenerateCallerToken(TokenGenerateOptions{
		OwnerUser:   "Alice",
		Project:     "Example Project",
		Environment: "Dev",
		KeySlug:     "Key 1",
		Allow:       []string{"default", "fast"},
		Reader:      strings.NewReader(strings.Repeat("a", 64)),
		Now:         time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(generated.Token, "rtr_metrum_alice_example-project_dev_key-1_") {
		t.Fatalf("unexpected token prefix: %s", generated.Token)
	}
	if generated.TokenID != "rtr_metrum_alice_example-project_dev_key-1" {
		t.Fatalf("token id=%q", generated.TokenID)
	}
	if strings.Contains(generated.TokenID, generated.Token[strings.LastIndex(generated.Token, "_")+1:]) {
		t.Fatal("token id contains secret")
	}
	if generated.TokenSHA256 != "" || generated.Caller.TokenSHA256 != "" {
		t.Fatalf("default generation must not emit deprecated token_sha256: %#v", generated)
	}
	if !strings.HasPrefix(generated.TokenHash, "pbkdf2-sha3-512:i=20000:") || generated.Caller.TokenHash != generated.TokenHash {
		t.Fatalf("token_hash format mismatch: %q", generated.TokenHash)
	}
	if ok, err := VerifyCallerTokenHash(generated.TokenHash, generated.Token); err != nil || !ok {
		t.Fatalf("token_hash does not verify the generated token: ok=%v err=%v", ok, err)
	}
	if ok, _ := VerifyCallerTokenHash(generated.TokenHash, generated.Token+"x"); ok {
		t.Fatal("token_hash must reject a different token")
	}
	if generated.Caller.ID != "alice-example-project-dev" || generated.Caller.OwnerUser != "alice" || generated.Caller.Project != "example-project" || generated.Caller.Environment != "dev" {
		t.Fatalf("caller metadata not normalized: %#v", generated.Caller)
	}
	if len(generated.Caller.Allow) != 2 || generated.Caller.Allow[0] != "default" || generated.Caller.Allow[1] != "fast" {
		t.Fatalf("allow list mismatch: %#v", generated.Caller.Allow)
	}
}

func TestGenerateCallerTokenRequiresAllow(t *testing.T) {
	_, err := GenerateCallerToken(TokenGenerateOptions{
		User:    "alice",
		Project: "example-project",
		Reader:  strings.NewReader(strings.Repeat("b", 64)),
		Now:     time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC),
	})
	if err == nil || !strings.Contains(err.Error(), "allowed model group") {
		t.Fatalf("err=%v, want missing allow error", err)
	}
}

func TestGenerateCallerTokenDefaultsMetadata(t *testing.T) {
	generated, err := GenerateCallerToken(TokenGenerateOptions{
		OwnerUser: "alice",
		Project:   "example-project",
		Allow:     []string{"example-basic"},
		Reader:    strings.NewReader(strings.Repeat("b", 64)),
		Now:       time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if generated.TokenID != "rtr_metrum_alice_example-project_dev_k20260613" {
		t.Fatalf("default token id=%q", generated.TokenID)
	}
	if len(generated.Caller.Allow) != 1 || generated.Caller.Allow[0] != "example-basic" {
		t.Fatalf("default allow=%#v", generated.Caller.Allow)
	}
}

func TestGenerateCallerTokenPreservesAllowedModelGroups(t *testing.T) {
	generated, err := GenerateCallerToken(TokenGenerateOptions{
		OwnerUser: "coder",
		Project:   "example-project",
		Allow:     []string{"default", "big-coder", "high"},
		Reader:    strings.NewReader(strings.Repeat("c", 64)),
		Now:       time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(generated.Caller.Allow, ","), "default,big-coder,high"; got != want {
		t.Fatalf("allow=%q want %q", got, want)
	}
}

func TestGenerateCallerTokenAcceptsLegacyUserAlias(t *testing.T) {
	generated, err := GenerateCallerToken(TokenGenerateOptions{
		User:    "legacy user",
		Project: "example-project",
		Allow:   []string{"default"},
		Reader:  strings.NewReader(strings.Repeat("d", 64)),
		Now:     time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if generated.Caller.OwnerUser != "legacy-user" {
		t.Fatalf("owner_user=%q", generated.Caller.OwnerUser)
	}
}

func TestPublicTokenIDStripsSecretSuffix(t *testing.T) {
	full := "rtr_metrum_alice_example-project_prod_k20260614_SYNTHETIC_PUBLIC_FIXTURE_PART_SYNTHETIC_SECRET_SUFFIX"
	if got, want := publicTokenID(full), "rtr_metrum_alice_example-project_prod_k20260614"; got != want {
		t.Fatalf("public token id=%q want %q", got, want)
	}
	clean := "rtr_metrum_alice_example-project_prod_k20260614"
	if got := publicTokenID(clean); got != clean {
		t.Fatalf("clean token id changed: %q", got)
	}
	if got := publicTokenID("sha256:abcdef123456"); got != "sha256:abcdef123456" {
		t.Fatalf("non-router token id changed: %q", got)
	}
}

func TestGenerateCallerTokenLegacySHA256Format(t *testing.T) {
	generated, err := GenerateCallerToken(TokenGenerateOptions{
		OwnerUser:  "alice",
		Project:    "search",
		Allow:      []string{"default"},
		HashFormat: TokenHashFormatLegacySHA256,
	})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(generated.Token))
	if generated.TokenHash != "" || generated.TokenSHA256 != hex.EncodeToString(sum[:]) || generated.Caller.TokenSHA256 != generated.TokenSHA256 {
		t.Fatalf("legacy format mismatch: %#v", generated)
	}
	if _, err := GenerateCallerToken(TokenGenerateOptions{OwnerUser: "alice", Project: "search", Allow: []string{"default"}, HashFormat: "md5"}); err == nil {
		t.Fatal("unknown hash format must be rejected")
	}
}

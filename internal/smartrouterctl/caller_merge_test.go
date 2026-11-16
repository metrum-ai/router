// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package smartrouterctl

import (
	"reflect"
	"strings"
	"testing"

	"github.com/metrum-ai/router/internal/router"
)

func callerFixture(id, tokenID string) router.CallerConfig {
	return router.CallerConfig{
		ID: id, OwnerUser: "ops", Project: "p", Environment: "run", Status: "active",
		TokenID: tokenID, TokenHash: "pbkdf2-sha3-512:i=1:c2FsdA:a2V5",
		Allow: []string{"local-tiny"},
	}
}

func TestRotateCallerTokenHashWritesEnforcedMetadata(t *testing.T) {
	old, err := router.GenerateCallerToken(router.TokenGenerateOptions{OwnerUser: "ops", Project: "p", Environment: "prod", Allow: []string{"local-tiny"}, Reader: strings.NewReader(strings.Repeat("a", 64))})
	if err != nil {
		t.Fatal(err)
	}
	old.Caller.ID = "ops-p-prod"
	cfg := &router.Config{Server: router.ServerConfig{Identifiers: router.IdentifierConfig{Mode: "passthrough"}}, Provider: map[string]router.ProviderConfig{"p": {BaseURL: "http://127.0.0.1", Dialect: "openai-chat"}}, Callers: []router.CallerConfig{old.Caller}, Models: map[string]router.ModelGroup{"local-tiny": {Strategy: "static", Targets: []router.Target{{Provider: "p", Model: "m"}}}}}
	rotated, err := router.GenerateCallerToken(router.TokenGenerateOptions{OwnerUser: "ops", Project: "p", Environment: "prod", Allow: []string{"local-tiny"}, Reader: strings.NewReader(strings.Repeat("b", 64))})
	if err != nil {
		t.Fatal(err)
	}
	if err := RotateCallerTokenHash(cfg, "ops-p-prod", rotated.TokenHash, rotated.TokenID); err != nil {
		t.Fatal(err)
	}
	cfg.Security.KeyHygiene = router.KeyHygieneConfig{MinEntropyBits: 128, Enforce: true}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("rotated file config does not pass enforcement: %v", err)
	}
	if cfg.Callers[0].TokenFormatVersion != 2 || cfg.Callers[0].TokenEntropyBits != 256 || cfg.Callers[0].RevokedTokenHash != "" {
		t.Fatalf("rotated metadata=%+v", cfg.Callers[0])
	}
}

// TestMergeCallerRefusesDuplicateID covers issue #755: minting a second caller
// with the same derived id used to replace the first one silently, so its token
// stopped authenticating. MergeCaller must refuse and leave the table untouched;
// replacing becomes an explicit choice.
func TestMergeCallerRefusesDuplicateID(t *testing.T) {
	cfg := &router.Config{Callers: []router.CallerConfig{
		callerFixture("keep-me", "rtr_metrum_keep_me_run_k1"),
		callerFixture("ops-p-run", "rtr_metrum_ops_p_run_k1"),
	}}
	err := MergeCaller(cfg, callerFixture("ops-p-run", "rtr_metrum_ops_p_run_k2"))
	if err == nil {
		t.Fatal("duplicate caller id was merged without consent")
	}
	if !strings.Contains(err.Error(), "ops-p-run") || !strings.Contains(err.Error(), "replace") {
		t.Fatalf("refusal must name the caller and the way out: %v", err)
	}
	if len(cfg.Callers) != 2 {
		t.Fatalf("refused merge changed the caller table: %#v", cfg.Callers)
	}
	for _, caller := range cfg.Callers {
		if caller.ID == "ops-p-run" && caller.TokenID != "rtr_metrum_ops_p_run_k1" {
			t.Fatalf("refused merge replaced the existing caller: %#v", caller)
		}
	}
}

// TestUpsertCallerReplacesOnlyThatCaller is the explicit --replace path: exactly
// one caller changes and every other caller keeps its fields.
func TestUpsertCallerReplacesOnlyThatCaller(t *testing.T) {
	cfg := &router.Config{Callers: []router.CallerConfig{
		callerFixture("keep-me", "rtr_metrum_keep_me_run_k1"),
		callerFixture("ops-p-run", "rtr_metrum_ops_p_run_k1"),
	}}
	before := cfg.Callers[0]
	replacement := callerFixture("ops-p-run", "rtr_metrum_ops_p_run_k2")
	replacement.Allow = []string{"local-tiny", "budget"}
	replaced, err := UpsertCaller(cfg, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if !replaced {
		t.Fatal("upsert did not report the replacement")
	}
	if len(cfg.Callers) != 2 {
		t.Fatalf("replace changed the caller count: %#v", cfg.Callers)
	}
	if !reflect.DeepEqual(cfg.Callers[0], before) {
		t.Fatalf("replace touched another caller: %#v", cfg.Callers[0])
	}
	if cfg.Callers[1].TokenID != "rtr_metrum_ops_p_run_k2" || len(cfg.Callers[1].Allow) != 2 {
		t.Fatalf("replace did not apply the new caller: %#v", cfg.Callers[1])
	}
}

// TestMergeCallerAppendsNewCaller keeps the documented merge behaviour: a new id
// is appended and existing callers are untouched.
func TestMergeCallerAppendsNewCaller(t *testing.T) {
	cfg := &router.Config{Callers: []router.CallerConfig{callerFixture("keep-me", "rtr_metrum_keep_me_run_k1")}}
	if err := MergeCaller(cfg, callerFixture("ops-p-run", "rtr_metrum_ops_p_run_k1")); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Callers) != 2 || cfg.Callers[1].ID != "ops-p-run" {
		t.Fatalf("merge did not append: %#v", cfg.Callers)
	}
	if !reflect.DeepEqual(cfg.Callers[0], callerFixture("keep-me", "rtr_metrum_keep_me_run_k1")) {
		t.Fatalf("merge changed an existing caller: %#v", cfg.Callers[0])
	}
}

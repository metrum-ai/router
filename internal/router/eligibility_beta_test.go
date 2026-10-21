// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"gopkg.in/yaml.v3"
	"net/http"
	"strings"
	"testing"
)

func TestAdaptiveThinkingReasoning(t *testing.T) {
	req, err := decodeRequest("anthropic", []byte(`{"model":"m","thinking":{"type":"adaptive"},"messages":[{"role":"user","content":"hi"}]}`), http.Header{})
	if err != nil || !requestRequiresReasoning(req) {
		t.Fatalf("adaptive not reasoning: %v %#v", err, req)
	}
}

func TestEligibilityFlag(t *testing.T) {
	target := Target{}
	req := &IRRequest{}
	for _, enabled := range []bool{true, false} {
		svc := &Service{cfg: &Config{Eligibility: EligibilityConfig{ImplicitOpenAIToAnthropic: &enabled}}}
		for _, dialect := range []string{"openai-chat", "openai-responses"} {
			fit := svc.targetRequestShapeFit(target, req, dialect, "anthropic", requestTokenEstimateLogRecord{})
			if (fit.FilterReason == "") != enabled {
				t.Fatalf("flag=%v: %#v", enabled, fit)
			}
			explicit := target
			explicit.RequestShapeSupport = RequestShapeSupport{SupportedInboundDialects: []string{dialect}, ValidationStatus: "passed"}
			fit = svc.targetRequestShapeFit(explicit, req, dialect, "anthropic", requestTokenEstimateLogRecord{})
			if fit.FilterReason != "" {
				t.Fatal(fit.FilterReason)
			}
			explicit.RequestShapeSupport.ValidationStatus = ""
			fit = svc.targetRequestShapeFit(explicit, req, dialect, "anthropic", requestTokenEstimateLogRecord{})
			if !enabled && fit.FilterReason != "request-shape-validation-unpassed" {
				t.Fatal(fit.FilterReason)
			}
		}
	}
}

func TestBetaAllowList(t *testing.T) {
	if got := permittedBetas(" a,b, a,c", []string{"a", "c"}); got != "a,c" {
		t.Fatal(got)
	}
	req := &IRRequest{Raw: map[string]any{"context_management": nil}, CallerBetas: "unrelated,context-management-2025-06-27"}
	if got := unpermittedBetaField(req, Target{AllowedBetas: []string{"unrelated"}}); got != "context_management" {
		t.Fatal(got)
	}
	if got := unpermittedBetaField(req, Target{AllowedBetas: []string{"context-management-2025-06-27"}}); got != "" {
		t.Fatal(got)
	}
}

func TestEligibilityPatcher(t *testing.T) {
	data := []byte("providers:\n  a: {dialect: anthropic}\nmodels:\n  m:\n    targets:\n      - provider: a\n        model: x\n        request_shape_support: {max_request_bytes: 100}\n")
	cfg := &Config{Provider: map[string]ProviderConfig{"a": {Dialect: "anthropic"}}, Models: map[string]ModelGroup{"m": {Targets: []Target{{Provider: "a", Model: "x"}}}}}
	out, err := PatchEligibility(data, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"supported_inbound_dialects", "openai-chat", "openai-responses", "validation_status: passed", "max_request_bytes: 100", "implicit_openai_to_anthropic: false"} {
		if !strings.Contains(string(out), s) {
			t.Fatalf("missing %s: %s", s, out)
		}
	}
	var patched Config
	if err := yaml.Unmarshal(out, &patched); err != nil {
		t.Fatal(err)
	}
	out2, err := PatchEligibility(out, cfg)
	if err != nil || string(out2) != string(out) {
		t.Fatalf("not idempotent: %v", err)
	}
}

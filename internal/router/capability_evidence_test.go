// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"os"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func capabilityTestConfig(provider ProviderConfig, target Target) *Config {
	return &Config{
		Provider: map[string]ProviderConfig{"p": provider},
		Models:   map[string]ModelGroup{"group": {Targets: []Target{target}}},
	}
}

func evidenceForSurface(surface capabilitySurface, capability string) CapabilityEvidence {
	return CapabilityEvidence{
		SchemaVersion:  "capability-smoke/v1",
		Status:         "passed",
		CapabilityCase: capability,
		Identity: CapabilityEvidenceIdentity{
			Provider:             surface.target.Provider,
			AccountIdentityClass: surface.accountIdentity,
			EndpointFingerprint:  surface.endpointFingerprint,
			EndpointPath:         surface.endpointPath,
			APISkin:              surface.apiSkin,
			Model:                surface.model,
			ModelSuffix:          surface.modelSuffix,
			InboundDialect:       surface.inbound,
			BridgeDirection:      surface.bridge,
			RequestShape:         capabilityRequestShape(capability),
			ProfileVersion:       "synthetic/" + surface.bridge + "/" + capability,
		},
	}
}

func completeSurfaceEvidence(t *testing.T, provider ProviderConfig, target Target) ([]CapabilityEvidence, []CapabilityEvidenceIdentity) {
	t.Helper()
	surfaces, err := capabilitySurfaces(provider, target)
	if err != nil {
		t.Fatal(err)
	}
	var evidence []CapabilityEvidence
	var expected []CapabilityEvidenceIdentity
	for _, surface := range surfaces {
		for _, capability := range surface.capabilities {
			row := evidenceForSurface(surface, capability)
			evidence = append(evidence, row)
			expected = append(expected, row.Identity)
		}
	}
	return evidence, expected
}

func TestAdvertisedCapabilitiesUseRuntimeToolVocabulary(t *testing.T) {
	tests := []struct {
		name        string
		dialect     string
		toolSupport ToolSupport
		unsupported []string
		wantOmitted bool
		wantAuto    bool
		wantForced  bool
	}{
		{name: "chat", dialect: "openai-chat", toolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}, wantOmitted: true, wantAuto: true, wantForced: true},
		{name: "responses", dialect: "openai-responses", toolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}}, wantOmitted: true, wantAuto: true, wantForced: true},
		{name: "anthropic", dialect: "anthropic", toolSupport: ToolSupport{AnthropicMessages: []string{"client_tools", "tool_choice"}}, wantOmitted: true, wantAuto: true, wantForced: true},
		{name: "auto-only", dialect: "openai-responses", toolSupport: ToolSupport{OpenAIResponses: []string{"function"}}, wantOmitted: true, wantAuto: true},
		{name: "forced-explicitly-unsupported", dialect: "openai-responses", toolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}}, unsupported: []string{"forced_tool_choice"}, wantOmitted: true, wantAuto: true},
		{name: "chat-tool-choice-explicitly-unsupported", dialect: "openai-chat", toolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}, unsupported: []string{"tool_choice"}, wantOmitted: true},
		{name: "responses-tool-choice-explicitly-unsupported", dialect: "openai-responses", toolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}}, unsupported: []string{"tool_choice"}, wantOmitted: true},
		{name: "anthropic-tool-choice-explicitly-unsupported", dialect: "anthropic", toolSupport: ToolSupport{AnthropicMessages: []string{"client_tools", "tool_choice"}}, unsupported: []string{"tool_choice"}, wantOmitted: true},
		{name: "tools-explicitly-unsupported", dialect: "openai-chat", toolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}, unsupported: []string{"tools"}},
		{name: "normalized-unsupported-tools", dialect: "openai-chat", toolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}, unsupported: []string{" TOOLS "}},
		{name: "cross-skin", dialect: "openai-chat", toolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}}},
		{name: "provider-hosted-is-not-client-tools", dialect: "openai-responses", toolSupport: ToolSupport{ProviderHosted: []string{"tools", "tool_choice"}}},
		{name: "chat-tool-choice-alone", dialect: "openai-chat", toolSupport: ToolSupport{OpenAIChat: []string{"tool_choice"}}},
		{name: "chat-alias", dialect: "openai-chat", toolSupport: ToolSupport{OpenAIChat: []string{"function_tools", "forced_tool_choice"}}},
		{name: "responses-alias", dialect: "openai-responses", toolSupport: ToolSupport{OpenAIResponses: []string{"tools", "functions", "tool_choice"}}},
		{name: "anthropic-alias", dialect: "anthropic", toolSupport: ToolSupport{AnthropicMessages: []string{"tools", "tool_use", "tool_choice"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := Target{ToolSupport: test.toolSupport, RequestShapeSupport: RequestShapeSupport{UnsupportedRequestFeatures: test.unsupported}}
			capabilities := advertisedCapabilities(target, test.dialect)
			if got := stringSliceContains(capabilities, "tools-omitted"); got != test.wantOmitted {
				t.Fatalf("tools-omitted=%v, want %v: %v", got, test.wantOmitted, capabilities)
			}
			if got := stringSliceContains(capabilities, "tools-auto"); got != test.wantAuto {
				t.Fatalf("tools-auto=%v, want %v: %v", got, test.wantAuto, capabilities)
			}
			if got := stringSliceContains(capabilities, "tools-forced"); got != test.wantForced {
				t.Fatalf("tools-forced=%v, want %v: %v", got, test.wantForced, capabilities)
			}
		})
	}
}

func TestUnsupportedExplicitToolChoiceCannotSatisfyAutoEvidence(t *testing.T) {
	tests := []struct {
		name        string
		dialect     string
		toolSupport ToolSupport
	}{
		{name: "chat", dialect: "openai-chat", toolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}},
		{name: "responses", dialect: "openai-responses", toolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}}},
		{name: "anthropic", dialect: "anthropic", toolSupport: ToolSupport{AnthropicMessages: []string{"client_tools", "tool_choice"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := Target{
				Provider:    "p",
				Model:       "synthetic",
				Dialect:     test.dialect,
				ToolSupport: test.toolSupport,
				RequestShapeSupport: RequestShapeSupport{
					UnsupportedRequestFeatures: []string{" tool_choice "},
				},
			}
			if !targetSupportsClientTools(target, test.dialect, false) {
				t.Fatal("omitted tool_choice should retain ordinary client-tool eligibility")
			}
			if targetSupportsExplicitAutoToolChoice(target, test.dialect) {
				t.Fatal("explicit tool_choice:auto should not be eligible")
			}
			provider := ProviderConfig{BaseURL: "https://provider.example/v1", Dialect: test.dialect, KeyID: "test"}
			cfg := capabilityTestConfig(provider, target)
			surfaces, err := capabilitySurfaces(provider, target)
			if err != nil {
				t.Fatal(err)
			}
			for _, surface := range surfaces {
				if surface.bridge != "none" {
					continue
				}
				if !stringSliceContains(surface.capabilities, "tools-omitted") {
					t.Fatalf("callable omitted tool choice lacked distinct evidence: %v", surface.capabilities)
				}
				if stringSliceContains(surface.capabilities, "tools-auto") || stringSliceContains(surface.capabilities, "tools-forced") {
					t.Fatalf("unsupported explicit tool_choice advertised: %v", surface.capabilities)
				}
				allEvidence, expected := completeSurfaceEvidence(t, provider, target)
				for index, row := range allEvidence {
					if row.CapabilityCase == "tools-omitted" {
						allEvidence = append(allEvidence[:index], allEvidence[index+1:]...)
						expected = append(expected[:index], expected[index+1:]...)
						if failures := cfg.VerifyAdvertisedCapabilities("group", allEvidence, expected); len(failures) != 1 {
							t.Fatalf("missing omitted tool evidence failures=%v, want one", failures)
						}
						break
					}
				}
				evidence := evidenceForSurface(surface, "tools-auto")
				if failures := cfg.VerifyCapabilityClaims("group", []CapabilityEvidence{evidence}, []CapabilityEvidenceIdentity{evidence.Identity}); len(failures) != 1 {
					t.Fatalf("explicit auto evidence failures=%v, want one advertisement failure", failures)
				}
			}
		})
	}
}

func TestProviderHostedStructuredOutputsDoNotSatisfyClientSkins(t *testing.T) {
	target := Target{
		ToolSupport: ToolSupport{ProviderHosted: []string{"structured_outputs"}},
	}
	for _, dialect := range []string{"openai-chat", "openai-responses", "anthropic"} {
		if capabilities := advertisedCapabilities(target, dialect); stringSliceContains(capabilities, "structured-outputs") {
			t.Fatalf("%s advertised provider-hosted structured output as client support: %v", dialect, capabilities)
		}
		if targetSupportsStructuredOutput(target, dialect, dialect, true) {
			t.Fatalf("%s accepted provider-hosted structured output as client support", dialect)
		}
	}
}

func TestProviderHostedStructuredOutputsStayInactiveAcrossConsumers(t *testing.T) {
	target := Target{
		Provider:    "p",
		Model:       "synthetic",
		ToolSupport: ToolSupport{ProviderHosted: []string{"structured_outputs"}},
	}
	req := &IRRequest{Raw: map[string]any{"response_format": map[string]any{"type": "json_schema"}}}
	contract := &ModelGroupContract{
		RequiredCaps: ContractRequiredCapabilities{StructuredOutputs: true},
	}
	if reason := targetPassesContract(contract, target, "openai-chat", "openai-chat", req, dynamicStats{}, time.Now().UTC()); reason != "contract-required-structured-outputs" {
		t.Fatalf("contract accepted provider-hosted structured output: reason=%q", reason)
	}
	filters := DynamicScoreHardFilters{RequireStructuredOutputSupport: true}
	if dynamicPassesHardFilters(target, req, "openai-chat", "openai-chat", filters) {
		t.Fatal("dynamic hard filter accepted provider-hosted structured output")
	}

	cfg := Config{
		Server: ServerConfig{Identifiers: IdentifierConfig{Mode: "passthrough"}, DecisionTelemetry: DecisionTelemetryConfig{Enabled: true}},
		Provider: map[string]ProviderConfig{
			"p": {
				Dialect: "openai-chat",
				Models: map[string]ProviderModel{
					"synthetic": {
						Model:       "synthetic",
						ToolSupport: target.ToolSupport,
					},
				},
			},
		},
		Models: map[string]ModelGroup{
			"group": {Targets: []Target{{Provider: "p", ModelRef: "synthetic"}}},
		},
	}
	group := ModelGroup{Targets: []Target{target}}
	rc := &requestContext{}
	(&Service{cfg: &cfg}).recordEligibilityTelemetry(rc, "group", group, req, "openai-chat")
	if len(rc.rec.DecisionCandidates) != 1 || rc.rec.DecisionCandidates[0].StructuredOutput {
		t.Fatalf("telemetry reported provider-hosted structured output as client support: %#v", rc.rec.DecisionCandidates)
	}

	report := buildAdminCatalogStatusResponse(cfg)
	if len(report.GroupSummary) != 1 || report.GroupSummary[0].OpenAIChatStructuredOutputTargets != 0 {
		t.Fatalf("admin group summary reported provider-hosted structured output as client support: %#v", report.GroupSummary)
	}
	for _, row := range report.Rows {
		if row.Source == "active_target" && row.EffectiveStructured {
			t.Fatalf("admin target reported provider-hosted structured output as client support: %#v", row)
		}
	}
}

func TestResponsesToChatStructuredOutputRemainsUnsupportedAcrossConsumers(t *testing.T) {
	target := Target{
		Provider:        "p",
		Model:           "synthetic",
		Dialect:         "openai-chat",
		ToolSupport:     ToolSupport{OpenAIChat: []string{"structured_outputs"}},
		ResponsesToChat: ResponsesToChatBridge{Enabled: true, StructuredOutputs: true},
	}
	req := &IRRequest{Raw: map[string]any{"response_format": map[string]any{"type": "json_schema"}}}
	if targetSupportsStructuredOutput(target, "openai-responses", "openai-chat", true) {
		t.Fatal("Responses-to-Chat bridge advertised unsupported structured output")
	}
	if reason := responsesToChatBridgeFilterReason(target, req, "openai-responses", "openai-chat"); reason != "responses-to-chat-structured-output" {
		t.Fatalf("runtime bridge filter reason=%q", reason)
	}
	contract := &ModelGroupContract{
		RequiredCaps: ContractRequiredCapabilities{StructuredOutputs: true},
	}
	if reason := targetPassesContract(contract, target, "openai-chat", "openai-responses", req, dynamicStats{}, time.Now().UTC()); reason != "contract-required-structured-outputs" {
		t.Fatalf("contract accepted Responses-to-Chat structured output: reason=%q", reason)
	}
	filters := DynamicScoreHardFilters{RequireStructuredOutputSupport: true}
	if dynamicPassesHardFilters(target, req, "openai-responses", "openai-chat", filters) {
		t.Fatal("dynamic hard filter accepted Responses-to-Chat structured output")
	}

	cfg := Config{
		Server:   ServerConfig{Identifiers: IdentifierConfig{Mode: "passthrough"}, DecisionTelemetry: DecisionTelemetryConfig{Enabled: true}},
		Provider: map[string]ProviderConfig{"p": {Dialect: "openai-chat"}},
	}
	rc := &requestContext{}
	(&Service{cfg: &cfg}).recordEligibilityTelemetry(rc, "group", ModelGroup{Targets: []Target{target}}, req, "openai-responses")
	if len(rc.rec.DecisionCandidates) != 1 || rc.rec.DecisionCandidates[0].StructuredOutput {
		t.Fatalf("telemetry reported Responses-to-Chat structured output as supported: %#v", rc.rec.DecisionCandidates)
	}
}

func TestAdvertisedCapabilitiesHonorToolOnlyAndUnsupportedFeatures(t *testing.T) {
	target := Target{
		ToolOnly:        true,
		InputModalities: []string{"text", "image"},
		ToolSupport: ToolSupport{OpenAIResponses: []string{
			"function", "tool_choice", "structured_outputs",
		}},
	}
	capabilities := advertisedCapabilities(target, "openai-responses")
	for _, forbidden := range []string{"text", "image-input", "structured-outputs"} {
		if stringSliceContains(capabilities, forbidden) {
			t.Fatalf("ToolOnly capability %q advertised as standalone: %v", forbidden, capabilities)
		}
	}
	for _, required := range []string{"tools-omitted", "tools-auto", "tools-forced"} {
		if !stringSliceContains(capabilities, required) {
			t.Fatalf("supported capability %q omitted: %v", required, capabilities)
		}
	}

	target.ToolOnly = false
	target.RequestShapeSupport.UnsupportedRequestFeatures = []string{" IMAGE ", "response_format"}
	capabilities = advertisedCapabilities(target, "openai-responses")
	for _, forbidden := range []string{"image-input", "structured-outputs"} {
		if stringSliceContains(capabilities, forbidden) {
			t.Fatalf("explicitly unsupported capability %q advertised: %v", forbidden, capabilities)
		}
	}

	target.RequestShapeSupport.UnsupportedRequestFeatures = nil
	target.RequestShapeSupport.RequiredInputModalities = []string{"image"}
	capabilities = advertisedCapabilities(target, "openai-responses")
	if len(capabilities) != 1 || capabilities[0] != "image-input" {
		t.Fatalf("image-required target advertised uncallable standalone shapes: %v", capabilities)
	}
}

func TestExampleConfigToolEvidenceCasesStayPerSkin(t *testing.T) {
	raw, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		group      string
		dialect    string
		wantForced bool
	}{
		{group: "warp-agent-smoke", dialect: "openai-chat", wantForced: true},
		{group: "agent-tools-smoke", dialect: "openai-responses", wantForced: false},
		{group: "claude-tools-smoke", dialect: "anthropic", wantForced: false},
	} {
		t.Run(test.group, func(t *testing.T) {
			group := cfg.Models[test.group]
			if len(group.Targets) != 1 {
				t.Fatalf("current config group target count=%d, want 1", len(group.Targets))
			}
			target, err := cfg.resolveTarget(test.group, group.Targets[0])
			if err != nil {
				t.Fatal(err)
			}
			capabilities := advertisedCapabilities(target, test.dialect)
			if !stringSliceContains(capabilities, "tools-auto") {
				t.Fatalf("current %s metadata did not map to tools-auto: %#v", test.dialect, target.ToolSupport)
			}
			if !stringSliceContains(capabilities, "tools-omitted") {
				t.Fatalf("current %s metadata did not map to tools-omitted: %#v", test.dialect, target.ToolSupport)
			}
			if got := stringSliceContains(capabilities, "tools-forced"); got != test.wantForced {
				t.Fatalf("current %s tools-forced=%v, want %v: %#v", test.dialect, got, test.wantForced, target.ToolSupport)
			}
			for _, otherDialect := range []string{"openai-chat", "openai-responses", "anthropic"} {
				if otherDialect != test.dialect && targetSupportsClientTools(target, otherDialect, false) {
					t.Fatalf("%s tool metadata leaked into %s: %#v", test.dialect, otherDialect, target.ToolSupport)
				}
			}
		})
	}
}

func TestVerifyCapabilityClaimsUsesResolvedTargetMetadata(t *testing.T) {
	provider := ProviderConfig{BaseURL: "https://provider.example/v1", Dialect: "openai-responses", KeyID: "test", Models: map[string]ProviderModel{
		"m": {Model: "synthetic", Dialect: "openai-responses", ToolSupport: ToolSupport{OpenAIResponses: []string{"function"}}},
	}}
	cfg := capabilityTestConfig(provider, Target{Provider: "p", ModelRef: "m", Weight: 1})
	resolved, err := cfg.resolveTarget("group", cfg.Models["group"].Targets[0])
	if err != nil {
		t.Fatal(err)
	}
	surfaces, err := capabilitySurfaces(provider, resolved)
	if err != nil {
		t.Fatal(err)
	}
	claim := evidenceForSurface(surfaces[0], "tools-auto")
	if failures := cfg.VerifyCapabilityClaims("group", []CapabilityEvidence{claim}, []CapabilityEvidenceIdentity{claim.Identity}); len(failures) != 0 {
		t.Fatalf("passing inherited capability rejected: %v", failures)
	}
	omitted := evidenceForSurface(surfaces[0], "tools-omitted")
	if failures := cfg.VerifyCapabilityClaims("group", []CapabilityEvidence{omitted}, []CapabilityEvidenceIdentity{omitted.Identity}); len(failures) != 0 {
		t.Fatalf("omitted tool claim rejected: %v", failures)
	}
	forced := evidenceForSurface(surfaces[0], "tools-forced")
	if failures := cfg.VerifyCapabilityClaims("group", []CapabilityEvidence{forced}, []CapabilityEvidenceIdentity{forced.Identity}); len(failures) != 1 {
		t.Fatalf("forced tool without metadata must fail: %v", failures)
	}
}

func TestVerifyCapabilityClaimsScopesDerivationToFullTargetIdentity(t *testing.T) {
	provider := ProviderConfig{BaseURL: "https://provider.example/v1", Dialect: "openai-responses", KeyID: "test"}
	valid := Target{Provider: "p", Model: "synthetic:nitro", Dialect: "openai-responses", Weight: 1}
	unrepresentable := Target{
		Provider:        "p",
		Model:           "synthetic:nitro",
		Dialect:         "openai-chat",
		ToolOnly:        true,
		InputModalities: []string{"text", "image"},
		ToolSupport:     ToolSupport{OpenAIChat: []string{"tools"}},
		Weight:          1,
	}
	cfg := &Config{
		Provider: map[string]ProviderConfig{"p": provider},
		Models:   map[string]ModelGroup{"group": {Targets: []Target{valid, unrepresentable}}},
	}
	validEvidence, validExpected := completeSurfaceEvidence(t, provider, valid)
	claim := validEvidence[0]
	if failures := cfg.VerifyCapabilityClaims("group", []CapabilityEvidence{claim}, []CapabilityEvidenceIdentity{claim.Identity}); len(failures) != 0 {
		t.Fatalf("valid claim rejected by unrelated same-provider target: %v", failures)
	}

	unrepresentableClaim := evidenceForSurface(capabilitySurface{
		target:          unrepresentable,
		accountIdentity: provider.KeyID,
		apiSkin:         "openai-chat",
		model:           "synthetic",
		modelSuffix:     ":nitro",
		inbound:         "openai-chat",
		bridge:          "none",
	}, "tools-auto")
	// Derive the endpoint values without calling capabilitySurfaces: this target
	// is intentionally unrepresentable and therefore cannot have a surface.
	path, fingerprint, err := capabilityEndpointIdentity(provider, unrepresentable, "openai-chat")
	if err != nil {
		t.Fatal(err)
	}
	unrepresentableClaim.Identity.EndpointPath = path
	unrepresentableClaim.Identity.EndpointFingerprint = fingerprint
	if failures := cfg.VerifyCapabilityClaims("group", []CapabilityEvidence{unrepresentableClaim}, []CapabilityEvidenceIdentity{unrepresentableClaim.Identity}); len(failures) == 0 {
		t.Fatal("claim for unrepresentable target did not fail closed")
	}
	if failures := cfg.VerifyAdvertisedCapabilities("group", validEvidence, validExpected); len(failures) != 1 {
		t.Fatalf("whole-group verification failures=%v, want unrepresentable target error", failures)
	}

	for _, mutation := range []struct {
		name   string
		change func(*CapabilityEvidenceIdentity)
	}{
		{name: "provider", change: func(identity *CapabilityEvidenceIdentity) { identity.Provider = "other" }},
		{name: "account", change: func(identity *CapabilityEvidenceIdentity) { identity.AccountIdentityClass = "other-account" }},
		{name: "endpoint", change: func(identity *CapabilityEvidenceIdentity) {
			identity.EndpointFingerprint = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}},
		{name: "api-skin", change: func(identity *CapabilityEvidenceIdentity) { identity.APISkin = "openai-chat" }},
		{name: "model", change: func(identity *CapabilityEvidenceIdentity) { identity.Model = "other" }},
		{name: "model-suffix", change: func(identity *CapabilityEvidenceIdentity) { identity.ModelSuffix = ":free" }},
		{name: "inbound", change: func(identity *CapabilityEvidenceIdentity) { identity.InboundDialect = "openai-chat" }},
		{name: "bridge", change: func(identity *CapabilityEvidenceIdentity) { identity.BridgeDirection = chatToResponsesBridgeDirection }},
		{name: "request-shape", change: func(identity *CapabilityEvidenceIdentity) { identity.RequestShape = "image" }},
		{name: "profile", change: func(identity *CapabilityEvidenceIdentity) { identity.ProfileVersion = "unapproved/v1" }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			mutated := claim
			mutation.change(&mutated.Identity)
			expected := mutated.Identity
			// A profile is evidence approval metadata rather than a resolved
			// target field, so the original approved profile must remain bound.
			if mutation.name == "profile" {
				expected = claim.Identity
			}
			if failures := cfg.VerifyCapabilityClaims("group", []CapabilityEvidence{mutated}, []CapabilityEvidenceIdentity{expected}); len(failures) == 0 {
				t.Fatal("cross-identity claim substitution bypassed verification")
			}
		})
	}
}

func TestCapabilityEvidenceUsesActualUpstreamEndpointPaths(t *testing.T) {
	tests := []struct {
		name       string
		baseURL    string
		dialect    string
		wantPath   string
		legacyPath string
	}{
		{name: "chat-noncanonical-base", baseURL: "https://provider.example/inference/v1", dialect: "openai-chat", wantPath: "/inference/v1/chat/completions", legacyPath: "/v1/chat/completions"},
		{name: "responses-noncanonical-base", baseURL: "https://provider.example/api/v1", dialect: "openai-responses", wantPath: "/api/v1/responses", legacyPath: "/v1/responses"},
		{name: "anthropic-root-base", baseURL: "https://provider.example", dialect: "anthropic", wantPath: "/v1/messages", legacyPath: "/anthropic/v1/messages"},
		{name: "anthropic-prefixed-base", baseURL: "https://provider.example/anthropic", dialect: "anthropic", wantPath: "/anthropic/v1/messages", legacyPath: "/v1/messages"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := ProviderConfig{BaseURL: test.baseURL, Dialect: test.dialect, KeyID: "test"}
			target := Target{Provider: "p", Model: "synthetic", Dialect: test.dialect, Weight: 1}
			cfg := capabilityTestConfig(provider, target)
			evidence, expected := completeSurfaceEvidence(t, provider, target)
			if evidence[0].Identity.EndpointPath != test.wantPath {
				t.Fatalf("endpoint path=%q, want %q", evidence[0].Identity.EndpointPath, test.wantPath)
			}
			if failures := cfg.VerifyAdvertisedCapabilities("group", evidence, expected); len(failures) != 0 {
				t.Fatalf("actual endpoint path rejected: %v", failures)
			}
			wrong := append([]CapabilityEvidence(nil), evidence...)
			wrongExpected := append([]CapabilityEvidenceIdentity(nil), expected...)
			wrong[0].Identity.EndpointPath = test.legacyPath
			wrongExpected[0] = wrong[0].Identity
			if failures := cfg.VerifyAdvertisedCapabilities("group", wrong, wrongExpected); len(failures) == 0 {
				t.Fatal("legacy path for an endpoint that is never called satisfied evidence")
			}
		})
	}

	provider := ProviderConfig{BaseURL: "://invalid", Dialect: "openai-chat", KeyID: "test"}
	target := Target{Provider: "p", Model: "synthetic", Dialect: "openai-chat", Weight: 1}
	if failures := capabilityTestConfig(provider, target).VerifyAdvertisedCapabilities("group", nil, nil); len(failures) != 1 {
		t.Fatalf("invalid endpoint construction did not fail closed: %v", failures)
	}
}
func TestCapabilityEndpointFingerprintBindsResolvedQuery(t *testing.T) {
	target := Target{Provider: "p", Model: "synthetic", Dialect: "openai-chat", Weight: 1}
	first := ProviderConfig{BaseURL: "https://provider.example/v1?api-version=one", Dialect: "openai-chat", KeyID: "test"}
	second := ProviderConfig{BaseURL: "https://provider.example/v1?api-version=two", Dialect: "openai-chat", KeyID: "test"}
	firstEvidence, _ := completeSurfaceEvidence(t, first, target)
	secondEvidence, secondExpected := completeSurfaceEvidence(t, second, target)
	if firstEvidence[0].Identity.EndpointFingerprint == secondEvidence[0].Identity.EndpointFingerprint {
		t.Fatal("different resolved endpoint queries produced the same fingerprint")
	}
	if failures := capabilityTestConfig(second, target).VerifyAdvertisedCapabilities("group", firstEvidence, secondExpected); len(failures) == 0 {
		t.Fatal("evidence for a different endpoint query satisfied promotion")
	}
}

func TestChatToResponsesBridgeRequiresDistinctShapeEvidence(t *testing.T) {
	provider := ProviderConfig{BaseURL: "https://provider.example/api/v1", Dialect: "openai-responses", KeyID: "test"}
	bridgeText := true
	target := Target{
		Provider:        "p",
		Model:           "synthetic",
		Dialect:         "openai-responses",
		Weight:          1,
		ToolSupport:     ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}},
		InputModalities: []string{"text", "image"},
		Bridges:         BridgeSupport{ChatToResponses: DialectBridgeSupport{Enabled: true, Text: &bridgeText, Tools: true, ToolChoice: true, Images: true}},
	}
	cfg := capabilityTestConfig(provider, target)
	evidence, expected := completeSurfaceEvidence(t, provider, target)
	if len(evidence) != 10 {
		t.Fatalf("direct plus bridge requirement count=%d, want 10", len(evidence))
	}
	if failures := cfg.VerifyAdvertisedCapabilities("group", evidence, expected); len(failures) != 0 {
		t.Fatalf("complete bridge evidence rejected: %v", failures)
	}

	var directOnly []CapabilityEvidence
	var directExpected []CapabilityEvidenceIdentity
	for _, row := range evidence {
		if row.Identity.BridgeDirection == "none" {
			directOnly = append(directOnly, row)
			directExpected = append(directExpected, row.Identity)
		}
	}
	if failures := cfg.VerifyAdvertisedCapabilities("group", directOnly, directExpected); len(failures) != 5 {
		t.Fatalf("direct evidence satisfied translated requirements: %v", failures)
	}

	bridgeIndex := -1
	for index, row := range evidence {
		if row.Identity.BridgeDirection == chatToResponsesBridgeDirection && row.CapabilityCase == "tools-omitted" {
			bridgeIndex = index
			break
		}
	}
	if bridgeIndex < 0 {
		t.Fatal("missing bridge tools-omitted fixture")
	}
	for _, mutation := range []struct {
		name   string
		change func(*CapabilityEvidenceIdentity)
	}{
		{name: "wrong-direction", change: func(identity *CapabilityEvidenceIdentity) { identity.BridgeDirection = responsesToChatBridgeDirection }},
		{name: "wrong-inbound", change: func(identity *CapabilityEvidenceIdentity) { identity.InboundDialect = "anthropic" }},
		{name: "wrong-shape", change: func(identity *CapabilityEvidenceIdentity) { identity.RequestShape = "text" }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			mutated := append([]CapabilityEvidence(nil), evidence...)
			mutatedExpected := append([]CapabilityEvidenceIdentity(nil), expected...)
			mutation.change(&mutated[bridgeIndex].Identity)
			mutatedExpected[bridgeIndex] = mutated[bridgeIndex].Identity
			if failures := cfg.VerifyAdvertisedCapabilities("group", mutated, mutatedExpected); len(failures) == 0 {
				t.Fatal("wrong translated identity satisfied bridge requirement")
			}
		})
	}
	mutated := append([]CapabilityEvidence(nil), evidence...)
	mutated[bridgeIndex].Identity.ProfileVersion = "unapproved/v1"
	if failures := cfg.VerifyAdvertisedCapabilities("group", mutated, expected); len(failures) == 0 {
		t.Fatal("unapproved bridge profile satisfied bridge requirement")
	}
}

func TestBridgeAutoEvidenceRequiresExplicitToolChoiceSupport(t *testing.T) {
	bridgeText := true
	responsesTarget := Target{
		Dialect: "openai-responses",
		ToolSupport: ToolSupport{OpenAIResponses: []string{
			"function", "tool_choice",
		}},
		Bridges: BridgeSupport{ChatToResponses: DialectBridgeSupport{
			Enabled: true, Text: &bridgeText, Tools: true,
		}},
	}
	if capabilities := chatToResponsesEvidenceCapabilities(responsesTarget, "openai-responses"); stringSliceContains(capabilities, "tools-auto") {
		t.Fatalf("Chat-to-Responses bridge advertised explicit auto without tool_choice bridge support: %v", capabilities)
	} else if !stringSliceContains(capabilities, "tools-omitted") {
		t.Fatalf("Chat-to-Responses bridge omitted callable omitted tool-choice evidence: %v", capabilities)
	}

	chatTarget := Target{
		Dialect: "openai-chat",
		ToolSupport: ToolSupport{OpenAIChat: []string{
			"tools", "tool_choice",
		}},
		RequestShapeSupport: RequestShapeSupport{
			UnsupportedRequestFeatures: []string{"tool_choice"},
		},
		ResponsesToChat: ResponsesToChatBridge{
			Enabled: true, FunctionTools: true, ToolChoice: true,
		},
	}
	if capabilities := responsesToChatEvidenceCapabilities(chatTarget, "openai-chat"); stringSliceContains(capabilities, "tools-auto") || stringSliceContains(capabilities, "tools-forced") {
		t.Fatalf("Responses-to-Chat bridge advertised unsupported explicit tool_choice: %v", capabilities)
	} else if !stringSliceContains(capabilities, "tools-omitted") {
		t.Fatalf("Responses-to-Chat bridge omitted callable omitted tool-choice evidence: %v", capabilities)
	}
}

func TestOmittedToolChoiceBridgeRuntimeMatchesDistinctEvidence(t *testing.T) {
	bridgeText := true
	responsesTarget := Target{
		Dialect:     "openai-responses",
		ToolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}},
		Bridges:     BridgeSupport{ChatToResponses: DialectBridgeSupport{Enabled: true, Text: &bridgeText, Tools: true}},
	}
	omittedChat := &IRRequest{Tools: []map[string]any{{"type": "function"}}, Raw: map[string]any{}}
	if reason := chatToResponsesBridgeFilterReason(responsesTarget, omittedChat, "openai-chat", "openai-responses"); reason != "" {
		t.Fatalf("Chat-to-Responses omitted tool choice rejected: %q", reason)
	}
	explicitChat := &IRRequest{Tools: omittedChat.Tools, Raw: map[string]any{"tool_choice": "auto"}}
	if reason := chatToResponsesBridgeFilterReason(responsesTarget, explicitChat, "openai-chat", "openai-responses"); reason != "chat-to-responses-tool-choice-unsupported" {
		t.Fatalf("Chat-to-Responses explicit auto reason=%q", reason)
	}
	nullChat := &IRRequest{Tools: omittedChat.Tools, Raw: map[string]any{"tool_choice": nil}}
	if reason := chatToResponsesBridgeFilterReason(responsesTarget, nullChat, "openai-chat", "openai-responses"); reason != "chat-to-responses-tool-choice-unsupported" {
		t.Fatalf("Chat-to-Responses explicit null without bridge support reason=%q", reason)
	}
	responsesTarget.Bridges.ChatToResponses.ToolChoice = true
	if reason := chatToResponsesBridgeFilterReason(responsesTarget, nullChat, "openai-chat", "openai-responses"); reason != "chat-to-responses-tool-choice-null-unsupported" {
		t.Fatalf("Chat-to-Responses explicit null with bridge support reason=%q", reason)
	}

	chatTarget := Target{
		Dialect:     "openai-chat",
		ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}},
		ResponsesToChat: ResponsesToChatBridge{
			Enabled: true, FunctionTools: true,
		},
	}
	omittedResponses := &IRRequest{Tools: []map[string]any{{"type": "function"}}, Raw: map[string]any{}}
	if reason := responsesToChatBridgeFilterReason(chatTarget, omittedResponses, "openai-responses", "openai-chat"); reason != "" {
		t.Fatalf("Responses-to-Chat omitted tool choice rejected: %q", reason)
	}
	explicitResponses := &IRRequest{Tools: omittedResponses.Tools, Raw: map[string]any{"tool_choice": "auto"}}
	if reason := responsesToChatBridgeFilterReason(chatTarget, explicitResponses, "openai-responses", "openai-chat"); reason != "responses-to-chat-tool-choice" {
		t.Fatalf("Responses-to-Chat explicit auto reason=%q", reason)
	}
	nullResponses := &IRRequest{Tools: omittedResponses.Tools, Raw: map[string]any{"tool_choice": nil}}
	if reason := responsesToChatBridgeFilterReason(chatTarget, nullResponses, "openai-responses", "openai-chat"); reason != "responses-to-chat-tool-choice" {
		t.Fatalf("Responses-to-Chat explicit null without bridge support reason=%q", reason)
	}
	chatTarget.ResponsesToChat.ToolChoice = true
	if reason := responsesToChatBridgeFilterReason(chatTarget, nullResponses, "openai-responses", "openai-chat"); reason != "responses-to-chat-tool-choice-null-unsupported" {
		t.Fatalf("Responses-to-Chat explicit null with bridge support reason=%q", reason)
	}
}

func TestBothRuntimeBridgeDirectionsAndDisabledShapes(t *testing.T) {
	chatProvider := ProviderConfig{BaseURL: "https://provider.example/v1", Dialect: "openai-chat", KeyID: "test"}
	chatTarget := Target{
		Provider:        "p",
		Model:           "synthetic",
		Dialect:         "openai-chat",
		Weight:          1,
		ToolSupport:     ToolSupport{OpenAIChat: []string{"tools", "tool_choice", "structured_outputs"}},
		InputModalities: []string{"text", "image"},
		ResponsesToChat: ResponsesToChatBridge{Enabled: true, Text: true, FunctionTools: true, ToolChoice: true, Images: true, StructuredOutputs: true},
	}
	chatCfg := capabilityTestConfig(chatProvider, chatTarget)
	chatEvidence, chatExpected := completeSurfaceEvidence(t, chatProvider, chatTarget)
	if len(chatEvidence) != 11 {
		t.Fatalf("Responses-to-Chat requirement count=%d, want direct 6 plus bridge 5", len(chatEvidence))
	}
	if failures := chatCfg.VerifyAdvertisedCapabilities("group", chatEvidence, chatExpected); len(failures) != 0 {
		t.Fatalf("Responses-to-Chat evidence rejected: %v", failures)
	}

	responsesProvider := ProviderConfig{BaseURL: "https://provider.example/v1", Dialect: "openai-responses", KeyID: "test"}
	bridgeText := true
	responsesTarget := Target{
		Provider:    "p",
		Model:       "synthetic",
		Dialect:     "openai-responses",
		Weight:      1,
		ToolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}},
		Bridges:     BridgeSupport{ChatToResponses: DialectBridgeSupport{Enabled: true, Text: &bridgeText}},
	}
	responsesCfg := capabilityTestConfig(responsesProvider, responsesTarget)
	responsesEvidence, responsesExpected := completeSurfaceEvidence(t, responsesProvider, responsesTarget)
	if len(responsesEvidence) != 5 {
		t.Fatalf("disabled bridge shape count=%d, want direct 4 plus bridge text", len(responsesEvidence))
	}
	if failures := responsesCfg.VerifyAdvertisedCapabilities("group", responsesEvidence, responsesExpected); len(failures) != 0 {
		t.Fatalf("disabled bridge shapes incorrectly required evidence: %v", failures)
	}

	bridgeText = false
	responsesTarget.Bridges.ChatToResponses.Text = &bridgeText
	if capabilities := chatToResponsesEvidenceCapabilities(responsesTarget, "openai-responses"); len(capabilities) != 0 {
		t.Fatalf("Text-disabled Chat-to-Responses bridge advertised unreachable shapes: %v", capabilities)
	}
	chatTarget.ResponsesToChat.Text = false
	capabilities := responsesToChatEvidenceCapabilities(chatTarget, "openai-chat")
	if len(capabilities) != 3 || !stringSliceContains(capabilities, "tools-omitted") || !stringSliceContains(capabilities, "tools-auto") || !stringSliceContains(capabilities, "tools-forced") {
		t.Fatalf("Text-disabled Responses-to-Chat capability set mismatch: %v", capabilities)
	}
}

func TestAnthropicInboundTranslationRequiresDistinctCallableEvidence(t *testing.T) {
	provider := ProviderConfig{BaseURL: "https://provider.example/v1", Dialect: "openai-responses", KeyID: "test"}
	target := Target{
		Provider:        "p",
		Model:           "synthetic",
		Dialect:         "openai-responses",
		Weight:          1,
		InputModalities: []string{"text", "image"},
		ToolSupport: ToolSupport{OpenAIResponses: []string{
			"function", "tool_choice", "structured_outputs",
		}},
		RequestShapeSupport: RequestShapeSupport{
			SupportedInboundDialects: []string{"anthropic"},
			ValidationStatus:         "passed",
		},
	}
	surfaces, err := capabilitySurfaces(provider, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(surfaces) != 1 {
		t.Fatalf("Anthropic-only translated surfaces=%d, want 1: %#v", len(surfaces), surfaces)
	}
	surface := surfaces[0]
	if surface.inbound != "anthropic" || surface.bridge != "anthropic_to_openai-responses" {
		t.Fatalf("Anthropic translation identity mismatch: %#v", surface)
	}
	if len(surface.capabilities) != 2 || !stringSliceContains(surface.capabilities, "text") || !stringSliceContains(surface.capabilities, "image-input") {
		t.Fatalf("Anthropic callable capability set mismatch: %v", surface.capabilities)
	}
	for _, unsupported := range []string{"tools-auto", "tools-forced", "structured-outputs"} {
		if stringSliceContains(surface.capabilities, unsupported) {
			t.Fatalf("uncallable Anthropic translation capability %q advertised: %v", unsupported, surface.capabilities)
		}
	}
}

func TestConfiguredGenericTranslationRequiresDistinctEvidence(t *testing.T) {
	provider := ProviderConfig{BaseURL: "https://provider.example", Dialect: "anthropic", KeyID: "test"}
	target := Target{
		Provider:        "p",
		Model:           "synthetic",
		Dialect:         "anthropic",
		Weight:          1,
		InputModalities: []string{"text", "image"},
		RequestShapeSupport: RequestShapeSupport{
			SupportedInboundDialects: []string{"openai-chat"},
		},
	}
	surfaces, err := capabilitySurfaces(provider, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(surfaces) != 1 {
		t.Fatalf("generic translation surfaces=%d, want 1: %#v", len(surfaces), surfaces)
	}
	surface := surfaces[0]
	if surface.inbound != "openai-chat" || surface.bridge != "openai-chat_to_anthropic" ||
		len(surface.capabilities) != 2 ||
		!stringSliceContains(surface.capabilities, "text") ||
		!stringSliceContains(surface.capabilities, "image-input") {
		t.Fatalf("generic translation evidence mismatch: %#v", surface)
	}
	malformed := evidenceForSurface(surface, "text").Identity
	malformed.BridgeDirection = "none"
	if capabilityIdentityComplete(malformed) {
		t.Fatal("mismatched bridge direction and dialect tuple was accepted")
	}
}

func TestImplicitGenericTranslationRequiresEvidenceWhenAllowlistIsEmpty(t *testing.T) {
	provider := ProviderConfig{BaseURL: "https://provider.example", Dialect: "anthropic", KeyID: "test"}
	target := Target{
		Provider:        "p",
		Model:           "synthetic",
		Dialect:         "anthropic",
		InputModalities: []string{"text", "image"},
	}
	surfaces, err := capabilitySurfaces(provider, target)
	if err != nil {
		t.Fatal(err)
	}
	directions := map[string]bool{}
	for _, surface := range surfaces {
		directions[surface.bridge] = true
	}
	for _, direction := range []string{"none", "openai-chat_to_anthropic", "openai-responses_to_anthropic"} {
		if !directions[direction] {
			t.Fatalf("implicit runtime translation %q omitted: %#v", direction, surfaces)
		}
	}
	if len(surfaces) != len(directions) || len(surfaces) != 3 {
		t.Fatalf("implicit translation surfaces=%d directions=%v, want three distinct surfaces", len(surfaces), directions)
	}
}

func TestUnrepresentableCompositeCapabilityShapesFailClosed(t *testing.T) {
	provider := ProviderConfig{BaseURL: "https://provider.example/v1", Dialect: "openai-responses", KeyID: "test"}
	tests := []Target{
		{
			Provider:        "p",
			Model:           "image-tools",
			Dialect:         "openai-responses",
			InputModalities: []string{"text", "image"},
			ToolSupport:     ToolSupport{OpenAIResponses: []string{"function"}},
			RequestShapeSupport: RequestShapeSupport{
				RequiredInputModalities: []string{"image"},
			},
		},
		{
			Provider:        "p",
			Model:           "tool-only-image",
			Dialect:         "openai-responses",
			ToolOnly:        true,
			InputModalities: []string{"text", "image"},
			ToolSupport:     ToolSupport{OpenAIResponses: []string{"function"}},
		},
	}
	for _, target := range tests {
		if _, err := capabilitySurfaces(provider, target); err == nil {
			t.Fatalf("unrepresentable composite surface passed for %s", target.Model)
		}
	}
}

func TestBridgeEvidenceHonorsSupportedInboundDialectAllowlist(t *testing.T) {
	provider := ProviderConfig{BaseURL: "https://provider.example/v1", Dialect: "openai-responses", KeyID: "test"}
	bridgeText := true
	target := Target{
		Provider:    "p",
		Model:       "synthetic",
		Dialect:     "openai-responses",
		Weight:      1,
		ToolSupport: ToolSupport{OpenAIResponses: []string{"function"}},
		Bridges:     BridgeSupport{ChatToResponses: DialectBridgeSupport{Enabled: true, Text: &bridgeText, Tools: true}},
		RequestShapeSupport: RequestShapeSupport{
			SupportedInboundDialects: []string{"openai-responses"},
		},
	}
	surfaces, err := capabilitySurfaces(provider, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(surfaces) != 1 || surfaces[0].inbound != "openai-responses" || surfaces[0].bridge != "none" {
		t.Fatalf("uncallable Chat bridge was advertised: %#v", surfaces)
	}
}

func TestExampleConfigAnthropicTranslationSurfacesMatchRuntime(t *testing.T) {
	raw, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	matched := 0
	for groupName, group := range cfg.Models {
		for _, rawTarget := range group.Targets {
			target, err := cfg.resolveTarget(groupName, rawTarget)
			if err != nil {
				t.Fatal(err)
			}
			apiSkin := targetDialect(cfg.Provider[target.Provider], target)
			if target.ToolOnly || apiSkin == "anthropic" ||
				!stringSliceContainsNormalizedDialect(target.RequestShapeSupport.SupportedInboundDialects, "anthropic") ||
				strings.ToLower(strings.TrimSpace(target.RequestShapeSupport.ValidationStatus)) != "passed" {
				continue
			}
			surfaces, err := capabilitySurfaces(cfg.Provider[target.Provider], target)
			if err != nil {
				t.Fatalf("%s/%s: %v", groupName, target.Model, err)
			}
			found := false
			for _, surface := range surfaces {
				if surface.inbound == "anthropic" && surface.bridge == anthropicTranslationBridgePrefix+apiSkin {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s/%s omitted validated Anthropic translation surface", groupName, target.Model)
			}
			matched++
		}
	}
	if matched == 0 {
		t.Fatal("example config has no validated Anthropic translation surfaces")
	}
}

func TestVerifyCapabilityClaimsRequiresExactApprovedIdentity(t *testing.T) {
	provider := ProviderConfig{BaseURL: "https://provider.example/v1", Dialect: "openai-responses", KeyID: "test"}
	target := Target{Provider: "p", Model: "synthetic", Dialect: "openai-responses", Weight: 1}
	cfg := capabilityTestConfig(provider, target)
	evidence, _ := completeSurfaceEvidence(t, provider, target)
	claim := evidence[0]
	expected := claim.Identity
	expected.AccountIdentityClass = "different-account-class"
	if failures := cfg.VerifyCapabilityClaims("group", []CapabilityEvidence{claim}, []CapabilityEvidenceIdentity{expected}); len(failures) != 1 {
		t.Fatalf("unapproved identity tuple must fail closed: %v", failures)
	}
}

func TestStructuredOutputClaimsRequireDirectAndBridgeEvidence(t *testing.T) {
	provider := ProviderConfig{BaseURL: "https://provider.example/v1", Dialect: "openai-responses", KeyID: "test"}
	bridgeText := true
	target := Target{
		Provider:    "p",
		Model:       "synthetic",
		Dialect:     "openai-responses",
		Weight:      1,
		ToolSupport: ToolSupport{OpenAIResponses: []string{"structured_outputs"}},
		Bridges:     BridgeSupport{ChatToResponses: DialectBridgeSupport{Enabled: true, Text: &bridgeText, StructuredOutputs: true}},
	}
	cfg := capabilityTestConfig(provider, target)
	evidence, expected := completeSurfaceEvidence(t, provider, target)
	if len(evidence) != 4 {
		t.Fatalf("structured-output requirement count=%d, want direct and bridge text plus structured output", len(evidence))
	}
	if failures := cfg.VerifyAdvertisedCapabilities("group", evidence, expected); len(failures) != 0 {
		t.Fatalf("complete structured-output evidence rejected: %v", failures)
	}
	for index, row := range evidence {
		if row.Identity.BridgeDirection == chatToResponsesBridgeDirection && row.CapabilityCase == "structured-outputs" {
			evidence = append(evidence[:index], evidence[index+1:]...)
			expected = append(expected[:index], expected[index+1:]...)
			if failures := cfg.VerifyAdvertisedCapabilities("group", evidence, expected); len(failures) != 1 {
				t.Fatalf("missing bridge structured-output evidence did not fail closed: %v", failures)
			}
			return
		}
	}
	t.Fatal("missing bridge structured-output requirement")
}

func TestEvidenceIdentityMustMatchResolvedTarget(t *testing.T) {
	provider := ProviderConfig{BaseURL: "https://provider.example/v1", Dialect: "openai-chat", KeyID: "account-a"}
	target := Target{Provider: "p", Model: "synthetic:nitro", Dialect: "openai-chat", Weight: 1}
	cfg := capabilityTestConfig(provider, target)
	evidence, expected := completeSurfaceEvidence(t, provider, target)
	if got := evidence[0].Identity; got.Model != "synthetic" || got.ModelSuffix != ":nitro" {
		t.Fatalf("model identity not split into base and suffix: %#v", got)
	}
	if failures := cfg.VerifyAdvertisedCapabilities("group", evidence, expected); len(failures) != 0 {
		t.Fatalf("exact target identity rejected: %v", failures)
	}
	if failures := cfg.VerifyCapabilityClaims("group", []CapabilityEvidence{evidence[0]}, []CapabilityEvidenceIdentity{expected[0]}); len(failures) != 0 {
		t.Fatalf("exact suffixed target claim rejected: %v", failures)
	}
	for _, mutation := range []struct {
		name   string
		change func(*CapabilityEvidenceIdentity)
	}{
		{name: "account", change: func(identity *CapabilityEvidenceIdentity) { identity.AccountIdentityClass = "account-b" }},
		{name: "endpoint", change: func(identity *CapabilityEvidenceIdentity) {
			identity.EndpointFingerprint = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}},
		{name: "model-suffix", change: func(identity *CapabilityEvidenceIdentity) { identity.ModelSuffix = ":free" }},
		{name: "api-skin", change: func(identity *CapabilityEvidenceIdentity) { identity.APISkin = "openai-responses" }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			mutatedEvidence := append([]CapabilityEvidence(nil), evidence...)
			mutatedExpected := append([]CapabilityEvidenceIdentity(nil), expected...)
			mutation.change(&mutatedEvidence[0].Identity)
			mutatedExpected[0] = mutatedEvidence[0].Identity
			if failures := cfg.VerifyAdvertisedCapabilities("group", mutatedEvidence, mutatedExpected); len(failures) == 0 {
				t.Fatal("evidence for a different active target identity passed")
			}
		})
	}
}

func TestCapabilityEvidenceRequiresProviderKeyID(t *testing.T) {
	provider := ProviderConfig{BaseURL: "https://provider.example/v1", Dialect: "openai-chat"}
	target := Target{Provider: "p", Model: "synthetic", Dialect: "openai-chat", Weight: 1}
	failures := capabilityTestConfig(provider, target).VerifyAdvertisedCapabilities("group", nil, nil)
	if len(failures) != 1 {
		t.Fatalf("provider without non-secret key identity did not fail closed: %v", failures)
	}
}

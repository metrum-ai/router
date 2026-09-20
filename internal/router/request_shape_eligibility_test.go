// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequestShapeFitSmallTextEligible(t *testing.T) {
	cfg := minimalConfig(t)
	target := cfg.Models["default"].Targets[0]
	target.ContextTokens = 4096
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{target}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	svc := &Service{cfg: cfg}
	req := &IRRequest{
		Model:    "default",
		Messages: []IRMessage{{Role: "user", Content: "hello"}},
		Raw:      map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hello"}}},
	}
	estimate := requestTokenEstimateFromIR(req, "openai-chat", 96)
	fit := svc.targetRequestShapeFit(cfg.Models["default"].Targets[0], req, "openai-chat", "openai-chat", estimate)
	if fit.FilterReason != "" || fit.EligibilityDecision != "eligible" || !fit.ContextFit {
		t.Fatalf("fit=%#v", fit)
	}
}

func TestRequestShapeFitRequiresImageWhenConfigured(t *testing.T) {
	cfg := minimalConfig(t)
	target := cfg.Models["default"].Targets[0]
	target.InputModalities = []string{"text", "image"}
	target.RequestShapeSupport.RequiredInputModalities = []string{"image"}
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{target}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	svc := &Service{cfg: cfg}
	textReq := &IRRequest{Model: "default", Messages: []IRMessage{{Role: "user", Content: "hello"}}}
	textFit := svc.targetRequestShapeFit(target, textReq, "openai-responses", "openai-responses", requestTokenEstimateFromIR(textReq, "openai-responses", 96))
	if textFit.FilterReason != "request-shape-required-input-modality" {
		t.Fatalf("text fit=%#v", textFit)
	}
	imageReq := &IRRequest{Model: "default", Messages: []IRMessage{{Role: "user", Parts: []IRContentPart{{Type: "image", ImageURL: "https://example.test/receipt.png"}}}}}
	imageFit := svc.targetRequestShapeFit(target, imageReq, "openai-responses", "openai-responses", requestTokenEstimateFromIR(imageReq, "openai-responses", 96))
	if imageFit.FilterReason != "" {
		t.Fatalf("image fit=%#v", imageFit)
	}
}

func TestRequestShapeFitExplicitOutputCapAffectsContext(t *testing.T) {
	cfg := minimalConfig(t)
	target := cfg.Models["default"].Targets[0]
	target.ContextTokens = 300
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{target}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	svc := &Service{cfg: cfg}
	req := &IRRequest{
		Model:          "default",
		Messages:       []IRMessage{{Role: "user", Content: strings.Repeat("x", 400)}},
		MaxTokens:      250,
		MaxTokensField: "max_tokens",
		Raw:            map[string]any{"max_tokens": float64(250)},
	}
	estimate := requestTokenEstimateFromIR(req, "openai-chat", 600)
	fit := svc.targetRequestShapeFit(cfg.Models["default"].Targets[0], req, "openai-chat", "openai-chat", estimate)
	if fit.FilterReason != "request-shape-context-exceeded" || fit.ContextFit {
		t.Fatalf("fit=%#v", fit)
	}
	req.MaxTokens = 16
	req.Raw["max_tokens"] = float64(16)
	estimate = requestTokenEstimateFromIR(req, "openai-chat", 600)
	fit = svc.targetRequestShapeFit(cfg.Models["default"].Targets[0], req, "openai-chat", "openai-chat", estimate)
	if fit.FilterReason != "" || !fit.ContextFit {
		t.Fatalf("fit with smaller cap=%#v", fit)
	}
}

func TestRequestShapeFitSkipsExplicitOutputCapsBelowTargetMinimum(t *testing.T) {
	cfg := minimalConfig(t)
	target := cfg.Models["default"].Targets[0]
	target.RequestShapeSupport.MinRequestedOutputTokens = 16
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{target}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	svc := &Service{cfg: cfg}
	req := &IRRequest{
		Model:          "default",
		Messages:       []IRMessage{{Role: "user", Content: "hello"}},
		MaxTokens:      1,
		MaxTokensField: "max_output_tokens",
		Raw:            map[string]any{"max_output_tokens": float64(1)},
	}
	estimate := requestTokenEstimateFromIR(req, "openai-responses", 96)
	fit := svc.targetRequestShapeFit(cfg.Models["default"].Targets[0], req, "openai-responses", "openai-responses", estimate)
	if fit.FilterReason != "request-shape-min-output-tokens" || fit.MinRequestedOutputTokens != 16 {
		t.Fatalf("fit=%#v", fit)
	}
	req.MaxTokens = 16
	req.Raw["max_output_tokens"] = float64(16)
	estimate = requestTokenEstimateFromIR(req, "openai-responses", 96)
	fit = svc.targetRequestShapeFit(cfg.Models["default"].Targets[0], req, "openai-responses", "openai-responses", estimate)
	if fit.FilterReason != "" {
		t.Fatalf("fit with accepted cap=%#v", fit)
	}
}

func TestRequestShapeFitOutputCapMinimumDoesNotRaiseOrRequireAnOmittedCallerCap(t *testing.T) {
	cfg := minimalConfig(t)
	target := cfg.Models["default"].Targets[0]
	target.RequestShapeSupport.MinRequestedOutputTokens = 256
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{target}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	svc := &Service{cfg: cfg}
	req := &IRRequest{
		Model:    "default",
		Messages: []IRMessage{{Role: "user", Content: "hello"}},
		Raw:      map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hello"}}},
	}
	estimate := requestTokenEstimateFromIR(req, "openai-responses", 96)
	fit := svc.targetRequestShapeFit(target, req, "openai-responses", "openai-responses", estimate)
	if fit.FilterReason != "" {
		t.Fatalf("omitted caller cap must remain eligible, fit=%#v", fit)
	}
	if fit.Estimate.RequestedOutputCapTokens != 0 {
		t.Fatalf("request estimate must not invent a caller cap, got %d", fit.Estimate.RequestedOutputCapTokens)
	}
}

func TestRequestShapeFitUsesTargetDialectDefaultOutputReservation(t *testing.T) {
	cfg := minimalConfig(t)
	target := cfg.Models["default"].Targets[0]
	target.ContextTokens = 1100
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{target}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	svc := &Service{cfg: cfg}
	req := &IRRequest{
		Model:    "default",
		Messages: []IRMessage{{Role: "user", Content: strings.Repeat("x", 600)}},
		Raw:      map[string]any{"messages": []any{map[string]any{"role": "user", "content": strings.Repeat("x", 600)}}},
	}
	estimate := requestTokenEstimateFromIR(req, "openai-chat", 900)
	if estimate.RequestedOutputCapTokens != 0 {
		t.Fatalf("caller estimate reserved output=%d, want 0", estimate.RequestedOutputCapTokens)
	}
	fit := svc.targetRequestShapeFit(cfg.Models["default"].Targets[0], req, "openai-chat", "anthropic", estimate)
	if fit.Estimate.RequestedOutputCapTokens != 1024 || !fit.Estimate.RouterDefaultOutputCapApplied || fit.Estimate.RequestedOutputCapField != "router_default" {
		t.Fatalf("target estimate=%#v, want Anthropic router default output reservation", fit.Estimate)
	}
	if fit.FilterReason != "request-shape-context-exceeded" || fit.ContextFit {
		t.Fatalf("fit=%#v", fit)
	}
}

func TestRequestShapeUnsupportedForcedToolChoiceIgnoresAutoAndNone(t *testing.T) {
	for _, tc := range []struct {
		name       string
		choice     any
		wantForced bool
	}{
		{name: "auto_string", choice: "auto"},
		{name: "none_string", choice: "none"},
		{name: "required_string", choice: "required", wantForced: true},
		{name: "function_object", choice: map[string]any{"type": "function", "function": map[string]any{"name": "pick"}}, wantForced: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := &IRRequest{Raw: map[string]any{"tool_choice": tc.choice}}
			got := requestFeaturePresent(req, "forced_tool_choice")
			if got != tc.wantForced {
				t.Fatalf("forced tool choice present=%v, want %v", got, tc.wantForced)
			}
			if !requestFeaturePresent(req, "tool_choice") {
				t.Fatal("tool_choice feature should be present whenever the field is present")
			}
		})
	}
}

func TestRequestShapeUnsupportedToolChoiceRejectsExplicitAutoOnly(t *testing.T) {
	target := Target{RequestShapeSupport: RequestShapeSupport{
		UnsupportedRequestFeatures: []string{" tool_choice "},
	}}
	for _, dialect := range []string{"openai-chat", "openai-responses", "anthropic"} {
		t.Run(dialect, func(t *testing.T) {
			explicitAuto := &IRRequest{
				Tools: []map[string]any{{"name": "pick"}},
				Raw:   map[string]any{"tool_choice": "auto"},
			}
			explicitFit := (&Service{}).targetRequestShapeFit(target, explicitAuto, dialect, dialect, requestTokenEstimateFromIR(explicitAuto, dialect, 96))
			if explicitFit.FilterReason != "request-shape-unsupported-feature" {
				t.Fatalf("explicit auto fit=%#v", explicitFit)
			}

			omittedChoice := &IRRequest{Tools: []map[string]any{{"name": "pick"}}}
			omittedFit := (&Service{}).targetRequestShapeFit(target, omittedChoice, dialect, dialect, requestTokenEstimateFromIR(omittedChoice, dialect, 96))
			if omittedFit.FilterReason != "" || omittedFit.EligibilityDecision != "eligible" {
				t.Fatalf("omitted tool_choice fit=%#v", omittedFit)
			}

			explicitNull := &IRRequest{
				Tools: []map[string]any{{"name": "pick"}},
				Raw:   map[string]any{"tool_choice": nil},
			}
			nullFit := (&Service{}).targetRequestShapeFit(target, explicitNull, dialect, dialect, requestTokenEstimateFromIR(explicitNull, dialect, 96))
			if nullFit.FilterReason != "request-shape-unsupported-feature" {
				t.Fatalf("explicit null fit=%#v", nullFit)
			}
		})
	}
}

func TestToolChoiceJSONPresenceDistinguishesOmittedAndNull(t *testing.T) {
	omitted, err := decodeRequest("openai-chat", []byte(`{"model":"group","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"pick"}}]}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	nullChoice, err := decodeRequest("openai-chat", []byte(`{"model":"group","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"pick"}}],"tool_choice":null}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	if requestFeaturePresent(omitted, "tool_choice") || rawKeyPresent(omitted.Raw, "tool_choice") {
		t.Fatal("omitted tool_choice reported as present")
	}
	if !requestFeaturePresent(nullChoice, "tool_choice") || !rawKeyPresent(nullChoice.Raw, "tool_choice") || rawValuePresent(nullChoice.Raw, "tool_choice") {
		t.Fatalf("explicit null presence was not preserved: %#v", nullChoice.Raw)
	}
}

func TestRequestShapeFeatureDetectsStreamOptions(t *testing.T) {
	req := &IRRequest{Raw: map[string]any{"stream_options": map[string]any{"include_usage": true}}}
	if !requestFeaturePresent(req, "stream_options") {
		t.Fatal("stream_options feature should be present when the raw request contains stream_options")
	}
	if requestFeaturePresent(&IRRequest{Raw: map[string]any{}}, "stream_options") {
		t.Fatal("stream_options feature should be absent when the raw request omits stream_options")
	}
}

func TestLargeCodingAgentPayloadSkipsSmallContextTargetAndPersistsSafeTelemetry(t *testing.T) {
	var seenModels []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode upstream body: %v", err)
		}
		seenModels = append(seenModels, stringValue(body["model"]))
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "chatcmpl_context_fit",
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	sum := sha256.Sum256([]byte(testToken))
	cfg := &Config{
		Server: ServerConfig{Identifiers: IdentifierConfig{Mode: "passthrough"},
			Listen:            ":0",
			UsageDB:           freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite")),
			DecisionTelemetry: DecisionTelemetryConfig{Enabled: true},
			Logging:           LoggingConfig{Path: filepath.Join(dir, "requests.jsonl")},
		},
		StatePath: filepath.Join(dir, "state.json"),
		Provider: map[string]ProviderConfig{
			"mock": {BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"},
		},
		Models: map[string]ModelGroup{
			"default": {
				Strategy: "weighted",
				Targets: []Target{
					{Provider: "mock", Model: "small-context", Weight: 1000, ContextTokens: 256},
					{Provider: "mock", Model: "large-context", Weight: 1, ContextTokens: 200000},
				},
			},
		},
		Callers: []CallerConfig{{
			ID:          "alice",
			User:        "alice",
			Project:     "metrum-insights",
			Environment: "test",
			TokenSHA256: hex.EncodeToString(sum[:]),
			TokenID:     "rtr_alice_test",
			Allow:       []string{"default"},
			Rate:        RateConfig{RPM: 100, TPM: 1000000, Concurrent: 4},
			Quota:       QuotaConfig{Day: BudgetConfig{Requests: 100, Tokens: 1000000}, Month: BudgetConfig{Tokens: 1000000}},
			Key:         KeyConfig{LifetimeTokens: 1000000},
		}},
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	secretPrompt := "do-not-persist-large-payload-secret"
	body := `{"model":"default","max_tokens":512,"messages":[{"role":"user","content":"` + secretPrompt + ` ` + strings.Repeat("x", 5000) + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("User-Agent", "Cursor/1.0")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(seenModels) != 1 || seenModels[0] != "large-context" {
		t.Fatalf("seen upstream models=%v", seenModels)
	}

	var usage usageRecord
	if err := svc.usage.db.First(&usage).Error; err != nil {
		t.Fatal(err)
	}
	var estimate requestTokenEstimateRecord
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).First(&estimate).Error; err != nil {
		t.Fatal(err)
	}
	if estimate.EstimatedTotalInputTokens <= 0 || estimate.RequestedOutputCapTokens != 512 || estimate.RequestBytes <= 0 {
		t.Fatalf("estimate=%#v", estimate)
	}
	var candidates []decisionTargetCandidateRecord
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).Order("candidate_index ASC").Find(&candidates).Error; err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidate count=%d", len(candidates))
	}
	if candidates[0].EligibilityDecision != "skipped" || candidates[0].EligibilityReason != "request-shape-context-exceeded" || candidates[0].ContextFit {
		t.Fatalf("small candidate=%#v", candidates[0])
	}
	if !candidates[1].Selected || candidates[1].EligibilityDecision != "eligible" || !candidates[1].ContextFit {
		t.Fatalf("large candidate=%#v", candidates[1])
	}
	var reasons []decisionTargetFilterReasonRecord
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).Find(&reasons).Error; err != nil {
		t.Fatal(err)
	}
	if !filterReasonsContain(reasons, "request_shape", "request-shape-context-exceeded") {
		t.Fatalf("missing context filter reason: %#v", reasons)
	}
	assertPersistedRequestShapeEligibilityRowsDoNotContain(t, estimate, candidates, reasons, secretPrompt, "provider-key", testToken)
}

func TestRequestShapeSupportConfigValidation(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Provider["mock"] = ProviderConfig{
		BaseURL: "http://127.0.0.1:1/v1",
		Dialect: "openai-chat",
		Models: map[string]ProviderModel{
			"small": {
				Model:         "mock-small",
				ContextTokens: 1024,
				RequestShapeSupport: RequestShapeSupport{
					MaxRequestBytes:          4096,
					MaxEstimatedInputTokens:  900,
					MaxToolSchemaBytes:       2048,
					SupportedInboundDialects: []string{"openai-chat"},
				},
			},
		},
	}
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", ModelRef: "small", RequestShapeSupport: RequestShapeSupport{MaxRequestBytes: 2048}}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	target := cfg.Models["default"].Targets[0]
	if target.ContextTokens != 1024 || target.RequestShapeSupport.MaxRequestBytes != 2048 || target.RequestShapeSupport.MaxEstimatedInputTokens != 900 {
		t.Fatalf("target metadata not resolved/merged: %#v", target)
	}
	cfg = minimalConfig(t)
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "mock-model", RequestShapeSupport: RequestShapeSupport{MaxRequestBytes: -1}}}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "max_request_bytes cannot be negative") {
		t.Fatalf("err=%v", err)
	}
}

func filterReasonsContain(reasons []decisionTargetFilterReasonRecord, stage, reason string) bool {
	for _, got := range reasons {
		if got.Stage == stage && got.Reason == reason {
			return true
		}
	}
	return false
}

func assertPersistedRequestShapeEligibilityRowsDoNotContain(t *testing.T, estimate requestTokenEstimateRecord, candidates []decisionTargetCandidateRecord, reasons []decisionTargetFilterReasonRecord, forbidden ...string) {
	t.Helper()
	raw, err := json.Marshal([]any{estimate, candidates, reasons})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ToLower(string(raw))
	for _, value := range forbidden {
		if strings.Contains(text, strings.ToLower(value)) {
			t.Fatalf("persisted request-shape eligibility telemetry leaked %q: %s", value, string(raw))
		}
	}
}

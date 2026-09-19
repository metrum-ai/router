// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type productionDerivedLargePayloadFixture struct {
	Name                       string `json:"name"`
	ClientName                 string `json:"client_name"`
	ModelGroup                 string `json:"model_group"`
	Stream                     bool   `json:"stream"`
	StreamOptionsPresent       bool   `json:"stream_options_present"`
	ParallelToolCallsPresent   bool   `json:"parallel_tool_calls_present"`
	MessageCount               int    `json:"message_count"`
	ToolCount                  int    `json:"tool_count"`
	ImageCount                 int    `json:"image_count"`
	OutputCapPresent           bool   `json:"output_cap_present"`
	EstimatedInputTokensBucket string `json:"estimated_input_tokens_bucket"`
	RequestBytesBucket         string `json:"request_bytes_bucket"`
	ToolSchemaBytesBucket      string `json:"tool_schema_bytes_bucket"`
	MustNotSelect              []struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Dialect  string `json:"dialect"`
	} `json:"must_not_select"`
	AllowedSelectedTargets []struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Dialect  string `json:"dialect"`
	} `json:"allowed_selected_targets"`
}

type productionDerivedErrorFixture struct {
	Name      string `json:"name"`
	Scenarios []struct {
		Name                 string   `json:"name"`
		UpstreamStatus       int      `json:"upstream_status"`
		ExpectedCallerStatus int      `json:"expected_caller_status"`
		ExpectedErrorType    string   `json:"expected_error_type"`
		ExpectedErrorClass   string   `json:"expected_error_class"`
		ExpectedRetryable    bool     `json:"expected_retryable"`
		RequiredSafeFields   []string `json:"required_safe_fields"`
	} `json:"scenarios"`
}

type productionDerivedAgentCompatibilityFixture struct {
	Name                 string `json:"name"`
	SourceIncidentIssue  string `json:"source_incident_issue"`
	ModelGroup           string `json:"model_group"`
	ProductionAccessNote string `json:"production_access_note"`
	Scenarios            []struct {
		Name                        string   `json:"name"`
		SourceIncidentIssue         string   `json:"source_incident_issue"`
		ClientName                  string   `json:"client_name"`
		Surface                     string   `json:"surface"`
		ModelGroup                  string   `json:"model_group"`
		Stream                      bool     `json:"stream"`
		MessageCount                int      `json:"message_count"`
		ToolCount                   int      `json:"tool_count"`
		ImageCount                  int      `json:"image_count"`
		ReasoningControl            string   `json:"reasoning_control"`
		OutputCapPresent            bool     `json:"output_cap_present"`
		EstimatedInputTokensBucket  string   `json:"estimated_input_tokens_bucket"`
		RequestBytesBucket          string   `json:"request_bytes_bucket"`
		ToolSchemaBytesBucket       string   `json:"tool_schema_bytes_bucket"`
		RequiredCapabilities        []string `json:"required_capabilities"`
		ExpectedBridgeDirection     string   `json:"expected_bridge_direction"`
		ExpectedTranslatedReasoning string   `json:"expected_translated_reasoning_control"`
		ExpectedErrorClass          string   `json:"expected_error_class"`
		ExpectedRejectionReason     string   `json:"expected_rejection_reason"`
		ProductionSafePayload       string   `json:"production_smoke_safe_payload_template"`
		PreviousResponseIDPresent   bool     `json:"previous_response_id_present"`
		DetectedPayloadDialect      string   `json:"detected_payload_dialect"`
		StructuredOutput            bool     `json:"structured_output"`
	} `json:"scenarios"`
}

type productionDerivedAnthropicImageFixture struct {
	Name                               string          `json:"name"`
	SourceIncidentIssue                string          `json:"source_incident_issue"`
	ObservedModelGroup                 string          `json:"observed_model_group"`
	TestModelGroup                     string          `json:"test_model_group"`
	ClientName                         string          `json:"client_name"`
	Surface                            string          `json:"surface"`
	Stream                             bool            `json:"stream"`
	MessageCount                       int             `json:"message_count"`
	ImageCount                         int             `json:"image_count"`
	OutputCapField                     string          `json:"output_cap_field"`
	OutputCapValue                     int             `json:"output_cap_value"`
	RequiredCapabilities               []string        `json:"required_capabilities"`
	ExpectedErrorWithoutEligibleTarget string          `json:"expected_error_without_eligible_target"`
	ProductionSafePayload              string          `json:"production_smoke_safe_payload_template"`
	Request                            json.RawMessage `json:"request"`
}

type productionDerivedCodexResponsesReasoningFixture struct {
	Name                                         string         `json:"name"`
	SourceIncidentIssue                          string         `json:"source_incident_issue"`
	ObservedModelGroup                           string         `json:"observed_model_group"`
	TestModelGroup                               string         `json:"test_model_group"`
	ClientName                                   string         `json:"client_name"`
	Surface                                      string         `json:"surface"`
	Stream                                       bool           `json:"stream"`
	MessageCount                                 int            `json:"message_count"`
	ToolCount                                    int            `json:"tool_count"`
	ImageCount                                   int            `json:"image_count"`
	ReasoningControl                             string         `json:"reasoning_control"`
	ReasoningEffort                              string         `json:"reasoning_effort"`
	OutputCapField                               string         `json:"output_cap_field"`
	OutputCapValue                               int            `json:"output_cap_value"`
	RequiredCapabilities                         []string       `json:"required_capabilities"`
	WithoutReasoningExpectedStatus               int            `json:"without_reasoning_expected_status"`
	WithReasoningMissingMetadataExpectedError    string         `json:"with_reasoning_missing_metadata_expected_error"`
	WithReasoningValidatedMetadataExpectedStatus int            `json:"with_reasoning_validated_metadata_expected_status"`
	ProductionSafePayload                        string         `json:"production_smoke_safe_payload_template"`
	RequestWithReasoning                         map[string]any `json:"request_with_reasoning"`
	RequestWithoutReasoning                      map[string]any `json:"request_without_reasoning"`
}

// TestProductionDerivedAdminReportsForwardedHTTPSTrust replays the 2026-09-08
// production incident in which /admin/reports answered every request with a
// Basic challenge, including a password that verified against the configured
// bcrypt hash, because the bundle trusted a reverse-proxy range that did not
// contain the cluster ingress controller address. The corresponding Fleet
// bundle guard regression now lives with the Fleet package.
func TestProductionDerivedAdminReportsForwardedHTTPSTrust(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "smokes", "production-derived", "admin-reports-forwarded-https-trust.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Name                     string `json:"name"`
		SourceIncidentIssue      string `json:"source_incident_issue"`
		Surface                  string `json:"surface"`
		Path                     string `json:"path"`
		ForwardedProtoHeader     string `json:"forwarded_proto_header"`
		ForwardedProtoValue      string `json:"forwarded_proto_value"`
		AllowInsecureHTTP        bool   `json:"allow_insecure_http"`
		UntrustedProxyCIDR       string `json:"untrusted_proxy_cidr"`
		TrustedProxyCIDR         string `json:"trusted_proxy_cidr"`
		ProxyRemoteAddr          string `json:"proxy_remote_addr"`
		AdminUsername            string `json:"admin_username"`
		AdminPassword            string `json:"admin_password"`
		AdminPasswordBcrypt      string `json:"admin_password_bcrypt"`
		ExpectedUntrustedStatus  int    `json:"expected_untrusted_status"`
		ExpectedChallengeHeader  string `json:"expected_untrusted_challenge_header"`
		ExpectedTrustedStatusNot int    `json:"expected_trusted_status_not"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Surface != "admin_basic_auth" || fixture.AllowInsecureHTTP || fixture.ExpectedUntrustedStatus != http.StatusUnauthorized {
		t.Fatalf("invalid forwarded-HTTPS trust fixture: %#v", fixture)
	}

	statusFor := func(t *testing.T, trustedCIDR string) int {
		t.Helper()
		t.Setenv("SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST", fixture.AdminPasswordBcrypt)
		dir := t.TempDir()
		cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
		cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
		cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{
			Enabled:           true,
			AllowInsecureHTTP: fixture.AllowInsecureHTTP,
			TrustedProxyCIDRs: []string{trustedCIDR},
			Users: []AdminBasicAuthUser{{
				Username:        fixture.AdminUsername,
				PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST",
				Subject:         "basic:admin",
				Domain:          "local/test",
			}},
		}
		cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{
			Enabled: true,
			Policy:  []string{"p, basic:admin, local/test, admin:reports, read|export|drilldown"},
		}
		cfg.Server.AdminReports = AdminReportsConfig{Enabled: true, DefaultSince: "24h", MaxRange: "31d", MaxRows: 100}
		svc, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer svc.Close()

		req := httptest.NewRequest(http.MethodGet, fixture.Path, nil)
		req.RemoteAddr = fixture.ProxyRemoteAddr
		req.Header.Set(fixture.ForwardedProtoHeader, fixture.ForwardedProtoValue)
		req.SetBasicAuth(fixture.AdminUsername, fixture.AdminPassword)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code == http.StatusUnauthorized && rr.Header().Get(fixture.ExpectedChallengeHeader) == "" {
			t.Fatalf("401 without %s challenge", fixture.ExpectedChallengeHeader)
		}
		return rr.Code
	}

	if got := statusFor(t, fixture.UntrustedProxyCIDR); got != fixture.ExpectedUntrustedStatus {
		t.Fatalf("untrusted reverse proxy status = %d want %d", got, fixture.ExpectedUntrustedStatus)
	}
	if got := statusFor(t, fixture.TrustedProxyCIDR); got == fixture.ExpectedTrustedStatusNot {
		t.Fatalf("trusted reverse proxy still challenged with a valid password: status = %d", got)
	}

}

// TestProductionDerivedAnthropicPlainTextToolOnly replays the 2026-09-08
// production smoke in which a plain-text Anthropic Messages request to a broad
// group returned 502 no-eligible-target because every Anthropic-dialect target
// was tool_only, while the same group served a tool-bearing request.
func TestProductionDerivedAnthropicPlainTextToolOnly(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "smokes", "production-derived", "anthropic-plain-text-tool-only.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Name                string `json:"name"`
		SourceIncidentIssue string `json:"source_incident_issue"`
		Surface             string `json:"surface"`
		Path                string `json:"path"`
		PlainTextStatus     int    `json:"plain_text_status"`
		PlainTextErrorType  string `json:"plain_text_error_type"`
		ToolRequestStatus   int    `json:"tool_request_status"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Surface != "anthropic_messages" || fixture.PlainTextStatus != http.StatusBadGateway || fixture.PlainTextErrorType != "no-eligible-target" {
		t.Fatalf("invalid Anthropic plain-text tool_only fixture: %#v", fixture)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id":          "msg_plain",
			"type":        "message",
			"role":        "assistant",
			"model":       "native-messages",
			"content":     []map[string]any{{"type": "text", "text": "OK"}},
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	defer upstream.Close()

	newSvc := func(t *testing.T, toolOnly bool) *Service {
		t.Helper()
		cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
		cfg.Provider["anthropic_native"] = ProviderConfig{BaseURL: upstream.URL, Dialect: "anthropic", APIKey: "provider-key"}
		cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{
			Provider:    "anthropic_native",
			Model:       "native-messages",
			ToolOnly:    toolOnly,
			ToolSupport: ToolSupport{AnthropicMessages: []string{"client_tools"}},
		}}}
		svc, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return svc
	}

	post := func(svc *Service, withTools bool) int {
		t.Helper()
		body := `{"model":"default","max_tokens":32,"messages":[{"role":"user","content":"Reply OK only."}]}`
		if withTools {
			body = `{"model":"default","max_tokens":32,"messages":[{"role":"user","content":"Reply OK only."}],"tools":[{"name":"echo","input_schema":{"type":"object","properties":{}}}]}`
		}
		req := httptest.NewRequest(http.MethodPost, fixture.Path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		return rr.Code
	}

	toolOnly := newSvc(t, true)
	defer toolOnly.Close()
	if got := post(toolOnly, false); got != fixture.PlainTextStatus {
		t.Fatalf("plain-text against tool_only-only group status=%d want %d", got, fixture.PlainTextStatus)
	}
	if got := post(toolOnly, true); got != fixture.ToolRequestStatus {
		t.Fatalf("tool request against tool_only group status=%d want %d", got, fixture.ToolRequestStatus)
	}

	textCapable := newSvc(t, false)
	defer textCapable.Close()
	if got := post(textCapable, false); got != http.StatusOK {
		t.Fatalf("plain-text against non-tool_only Anthropic target status=%d want 200", got)
	}
}

func TestProductionDerivedAnthropicMessagesImageEligibility(t *testing.T) {
	fixture := loadProductionDerivedAnthropicImageFixture(t, "anthropic-messages-image-eligibility.json")
	if fixture.SourceIncidentIssue != "#660" || fixture.ObservedModelGroup != "big-coder" ||
		fixture.TestModelGroup != "anthropic-image-smoke" || fixture.ClientName != "Claude Code" ||
		fixture.Surface != "anthropic_messages" || fixture.Stream || fixture.MessageCount != 1 ||
		fixture.ImageCount != 1 || fixture.OutputCapField != "max_tokens" || fixture.OutputCapValue != 512 ||
		fixture.ExpectedErrorWithoutEligibleTarget != "no-eligible-target" || fixture.ProductionSafePayload == "" {
		t.Fatalf("invalid production-derived Anthropic image fixture: %#v", fixture)
	}
	for _, capability := range []string{"anthropic_messages", "image", "max_tokens"} {
		if !stringSliceContains(fixture.RequiredCapabilities, capability) {
			t.Fatalf("fixture required_capabilities=%v missing %q", fixture.RequiredCapabilities, capability)
		}
	}

	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":          "msg_production_derived_image",
			"type":        "message",
			"role":        "assistant",
			"model":       "image-anthropic",
			"stop_reason": "end_turn",
			"content":     []map[string]any{{"type": "text", "text": "Synthetic Merchant"}},
			"usage":       map[string]any{"input_tokens": 32, "output_tokens": 2},
		})
	}))
	defer upstream.Close()

	newConfig := func(dir string, includeImageTarget bool) *Config {
		cfg := testConfig(t, upstream.URL, "provider-key", dir)
		cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
		cfg.Provider["text_anthropic"] = ProviderConfig{BaseURL: upstream.URL, Dialect: "anthropic", APIKey: "provider-key"}
		cfg.Provider["image_anthropic"] = ProviderConfig{BaseURL: upstream.URL, Dialect: "anthropic", APIKey: "provider-key"}
		targets := []Target{{
			Provider:         "text_anthropic",
			Model:            "text-anthropic",
			InputModalities:  []string{"text"},
			OutputModalities: []string{"text"},
		}}
		if includeImageTarget {
			honorsMaxTokens := true
			targets = append(targets, Target{
				Provider:         "image_anthropic",
				Model:            "image-anthropic",
				InputModalities:  []string{"text", "image"},
				OutputModalities: []string{"text"},
				HonorsMaxTokens:  &honorsMaxTokens,
				RequestShapeSupport: RequestShapeSupport{
					SupportedInboundDialects: []string{"anthropic"},
					ValidationStatus:         "passed",
				},
			})
		}
		cfg.Models[fixture.TestModelGroup] = ModelGroup{Strategy: "static", Targets: targets}
		cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, fixture.TestModelGroup)
		return cfg
	}

	t.Run("no eligible target remains actionable", func(t *testing.T) {
		dir := t.TempDir()
		svc, err := New(newConfig(dir, false))
		if err != nil {
			t.Fatal(err)
		}
		defer svc.Close()
		req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(string(fixture.Request)))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusBadGateway {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		decodeProductionDerivedErrorDetails(t, rr.Body.Bytes(), fixture.ExpectedErrorWithoutEligibleTarget)
		assertProductionDerivedArtifactsDoNotContain(t, svc, dir, "Read the synthetic receipt", "https://example.com/synthetic-receipt.png", "provider-key", testToken)
	})

	t.Run("validated Anthropic image target receives shape", func(t *testing.T) {
		dir := t.TempDir()
		svc, err := New(newConfig(dir, true))
		if err != nil {
			t.Fatal(err)
		}
		defer svc.Close()
		req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(string(fixture.Request)))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		if upstreamBody["model"] != "image-anthropic" || upstreamBody["max_tokens"] != float64(fixture.OutputCapValue) {
			t.Fatalf("upstream model/cap mismatch: %#v", upstreamBody)
		}
		messages := upstreamBody["messages"].([]any)
		content := messages[0].(map[string]any)["content"].([]any)
		source := content[1].(map[string]any)["source"].(map[string]any)
		if source["type"] != "url" || source["url"] != "https://example.com/synthetic-receipt.png" {
			t.Fatalf("upstream image source=%#v", source)
		}
		assertProductionDerivedArtifactsDoNotContain(t, svc, dir, "Read the synthetic receipt", "https://example.com/synthetic-receipt.png", "provider-key", testToken)
	})
}

func TestProductionDerivedCodexResponsesReasoningEligibility(t *testing.T) {
	fixture := loadProductionDerivedCodexResponsesReasoningFixture(t, "codex-responses-reasoning-eligibility.json")
	if fixture.SourceIncidentIssue != "#1056" || fixture.ObservedModelGroup != "big-coder" ||
		fixture.TestModelGroup != "codex-responses-reasoning-smoke" || fixture.ClientName != "Codex CLI" ||
		fixture.Surface != "openai_responses" || fixture.ToolCount != 1 || fixture.ReasoningControl != "reasoning" ||
		fixture.WithReasoningMissingMetadataExpectedError != "no-eligible-target" ||
		fixture.WithReasoningValidatedMetadataExpectedStatus != http.StatusOK ||
		fixture.WithoutReasoningExpectedStatus != http.StatusOK || fixture.ProductionSafePayload == "" {
		t.Fatalf("invalid #1056 production-derived fixture: %#v", fixture)
	}
	for _, capability := range []string{"openai_responses", "tools", "openai-responses_tool_passthrough", "reasoning"} {
		if !stringSliceContains(fixture.RequiredCapabilities, capability) {
			t.Fatalf("fixture required_capabilities=%v missing %q", fixture.RequiredCapabilities, capability)
		}
	}
	if len(fixture.RequestWithReasoning) == 0 || len(fixture.RequestWithoutReasoning) == 0 {
		t.Fatal("fixture missing synthetic request bodies")
	}

	var upstreamHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"id":          "resp_codex_reasoning_fixture",
			"object":      "response",
			"status":      "completed",
			"model":       "reasoning-responses",
			"output_text": "OK",
			"output":      []map[string]any{{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "OK"}}}},
			"usage":       map[string]any{"input_tokens": 8, "output_tokens": 2, "total_tokens": 10},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	cfg.Provider["responses_plain"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Provider["responses_reasoning"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	plainTarget := Target{Provider: "responses_plain", Model: "plain-responses", Weight: 50, ToolSupport: ToolSupport{OpenAIResponses: []string{"function"}}}
	reasoningTarget := Target{
		Provider:    "responses_reasoning",
		Model:       "reasoning-responses",
		Weight:      50,
		ToolSupport: ToolSupport{OpenAIResponses: []string{"function"}},
		Reasoning:   ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum},
	}
	cfg.Models[fixture.TestModelGroup] = ModelGroup{Strategy: "static", Targets: []Target{plainTarget}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, fixture.TestModelGroup)

	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	t.Run("without-reasoning-succeeds", func(t *testing.T) {
		upstreamHits.Store(0)
		body, err := json.Marshal(fixture.RequestWithoutReasoning)
		if err != nil {
			t.Fatal(err)
		}
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != fixture.WithoutReasoningExpectedStatus {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		if upstreamHits.Load() != 1 {
			t.Fatalf("upstream hits=%d, want 1", upstreamHits.Load())
		}
	})

	t.Run("with-reasoning-missing-metadata-no-eligible", func(t *testing.T) {
		upstreamHits.Store(0)
		body, err := json.Marshal(fixture.RequestWithReasoning)
		if err != nil {
			t.Fatal(err)
		}
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), fixture.WithReasoningMissingMetadataExpectedError) {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		if upstreamHits.Load() != 0 {
			t.Fatalf("upstream hits=%d, want 0 before eligibility failure", upstreamHits.Load())
		}
		details := decodeProductionDerivedErrorDetails(t, rr.Body.Bytes(), "no-eligible-target")
		requirements, _ := details["requirements"].([]any)
		joined := fmt.Sprint(requirements)
		for _, want := range []string{"tools", "openai-responses_tool_passthrough", "reasoning"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("requirements=%v missing %q", requirements, want)
			}
		}
	})

	t.Run("with-reasoning-validated-metadata-succeeds", func(t *testing.T) {
		dir2 := t.TempDir()
		cfg2 := testConfig(t, upstream.URL, "provider-key", dir2)
		cfg2.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir2, "usage.sqlite"))
		cfg2.Server.DecisionTelemetry.Enabled = true
		cfg2.Provider["responses_plain"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
		cfg2.Provider["responses_reasoning"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
		cfg2.Models[fixture.TestModelGroup] = ModelGroup{Strategy: "static", Targets: []Target{plainTarget, reasoningTarget}}
		cfg2.Callers[0].Allow = append(cfg2.Callers[0].Allow, fixture.TestModelGroup)
		svc2, err := New(cfg2)
		if err != nil {
			t.Fatal(err)
		}
		defer svc2.Close()
		upstreamHits.Store(0)
		body, err := json.Marshal(fixture.RequestWithReasoning)
		if err != nil {
			t.Fatal(err)
		}
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		svc2.Handler().ServeHTTP(rr, req)
		if rr.Code != fixture.WithReasoningValidatedMetadataExpectedStatus {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		if upstreamHits.Load() != 1 {
			t.Fatalf("upstream hits=%d, want 1", upstreamHits.Load())
		}
		assertProductionDerivedArtifactsDoNotContain(t, svc2, dir2, "Reply OK only", "provider-key", testToken)
	})
}

func TestProductionDerivedAgentCompatibilityFixtureCoversRequiredScenarios(t *testing.T) {
	fixture := loadProductionDerivedAgentCompatibilityFixture(t, "agent-reasoning-bridge-compatibility.json")
	if fixture.ModelGroup != "reasoning-bridge-smoke" {
		t.Fatalf("fixture model_group=%q, want reasoning-bridge-smoke", fixture.ModelGroup)
	}
	if !strings.Contains(fixture.ProductionAccessNote, "Harbor/Chetan") || strings.Contains(fixture.ProductionAccessNote, "change production big-coder") {
		t.Fatalf("production access note must mention Harbor/Chetan access without directing big-coder edits: %q", fixture.ProductionAccessNote)
	}
	requiredIssues := []string{"#319", "#320", "#321", "#330", "#333", "#335", "#344", "#350", "#352", "#357", "#358", "#359"}
	requiredClients := []string{"Codex CLI", "Cursor/1.0", "Claude Code", "opencode", "aider", "Generic SDK"}
	requiredScenarios := []string{
		"codex-responses-reasoning-tools",
		"cursor-chat-reasoning-tools-chat-to-responses",
		"claude-code-messages-thinking-tools",
		"responses-to-chat-function-tool",
		"cursor-mixed-responses-body-chat-endpoint",
		"previous-response-id-stateless-bridge-negative",
		"chat-to-responses-streaming-negative",
		"reasoning-no-compatible-target-negative",
		"reasoning-models-metadata-advertisement",
		"anthropic-thinking-budget-output-cap-negative",
		"cursor-image-tools-no-eligible",
		"no-eligible-target-diagnostics-proof",
		"upstream-entitlement-401-fallback-proof",
		"provider-skin-mismatch-negative",
		"opencode-chat-large-tool-schema",
		"aider-chat-output-cap-structured-output",
	}
	issues := map[string]bool{}
	clients := map[string]bool{}
	scenarios := map[string]bool{}
	surfaces := map[string]bool{}
	for _, scenario := range fixture.Scenarios {
		if scenario.Name == "" || scenario.SourceIncidentIssue == "" || scenario.ClientName == "" || scenario.Surface == "" || scenario.ModelGroup == "" || scenario.ProductionSafePayload == "" {
			t.Fatalf("scenario missing required safe metadata: %#v", scenario)
		}
		if scenario.ModelGroup == "big-coder" {
			t.Fatalf("agent compatibility fixture must use a dedicated smoke group, got big-coder in %#v", scenario)
		}
		if scenario.RequestBytesBucket == "" || scenario.ToolSchemaBytesBucket == "" || scenario.EstimatedInputTokensBucket == "" {
			t.Fatalf("scenario missing shape buckets: %#v", scenario)
		}
		issues[scenario.SourceIncidentIssue] = true
		clients[scenario.ClientName] = true
		scenarios[scenario.Name] = true
		surfaces[scenario.Surface] = true
	}
	for _, issue := range requiredIssues {
		if !issues[issue] {
			t.Fatalf("fixture missing source issue %s", issue)
		}
	}
	for _, client := range requiredClients {
		if !clients[client] {
			t.Fatalf("fixture missing client %s", client)
		}
	}
	for _, name := range requiredScenarios {
		if !scenarios[name] {
			t.Fatalf("fixture missing scenario %s", name)
		}
	}
	for _, surface := range []string{"openai_chat", "openai_responses", "anthropic_messages"} {
		if !surfaces[surface] {
			t.Fatalf("fixture missing surface %s", surface)
		}
	}
}

func TestProductionDerivedAgentCompatibilityNegativeBridgeReasonsMatchRouter(t *testing.T) {
	fixture := loadProductionDerivedAgentCompatibilityFixture(t, "agent-reasoning-bridge-compatibility.json")
	byName := map[string]string{}
	for _, scenario := range fixture.Scenarios {
		byName[scenario.Name] = scenario.ExpectedRejectionReason
	}

	req, err := decodeRequest("openai-responses", []byte(`{"model":"reasoning-bridge-smoke","input":"hi","previous_response_id":"resp_fixture"}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	responsesTarget := Target{ResponsesToChat: ResponsesToChatBridge{Enabled: true, Text: true}}
	if got := responsesToChatBridgeFilterReason(responsesTarget, req, "openai-responses", "openai-chat"); got != byName["previous-response-id-stateless-bridge-negative"] {
		t.Fatalf("previous_response_id filter=%q, want fixture reason %q", got, byName["previous-response-id-stateless-bridge-negative"])
	}

	req, err = decodeRequest("openai-chat", []byte(`{"model":"reasoning-bridge-smoke","stream":true,"messages":[{"role":"user","content":"hi"}]}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	chatTarget := Target{Bridges: BridgeSupport{ChatToResponses: DialectBridgeSupport{Enabled: true}}}
	if got := chatToResponsesBridgeFilterReason(chatTarget, req, "openai-chat", "openai-responses"); got != byName["chat-to-responses-streaming-negative"] {
		t.Fatalf("streaming bridge filter=%q, want fixture reason %q", got, byName["chat-to-responses-streaming-negative"])
	}
}

func TestProductionDerivedLargeOpenAIChatToolPayloadSkipsShapeLimitedTarget(t *testing.T) {
	fixture := loadProductionDerivedLargePayloadFixture(t, "large-openai-chat-tools.json")
	body := syntheticProductionDerivedOpenAIChatPayload(t, fixture)

	var selectedModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var decoded map[string]any
		if err := json.NewDecoder(r.Body).Decode(&decoded); err != nil {
			t.Fatalf("decode upstream body: %v", err)
		}
		selectedModel = stringValue(decoded["model"])
		if selectedModel == fixture.MustNotSelect[0].Model {
			t.Fatalf("unsafe production-derived target was selected: %s", selectedModel)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "chatcmpl_production_derived",
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 1200, "completion_tokens": 2, "total_tokens": 1202},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := productionDerivedRegressionConfig(t, dir, upstream.URL)
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("User-Agent", fixture.ClientName)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if selectedModel != fixture.AllowedSelectedTargets[0].Model {
		t.Fatalf("selected upstream model=%q, want %q", selectedModel, fixture.AllowedSelectedTargets[0].Model)
	}

	var usage usageRecord
	if err := svc.usage.db.First(&usage).Error; err != nil {
		t.Fatal(err)
	}
	var shape requestShapeRecord
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).First(&shape).Error; err != nil {
		t.Fatal(err)
	}
	if !shape.Stream || shape.MessageCount != fixture.MessageCount || shape.ToolCount != fixture.ToolCount || shape.ImageCount != fixture.ImageCount {
		t.Fatalf("unexpected request shape counts: %#v", shape)
	}
	if shape.TotalRequestBytesBucket != fixture.RequestBytesBucket || shape.ToolSchemaBytesBucket != fixture.ToolSchemaBytesBucket || shape.EstimatedInputTokensBucket != fixture.EstimatedInputTokensBucket || shape.RequestedOutputCapBucket != "omitted" {
		t.Fatalf("unexpected request shape buckets: %#v", shape)
	}
	var candidates []decisionTargetCandidateRecord
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).Order("candidate_index ASC").Find(&candidates).Error; err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidate count=%d", len(candidates))
	}
	if candidates[0].Provider != fixture.MustNotSelect[0].Provider || candidates[0].Model != fixture.MustNotSelect[0].Model || candidates[0].EligibilityReason != "request-shape-max-request-bytes" || candidates[0].Selected {
		t.Fatalf("unsafe target candidate not skipped by request shape: %#v", candidates[0])
	}
	if candidates[1].Provider != fixture.AllowedSelectedTargets[0].Provider || candidates[1].Model != fixture.AllowedSelectedTargets[0].Model || !candidates[1].Selected {
		t.Fatalf("safe target was not selected: %#v", candidates[1])
	}
	var reasons []decisionTargetFilterReasonRecord
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).Find(&reasons).Error; err != nil {
		t.Fatal(err)
	}
	if !filterReasonsContain(reasons, "request_shape", "request-shape-max-request-bytes") {
		t.Fatalf("missing request-shape filter reason: %#v", reasons)
	}
	var translation requestTranslationShapeRecord
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).First(&translation).Error; err != nil {
		t.Fatal(err)
	}
	if translation.Provider != fixture.AllowedSelectedTargets[0].Provider || translation.Model != fixture.AllowedSelectedTargets[0].Model || translation.TranslatedToolCount != fixture.ToolCount || translation.TranslatedRequestBytesBucket != fixture.RequestBytesBucket {
		t.Fatalf("unexpected translation shape: %#v", translation)
	}
	assertProductionDerivedArtifactsDoNotContain(t, svc, dir, "production-derived-secret", "provider-key", testToken)
}

func TestProductionDerivedOpenCodeAIStreamingOptionsSkipIncompatibleTargets(t *testing.T) {
	fixture := loadProductionDerivedLargePayloadFixture(t, "opencode-ai-sdk-chat-stream-options.json")
	body := syntheticProductionDerivedOpenAIChatPayload(t, fixture)

	var selectedModel string
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatalf("decode upstream body: %v", err)
		}
		selectedModel = stringValue(upstreamBody["model"])
		for _, target := range fixture.MustNotSelect {
			if selectedModel == target.Model {
				t.Fatalf("incompatible production-derived target was selected: %s", selectedModel)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl_opencode_stream_options\",\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl_opencode_stream_options\",\"choices\":[],\"usage\":{\"prompt_tokens\":1200,\"completion_tokens\":2,\"total_tokens\":1202}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := productionDerivedOpenCodeStreamOptionsConfig(t, dir, upstream.URL)
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("User-Agent", fixture.ClientName)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if selectedModel != fixture.AllowedSelectedTargets[0].Model {
		t.Fatalf("selected upstream model=%q, want %q", selectedModel, fixture.AllowedSelectedTargets[0].Model)
	}
	streamOptions, ok := upstreamBody["stream_options"].(map[string]any)
	if !ok || streamOptions["include_usage"] != true {
		t.Fatalf("upstream stream_options=%#v, want include_usage preserved for native streaming", upstreamBody["stream_options"])
	}
	if upstreamBody["stream"] != true {
		t.Fatalf("upstream stream=%#v, want true", upstreamBody["stream"])
	}

	var usage usageRecord
	if err := svc.usage.db.First(&usage).Error; err != nil {
		t.Fatal(err)
	}
	var shape requestShapeRecord
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).First(&shape).Error; err != nil {
		t.Fatal(err)
	}
	if !shape.Stream || !shape.ParallelToolCallsPresent || shape.ToolCount != fixture.ToolCount || shape.ToolChoiceMode != "auto" {
		t.Fatalf("unexpected request shape: %#v", shape)
	}
	if shape.TotalRequestBytesBucket != fixture.RequestBytesBucket || shape.ToolSchemaBytesBucket != fixture.ToolSchemaBytesBucket || shape.EstimatedInputTokensBucket != fixture.EstimatedInputTokensBucket {
		t.Fatalf("unexpected request shape buckets: %#v", shape)
	}
	var candidates []decisionTargetCandidateRecord
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).Order("candidate_index ASC").Find(&candidates).Error; err != nil {
		t.Fatal(err)
	}
	if len(candidates) != len(fixture.MustNotSelect)+1 {
		t.Fatalf("candidate count=%d candidates=%#v", len(candidates), candidates)
	}
	for i, target := range fixture.MustNotSelect {
		candidate := candidates[i]
		if candidate.Provider != target.Provider || candidate.Model != target.Model || candidate.EligibilityReason != "request-shape-unsupported-feature" || candidate.Selected {
			t.Fatalf("candidate %d not skipped by stream_options metadata: %#v", i, candidate)
		}
	}
	selected := candidates[len(candidates)-1]
	if selected.Provider != fixture.AllowedSelectedTargets[0].Provider || selected.Model != fixture.AllowedSelectedTargets[0].Model || !selected.Selected {
		t.Fatalf("fallback selected candidate=%#v", selected)
	}
	var reasons []decisionTargetFilterReasonRecord
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).Find(&reasons).Error; err != nil {
		t.Fatal(err)
	}
	if !filterReasonsContain(reasons, "request_shape", "request-shape-unsupported-feature") {
		t.Fatalf("missing request-shape unsupported-feature reason: %#v", reasons)
	}
	assertProductionDerivedArtifactsDoNotContain(t, svc, dir, "production-derived-secret", "provider-key", testToken)
}

func TestProductionDerivedHighGt1MBOpenAIChatToolPayloadRoutesToMiniMax(t *testing.T) {
	fixture := loadProductionDerivedLargePayloadFixture(t, "high-gt1mb-openai-chat-tools.json")
	body := syntheticProductionDerivedOpenAIChatPayload(t, fixture)

	var selectedModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var decoded map[string]any
		if err := json.NewDecoder(r.Body).Decode(&decoded); err != nil {
			t.Fatalf("decode upstream body: %v", err)
		}
		selectedModel = stringValue(decoded["model"])
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "chatcmpl_high_gt1mb",
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 260000, "completion_tokens": 2, "total_tokens": 260002},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := productionDerivedHighRegressionConfig(t, dir, upstream.URL, fixture)
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("User-Agent", fixture.ClientName)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if selectedModel != fixture.AllowedSelectedTargets[0].Model {
		t.Fatalf("selected upstream model=%q, want %q", selectedModel, fixture.AllowedSelectedTargets[0].Model)
	}

	var usage usageRecord
	if err := svc.usage.db.First(&usage).Error; err != nil {
		t.Fatal(err)
	}
	var shape requestShapeRecord
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).First(&shape).Error; err != nil {
		t.Fatal(err)
	}
	if !shape.Stream || shape.MessageCount != fixture.MessageCount || shape.ToolCount != fixture.ToolCount || shape.ImageCount != fixture.ImageCount {
		t.Fatalf("unexpected request shape counts: %#v", shape)
	}
	if shape.TotalRequestBytesBucket != fixture.RequestBytesBucket || shape.ToolSchemaBytesBucket != fixture.ToolSchemaBytesBucket || shape.EstimatedInputTokensBucket != fixture.EstimatedInputTokensBucket {
		t.Fatalf("unexpected request shape buckets: %#v", shape)
	}

	var candidates []decisionTargetCandidateRecord
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).Order("candidate_index ASC").Find(&candidates).Error; err != nil {
		t.Fatal(err)
	}
	if len(candidates) != len(fixture.MustNotSelect)+len(fixture.AllowedSelectedTargets) {
		t.Fatalf("candidate count=%d candidates=%#v", len(candidates), candidates)
	}
	skipped := map[string]bool{}
	for _, candidate := range candidates {
		key := candidate.Provider + "|" + candidate.Model + "|" + candidate.Dialect
		if candidate.Selected {
			want := fixture.AllowedSelectedTargets[0]
			if candidate.Provider != want.Provider || candidate.Model != want.Model || candidate.Dialect != want.Dialect {
				t.Fatalf("unexpected selected candidate=%#v", candidate)
			}
			continue
		}
		wantReason := "request-shape-max-request-bytes"
		if candidate.Provider == "openai" && candidate.Dialect == "openai-responses" {
			wantReason = "chat-to-responses-bridge-disabled"
		}
		if candidate.EligibilityReason != wantReason {
			t.Fatalf("candidate not skipped by max request bytes: %#v", candidate)
		}
		skipped[key] = true
	}
	for _, denied := range fixture.MustNotSelect {
		key := denied.Provider + "|" + denied.Model + "|" + denied.Dialect
		if !skipped[key] {
			t.Fatalf("must-not-select target was not shape-skipped: %s candidates=%#v", key, candidates)
		}
	}
	var reasons []decisionTargetFilterReasonRecord
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).Find(&reasons).Error; err != nil {
		t.Fatal(err)
	}
	if !filterReasonsContain(reasons, "request_shape", "request-shape-max-request-bytes") {
		t.Fatalf("missing request-shape filter reason: %#v", reasons)
	}
	var translation requestTranslationShapeRecord
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).First(&translation).Error; err != nil {
		t.Fatal(err)
	}
	if translation.Provider != fixture.AllowedSelectedTargets[0].Provider || translation.Model != fixture.AllowedSelectedTargets[0].Model || translation.TranslatedToolCount != fixture.ToolCount || translation.TranslatedRequestBytesBucket != fixture.RequestBytesBucket {
		t.Fatalf("unexpected translation shape: %#v", translation)
	}
	assertProductionDerivedArtifactsDoNotContain(t, svc, dir, "production-derived-secret", "provider-key", testToken)
}

func TestProductionDerivedUpstreamErrorClassificationIsCallerVisible(t *testing.T) {
	fixture := loadProductionDerivedErrorFixture(t, "upstream-error-classification.json")
	for _, scenario := range fixture.Scenarios {
		if scenario.UpstreamStatus == 0 {
			continue
		}
		t.Run(scenario.Name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(scenario.UpstreamStatus)
				_, _ = w.Write([]byte(`{"error":{"message":"unsupported request shape","type":"invalid_request_error","code":"unsupported_value","param":"tools"}}`))
			}))
			defer upstream.Close()

			dir := t.TempDir()
			cfg := testConfig(t, upstream.URL, "provider-key", dir)
			cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
			cfg.Server.Diagnostics.StoreSanitizedUpstreamError = boolPtr(true)
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hello"}]}`))
			req.Header.Set("Authorization", "Bearer "+testToken)
			req.Header.Set("User-Agent", "Cursor/1.0")
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != scenario.ExpectedCallerStatus {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if rr.Header().Get("X-Router-Error-Class") != scenario.ExpectedErrorClass ||
				rr.Header().Get("X-Metrum-Error-Class") != scenario.ExpectedErrorClass ||
				rr.Header().Get("X-Upstream-Status") == "" {
				t.Fatalf("missing upstream diagnostic headers: %#v", rr.Header())
			}
			details := decodeProductionDerivedErrorDetails(t, rr.Body.Bytes(), scenario.ExpectedErrorType)
			if details["error_class"] != scenario.ExpectedErrorClass || int(details["upstream_status"].(float64)) != scenario.UpstreamStatus || details["retryable"] != scenario.ExpectedRetryable {
				t.Fatalf("unexpected error details: %#v", details)
			}
			for _, field := range scenario.RequiredSafeFields {
				if _, ok := details[field]; !ok {
					t.Fatalf("safe field %q missing from details %#v", field, details)
				}
			}
			if strings.Contains(rr.Body.String(), "provider-key") || strings.Contains(rr.Body.String(), testToken) {
				t.Fatalf("caller error leaked secret material: %s", rr.Body.String())
			}

			var usage usageRecord
			if err := svc.usage.db.First(&usage).Error; err != nil {
				t.Fatal(err)
			}
			if usage.Error != scenario.ExpectedErrorType || usage.Status != scenario.ExpectedCallerStatus || usage.QuotaState != "ok" || usage.KeyState != "active" {
				t.Fatalf("unexpected usage error state: %#v", usage)
			}
			var requestErr requestErrorRecord
			if err := svc.usage.db.Where("request_id = ?", usage.RequestID).First(&requestErr).Error; err != nil {
				t.Fatal(err)
			}
			if requestErr.ErrorClass != scenario.ExpectedErrorClass || requestErr.ErrorType != scenario.ExpectedErrorType || requestErr.Status != scenario.ExpectedCallerStatus || requestErr.Retryable != scenario.ExpectedRetryable {
				t.Fatalf("unexpected request error: %#v", requestErr)
			}
			var upstreamDetails []requestUpstreamErrorDetailRecord
			if err := svc.usage.db.Where("request_id = ?", usage.RequestID).Find(&upstreamDetails).Error; err != nil {
				t.Fatal(err)
			}
			if !upstreamDetailsContain(upstreamDetails, scenario.UpstreamStatus, scenario.ExpectedErrorClass, "type", "invalid_request_error") ||
				!upstreamDetailsContain(upstreamDetails, scenario.UpstreamStatus, scenario.ExpectedErrorClass, "code", "unsupported_value") ||
				!upstreamDetailsContain(upstreamDetails, scenario.UpstreamStatus, scenario.ExpectedErrorClass, "param", "tools") {
				t.Fatalf("unexpected sanitized upstream error details: %#v", upstreamDetails)
			}
		})
	}
}

func TestProductionDerivedRouterTrafficShapeRejectionSkipsUpstream(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeChatTestResponse(w, "ok")
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	on := true
	cfg.Callers[0].TrafficShape = TrafficShapeConfig{Enabled: &on, RequestStartPerSec: 0.01, RequestBurst: 1}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	first := performChatRequest(t, svc, `{"model":"default","messages":[{"role":"user","content":"first"}]}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	second := performChatRequest(t, svc, `{"model":"default","messages":[{"role":"user","content":"second"}]}`)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status=%d body=%s", second.Code, second.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls=%d, want only admitted request", calls.Load())
	}
	assertTrafficShapeError(t, second.Body.Bytes(), "caller.request_start_per_sec")

	var usage usageRecord
	if err := svc.usage.db.Where("status = ?", http.StatusTooManyRequests).First(&usage).Error; err != nil {
		t.Fatal(err)
	}
	if usage.Error != "traffic-shaped" || usage.Attempts != 0 || usage.TrafficShapeDecision != "rejected" {
		t.Fatalf("unexpected router-side traffic shape telemetry: %#v", usage)
	}
	var requestErr requestErrorRecord
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).First(&requestErr).Error; err != nil {
		t.Fatal(err)
	}
	if requestErr.ErrorClass != "traffic-shaped" || requestErr.Attempts != 0 {
		t.Fatalf("unexpected router-side request error telemetry: %#v", requestErr)
	}
}

func productionDerivedHighRegressionConfig(t *testing.T, dir, upstreamURL string, fixture productionDerivedLargePayloadFixture) *Config {
	t.Helper()
	sum := sha256.Sum256([]byte(testToken))
	providers := map[string]ProviderConfig{}
	targets := make([]Target, 0, len(fixture.MustNotSelect)+len(fixture.AllowedSelectedTargets))
	for _, denied := range fixture.MustNotSelect {
		providers[denied.Provider] = ProviderConfig{BaseURL: upstreamURL + "/v1", Dialect: denied.Dialect, APIKey: "provider-key"}
		targets = append(targets, Target{
			Provider:            denied.Provider,
			Model:               denied.Model,
			Weight:              10,
			ContextTokens:       2000000,
			ToolSupport:         ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}},
			RequestShapeSupport: RequestShapeSupport{MaxRequestBytes: 1048576},
		})
	}
	for _, allowed := range fixture.AllowedSelectedTargets {
		providers[allowed.Provider] = ProviderConfig{BaseURL: upstreamURL + "/v1", Dialect: allowed.Dialect, APIKey: "provider-key"}
		targets = append(targets, Target{
			Provider:      allowed.Provider,
			Model:         allowed.Model,
			Weight:        10,
			ContextTokens: 2000000,
			ToolSupport:   ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}},
		})
	}
	return &Config{
		Server: ServerConfig{Identifiers: IdentifierConfig{Mode: "passthrough"},
			Listen:            ":0",
			DefaultModelGroup: fixture.ModelGroup,
			UsageDB:           freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite")),
			DecisionTelemetry: DecisionTelemetryConfig{Enabled: true},
			Logging:           LoggingConfig{Path: filepath.Join(dir, "requests.jsonl")},
		},
		StatePath: filepath.Join(dir, "state.json"),
		Provider:  providers,
		Models: map[string]ModelGroup{
			fixture.ModelGroup: {
				Strategy: "weighted",
				Targets:  targets,
			},
		},
		Callers: []CallerConfig{{
			ID:          "alice",
			User:        "alice",
			Project:     "metrum-insights",
			Environment: "test",
			TokenSHA256: hex.EncodeToString(sum[:]),
			TokenID:     "rtr_alice_test",
			Allow:       []string{fixture.ModelGroup},
			Rate:        RateConfig{RPM: 100, TPM: 1000000, Concurrent: 4},
			Quota:       QuotaConfig{Day: BudgetConfig{Requests: 100, Tokens: 1000000}, Month: BudgetConfig{Tokens: 1000000}},
			Key:         KeyConfig{LifetimeTokens: 1000000},
		}},
	}
}

func productionDerivedRegressionConfig(t *testing.T, dir, upstreamURL string) *Config {
	t.Helper()
	sum := sha256.Sum256([]byte(testToken))
	return &Config{
		Server: ServerConfig{Identifiers: IdentifierConfig{Mode: "passthrough"},
			Listen:            ":0",
			DefaultModelGroup: "large-openai-chat-tools-smoke",
			UsageDB:           freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite")),
			DecisionTelemetry: DecisionTelemetryConfig{Enabled: true},
			Logging:           LoggingConfig{Path: filepath.Join(dir, "requests.jsonl")},
		},
		StatePath: filepath.Join(dir, "state.json"),
		Provider: map[string]ProviderConfig{
			"limited-large-payload":   {BaseURL: upstreamURL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"},
			"validated-large-payload": {BaseURL: upstreamURL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"},
		},
		Models: map[string]ModelGroup{
			"large-openai-chat-tools-smoke": {
				Strategy: "static",
				Targets: []Target{
					{
						Provider:            "limited-large-payload",
						Model:               "limited-tool-model",
						ContextTokens:       200000,
						ToolSupport:         ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}},
						RequestShapeSupport: RequestShapeSupport{MaxRequestBytes: 196608},
					},
					{
						Provider:      "validated-large-payload",
						Model:         "validated-tool-model",
						ContextTokens: 200000,
						ToolSupport:   ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}},
					},
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
			Allow:       []string{"large-openai-chat-tools-smoke"},
			Rate:        RateConfig{RPM: 100, TPM: 1000000, Concurrent: 4},
			Quota:       QuotaConfig{Day: BudgetConfig{Requests: 100, Tokens: 1000000}, Month: BudgetConfig{Tokens: 1000000}},
			Key:         KeyConfig{LifetimeTokens: 1000000},
		}},
	}
}

func productionDerivedOpenCodeStreamOptionsConfig(t *testing.T, dir, upstreamURL string) *Config {
	t.Helper()
	sum := sha256.Sum256([]byte(testToken))
	return &Config{
		Server: ServerConfig{Identifiers: IdentifierConfig{Mode: "passthrough"},
			Listen:            ":0",
			DefaultModelGroup: "big-coder-opencode-stream-options-smoke",
			UsageDB:           freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite")),
			DecisionTelemetry: DecisionTelemetryConfig{Enabled: true},
			Logging:           LoggingConfig{Path: filepath.Join(dir, "requests.jsonl")},
		},
		StatePath: filepath.Join(dir, "state.json"),
		Provider: map[string]ProviderConfig{
			"xai":       {BaseURL: upstreamURL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"},
			"fireworks": {BaseURL: upstreamURL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"},
			"kimi":      {BaseURL: upstreamURL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"},
			"minimax":   {BaseURL: upstreamURL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"},
		},
		Models: map[string]ModelGroup{
			"big-coder-opencode-stream-options-smoke": {
				Strategy: "static",
				Targets: []Target{
					{
						Provider:            "xai",
						Model:               "grok-4.5",
						ContextTokens:       500000,
						ToolSupport:         ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}},
						RequestShapeSupport: RequestShapeSupport{UnsupportedRequestFeatures: []string{"stream_options"}},
					},
					{
						Provider:            "fireworks",
						Model:               "accounts/fireworks/models/deepseek-v4-flash",
						ContextTokens:       160000,
						ToolSupport:         ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}},
						RequestShapeSupport: RequestShapeSupport{UnsupportedRequestFeatures: []string{"stream_options"}},
					},
					{
						Provider:            "kimi",
						Model:               "kimi-k2.7-code",
						ContextTokens:       262144,
						ToolSupport:         ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}},
						RequestShapeSupport: RequestShapeSupport{UnsupportedRequestFeatures: []string{"stream_options"}},
					},
					{
						Provider:      "minimax",
						Model:         "MiniMax-M3",
						ContextTokens: 1000000,
						ToolSupport:   ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}},
					},
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
			Allow:       []string{"big-coder-opencode-stream-options-smoke"},
			Rate:        RateConfig{RPM: 100, TPM: 1000000, Concurrent: 4},
			Quota:       QuotaConfig{Day: BudgetConfig{Requests: 100, Tokens: 1000000}, Month: BudgetConfig{Tokens: 1000000}},
			Key:         KeyConfig{LifetimeTokens: 1000000},
		}},
	}
}

func syntheticProductionDerivedOpenAIChatPayload(t *testing.T, fixture productionDerivedLargePayloadFixture) string {
	t.Helper()
	messages := make([]map[string]any, 0, fixture.MessageCount)
	messages = append(messages, map[string]any{"role": "system", "content": "Synthetic production-derived large coding-agent fixture. Do not persist raw content."})
	filler := strings.Repeat("production-derived-safe-filler ", 62)
	for i := 1; i < fixture.MessageCount; i++ {
		role := "user"
		if i%2 == 0 {
			role = "assistant"
		}
		messages = append(messages, map[string]any{"role": role, "content": filler + "turn " + string(rune('a'+(i%26)))})
	}
	tools := make([]map[string]any, 0, fixture.ToolCount)
	descriptionRepeat := 42
	if fixture.RequestBytesBucket == "gt-1mb" || fixture.ToolSchemaBytesBucket == "gt-1mb" {
		descriptionRepeat = 5000
	}
	description := strings.Repeat("safe schema description ", descriptionRepeat)
	for i := 0; i < fixture.ToolCount; i++ {
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "fixture_tool_" + string(rune('a'+i)),
				"description": description,
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":    map[string]any{"type": "string", "description": strings.Repeat("safe path field ", 16)},
						"content": map[string]any{"type": "string", "description": strings.Repeat("safe content field ", 16)},
					},
					"required": []string{"path"},
				},
			},
		})
	}
	payload := map[string]any{
		"model":       fixture.ModelGroup,
		"stream":      fixture.Stream,
		"messages":    messages,
		"tools":       tools,
		"tool_choice": "auto",
		"metadata":    map[string]any{"fixture": fixture.Name},
	}
	if fixture.ParallelToolCallsPresent {
		payload["parallel_tool_calls"] = true
	}
	if fixture.StreamOptionsPresent {
		payload["stream_options"] = map[string]any{"include_usage": true}
	}
	if fixture.OutputCapPresent {
		payload["max_tokens"] = 512
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if got := byteBucket(len(raw)); got != fixture.RequestBytesBucket {
		t.Fatalf("fixture request bytes=%d bucket=%s want %s", len(raw), got, fixture.RequestBytesBucket)
	}
	if got := byteBucket(jsonValueLen(tools)); got != fixture.ToolSchemaBytesBucket {
		t.Fatalf("fixture tool schema bytes=%d bucket=%s want %s", jsonValueLen(tools), got, fixture.ToolSchemaBytesBucket)
	}
	return string(raw)
}

func loadProductionDerivedLargePayloadFixture(t *testing.T, name string) productionDerivedLargePayloadFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "smokes", "production-derived", name))
	if err != nil {
		t.Fatal(err)
	}
	var fixture productionDerivedLargePayloadFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func loadProductionDerivedErrorFixture(t *testing.T, name string) productionDerivedErrorFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "smokes", "production-derived", name))
	if err != nil {
		t.Fatal(err)
	}
	var fixture productionDerivedErrorFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func loadProductionDerivedAnthropicImageFixture(t *testing.T, name string) productionDerivedAnthropicImageFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "smokes", "production-derived", name))
	if err != nil {
		t.Fatal(err)
	}
	var fixture productionDerivedAnthropicImageFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func loadProductionDerivedCodexResponsesReasoningFixture(t *testing.T, name string) productionDerivedCodexResponsesReasoningFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "smokes", "production-derived", name))
	if err != nil {
		t.Fatal(err)
	}
	var fixture productionDerivedCodexResponsesReasoningFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func loadProductionDerivedAgentCompatibilityFixture(t *testing.T, name string) productionDerivedAgentCompatibilityFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "smokes", "production-derived", name))
	if err != nil {
		t.Fatal(err)
	}
	var fixture productionDerivedAgentCompatibilityFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func decodeProductionDerivedErrorDetails(t *testing.T, raw []byte, wantType string) map[string]any {
	t.Helper()
	var payload struct {
		Error struct {
			Type    string         `json:"type"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Type != wantType {
		t.Fatalf("error type=%q want %q body=%s", payload.Error.Type, wantType, raw)
	}
	return payload.Error.Details
}

func upstreamDetailsContain(details []requestUpstreamErrorDetailRecord, status int, class, field, value string) bool {
	for _, detail := range details {
		if detail.StatusCode == status && detail.ErrorClass == class && detail.FieldName == field && detail.FieldValue == value {
			return true
		}
	}
	return false
}

func assertProductionDerivedArtifactsDoNotContain(t *testing.T, svc *Service, dir string, forbidden ...string) {
	t.Helper()
	raw, _ := os.ReadFile(filepath.Join(dir, "requests.jsonl"))
	for _, value := range forbidden {
		if value != "" && strings.Contains(string(raw), value) {
			t.Fatalf("log artifacts contain forbidden value %q", value)
		}
	}
	var estimates []requestTokenEstimateRecord
	if err := svc.usage.db.Find(&estimates).Error; err != nil {
		t.Fatal(err)
	}
	serialized, err := json.Marshal(estimates)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range forbidden {
		if value != "" && strings.Contains(string(serialized), value) {
			t.Fatalf("usage artifacts contain forbidden value %q", value)
		}
	}
}

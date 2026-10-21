// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var updateProofRoutingGolden = flag.Bool("update-proof-routing-golden", false, "rewrite testdata/proof/expected.json from live request-evidence")

func TestProofRoutingSameGroupDifferentUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_proof",
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 8, "completion_tokens": 4, "total_tokens": 12},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.Cache.Enabled = false
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	trueValue := true
	cfg.Server.Diagnostics.Enabled = &trueValue
	cfg.Server.AdminReports = AdminReportsConfig{Enabled: true, DefaultSince: "24h", MaxRange: "31d", MaxRows: 50}
	hash := mustBcryptHash(t, "yell-yell-yum")
	t.Setenv("SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST", hash)
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{
		Enabled:           true,
		Realm:             "Unit Test Admin",
		AllowInsecureHTTP: true,
		Users: []AdminBasicAuthUser{{
			Username:        "admin",
			PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST",
			Subject:         "basic:admin",
			Domain:          "metrum-insights/test",
		}},
	}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{
		Enabled: true,
		Policy: []string{
			"g, basic:admin, reports_admin, metrum-insights/test",
			"p, reports_admin, metrum-insights/test, admin:reports, read|export|drilldown",
		},
	}

	falseValue := false
	group := ModelGroup{
		Strategy: "dynamic_score",
		RoutingPolicy: RoutingPolicyConfig{DynamicScore: DynamicScoreConfig{
			MinObservations:           2,
			MaxScoreAdjustmentPercent: 100,
			Affinity:                  DynamicScoreAffinityConfig{Enabled: &falseValue},
			Signals: DynamicScoreSignals{
				PromptFeatures: DynamicSignalPromptFeatures{
					Enabled:      true,
					MaxScanBytes: 16384,
					Features:     []string{"summarize", "code"},
				},
				Cost:               DynamicSignalNamedFields{Enabled: true},
				EvaluationMetadata: DynamicSignalEvaluationMetadata{Enabled: true},
			},
			ScoreTerms: []DynamicScoreTerm{
				{
					Name:       "summarize_cheap",
					When:       map[string]any{"prompt_feature": "summarize"},
					Expression: "1.0 * cost_score",
				},
				{
					Name:        "code_validated",
					When:        map[string]any{"prompt_feature": "code"},
					RequireTags: []string{"validated"},
					Expression:  "1.0 * eval_quality_score",
				},
			},
			EvaluationMetadata: []DynamicEvaluationTarget{
				{Provider: "mock", Model: "cheap-summarizer", QualityScore: 0.55},
				{Provider: "mock", Model: "validated-coder", QualityScore: 0.95},
			},
		}},
		Targets: []Target{
			{
				Provider:                 "mock",
				Model:                    "cheap-summarizer",
				Weight:                   50,
				InputPricePerMillionUSD:  0.1,
				OutputPricePerMillionUSD: 0.2,
				ToolSupport:              ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}},
			},
			{
				Provider:                 "mock",
				Model:                    "validated-coder",
				Weight:                   50,
				Tags:                     []string{"validated"},
				InputPricePerMillionUSD:  5.0,
				OutputPricePerMillionUSD: 10.0,
				ToolSupport:              ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}},
			},
		},
	}
	cfg.Models["proof-routing"] = group
	caller := cfg.Callers[0]
	caller.Allow = append(caller.Allow, "proof-routing")
	cfg.Callers[0] = caller

	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now().UTC()
	for i := 0; i < 2; i++ {
		for _, model := range []string{"cheap-summarizer", "validated-coder"} {
			svc.observations.add(dynamicObservationKey("proof-routing", "mock", model), dynamicObservation{
				TS:                 now,
				Status:             http.StatusOK,
				LatencyMS:          100,
				UpstreamMS:         90,
				OutputTokensPerSec: 40,
			}, defaultDynamicObservationWindow)
		}
	}

	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	fixtures := []string{"trivial.json", "complex.json"}
	got := make([]map[string]any, 0, len(fixtures))
	for _, name := range fixtures {
		body, err := os.ReadFile(filepath.Join(repoRoot, "testdata", "proof", name))
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", name, rr.Code, rr.Body.String())
		}
		requestID := rr.Header().Get("X-Request-Id")
		if requestID == "" {
			t.Fatalf("%s missing X-Request-Id", name)
		}

		flushUsageForTest(t, svc)
		evidenceReq := httptest.NewRequest(http.MethodGet, "/admin/reports/api/request-evidence?request_id="+requestID, nil)
		evidenceReq.SetBasicAuth("admin", "yell-yell-yum")
		evidenceRR := httptest.NewRecorder()
		svc.Handler().ServeHTTP(evidenceRR, evidenceReq)
		if evidenceRR.Code != http.StatusOK {
			t.Fatalf("%s evidence status=%d body=%s", name, evidenceRR.Code, evidenceRR.Body.String())
		}
		projected, err := projectProofEvidence(evidenceRR.Body.Bytes())
		if err != nil {
			t.Fatalf("%s project evidence: %v body=%s", name, err, evidenceRR.Body.String())
		}
		got = append(got, projected)
	}

	if got[0]["selectedModel"] == got[1]["selectedModel"] {
		t.Fatalf("expected different selected models, got %#v", got)
	}
	if got[0]["modelGroup"] != "proof-routing" || got[1]["modelGroup"] != "proof-routing" {
		t.Fatalf("expected same proof-routing group, got %#v", got)
	}

	goldenPath := filepath.Join(repoRoot, "testdata", "proof", "expected.json")
	if *updateProofRoutingGolden || os.Getenv("PROOF_ROUTING_UPDATE") == "1" {
		raw, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, '\n')
		if err := os.WriteFile(goldenPath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	wantRaw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with PROOF_ROUTING_UPDATE=1 first): %v", err)
	}
	var want []map[string]any
	if err := json.Unmarshal(wantRaw, &want); err != nil {
		t.Fatal(err)
	}
	gotRaw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantCmp, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotRaw) != string(wantCmp) {
		t.Fatalf("proof evidence mismatch\ngot:  %s\nwant: %s", gotRaw, wantCmp)
	}
}

func projectProofEvidence(raw []byte) (map[string]any, error) {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	request, _ := body["request"].(map[string]any)
	decisionTelemetry, _ := body["decisionTelemetry"].(map[string]any)
	routingDecisions, _ := decisionTelemetry["routingDecisions"].([]any)
	var strategy any
	var selectedCandidateIndex any
	if len(routingDecisions) > 0 {
		if decision, ok := routingDecisions[0].(map[string]any); ok {
			strategy = decision["strategy"]
			selectedCandidateIndex = decision["selectedCandidateIndex"]
		}
	}
	termNames := []string{}
	if terms, ok := decisionTelemetry["dynamicScoreTerms"].([]any); ok {
		for _, item := range terms {
			term, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if term["selected"] == true {
				if name, ok := term["termName"].(string); ok && name != "" {
					termNames = append(termNames, name)
				}
			}
		}
	}
	return map[string]any{
		"requestedModel":         request["requestedModel"],
		"modelGroup":             request["modelGroup"],
		"selectedProvider":       request["provider"],
		"selectedModel":          request["model"],
		"strategy":               strategy,
		"selectedCandidateIndex": selectedCandidateIndex,
		"termNames":              termNames,
	}, nil
}

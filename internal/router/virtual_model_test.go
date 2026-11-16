// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVirtualModelMembershipUsesImportedFamilyVector(t *testing.T) {
	toolReliability := 0.9
	vector := TargetCapabilityVector{
		QualityScore: 0.92, ToolReliability: &toolReliability, ContextTokens: 4096,
		P95LatencyMS: 850, MeanCostUSD: 0.002, EvalRunID: "eval-1",
		EvalReportSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		TaskFamily:       "coding", MeasuredAt: "2026-11-16",
	}
	target := Target{Validation: &TargetValidation{Status: "passed", Harness: "eval-infra", Capabilities: map[string]TargetCapabilityVector{"coding": vector}}}
	quality, reliability, latency, cost := 0.9, 0.8, 1000.0, 0.003
	group := &VirtualModelConfig{TaskFamily: "coding", MinQualityScore: &quality, MinToolReliability: &reliability, MinContextTokens: 2048, MaxP95LatencyMS: &latency, MaxCostUSD: &cost}
	if reason := virtualModelTargetReason(group, target); reason != "" {
		t.Fatalf("eligible target excluded: %s", reason)
	}
	quality = 0.95
	if reason := virtualModelTargetReason(group, target); reason != "virtual-model-quality-floor" {
		t.Fatalf("quality exclusion reason=%q", reason)
	}
	quality = 0.9
	reliability = 0.95
	if reason := virtualModelTargetReason(group, target); reason != "virtual-model-tool-reliability" {
		t.Fatalf("tool reliability exclusion reason=%q", reason)
	}
	reliability = 0.8
	group.MinContextTokens = 5000
	if reason := virtualModelTargetReason(group, target); reason != "virtual-model-context-floor" {
		t.Fatalf("context exclusion reason=%q", reason)
	}
	group.MinContextTokens = 2048
	cost = 0.001
	if reason := virtualModelTargetReason(group, target); reason != "virtual-model-cost-ceiling" {
		t.Fatalf("cost exclusion reason=%q", reason)
	}
	cost = 0.003
	group.TaskFamily = "summarization"
	if reason := virtualModelTargetReason(group, target); reason != "virtual-model-no-eval-vector" {
		t.Fatalf("missing-family reason=%q", reason)
	}
	group.TaskFamily = "coding"
	group.MinToolReliability = nil
	latency = 700
	if reason := virtualModelTargetReason(group, target); reason != "virtual-model-latency-ceiling" {
		t.Fatalf("latency exclusion reason=%q", reason)
	}
}

func TestVirtualModelRoutingUsesMeasuredMembershipHTTP(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"v","object":"chat.completion","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, body.Model)
	}))
	defer upstream.Close()
	svc := newTestService(t, upstream.URL, "provider-key")
	defer svc.Close()
	quality := 0.8
	reliability := 0.9
	vector := TargetCapabilityVector{QualityScore: 0.95, ToolReliability: &reliability, ContextTokens: 2048, P95LatencyMS: 120, MeanCostUSD: 0.001, EvalRunID: "run-1", EvalReportSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", TaskFamily: "coding", MeasuredAt: "2026-11-16"}
	svc.cfg.Models["default"] = ModelGroup{Strategy: "weighted", VirtualModel: &VirtualModelConfig{TaskFamily: "coding", MinQualityScore: &quality}, Targets: []Target{
		{Provider: "mock", Model: "measured", Weight: 1, Validation: &TargetValidation{Status: "passed", Harness: "eval-infra", Capabilities: map[string]TargetCapabilityVector{"coding": vector}}},
		{Provider: "mock", Model: "unmeasured", Weight: 100},
	}}
	router := httptest.NewServer(svc.Handler())
	defer router.Close()
	call := func() *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, router.URL+"/v1/chat/completions", strings.NewReader(`{"model":"default","max_tokens":8,"messages":[{"role":"user","content":"solve"}]}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := call()
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || upstreamCalls != 1 {
		t.Fatalf("measured target status=%d upstream calls=%d", resp.StatusCode, upstreamCalls)
	}
	quality = 0.99
	resp = call()
	resp.Body.Close()
	if resp.StatusCode < 400 || upstreamCalls != 1 {
		t.Fatalf("no-member request status=%d upstream calls=%d, want rejection without upstream", resp.StatusCode, upstreamCalls)
	}
}

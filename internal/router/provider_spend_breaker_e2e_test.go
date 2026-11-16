// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderSpendBreakerFailoverLocalhostE2E(t *testing.T) {
	var primaryCalls, fallbackCalls int
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls++
		writeJSON(w, http.StatusOK, map[string]any{"id": "primary", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "primary"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 1000, "completion_tokens": 0, "total_tokens": 1000}})
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls++
		writeJSON(w, http.StatusOK, map[string]any{"id": "fallback", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "fallback"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
	}))
	defer fallback.Close()
	dir := t.TempDir()
	cfg := testConfig(t, primary.URL, "primary-test-key", dir)
	primaryProvider := cfg.Provider["mock"]
	primaryProvider.SpendBreaker = ProviderSpendBreakerConfig{HourlyUSD: 1, OnTrip: "failover"}
	cfg.Provider["mock"] = primaryProvider
	cfg.Provider["fallback"] = ProviderConfig{BaseURL: fallback.URL, Dialect: "openai-chat", APIKey: "fallback-test-key"}
	group := cfg.Models["default"]
	group.Strategy = "failover"
	group.Targets = append(group.Targets, Target{Provider: "fallback", Model: "fallback-model", InputPricePerMillionUSD: 1})
	group.Targets[0].InputPricePerMillionUSD = 1000
	cfg.Models["default"] = group
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	router := httptest.NewServer(svc.Handler())
	defer router.Close()
	post := func() string {
		req, _ := http.NewRequest(http.MethodPost, router.URL+"/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hello"}]}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d body=%s", resp.StatusCode, body)
		}
		var result map[string]any
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatal(err)
		}
		return result["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
	}
	if got := post(); got != "primary" {
		t.Fatalf("first response=%q", got)
	}
	if got := post(); got != "fallback" {
		t.Fatalf("second response=%q", got)
	}
	if primaryCalls != 1 || fallbackCalls != 1 {
		t.Fatalf("calls primary=%d fallback=%d", primaryCalls, fallbackCalls)
	}
}

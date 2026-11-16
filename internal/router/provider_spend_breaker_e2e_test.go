// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProviderSpendBreakerFailoverLocalhostE2E(t *testing.T) {
	var capturedLog bytes.Buffer
	oldLog := log.Writer()
	log.SetOutput(&capturedLog)
	t.Cleanup(func() { log.SetOutput(oldLog) })
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
	eventsPath := filepath.Join(dir, "security.jsonl")
	cfg.Server.Security.Events = SecurityEventStreamConfig{Enabled: true, Version: "1.4.0", Output: "file", Path: eventsPath}
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
	cfg.Callers[0].Project = "local"
	cfg.Server.AdminReports = AdminReportsConfig{Enabled: true, DefaultSince: "24h", MaxRange: "31d", MaxRows: 100, Security: AdminSecurityReportsConfig{Enabled: true, RetentionDays: 30}}
	t.Setenv("SPEND_BREAKER_ADMIN_HASH", mustBcryptHash(t, "spend-breaker-admin-password"))
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{Enabled: true, AllowInsecureHTTP: true, Users: []AdminBasicAuthUser{{Username: "admin", PasswordHashEnv: "SPEND_BREAKER_ADMIN_HASH", Subject: "basic:admin", Domain: "local/test"}}}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, basic:admin, local/test, admin:reports, read|export|drilldown"}}
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	now := time.Now().UTC().Truncate(time.Hour)
	svc.providerSpend.nowFunc = func() time.Time { return now }
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
	now = now.Add(time.Hour)
	if got := post(); got != "primary" {
		t.Fatalf("third response=%q after window reset", got)
	}
	if primaryCalls != 2 || fallbackCalls != 1 {
		t.Fatalf("calls primary=%d fallback=%d", primaryCalls, fallbackCalls)
	}
	for _, secret := range []string{"primary-test-key", "fallback-test-key"} {
		if bytes.Contains(capturedLog.Bytes(), []byte(secret)) {
			t.Fatal("provider key appeared in logs")
		}
	}
	var rows []usageRecord
	var breakerEvents int64
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := svc.usage.db.Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if err := svc.usage.db.Model(&securityAccessEventRecord{}).Where("event_type IN ?", []string{"provider_spend_breaker_trip", "provider_spend_breaker_reset"}).Count(&breakerEvents).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) >= 3 && breakerEvents >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for spend-breaker usage rows")
		}
		time.Sleep(10 * time.Millisecond)
	}
	serializedRows, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"primary-test-key", "fallback-test-key"} {
		if bytes.Contains(serializedRows, []byte(secret)) {
			t.Fatal("provider key appeared in usage rows")
		}
	}
	var events []securityAccessEventRecord
	if err := svc.usage.db.Where("event_type IN ?", []string{"provider_spend_breaker_trip", "provider_spend_breaker_reset"}).Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) < 3 {
		t.Fatalf("breaker audit events=%d, want trip and reset records", len(events))
	}
	for _, event := range events {
		if event.ModelGroup != "" || !strings.HasPrefix(event.ReasonCode, "provider_spend_breaker_") || !strings.HasSuffix(event.ReasonCode, ":mock") {
			t.Fatalf("provider mislabeled in breaker event: %+v", event)
		}
	}
	serializedEvents, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"primary-test-key", "fallback-test-key"} {
		if bytes.Contains(serializedEvents, []byte(secret)) {
			t.Fatal("provider key appeared in breaker audit events")
		}
	}
	svc.securityEventStream.close()
	streamEvents, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(streamEvents, []byte("provider_spend_breaker_trip")) || !bytes.Contains(streamEvents, []byte("provider_spend_breaker_reset")) || bytes.Contains(streamEvents, []byte("primary-test-key")) || bytes.Contains(streamEvents, []byte("fallback-test-key")) {
		t.Fatalf("stream missed breaker events or exposed a credential: %s", streamEvents)
	}
}

func TestProviderSpendBreakerStopLocalhostE2E(t *testing.T) {
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
	provider := cfg.Provider["mock"]
	provider.SpendBreaker = ProviderSpendBreakerConfig{HourlyUSD: 1, OnTrip: "stop"}
	cfg.Provider["mock"] = provider
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
	post := func() *http.Response {
		req, _ := http.NewRequest(http.MethodPost, router.URL+"/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hello"}]}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	first := post()
	io.Copy(io.Discard, first.Body)
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first status=%d, want 200", first.StatusCode)
	}
	second := post()
	body, _ := io.ReadAll(second.Body)
	second.Body.Close()
	if second.StatusCode != http.StatusServiceUnavailable || !bytes.Contains(body, []byte(`"code":"provider_spend_breaker"`)) {
		t.Fatalf("stop response status=%d body=%s", second.StatusCode, body)
	}
	if primaryCalls != 1 || fallbackCalls != 0 {
		t.Fatalf("calls primary=%d fallback=%d", primaryCalls, fallbackCalls)
	}
}

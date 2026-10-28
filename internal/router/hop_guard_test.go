// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMaxRouterHopsDefaultAndBounds(t *testing.T) {
	for _, tt := range []struct {
		input, want int
		valid       bool
	}{
		{0, 16, true}, {1, 1, true}, {128, 128, true}, {-1, -1, false}, {129, 129, false},
	} {
		cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
		cfg.Server.Upstream.MaxRouterHops = tt.input
		cfg.setDefaults()
		err := cfg.Validate()
		if cfg.Server.Upstream.MaxRouterHops != tt.want || (err == nil) != tt.valid {
			t.Errorf("input=%d got=%d err=%v", tt.input, cfg.Server.Upstream.MaxRouterHops, err)
		}
	}
}

func TestRouterViaHops(t *testing.T) {
	tests := []struct {
		values []string
		hops   int
		valid  bool
	}{
		{nil, 0, true},
		{[]string{"1.1 proxy (a, b)", "2.0 METRUM-AI-ROUTER, 1.1 metrum-ai-router (nested (comment\\) with comma, x))"}, 2, true},
		{[]string{"1.1 proxy (metrum-ai-router)"}, 0, true},
		{[]string{"1.1 proxy (bad"}, 0, false},
		{[]string{"1.1 proxy,, 1.1 other"}, 0, false},
		{[]string{"1.1 proxy)"}, 0, false},
	}
	for _, tt := range tests {
		got, valid := routerViaHops(tt.values)
		if got != tt.hops || valid != tt.valid {
			t.Errorf("routerViaHops(%q) = %d, %v; want %d, %v", tt.values, got, valid, tt.hops, tt.valid)
		}
	}
}

func TestRouterViaPropagatesAndRejectsMalformed(t *testing.T) {
	var got []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append([]string(nil), r.Header.Values("Via")...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"one","object":"chat.completion","model":"mock-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Add("Via", "1.1 example (edge, one)")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(got) != 2 || got[0] != "1.1 example (edge, one)" || got[1] != "1.1 metrum-ai-router" {
		t.Fatalf("Via=%q", got)
	}
	got = nil
	req = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Via", "1.1 proxy (unterminated")
	rr = httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "invalid-via") || len(got) != 0 {
		t.Fatalf("status=%d body=%s upstream=%q", rr.Code, rr.Body.String(), got)
	}
}

func TestLoopStatusNeverRetriesEvenIfBodyMentionsQuota(t *testing.T) {
	err := classifyUpstreamStatus(http.StatusLoopDetected, []byte(`quota_exhausted`))
	if err.Class != "upstream_loop_detected" || err.Retryable || err.Fallbackable {
		t.Fatalf("%+v", err)
	}
	code, status := upstreamFailureResponse(err, nil)
	if code != "upstream-loop-detected" || status != http.StatusLoopDetected {
		t.Fatalf("%s %d", code, status)
	}
}

func TestCountTokensViaStopsSelfLoop(t *testing.T) {
	var hits atomic.Int32
	var svc *Service
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		svc.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	cfg := testConfig(t, server.URL, testToken, t.TempDir())
	cfg.Server.Upstream.MaxRouterHops = 3
	cfg.Provider["mock"] = ProviderConfig{BaseURL: server.URL + "/v1", Dialect: "anthropic", APIKey: testToken}
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "default", Dialect: "anthropic"}}}
	var err error
	svc, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	req, err := http.NewRequest("POST", server.URL+"/v1/messages/count_tokens", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-API-Key", testToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusLoopDetected || !strings.Contains(string(body), "router-hop-limit") {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if got := hits.Load(); got != 4 {
		t.Fatalf("expected 3 forwarded requests and one rejection, got %d", got)
	}
}

func TestRouterViaStopsSelfLoopWithoutRetry(t *testing.T) {
	var hits atomic.Int32
	var svc *Service
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		svc.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	cfg := testConfig(t, server.URL, testToken, t.TempDir())
	cfg.Server.Upstream.MaxRouterHops = 3
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "default"}}}
	var err error
	svc, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	req, err := http.NewRequest("POST", server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusLoopDetected || !strings.Contains(string(body), "upstream-loop-detected") {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if got := hits.Load(); got != 4 {
		t.Fatalf("expected 3 forwarded requests and one rejection, got %d", got)
	}
}

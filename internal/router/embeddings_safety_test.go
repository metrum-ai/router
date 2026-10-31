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
	"time"
)

func TestEmbeddingsAdaptiveBackoff(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"private"}}`)
	}))
	defer up.Close()
	cfg := embeddingTestConfig(t, up.URL)
	enabled := true
	honor := true
	p := cfg.Provider["mock"]
	p.TrafficShape = TrafficShapeConfig{Enabled: &enabled, Upstream429Backoff: TrafficBackoffConfig{Enabled: &enabled, MinBackoffMS: 100, MaxBackoffMS: 1000, Multiplier: 2, HonorRetryAfter: &honor}}
	cfg.Provider["mock"] = p
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	s.shaping.now = func() time.Time { return now }
	first := embeddingRequest(s, `{"model":"default","input":"x"}`)
	if first.Code != 429 || !strings.Contains(first.Body.String(), `"type":"rate_limit_error"`) || first.Header().Get("Retry-After") != "" {
		t.Fatalf("first=%d %s", first.Code, first.Body.String())
	}
	second := embeddingRequest(s, `{"model":"default","input":"y"}`)
	if second.Code != 503 || calls.Load() != 1 {
		t.Fatalf("second=%d calls=%d", second.Code, calls.Load())
	}
}

func TestEmbeddingsUnreadableSuccessSettlesEstimate(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, strings.Repeat("x", 128))
	}))
	defer up.Close()
	cfg := embeddingTestConfig(t, up.URL)
	cfg.Server.Upstream.MaxResponseBytes = 64
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w := embeddingRequest(s, `{"model":"default","input":"abcdefgh"}`)
	if w.Code != 502 || calls.Load() != 1 {
		t.Fatalf("status=%d calls=%d", w.Code, calls.Load())
	}
	flushUsageForTest(t, s)
	var row usageRecord
	if err := s.usage.db.Where("endpoint = ?", "embeddings").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.InputTokens != 3 || row.UsageSource != "estimated" || row.TotalCostUSD != 0.000003 {
		t.Fatalf("estimated settlement missing: input=%d source=%s cost=%v", row.InputTokens, row.UsageSource, row.TotalCostUSD)
	}
}

func TestEmbeddingsAuthentication(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer up.Close()
	cfg := embeddingTestConfig(t, up.URL)
	cfg.Callers[0].Allow = []string{"default"}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"default","input":"x"}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("unauth status=%d", w.Code)
	}
	for _, model := range []string{"other", "missing"} {
		w := embeddingRequest(s, `{"model":"`+model+`","input":"x"}`)
		if w.Code != 404 || !strings.Contains(w.Body.String(), `"code":"model_not_found"`) {
			t.Fatalf("model status=%d body=%s", w.Code, w.Body.String())
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unauthorized request reached upstream")
	}
}

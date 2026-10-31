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
	"sync/atomic"
	"testing"
	"time"
)

func embeddingTestConfig(t *testing.T, url string) *Config {
	t.Helper()
	dir := t.TempDir()
	cfg := testConfig(t, url, "test-upstream-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.db"))
	g := cfg.Models["default"]
	g.Strategy = "failover"
	g.Targets[0].Model = "embedding-private-model"
	g.Targets[0].OutputModalities = []string{"embeddings"}
	g.Targets[0].EmbeddingDimensions = []int{2, 512}
	g.Targets[0].ContextTokens = 8191
	g.Targets[0].InputPricePerMillionUSD = 1
	cfg.Models["default"] = g
	return cfg
}

func embeddingRequest(s *Service, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestEmbeddingsEndToEndAndUsage(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer test-upstream-key" {
			t.Errorf("incorrect upstream request")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "embedding-private-model" || body["dimensions"] != float64(2) {
			t.Errorf("incorrect upstream model/dimensions")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"model":"embedding-private-model","usage":{"prompt_tokens":7,"total_tokens":7}}`)
	}))
	defer up.Close()
	cfg := embeddingTestConfig(t, up.URL)
	provider := cfg.Provider["mock"]
	provider.BaseURL += "/responses"
	cfg.Provider["mock"] = provider
	cfg.Server.Identifiers = identifierTestConfig()
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Duplicate model keys must not survive canonicalization and override the authorized target.
	w := embeddingRequest(s, `{"model":"ignored","model":"default","input":"private example","dimensions":2}`)
	if w.Code != 200 || calls.Load() != 1 {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"model":"default"`) || strings.Contains(w.Body.String(), "embedding-private-model") {
		t.Fatalf("model was not rewritten: %s", w.Body.String())
	}
	flushUsageForTest(t, s)
	var row usageRecord
	if err := s.usage.db.Where("status = ?", 200).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Endpoint != "embeddings" || row.InputTokens != 7 || row.OutputTokens != 0 || row.TotalTokens != 7 || row.TotalCostUSD != 0.000007 {
		t.Fatalf("usage mismatch: %+v", row)
	}
	emitUsageForTest(t, s, logRecord{RequestID: "other-endpoint", Endpoint: "responses.compact", TS: time.Now().UTC().Format(time.RFC3339Nano), Status: 200})
	filters, ok := s.parseAdminReportFilters(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/admin/reports/api/requests?since=24h&endpoint=embeddings", nil), true, adminAuthSubject{}, true)
	if !ok || filters.Endpoint != "embeddings" {
		t.Fatal("admin endpoint filter was not parsed")
	}
	rows, err := s.usage.rows(filters.UsageReportOptions)
	if err != nil || len(rows) != 1 || rows[0].Endpoint != "embeddings" {
		t.Fatalf("endpoint-filtered rows=%d err=%v", len(rows), err)
	}
	if got := adminRequestFromRow(rows[0]).Endpoint; got != "embeddings" {
		t.Fatalf("admin endpoint=%q", got)
	}
	day := time.Now().UTC().Truncate(24 * time.Hour)
	if err := validateRollupReportOptions(UsageReportOptions{From: day, To: day.Add(24 * time.Hour), Endpoint: "embeddings"}); err == nil {
		t.Fatal("endpoint filtering must not silently use rollups without endpoint dimensions")
	}
}

func TestEmbeddingsValidationAndQuota(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"object":"list","data":[],"model":"x","usage":{"prompt_tokens":1}}`)
	}))
	defer up.Close()
	cfg := embeddingTestConfig(t, up.URL)
	g := cfg.Models["default"]
	g.Targets[0].ContextTokens = 3
	cfg.Models["default"] = g
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, tc := range []struct {
		body, code string
		status     int
	}{
		{`{"model":"other","input":"a"}`, "unsupported_endpoint_for_model", 400},
		{`{"model":"missing","input":"a"}`, "model_not_found", 404},
		{`{"model":"default","input":"a","dimensions":3}`, "unsupported_dimensions", 400},
		{`{"model":"default","input":[1,2,3,4]}`, "unsupported_embedding_input", 400},
		{`{"model":"default","input":[1,"two"]}`, "invalid_request", 400},
	} {
		w := embeddingRequest(s, tc.body)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) {
			t.Fatalf("got %d %s", w.Code, w.Body.String())
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid requests reached upstream")
	}
	cfg2 := embeddingTestConfig(t, up.URL)
	cfg2.Callers[0].Quota.Day.Tokens = 1
	s2, err := New(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	w := embeddingRequest(s2, `{"model":"default","input":"abcdefgh"}`)
	if w.Code != 429 || calls.Load() != 0 {
		t.Fatalf("quota status=%d upstream calls=%d", w.Code, calls.Load())
	}
}

func TestEmbeddingsFallbackAndErrors(t *testing.T) {
	var first, second atomic.Int32
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first.Add(1)
		w.WriteHeader(503)
		io.WriteString(w, `{"error":"sensitive provider detail"}`)
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		second.Add(1)
		io.WriteString(w, `{"object":"list","data":[],"model":"x","usage":{"prompt_tokens":2}}`)
	}))
	defer b.Close()
	cfg := embeddingTestConfig(t, a.URL)
	cfg.Provider["second"] = ProviderConfig{BaseURL: b.URL + "/v1", Dialect: "openai"}
	g := cfg.Models["default"]
	g.Targets[0].EmbeddingSpace = "shared"
	other := g.Targets[0]
	other.Provider = "second"
	other.Model = "other-model"
	g.Targets = append(g.Targets, other)
	cfg.Models["default"] = g
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w := embeddingRequest(s, `{"model":"default","input":"hello"}`)
	if w.Code != 200 || first.Load() != 1 || second.Load() != 1 {
		t.Fatalf("status=%d calls=%d/%d", w.Code, first.Load(), second.Load())
	}
	for _, status := range []int{400, 429, 508} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "19")
				w.WriteHeader(status)
				io.WriteString(w, `{"error":"sensitive provider detail"}`)
			}))
			defer up.Close()
			cfg := embeddingTestConfig(t, up.URL)
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			w := embeddingRequest(svc, `{"model":"default","input":"x"}`)
			if w.Code != status || strings.Contains(w.Body.String(), "sensitive") || w.Header().Get("Retry-After") != "" {
				t.Fatalf("status=%d body=%s headers=%v", w.Code, w.Body.String(), w.Header())
			}
		})
	}
}

func TestEmbeddingsPIIFilter(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "person@example.com") {
			t.Error("raw PII reached upstream")
		}
		io.WriteString(w, `{"object":"list","data":[],"model":"x","usage":{"prompt_tokens":3}}`)
	}))
	defer up.Close()
	cfg := embeddingTestConfig(t, up.URL)
	g := cfg.Models["default"]
	g.PIIFilter = testPIIFilterConfig("redact_only")
	cfg.Models["default"] = g
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w := embeddingRequest(s, `{"model":"default","input":["person@example.com","x"]}`)
	if w.Code != 200 {
		t.Fatalf("status=%d %s", w.Code, w.Body.String())
	}
	w = embeddingRequest(s, `{"model":"default","input":[1,2]}`)
	if w.Code != 400 || calls.Load() != 1 {
		t.Fatalf("status=%d calls=%d", w.Code, calls.Load())
	}
}

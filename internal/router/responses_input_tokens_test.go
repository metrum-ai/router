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
)

func TestResponsesInputTokensPassThroughUnbilled(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/responses/input_tokens" || r.Header.Get("Authorization") != "Bearer provider-key" {
			t.Errorf("unexpected upstream request path=%s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "mock-model" || body["input"] != "hello" || body["extension"] != "kept" {
			t.Errorf("bad upstream body: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"response.input_tokens","input_tokens":9}`)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	p := cfg.Provider["mock"]
	p.Dialect = "openai-responses"
	cfg.Provider["mock"] = p
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Callers[0].Rate.RPM = 2
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			svc.Close()
		}
	})
	do := func(token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/v1/responses/input_tokens", strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		return w
	}
	if w := do("", `{}`); w.Code != 401 {
		t.Fatalf("unauthorized: %d", w.Code)
	}
	if w := do(testToken, `{"model":"does-not-exist","input":"hello"}`); w.Code != 404 || !strings.Contains(w.Body.String(), "model_not_found") {
		t.Fatalf("model privacy: %d %s", w.Code, w.Body.String())
	}
	body := `{"model":"default","input":"hello","extension":"kept"}`
	w := do(testToken, body)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"input_tokens":9`) {
		t.Fatalf("count: %d %s", w.Code, w.Body.String())
	}
	if w := do(testToken, body); w.Code != 200 {
		t.Fatalf("second count: %d %s", w.Code, w.Body.String())
	}
	if w := do(testToken, body); w.Code != 429 {
		t.Fatalf("RPM not enforced: %d %s", w.Code, w.Body.String())
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls %d", calls.Load())
	}
	// Wait for the asynchronous writer to drain before checking the durable row.
	svc.Close()
	closed = true
	store, err := OpenUsageStorePath(filepath.Join(dir, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var rows []usageRecord
	if err := store.db.Where("endpoint = ? AND status = ?", "responses.input_tokens", 200).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected two audit rows, got %d", len(rows))
	}
	for _, row := range rows {
		if row.RequestKind != "count_tokens" || row.InputTokens != 0 || row.OutputTokens != 0 || row.TotalCostUSD != 0 {
			t.Fatalf("count billed: %+v", row)
		}
	}
}

func TestResponsesInputTokensUnsupportedAndSanitized(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `private-provider-detail`)
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	do := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/v1/responses/input_tokens", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		return w
	}
	body := `{"model":"default","input":"hi"}`
	if w := do(body); w.Code != 501 || !strings.Contains(w.Body.String(), "unsupported_endpoint") {
		t.Fatalf("chat-only group: %d %s", w.Code, w.Body.String())
	}
	p := svc.cfg.Provider["mock"]
	p.Dialect = "openai-responses"
	svc.cfg.Provider["mock"] = p
	if w := do(`{"model":"default","previous_response_id":"resp_123","input":"hi"}`); w.Code != 501 {
		t.Fatalf("unowned reference: %d %s", w.Code, w.Body.String())
	}
	if w := do(body); w.Code != 400 || strings.Contains(w.Body.String(), "private-provider-detail") {
		t.Fatalf("unsanitized upstream error: %d %s", w.Code, w.Body.String())
	}
}

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

func TestResponsesCompactMetersAndAvoidsCache(t *testing.T) {
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/responses/compact" || r.Header.Get("Authorization") != "Bearer provider-key" {
			t.Errorf("bad compact upstream request path=%s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "mock-model" || body["input"] != "hello" || body["extra"] != "kept" {
			t.Errorf("bad compact request: %#v", body)
		}
		if _, ok := body["stream"]; ok {
			t.Error("unexpected injected stream field")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"cmp_1","object":"response.compaction","output":[],"usage":{"input_tokens":12,"output_tokens":3,"total_tokens":15}}`)
	}))
	defer up.Close()
	dir := t.TempDir()
	cfg := testConfig(t, up.URL, "provider-key", dir)
	p := cfg.Provider["mock"]
	p.Dialect = "openai-responses"
	cfg.Provider["mock"] = p
	g := cfg.Models["default"]
	g.Targets[0].InputPricePerMillionUSD = 10
	g.Targets[0].OutputPricePerMillionUSD = 20
	cfg.Models["default"] = g
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
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
	request := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/v1/responses/compact", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		return w
	}
	if w := request(`{"model":"default","previous_response_id":"resp_1","input":"hello"}`); w.Code != 501 {
		t.Fatalf("must not misroute stored id: %d %s", w.Code, w.Body.String())
	}
	body := `{"model":"default","input":"hello","extra":"kept"}`
	for i := 0; i < 2; i++ {
		w := request(body)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"object":"response.compaction"`) {
			t.Fatalf("compact: %d %s", w.Code, w.Body.String())
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("compact requests cached or rerouted: %d", calls.Load())
	}
	svc.Close()
	closed = true
	store, err := OpenUsageStorePath(filepath.Join(dir, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var rows []usageRecord
	if err := store.db.Where("endpoint = ? AND status = ?", "responses.compact", 200).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected two metered compact rows, got %d", len(rows))
	}
	for _, row := range rows {
		if row.InputTokens != 12 || row.OutputTokens != 3 || row.TotalCostUSD <= 0 {
			t.Fatalf("bad compact billing: %+v", row)
		}
	}
}

func TestResponsesCompactNoNativeTarget(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	r := httptest.NewRequest("POST", "/v1/responses/compact", strings.NewReader(`{"model":"default","input":"hi"}`))
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, r)
	if w.Code != 501 || !strings.Contains(w.Body.String(), "unsupported_endpoint") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

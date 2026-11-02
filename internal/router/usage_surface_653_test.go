// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestUsageEndpointBoundedLabels(t *testing.T) {
	cases := []struct{ method, path, want string }{
		{"POST", "/v1/chat/completions", "chat.completions"},
		{"POST", "/v1/responses", "responses"},
		{"POST", "/v1/responses/input_tokens", "responses.input_tokens"},
		{"POST", "/v1/responses/compact", "responses.compact"},
		{"POST", "/v1/messages", "messages"},
		{"POST", "/anthropic/v1/messages", "messages"},
		{"POST", "/v1/messages/count_tokens", "messages.count_tokens"},
		{"POST", "/anthropic/v1/messages/count_tokens", "messages.count_tokens"},
		{"GET", "/v1/models", "models.list"},
		{"GET", "/v1/models/private-id", "models.retrieve"},
		{"GET", "/v1/codex/models.json", "models.codex"},
		{"GET", "/v1/usage", "usage"},
		{"GET", "/metrics", "metrics"},
		{"DELETE", "/v1/content-captures/private-id", "content_captures.delete"},
		{"POST", "/v1/content-captures/purge-expired", "content_captures.purge_expired"},
		{"POST", "/v1/models/private-id", "unsupported"},
		{"GET", "/v1/responses/private-id", "unsupported"},
	}
	for _, tc := range cases {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			if got := usageEndpoint(httptest.NewRequest(tc.method, tc.path, nil)); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestUsageEndpointControlSurfacesSQLite(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	cfg.Callers[0].MetricsAdmin = true
	cfg.Callers[0].ContentAdmin = true
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	cases := []struct {
		method, path, endpoint string
		status                 int
	}{
		{"GET", "/v1/models", "models.list", 200},
		{"GET", "/v1/models/default", "models.retrieve", 200},
		{"GET", "/v1/codex/models.json", "models.codex", 200},
		{"GET", "/v1/usage", "usage", 200},
		{"GET", "/metrics", "metrics", 200},
		{"DELETE", "/v1/content-captures/private-id", "content_captures.delete", 200},
		{"POST", "/v1/content-captures/purge-expired", "content_captures.purge_expired", 200},
		{"POST", "/v1/embeddings", "unsupported", 501},
	}
	for _, tc := range cases {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body.String())
			}
			id := w.Header().Get("X-Request-Id")
			if id == "" {
				t.Fatalf("no request ID, status=%d", w.Code)
			}
			flushUsageForTest(t, svc)
			var row usageRecord
			if err := svc.usage.db.Where("request_id = ?", id).First(&row).Error; err != nil {
				t.Fatal(err)
			}
			if row.Endpoint != tc.endpoint || row.Status != w.Code {
				t.Fatalf("status=%d row status=%d endpoint=%q want=%q", w.Code, row.Status, row.Endpoint, tc.endpoint)
			}
		})
	}
}

func TestUsageEndpointInferenceSQLite(t *testing.T) {
	cases := []struct{ name, path, dialect, body, response, endpoint string }{
		{"chat", "/v1/chat/completions", "openai-chat", `{"model":"default","messages":[{"role":"user","content":"hi"}]}`, `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, "chat.completions"},
		{"responses", "/v1/responses", "openai-responses", `{"model":"default","input":"hi"}`, `{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`, "responses"},
		{"messages", "/v1/messages", "anthropic", `{"model":"default","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`, "messages"},
		{"messages-alias", "/anthropic/v1/messages", "anthropic", `{"model":"default","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`, "messages"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.response)
			}))
			defer up.Close()
			dir := t.TempDir()
			cfg := testConfig(t, up.URL, "provider-key", dir)
			cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
			p := cfg.Provider["mock"]
			p.Dialect = tc.dialect
			cfg.Provider["mock"] = p
			svc, err := newServiceWithVerifiedFixture(cfg)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, req)
			if w.Code != 200 {
				svc.Close()
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			svc.Close()
			store, err := OpenUsageStorePath(filepath.Join(dir, "usage.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			var rows []usageRecord
			if err := store.db.Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].Endpoint != tc.endpoint {
				t.Fatalf("rows=%+v want endpoint=%q", rows, tc.endpoint)
			}
		})
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// Issue #778: an OpenAI-compatible target that only accepts
// max_completion_tokens rejected the router's max_tokens and the 400 came back
// as an opaque upstream error. The router now retries that exact refusal once
// with the field the provider names, and only that refusal.

const modernOnlyRefusal = `{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.","type":"invalid_request_error","param":"max_tokens","code":"unsupported_parameter"}}`

const legacyOnlyRefusal = `{"error":{"message":"Unsupported parameter: 'max_completion_tokens' is not supported with this model. Use 'max_tokens' instead.","type":"invalid_request_error","param":"max_completion_tokens","code":"unsupported_parameter"}}`

type capFieldUpstream struct {
	calls      atomic.Int32
	rejectBody string
	rejectWhen string // "max_tokens" or "max_completion_tokens": the field this upstream refuses
	seen       []string
}

func (u *capFieldUpstream) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.calls.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		sent := ""
		if _, ok := body["max_tokens"]; ok {
			sent = "max_tokens"
		}
		if _, ok := body["max_completion_tokens"]; ok {
			sent = "max_completion_tokens"
		}
		u.seen = append(u.seen, sent)
		if sent == u.rejectWhen {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, u.rejectBody)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl_cap","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	})
}

func capFieldService(t *testing.T, upstream *capFieldUpstream, mutate func(*Config)) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	srv := httptest.NewServer(upstream.handler(t))
	t.Cleanup(srv.Close)
	cfg := testConfig(t, srv.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	if mutate != nil {
		mutate(cfg)
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc, filepath.Join(dir, "usage.sqlite")
}

func postCappedChat(t *testing.T, svc *Service, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, req)
	return w
}

// TestOutputCapFieldRetryHealsModernOnlyTarget: the reported #778 shape. A
// legacy caller sends max_tokens, the model requires max_completion_tokens.
func TestOutputCapFieldRetryHealsModernOnlyTarget(t *testing.T) {
	upstream := &capFieldUpstream{rejectBody: modernOnlyRefusal, rejectWhen: "max_tokens"}
	svc, dbPath := capFieldService(t, upstream, nil)

	w := postCappedChat(t, svc, `{"model":"default","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s; want the cap field healed on one retry", w.Code, w.Body.String())
	}
	if got := upstream.calls.Load(); got != 2 {
		t.Fatalf("upstream calls=%d, want 2 (the refusal and one retry)", got)
	}
	if len(upstream.seen) != 2 || upstream.seen[0] != "max_tokens" || upstream.seen[1] != "max_completion_tokens" {
		t.Fatalf("upstream saw cap fields %v, want [max_tokens max_completion_tokens]", upstream.seen)
	}

	// The retry is recorded, and the request keeps one attempt row.
	flushUsageForTest(t, svc)
	store, err := OpenUsageStorePath(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var events []requestTraceEventRecord
	if err := store.db.Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Event == "output_token_field_retry" && strings.Contains(event.Message, "max_completion_tokens") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing output_token_field_retry trace event: %+v", events)
	}
	var rows []usageRecord
	if err := store.db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != http.StatusOK {
		t.Fatalf("usage rows=%+v, want one successful row", rows)
	}
}

// TestOutputCapFieldRetryHonoursMaxTokensTargets: the reverse direction. A
// target declared as max_completion_tokens that actually requires max_tokens is
// healed too, so targets that require max_tokens keep working.
func TestOutputCapFieldRetryHonoursMaxTokensTargets(t *testing.T) {
	upstream := &capFieldUpstream{rejectBody: legacyOnlyRefusal, rejectWhen: "max_completion_tokens"}
	svc, _ := capFieldService(t, upstream, func(cfg *Config) {
		group := cfg.Models["default"]
		group.Targets[0].OutputTokenField = "max_completion_tokens"
		cfg.Models["default"] = group
	})

	w := postCappedChat(t, svc, `{"model":"default","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s; want the retry to heal a max_tokens-only target", w.Code, w.Body.String())
	}
	if len(upstream.seen) != 2 || upstream.seen[0] != "max_completion_tokens" || upstream.seen[1] != "max_tokens" {
		t.Fatalf("upstream saw cap fields %v, want [max_completion_tokens max_tokens]", upstream.seen)
	}
}

// TestNoOutputCapFieldRetryOnUnrelatedBadRequest: only the field refusal is
// retried; any other 400 stays a single attempt.
func TestNoOutputCapFieldRetryOnUnrelatedBadRequest(t *testing.T) {
	upstream := &capFieldUpstream{
		rejectWhen: "max_tokens",
		rejectBody: `{"error":{"message":"Invalid 'messages': empty array.","type":"invalid_request_error","param":"messages"}}`,
	}
	svc, _ := capFieldService(t, upstream, nil)

	w := postCappedChat(t, svc, `{"model":"default","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	if w.Code == http.StatusOK {
		t.Fatalf("status=%d, want the unrelated 400 surfaced", w.Code)
	}
	if got := upstream.calls.Load(); got != 1 {
		t.Fatalf("upstream calls=%d, want 1 (no retry for an unrelated 400)", got)
	}
}

// TestOutputCapFieldRetryStopsAfterOneAttempt: a provider that rejects both
// fields is not retried in a loop.
func TestOutputCapFieldRetryStopsAfterOneAttempt(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		if _, ok := body["max_tokens"]; ok {
			fmt.Fprint(w, modernOnlyRefusal)
			return
		}
		fmt.Fprint(w, legacyOnlyRefusal)
	}))
	t.Cleanup(srv.Close)
	cfg := testConfig(t, srv.URL, "provider-key", t.TempDir())
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)

	w := postCappedChat(t, svc, `{"model":"default","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	if w.Code == http.StatusOK {
		t.Fatalf("status=%d, want the provider refusal surfaced", w.Code)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream calls=%d, want exactly 2 (one retry, no loop)", got)
	}
}

// Unit table for the detection and the body rewrite.
func TestOutputTokenFieldRetryBodyDetection(t *testing.T) {
	const sentLegacy = `{"model":"m","max_tokens":64,"messages":[]}`
	const sentModern = `{"model":"m","max_completion_tokens":64,"messages":[]}`
	cases := []struct {
		name      string
		sent      string
		provider  string
		wantOK    bool
		wantField string
	}{
		{"legacy-rejected-modern-named", sentLegacy, modernOnlyRefusal, true, "max_completion_tokens"},
		{"modern-rejected-legacy-named", sentModern, legacyOnlyRefusal, true, "max_tokens"},
		{"unrelated-param", sentLegacy, `{"error":{"message":"bad tools","param":"tools"}}`, false, ""},
		{"no-param", sentLegacy, `{"error":{"message":"Use 'max_completion_tokens' instead."}}`, false, ""},
		{"message-names-only-own-field", sentLegacy, `{"error":{"message":"max_tokens is invalid","param":"max_tokens"}}`, false, ""},
		{"destination-already-present", `{"max_tokens":1,"max_completion_tokens":2}`, modernOnlyRefusal, false, ""},
		{"source-absent", `{"model":"m"}`, modernOnlyRefusal, false, ""},
		{"not-json-provider-body", sentLegacy, `not json`, false, ""},
		{"non-object-body", `[]`, modernOnlyRefusal, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next, field, ok := outputTokenFieldRetryBody([]byte(tc.sent), []byte(tc.provider))
			if ok != tc.wantOK || field != tc.wantField {
				t.Fatalf("ok=%t field=%q, want ok=%t field=%q", ok, field, tc.wantOK, tc.wantField)
			}
			if !ok {
				return
			}
			var decoded map[string]any
			if err := json.Unmarshal(next, &decoded); err != nil {
				t.Fatalf("rewritten body is not JSON: %v", err)
			}
			if _, ok := decoded[field]; !ok {
				t.Fatalf("rewritten body missing %q: %s", field, next)
			}
		})
	}
}

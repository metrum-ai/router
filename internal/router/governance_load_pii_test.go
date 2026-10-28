// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Synthetic PII must be redacted before both a failing primary and a fallback,
// across mixed streaming and unary requests. This is one bounded localhost case,
// not a guarantee about arbitrary PII formats or every failure mode.
func TestGovernanceLoadPIIFallback(t *testing.T) {
	const workers = 128
	var primaryCalls, fallbackCalls, leaks atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad synthetic request", http.StatusBadRequest)
			return
		}
		raw, _ := json.Marshal(body)
		if bytes.Contains(raw, []byte("@example.com")) || !bytes.Contains(raw, []byte("[EMAIL_1]")) {
			leaks.Add(1)
		}
		if body["model"] == "primary-model" {
			primaryCalls.Add(1)
			http.Error(w, "synthetic failure", http.StatusServiceUnavailable)
			return
		}
		if body["model"] != "fallback-model" {
			http.Error(w, "unexpected synthetic target", http.StatusBadRequest)
			return
		}
		fallbackCalls.Add(1)
		if stream, _ := body["stream"].(bool); stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"id\":\"chatcmpl_synthetic\",\"object\":\"chat.completion.chunk\",\"model\":\"fallback-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"redacted [EMAIL_1]\"},\"finish_reason\":null}]}\n\n")
			fmt.Fprint(w, "data: {\"id\":\"chatcmpl_synthetic\",\"object\":\"chat.completion.chunk\",\"model\":\"fallback-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":4,\"total_tokens\":14}}\n\ndata: [DONE]\n\n")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "chatcmpl_synthetic", "object": "chat.completion", "model": "fallback-model",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "redacted [EMAIL_1]"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 4, "total_tokens": 14},
		})
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Server.Cache.Enabled = false
	cfg.Callers[0].Rate = RateConfig{RPM: 10000, TPM: 10000000, Concurrent: workers + 10}
	cfg.Callers[0].Quota.Day = BudgetConfig{Requests: 10000, Tokens: 10000000}
	cfg.Callers[0].Quota.Month = BudgetConfig{Requests: 10000, Tokens: 10000000}
	cfg.Callers[0].Key.LifetimeTokens = 10000000
	cfg.Models["default"] = ModelGroup{Strategy: "static", PIIFilter: testPIIFilterConfig("redact_only"), Targets: []Target{
		{Provider: "mock", Model: "primary-model"}, {Provider: "mock", Model: "fallback-model"},
	}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	router := httptest.NewServer(svc.Handler())
	defer router.Close()
	client := &http.Client{Timeout: 90 * time.Second}
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			body, _ := json.Marshal(map[string]any{
				"model": "default", "stream": i%2 == 0, "max_tokens": 64,
				"messages": []any{map[string]any{"role": "user", "content": fmt.Sprintf("Contact synthetic%d@example.com", i)}},
			})
			req, err := http.NewRequest("POST", router.URL+"/v1/chat/completions", bytes.NewReader(body))
			if err != nil {
				errs <- err
				return
			}
			req.Header.Set("Authorization", "Bearer "+testToken)
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			data, err := io.ReadAll(resp.Body)
			if err != nil || resp.StatusCode != http.StatusOK || !bytes.Contains(data, []byte("[EMAIL_1]")) || bytes.Contains(data, []byte("@example.com")) {
				errs <- fmt.Errorf("worker %d status=%d read_error=%v or unexpected synthetic result", i, resp.StatusCode, err)
			}
		}(i)
	}
	close(start)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(120 * time.Second):
		t.Fatal("governance PII load timed out")
	}
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if primaryCalls.Load() != workers || fallbackCalls.Load() != workers || leaks.Load() != 0 {
		t.Fatalf("primary=%d fallback=%d unredacted_or_missing_placeholder=%d", primaryCalls.Load(), fallbackCalls.Load(), leaks.Load())
	}
	t.Logf("workers=%d streaming=%d unary=%d primary=%d fallback=%d leakage_or_missing_placeholder=%d", workers, workers/2, workers/2, primaryCalls.Load(), fallbackCalls.Load(), leaks.Load())
}

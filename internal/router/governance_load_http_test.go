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
	"testing"
	"time"
)

// This is a bounded single-router HTTP test with synthetic provider usage.
// It is not a bound for arbitrary upstream usage or distributed deployments.
func TestGovernanceLoadHTTPTokenReservations(t *testing.T) {
	for _, workers := range []int{8, 32, 128} {
		for _, dimension := range []string{"day", "month", "lifetime", "tpm", "rpm", "concurrent"} {
			t.Run(fmt.Sprintf("%s/concurrency-%d", dimension, workers), func(t *testing.T) {
				const actual = 20 // synthetic provider: 12 input + 8 output tokens
				acceptedLimit := workers / 4
				body := func(stream bool) []byte {
					b, err := json.Marshal(map[string]any{
						"model": "default", "max_tokens": 8, "stream": stream,
						"messages": []any{map[string]any{"role": "user", "content": "hello"}},
					})
					if err != nil {
						t.Fatal(err)
					}
					return b
				}
				unary, streaming := body(false), body(true)
				ir, err := decodeRequest("openai-chat", unary, http.Header{})
				if err != nil {
					t.Fatal(err)
				}
				estimate := reservationEstimate(ir, "openai-chat")
				otherIR, err := decodeRequest("openai-chat", streaming, http.Header{})
				if err != nil || reservationEstimate(otherIR, "openai-chat") != estimate || estimate <= 0 || estimate >= actual {
					t.Fatalf("unexpected test estimates: unary=%d streaming_err=%v", estimate, err)
				}
				budget := acceptedLimit * estimate
				arrivals := make(chan bool, workers)
				release := make(chan struct{})
				var releaseOnce sync.Once
				unblock := func() { releaseOnce.Do(func() { close(release) }) }
				defer unblock()
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var req map[string]any
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						http.Error(w, "invalid synthetic request", http.StatusBadRequest)
						return
					}
					stream, _ := req["stream"].(bool)
					arrivals <- stream
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"mock\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
						fmt.Fprint(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"mock\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":8,\"total_tokens\":20}}\n\ndata: [DONE]\n\n")
						return
					}
					writeJSON(w, http.StatusOK, map[string]any{
						"id": "fixture", "object": "chat.completion", "model": "mock",
						"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
						"usage":   map[string]any{"prompt_tokens": 12, "completion_tokens": 8, "total_tokens": actual},
					})
				}))
				defer upstream.Close()
				cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
				cfg.Server.Cache.Enabled = false
				caller := &cfg.Callers[0]
				caller.Rate = RateConfig{RPM: workers + 10, Concurrent: workers + 10}
				caller.Quota = QuotaConfig{}
				caller.Key = KeyConfig{}
				expectedStatus, expectedReason := http.StatusTooManyRequests, "quota-exhausted"
				switch dimension {
				case "day":
					caller.Quota.Day.Tokens = int64(budget)
				case "month":
					caller.Quota.Month.Tokens = int64(budget)
				case "lifetime":
					caller.Key.LifetimeTokens = int64(budget)
					expectedStatus, expectedReason = http.StatusForbidden, "key-exhausted"
				case "tpm":
					caller.Rate.TPM = budget
					expectedReason = "tpm-exceeded"
				case "rpm":
					caller.Rate.RPM = acceptedLimit
					expectedReason = "rpm-exceeded"
				case "concurrent":
					caller.Rate.Concurrent = acceptedLimit
					expectedReason = "concurrency-exceeded"
				}
				svc, err := New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer svc.Close()
				server := httptest.NewServer(svc.Handler())
				defer server.Close()
				client := &http.Client{Timeout: 60 * time.Second}
				defer client.CloseIdleConnections()
				type result struct {
					status     int
					body       []byte
					retryAfter string
					err        error
				}
				results := make(chan result, workers)
				request := func(b []byte) {
					req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", bytes.NewReader(b))
					if err != nil {
						results <- result{err: err}
						return
					}
					req.Header.Set("Authorization", "Bearer "+testToken)
					req.Header.Set("Content-Type", "application/json")
					resp, err := client.Do(req)
					if err != nil {
						results <- result{err: err}
						return
					}
					defer resp.Body.Close()
					data, err := io.ReadAll(resp.Body)
					results <- result{status: resp.StatusCode, body: data, retryAfter: resp.Header.Get("Retry-After"), err: err}
				}
				// An invalid caller token must be rejected without reaching upstream.
				badReq, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", bytes.NewReader(unary))
				if err != nil {
					t.Fatal(err)
				}
				badReq.Header.Set("Authorization", "Bearer invalid-synthetic-token")
				badReq.Header.Set("Content-Type", "application/json")
				badClient := &http.Client{Timeout: 5 * time.Second}
				badResp, err := badClient.Do(badReq)
				if err != nil {
					unblock()
					t.Fatalf("invalid-caller request: %v", err)
				}
				_, _ = io.Copy(io.Discard, badResp.Body)
				badResp.Body.Close()
				if badResp.StatusCode != http.StatusUnauthorized {
					unblock()
					t.Fatalf("invalid caller status=%d, want 401", badResp.StatusCode)
				}
				select {
				case <-arrivals:
					unblock()
					t.Fatal("invalid caller reached upstream")
				default:
				}

				// Ensure both response modes are represented among held admissions.
				go request(streaming)
				go request(unary)
				seenStream, seenUnary := false, false
				for i := 0; i < 2; i++ {
					select {
					case stream := <-arrivals:
						seenStream = seenStream || stream
						seenUnary = seenUnary || !stream
					case <-time.After(30 * time.Second):
						unblock()
						t.Fatal("initial mixed admissions did not reach upstream")
					}
				}
				if !seenStream || !seenUnary {
					unblock()
					t.Fatal("fixture did not admit both stream modes")
				}
				start := make(chan struct{})
				for i := 2; i < workers; i++ {
					b := unary
					if i%2 == 0 {
						b = streaming
					}
					go func(b []byte) { <-start; request(b) }(b)
				}
				close(start)
				arrived, rejected := 2, 0
				deadline := time.After(60 * time.Second)
				for arrived+rejected < workers {
					select {
					case <-arrivals:
						arrived++
					case r := <-results:
						if r.err != nil || r.status != expectedStatus || !bytes.Contains(r.body, []byte(expectedReason)) {
							unblock()
							t.Fatalf("unexpected pre-release rejection: status=%d err=%v body=%s", r.status, r.err, r.body)
						}
						if (dimension == "rpm" && r.retryAfter != "60") || (dimension == "concurrent" && r.retryAfter != "1") {
							unblock()
							t.Fatalf("dimension=%s Retry-After=%q", dimension, r.retryAfter)
						}
						rejected++
					case <-deadline:
						unblock()
						t.Fatalf("admission timed out: arrived=%d rejected=%d", arrived, rejected)
					}
				}
				if arrived != acceptedLimit || rejected != workers-acceptedLimit {
					unblock()
					t.Fatalf("arrived=%d rejected=%d; want %d/%d", arrived, rejected, acceptedLimit, workers-acceptedLimit)
				}
				unblock()
				for i := 0; i < arrived; i++ {
					select {
					case r := <-results:
						if r.err != nil || r.status != http.StatusOK || !bytes.Contains(r.body, []byte("ok")) {
							t.Errorf("accepted response: status=%d err=%v, missing synthetic content=%t", r.status, r.err, !bytes.Contains(r.body, []byte("ok")))
						}
					case <-time.After(60 * time.Second):
						t.Fatal("accepted response timed out")
					}
				}
				// Wait for the router's deferred accounting and concurrency release.
				want := int64(acceptedLimit * actual)
				stop := time.Now().Add(10 * time.Second)
				for {
					svc.quota.mu.Lock()
					state := svc.quota.state.Callers[caller.ID]
					runtime := svc.quota.callers[caller.ID]
					matched := state != nil && state.DayTokens == want && state.MonthTokens == want && state.LifetimeTokens == want && runtime.inFlight == 0 && runtime.inFlightReservedTokens == 0
					svc.quota.mu.Unlock()
					if matched {
						break
					}
					if time.Now().After(stop) {
						t.Fatal("accounting or concurrency did not settle")
					}
					time.Sleep(10 * time.Millisecond)
				}
				if dimension == "concurrent" {
					// Completion releases the in-flight slot; a fresh request can enter.
					request(unary)
					r := <-results
					if r.err != nil || r.status != http.StatusOK || !bytes.Contains(r.body, []byte("ok")) {
						t.Fatalf("request after concurrency release: status=%d err=%v", r.status, r.err)
					}
				}
				if dimension == "rpm" || dimension == "concurrent" {
					t.Logf("dimension=%s workers=%d limit=%d admitted=%d rejected=%d settled_tokens=%d", dimension, workers, acceptedLimit, arrived, rejected, want)
				} else {
					t.Logf("dimension=%s workers=%d admitted=%d rejected=%d reservation=%d actual=%d quota=%d settled=%d overshoot=%d", dimension, workers, arrived, rejected, estimate, actual, budget, want, want-int64(budget))
				}
			})
		}
	}
}

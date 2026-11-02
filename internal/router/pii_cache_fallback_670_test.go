// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPIICacheFallbackAndMatch670(t *testing.T) {
	for _, mode := range []string{"redact_only", "fail_on_match"} {
		t.Run(mode, func(t *testing.T) {
			var failed, served atomic.Int32
			var raw atomic.Bool
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { failed.Add(1); w.WriteHeader(500) }))
			defer first.Close()
			second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				served.Add(1)
				data, _ := io.ReadAll(r.Body)
				if strings.Contains(string(data), "alice@example.com") {
					raw.Store(true)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"r","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
			}))
			defer second.Close()
			svc := nativeStreamTestService(t, first.URL, "openai-responses", []Target{{Provider: "native", Model: "first"}, {Provider: "second", Model: "second"}}, func(cfg *Config) {
				cfg.Provider["second"] = ProviderConfig{BaseURL: second.URL, Dialect: "openai-responses", APIKey: "provider-key"}
				cfg.Server.Cache = CacheConfig{Enabled: true, MaxBytes: 1 << 20, DefaultTTL: time.Minute}
				g := cfg.Models["native-stream"]
				g.PIIFilter = testPIIFilterConfig(mode)
				cfg.Models["native-stream"] = g
			})
			for i := 0; i < 2; i++ {
				r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"native-stream","temperature":0,"input":"alice@example.com"}`))
				r.Header.Set("Authorization", "Bearer "+testToken)
				w := httptest.NewRecorder()
				svc.Handler().ServeHTTP(w, r)
				if mode == "redact_only" && w.Code != 200 {
					t.Fatalf("redact status=%d body=%s", w.Code, w.Body.String())
				}
				if mode == "fail_on_match" && w.Code != 400 {
					t.Fatalf("fail status=%d body=%s", w.Code, w.Body.String())
				}
			}
			if mode == "fail_on_match" && (failed.Load() != 0 || served.Load() != 0) {
				t.Fatal("fail_on_match dispatched upstream")
			}
			if mode == "redact_only" && (failed.Load() == 0 || served.Load() != 2 || raw.Load()) {
				t.Fatalf("fallback=%d served=%d raw=%t", failed.Load(), served.Load(), raw.Load())
			}
			if stats := svc.cache.Stats(); !stats.Enabled || stats.Items != 0 {
				t.Fatalf("cache populated: %+v", stats)
			}
		})
	}
}

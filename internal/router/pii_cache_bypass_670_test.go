// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPIICacheBypassExistingAndDisabledControl670(t *testing.T) {
	for _, mode := range []string{"disabled", "redact_only", "fail_on_match"} {
		t.Run(mode, func(t *testing.T) {
			enabled := mode != "disabled"
			var hits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"id":"resp_%d","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fresh-%d"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`, n, n)
			}))
			defer upstream.Close()
			target := Target{Provider: "native", Model: "model"}
			svc := nativeStreamTestService(t, upstream.URL, "openai-responses", []Target{target}, func(cfg *Config) {
				cfg.Server.Cache = CacheConfig{Enabled: true, MaxBytes: 1 << 20, DefaultTTL: time.Minute}
				if enabled {
					g := cfg.Models["native-stream"]
					g.PIIFilter = testPIIFilterConfig(mode)
					cfg.Models["native-stream"] = g
				}
			})
			body := `{"model":"native-stream","temperature":0,"input":"hello"}`
			req, err := decodeRequest("openai-responses", []byte(body), http.Header{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := applyPIIFilter(req, svc.cfg.Models["native-stream"].PIIFilter); err != nil {
				t.Fatal(err)
			}
			caller := svc.cfg.Callers[0]
			key := cacheKey(req, target, caller.ID, callerProject(caller))
			svc.cache.Put(key, &IRResponse{Text: "stale-answer", Model: "model"})
			if _, ok := svc.cache.Get(key); !ok {
				t.Fatal("failed to seed existing cache entry")
			}
			for i := 0; i < 2; i++ {
				r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+testToken)
				w := httptest.NewRecorder()
				svc.Handler().ServeHTTP(w, r)
				if w.Code != 200 || strings.Contains(w.Body.String(), "stale-answer") == enabled {
					t.Fatalf("status=%d pii=%t body=%s", w.Code, enabled, w.Body.String())
				}
			}
			want := int32(0)
			if enabled {
				want = 2
			}
			if hits.Load() != want {
				t.Fatalf("hits=%d want=%d", hits.Load(), want)
			}
			cached, ok := svc.cache.Get(key)
			if !ok || cached.Text != "stale-answer" || svc.cache.Stats().Items != 1 {
				t.Fatalf("cache entry was modified: %#v", cached)
			}
		})
	}
}

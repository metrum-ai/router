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
)

func TestSystemPIIMixedTargets668(t *testing.T) {
	for _, nativeFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("native_fails=%t", nativeFails), func(t *testing.T) {
			var anthHits, nativeHits atomic.Int32
			unblock := make(chan struct{})
			anth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.UserAgent(), "metrum-ai-router-shadow/") {
					<-unblock
				}
				anthHits.Add(1)
				w.WriteHeader(500)
			}))
			defer anth.Close()
			defer close(unblock)
			native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nativeHits.Add(1)
				if nativeFails {
					w.WriteHeader(500)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"resp_668","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
			}))
			defer native.Close()
			svc := nativeStreamTestService(t, anth.URL, "anthropic", []Target{anthropicToolTarget(), {Provider: "oai", Model: "native-model"}}, func(cfg *Config) {
				cfg.Provider["oai"] = ProviderConfig{BaseURL: native.URL, Dialect: "openai-responses", APIKey: "provider-key"}
				group := cfg.Models["native-stream"]
				group.PIIFilter = testPIIFilterConfig("redact_only")
				group.PIIFilter.Rules = []PIIFilterRule{{Name: "secret", Expression: "secret", PlaceholderPrefix: "SECRET"}}
				cfg.Models["native-stream"] = group
			})
			runs := map[string]*shadowRunState{"native-stream": {runID: "test", group: "native-stream", provider: "mock", model: "mock-model", rate: 1}}
			svc.shadows.runs.Store(&runs)
			body := `{"model":"native-stream","input":[{"role":"system","content":[{"type":"input_text","text":"sec"},{"type":"input_text","text":"ret"}]},{"role":"user","content":"hi"}]}`
			r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			if anthHits.Load() != 0 || nativeHits.Load() == 0 || len(svc.shadows.slots) != 0 {
				t.Fatalf("anthropic=%d native=%d shadow=%d status=%d", anthHits.Load(), nativeHits.Load(), len(svc.shadows.slots), w.Code)
			}
			if !nativeFails && w.Code != 200 {
				t.Fatalf("native route status=%d body=%s", w.Code, w.Body.String())
			}
			if nativeFails && w.Code == 200 {
				t.Fatal("native failure must not silently use excluded target")
			}
		})
	}
}

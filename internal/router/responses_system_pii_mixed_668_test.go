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
)

// A PII-enabled group with a Responses system history now offers Messages
// targets, because the assembled instruction is filtered at its transmitted
// boundary (#645). The Anthropic attempt must carry placeholders only, and a
// failure there must fail over to the native target without shadow replay.
func TestSystemPIIMixedTargets668(t *testing.T) {
	for _, nativeFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("native_fails=%t", nativeFails), func(t *testing.T) {
			var anthHits, nativeHits atomic.Int32
			var anthBody atomic.Value
			anth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, _ := io.ReadAll(r.Body)
				anthBody.Store(string(data))
				anthHits.Add(1)
				w.WriteHeader(500)
			}))
			defer anth.Close()
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
				piiInstructionGroup(cfg, "redact_only", PIIFilterRule{Name: "secret", Expression: "secret", PlaceholderPrefix: "SECRET"})
			})
			runs := map[string]*shadowRunState{"native-stream": {runID: "test", group: "native-stream", provider: "mock", model: "mock-model", rate: 1}}
			svc.shadows.runs.Store(&runs)
			body := `{"model":"native-stream","input":[{"role":"system","content":[{"type":"input_text","text":"sec"},{"type":"input_text","text":"ret"}]},{"role":"user","content":"hi"}]}`
			r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			if anthHits.Load() == 0 {
				t.Fatalf("filtered system history must be eligible for a Messages target: status=%d body=%s", w.Code, w.Body.String())
			}
			sent, _ := anthBody.Load().(string)
			if strings.Contains(sent, "secret") {
				t.Fatalf("assembled instruction reached the Messages target unfiltered: %s", sent)
			}
			if !strings.Contains(sent, "[SECRET_1]") {
				t.Fatalf("Messages target did not receive the placeholder: %s", sent)
			}
			if nativeHits.Load() == 0 {
				t.Fatal("failover to the native target did not happen")
			}
			if len(svc.shadows.slots) != 0 {
				t.Fatalf("PII-enabled request was shadow replayed: %d", len(svc.shadows.slots))
			}
			if !nativeFails && w.Code != http.StatusOK {
				t.Fatalf("native route status=%d body=%s", w.Code, w.Body.String())
			}
			if nativeFails && w.Code == http.StatusOK {
				t.Fatal("native failure must not report success")
			}
		})
	}
}

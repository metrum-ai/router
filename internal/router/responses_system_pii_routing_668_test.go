// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSystemPIITransportAndShape668(t *testing.T) {
	inputs := []struct {
		name, path, input string
		compat            bool
	}{
		{"explicit_message", "/v1/responses", `[{"type":"message","role":"system","content":"rule"},{"role":"user","content":"hi"}]`, false},
		{"late_system", "/v1/responses", `[{"role":"user","content":"hi"},{"role":"system","content":"rule"}]`, false},
		{"nested_object", "/v1/responses", `{"input":[{"role":"system","content":"rule"},{"role":"user","content":"hi"}]}`, false},
		{"non_message_role", "/v1/responses", `[{"type":"function_call","role":"system","content":"rule"},{"role":"user","content":"hi"}]`, false},
		{"chat_compat", "/v1/chat/completions", `[{"role":"system","content":"rule"},{"role":"user","content":"hi"}]`, true},
	}
	for _, transport := range []string{"anthropic", "override", "bedrock"} {
		for _, tc := range inputs {
			t.Run(transport+"/"+tc.name, func(t *testing.T) {
				var hits atomic.Int32
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(500) }))
				defer up.Close()
				target := anthropicToolTarget()
				dialect := "anthropic"
				if transport == "override" {
					dialect = "openai-chat"
					target.Dialect = "anthropic"
				}
				if transport == "bedrock" {
					dialect = "openai-chat"
					target.Dialect = ""
					target.ResponsesToChat = ResponsesToChatBridge{Enabled: true, Text: true}
				}
				svc := nativeStreamTestService(t, up.URL, dialect, []Target{target}, func(cfg *Config) {
					group := cfg.Models["native-stream"]
					group.PIIFilter = testPIIFilterConfig("redact_only")
					cfg.Models["native-stream"] = group
					cfg.Server.OpenAICompatibility.TolerateResponsesBodyOnChatEndpoint = tc.compat
					if transport == "bedrock" {
						p := cfg.Provider["native"]
						p.AuthScheme = "bedrock"
						cfg.Provider["native"] = p
					}
				})
				body := `{"model":"native-stream","input":` + tc.input + `}`
				r := httptest.NewRequest("POST", tc.path, strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+testToken)
				w := httptest.NewRecorder()
				svc.Handler().ServeHTTP(w, r)
				if w.Code == 200 || hits.Load() != 0 {
					t.Fatalf("unsafe dispatch status=%d hits=%d body=%s", w.Code, hits.Load(), w.Body.String())
				}
			})
		}
	}
}

func TestSystemPIIExternalPolicyCannotRestoreExcludedTarget668(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprintf("fallback=%t", fallback), func(t *testing.T) {
			var anthHits, nativeHits, policyHits atomic.Int32
			anth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { anthHits.Add(1); w.WriteHeader(500) }))
			defer anth.Close()
			native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { nativeHits.Add(1); w.WriteHeader(500) }))
			defer native.Close()
			policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				policyHits.Add(1)
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				targets, _ := payload["targets"].([]any)
				if len(targets) != 1 {
					t.Errorf("policy saw %d eligible targets, want 1", len(targets))
				}
				w.Header().Set("Content-Type", "application/json")
				if fallback {
					fmt.Fprint(w, `{"target":{"provider":"oai","model":"native-model"},"fallbacks":[{"provider":"native","model":"claude-model"}]}`)
				} else {
					fmt.Fprint(w, `{"target":{"provider":"native","model":"claude-model"}}`)
				}
			}))
			defer policy.Close()
			u, _ := url.Parse(policy.URL)
			anthTarget := anthropicToolTarget()
			anthTarget.Model = "claude-model"
			svc := nativeStreamTestService(t, anth.URL, "anthropic", []Target{anthTarget, {Provider: "oai", Model: "native-model"}}, func(cfg *Config) {
				cfg.Provider["oai"] = ProviderConfig{BaseURL: native.URL, Dialect: "openai-responses", APIKey: "provider-key"}
				group := cfg.Models["native-stream"]
				group.PIIFilter = testPIIFilterConfig("redact_only")
				group.Strategy = "external"
				group.ExternalPolicy = ExternalPolicyConfig{URL: policy.URL, AllowHosts: []string{u.Hostname()}, Mode: "enforce"}
				cfg.Models["native-stream"] = group
			})
			body := `{"model":"native-stream","input":[{"role":"system","content":"rule"},{"role":"user","content":"hi"}]}`
			r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			if policyHits.Load() != 1 || anthHits.Load() != 0 || nativeHits.Load() != 0 || w.Code == 200 {
				t.Fatalf("status=%d policy=%d anth=%d native=%d body=%s", w.Code, policyHits.Load(), anthHits.Load(), nativeHits.Load(), w.Body.String())
			}
		})
	}
}

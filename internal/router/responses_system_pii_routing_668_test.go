// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// The transport and shape matrix for a Responses system history on a
// PII-enabled group. Eligibility changed with #645: a leading system item now
// reaches Messages targets because the assembled instruction is filtered at its
// transmitted boundary, while shape-invalid histories and Messages targets that
// cannot carry the shape still refuse before any dispatch.
func TestSystemPIITransportAndShape668(t *testing.T) {
	inputs := []struct {
		name, path, input string
		compat            bool
		// dispatched reports whether the target may be called for this shape.
		dispatched map[string]bool
	}{
		{"explicit_message", "/v1/responses", `[{"type":"message","role":"system","content":"secret"},{"role":"user","content":"hi"}]`, false,
			map[string]bool{"anthropic": true, "override": true, "bedrock": false}},
		{"late_system", "/v1/responses", `[{"role":"user","content":"hi"},{"role":"system","content":"secret"}]`, false,
			map[string]bool{"anthropic": false, "override": false, "bedrock": false}},
		{"nested_object", "/v1/responses", `{"input":[{"role":"system","content":"secret"},{"role":"user","content":"hi"}]}`, false,
			map[string]bool{"anthropic": false, "override": false, "bedrock": false}},
		{"non_message_role", "/v1/responses", `[{"type":"function_call","role":"system","content":"secret"},{"role":"user","content":"hi"}]`, false,
			map[string]bool{"anthropic": false, "override": false, "bedrock": false}},
		{"chat_compat", "/v1/chat/completions", `[{"role":"system","content":"secret"},{"role":"user","content":"hi"}]`, true,
			map[string]bool{"anthropic": true, "override": true, "bedrock": false}},
	}
	for _, transport := range []string{"anthropic", "override", "bedrock"} {
		for _, tc := range inputs {
			t.Run(transport+"/"+tc.name, func(t *testing.T) {
				var hits atomic.Int32
				var sent atomic.Value
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					data, _ := io.ReadAll(r.Body)
					sent.Store(string(data))
					w.WriteHeader(500)
				}))
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
					piiInstructionGroup(cfg, "redact_only", PIIFilterRule{Name: "secret", Expression: "secret", PlaceholderPrefix: "SECRET"})
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
				if !tc.dispatched[transport] {
					if hits.Load() != 0 || w.Code == http.StatusOK {
						t.Fatalf("unsafe dispatch status=%d hits=%d body=%s", w.Code, hits.Load(), w.Body.String())
					}
					return
				}
				if hits.Load() == 0 {
					t.Fatalf("eligible shape was not dispatched: status=%d body=%s", w.Code, w.Body.String())
				}
				if payload, _ := sent.Load().(string); strings.Contains(payload, "secret") {
					t.Fatalf("instruction reached the target unfiltered: %s", payload)
				} else if !strings.Contains(payload, "[SECRET_1]") {
					t.Fatalf("target did not receive the placeholder: %s", payload)
				}
			})
		}
	}
}

// TestSystemPIIExternalPolicy668 keeps the external-policy contract: a policy
// now sees both targets for a filtered system history, it can select either, and
// neither the policy payload nor the selected upstream may carry matched
// cleartext (the router holds the only placeholder map and restores nothing).
func TestSystemPIIExternalPolicy668(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprintf("fallback=%t", fallback), func(t *testing.T) {
			var anthHits, nativeHits, policyHits atomic.Int32
			var policyCleartext, anthCleartext atomic.Bool
			anth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				anthHits.Add(1)
				data, _ := io.ReadAll(r.Body)
				if strings.Contains(string(data), "secret") {
					anthCleartext.Store(true)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"msg","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
			}))
			defer anth.Close()
			native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nativeHits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"resp","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
			}))
			defer native.Close()
			policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				policyHits.Add(1)
				data, _ := io.ReadAll(r.Body)
				if strings.Contains(string(data), "secret") {
					policyCleartext.Store(true)
				}
				var payload map[string]any
				if err := json.Unmarshal(data, &payload); err != nil {
					t.Error(err)
				}
				if targets, _ := payload["targets"].([]any); len(targets) != 2 {
					t.Errorf("policy saw %d eligible targets, want 2", len(targets))
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
				group.PIIFilter.Rules = []PIIFilterRule{{Name: "secret", Expression: "secret", PlaceholderPrefix: "SECRET"}}
				group.Strategy = "external"
				group.ExternalPolicy = ExternalPolicyConfig{URL: policy.URL, AllowHosts: []string{u.Hostname()}, Mode: "enforce"}
				cfg.Models["native-stream"] = group
			})
			body := `{"model":"native-stream","input":[{"role":"system","content":"secret"},{"role":"user","content":"hi"}]}`
			r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			if policyHits.Load() != 1 || w.Code != http.StatusOK {
				t.Fatalf("status=%d policy=%d body=%s", w.Code, policyHits.Load(), w.Body.String())
			}
			if policyCleartext.Load() {
				t.Fatal("external policy payload carried matched cleartext")
			}
			if anthCleartext.Load() {
				t.Fatal("policy-selected Messages target received matched cleartext")
			}
			if fallback && nativeHits.Load() == 0 {
				t.Fatal("policy-requested fallback was not served")
			}
			if !fallback && anthHits.Load() == 0 {
				t.Fatal("policy-selected Messages target was not called")
			}
		})
	}
}

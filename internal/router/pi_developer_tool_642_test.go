// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeveloperMultipartPIIAndCache642(t *testing.T) {
	for _, mode := range []string{"fail_on_match", "redact_only"} {
		t.Run(mode, func(t *testing.T) {
			var hits atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"msg","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
			}))
			defer up.Close()
			svc := nativeStreamTestService(t, up.URL, "anthropic", []Target{anthropicToolTarget()}, func(cfg *Config) {
				cfg.Server.Cache = CacheConfig{Enabled: true, MaxBytes: 1 << 20, DefaultTTL: time.Minute}
				group := cfg.Models["native-stream"]
				group.PIIFilter = testPIIFilterConfig(mode)
				group.PIIFilter.Rules = []PIIFilterRule{{Name: "secret", Expression: "secret", PlaceholderPrefix: "SECRET"}}
				cfg.Models["native-stream"] = group
			})
			for _, input := range []string{`[{"role":"developer","content":[{"type":"input_text","text":"secret"},{"type":"input_text","text":"extra"}]},{"role":"user","content":"hi"}]`, `[{"role":"developer","content":[{"type":"input_text","text":"sec"},{"type":"input_text","text":"ret"}]},{"role":"user","content":"hi"}]`, `[{"role":"developer","content":[{"type":"input_text","text":"a"},{"type":"input_text","text":"b\nc"}]},{"role":"user","content":"hi"}]`, `[{"role":"developer","content":[{"type":"input_text","text":"a\nb"},{"type":"input_text","text":"c"}]},{"role":"user","content":"hi"}]`} {
				for _, extra := range []string{"}", `,"tools":[{"type":"function","name":"write","parameters":{"type":"object"}}]}`} {
					body := `{"model":"native-stream","temperature":0,"input":` + input + extra
					r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
					r.Header.Set("Authorization", "Bearer "+testToken)
					w := httptest.NewRecorder()
					svc.Handler().ServeHTTP(w, r)
					var response struct {
						Error struct{ Type, Param, Message string } `json:"error"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 400 || hits.Load() != 0 || response.Error.Type != "invalid_request_error" || response.Error.Param != "input" || !strings.Contains(response.Error.Message, "single text part") {
						t.Fatalf("multipart precedence failed: status=%d hits=%d error=%#v", w.Code, hits.Load(), response.Error)
					}
				}
			}
			if stats := svc.cache.Stats(); !stats.Enabled || stats.Items != 0 {
				t.Fatalf("cache disabled or multipart populated it: %#v", stats)
			}
		})
	}
}

func TestDeveloperPIIMixedTargets642(t *testing.T) {
	var anthHits, nativeHits atomic.Int32
	anth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { anthHits.Add(1); w.WriteHeader(500) }))
	defer anth.Close()
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nativeHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp_642","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
	}))
	defer native.Close()
	targets := []Target{anthropicToolTarget(), {Provider: "oai", Model: "native-model"}}
	svc := nativeStreamTestService(t, anth.URL, "anthropic", targets, func(cfg *Config) {
		cfg.Provider["oai"] = ProviderConfig{BaseURL: native.URL, Dialect: "openai-responses", APIKey: "provider-key"}
		g := cfg.Models["native-stream"]
		g.PIIFilter = testPIIFilterConfig("redact_only")
		cfg.Models["native-stream"] = g
	})
	body := `{"model":"native-stream","input":[{"role":"developer","content":"safe"},{"role":"user","content":"hello"}]}`
	r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, r)
	if w.Code != 200 || anthHits.Load() != 0 || nativeHits.Load() != 1 {
		t.Fatalf("mixed route status=%d anthropic=%d native=%d", w.Code, anthHits.Load(), nativeHits.Load())
	}
}

func TestDeveloperPIIMixedLimitPrecedence642(t *testing.T) {
	for _, kind := range []string{"redaction-shrinks", "request-byte-limit"} {
		t.Run(kind, func(t *testing.T) {
			var anthHits, nativeHits atomic.Int32
			anth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { anthHits.Add(1); w.WriteHeader(500) }))
			defer anth.Close()
			native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nativeHits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"resp_642","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
			}))
			defer native.Close()
			nativeTarget := Target{Provider: "oai", Model: "native-model"}
			if kind == "redaction-shrinks" {
				nativeTarget.RequestShapeSupport.MaxEstimatedInputTokens = 100
			} else {
				nativeTarget.RequestShapeSupport.MaxRequestBytes = 80
			}
			svc := nativeStreamTestService(t, anth.URL, "anthropic", []Target{anthropicToolTarget(), nativeTarget}, func(cfg *Config) {
				cfg.Provider["oai"] = ProviderConfig{BaseURL: native.URL, Dialect: "openai-responses", APIKey: "provider-key"}
				g := cfg.Models["native-stream"]
				g.PIIFilter = testPIIFilterConfig("redact_only")
				g.PIIFilter.Rules = []PIIFilterRule{{Name: "secret", Expression: "X+", PlaceholderPrefix: "SECRET"}}
				if kind == "request-byte-limit" {
					g.PIIFilter.Mode = "fail_on_match"
				}
				cfg.Models["native-stream"] = g
			})
			var body string
			if kind == "redaction-shrinks" {
				body = `{"model":"native-stream","input":[{"role":"developer","content":"` + strings.Repeat("X", 1200) + `"},{"role":"user","content":"hi"}]}`
			} else {
				body = `{"model":"native-stream","input":[{"role":"developer","content":[{"type":"input_text","text":"XXXXX"},{"type":"input_text","text":"extra"}]},{"role":"user","content":"hi"}]}`
			}
			r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			if kind == "redaction-shrinks" {
				if w.Code != 200 || nativeHits.Load() != 1 || anthHits.Load() != 0 {
					t.Fatalf("redacted native route status=%d native=%d anth=%d body=%s", w.Code, nativeHits.Load(), anthHits.Load(), w.Body.String())
				}
			} else {
				var response struct {
					Error struct{ Type, Param, Message string } `json:"error"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 400 || nativeHits.Load() != 0 || anthHits.Load() != 0 || response.Error.Type != "invalid_request_error" || response.Error.Param != "input" || !strings.Contains(response.Error.Message, "single text part") {
					t.Fatalf("shape precedence status=%d error=%#v", w.Code, response.Error)
				}
			}
		})
	}
}

func TestDeveloperPIIFailClosed642(t *testing.T) {
	for _, mode := range []string{"fail_on_match", "redact_only"} {
		for _, selective := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/selective=%t", mode, selective), func(t *testing.T) {
				var hits atomic.Int32
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(500) }))
				defer up.Close()
				svc := nativeStreamTestService(t, up.URL, "anthropic", []Target{anthropicToolTarget()}, func(cfg *Config) {
					group := cfg.Models["native-stream"]
					group.PIIFilter = testPIIFilterConfig(mode)
					group.PIIFilter.Rules = []PIIFilterRule{{Name: "secret", Expression: "secret", PlaceholderPrefix: "SECRET"}}
					if selective {
						yes, no := true, false
						group.PIIFilter.ApplyTo = PIIFilterApplyTo{ResponsesInput: &yes, Messages: &no, System: &no}
					}
					cfg.Models["native-stream"] = group
				})
				for _, input := range []string{`[{"role":"developer","content":"secret"},{"role":"user","content":"hi"}]`, `[{"role":"developer","content":[{"type":"input_text","text":"safe"}]},{"role":"user","content":"hi"}]`, `[{"role":"system","content":[{"type":"input_text","text":"sec"},{"type":"input_text","text":"ret"}]},{"role":"developer","content":"safe"},{"role":"user","content":"hi"}]`, `[{"role":"developer","content":"123"},{"role":"developer","content":"45 6789"},{"role":"user","content":"hi"}]`} {
					for _, extra := range []string{"}", `,"tools":[{"type":"function","name":"write","parameters":{"type":"object"}}]}`} {
						r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"native-stream","input":`+input+extra))
						r.Header.Set("Authorization", "Bearer "+testToken)
						w := httptest.NewRecorder()
						svc.Handler().ServeHTTP(w, r)
						var response struct {
							Error struct{ Type, Param, Message string } `json:"error"`
						}
						if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 400 || hits.Load() != 0 || response.Error.Type != "invalid_request_error" || response.Error.Param != "input" || !strings.Contains(response.Error.Message, "not supported with PII filtering") {
							t.Fatalf("PII route did not fail closed: status=%d hits=%d error=%#v", w.Code, hits.Load(), response.Error)
						}
					}
				}
				late := `{"model":"native-stream","input":[{"role":"user","content":"secret"},{"role":"developer","content":"late"}]}`
				r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(late))
				r.Header.Set("Authorization", "Bearer "+testToken)
				w := httptest.NewRecorder()
				svc.Handler().ServeHTTP(w, r)
				var response struct {
					Error struct{ Type, Param, Code string } `json:"error"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 400 || response.Error.Type != "invalid_request_error" || response.Error.Param != "input" || response.Error.Code == "developer_pii_unsupported" || response.Error.Code == "pii-filter-blocked" || hits.Load() != 0 {
					t.Fatalf("late shape precedence: status=%d error=%#v", w.Code, response.Error)
				}
			})
		}
	}
}

func TestDeveloperToAnthropicTools642(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	var seen map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits++
		if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg_642","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
	}))
	defer up.Close()
	svc := nativeStreamTestService(t, up.URL, "anthropic", []Target{anthropicToolTarget()})
	post := func(path, body string) (*httptest.ResponseRecorder, int, any) {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		mu.Lock()
		defer mu.Unlock()
		return w, hits, seen["system"]
	}
	tools := `,"tools":[{"type":"function","name":"write","parameters":{"type":"object"}}]}`
	prefix := `{"model":"native-stream","instructions":"base","input":`
	good := `[{"role":"system","content":"first"},{"role":"developer","content":[{"type":"input_text","text":"devrules"}]},{"role":"system","content":"last"},{"role":"user","content":"hi"}]`
	w, count, system := post("/v1/responses", prefix+good+tools)
	if w.Code != 200 || count != 1 || system != "base\n\nfirst\n\ndevrules\n\nlast" {
		t.Fatalf("leading role failed: status=%d hits=%d system=%v", w.Code, count, system)
	}
	for _, input := range []string{`[{"role":"user","content":"hi"},{"role":"developer","content":"late"}]`, `[{"type":"reasoning","id":"rs_foreign","summary":[]},{"role":"developer","content":"late"},{"role":"user","content":"hi"}]`, `[{"role":"developer","content":"first"},{"type":"reasoning","id":"rs_foreign","summary":[]},{"role":"system","content":"late"},{"role":"user","content":"hi"}]`, `[{"role":"developer","content":"no conversation"}]`, `[{"role":"developer","content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]},{"role":"user","content":"hi"}]`, `[{"role":"developer","content":[{"type":"input_text","text":"x","unsupported":true}]},{"role":"user","content":"hi"}]`} {
		w, count, _ = post("/v1/responses", prefix+input+tools)
		if w.Code != 400 || count != 1 {
			t.Fatalf("unsafe developer dispatched status=%d hits=%d", w.Code, count)
		}
	}
	for _, input := range []string{`[{"role":"developer","content":[{"type":"input_text","text":"sec"},{"type":"input_text","text":"ret"}]},{"role":"user","content":"hi"}]`, `[{"role":"developer","content":[{"type":"input_text","text":"a"},{"type":"input_text","text":"b\nc"}]},{"role":"user","content":"hi"}]`, `[{"role":"developer","content":[{"type":"input_text","text":"a\nb"},{"type":"input_text","text":"c"}]},{"role":"user","content":"hi"}]`} {
		for _, extra := range []string{"}", tools} {
			w, count, _ = post("/v1/responses", prefix+input+extra)
			var response struct {
				Error struct{ Type, Code, Param, Message string } `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 400 || count != 1 || response.Error.Type != "invalid_request_error" || response.Error.Param != "input" || !strings.Contains(response.Error.Message, "single text part") {
				t.Fatalf("multipart not safely rejected: status=%d hits=%d error=%#v", w.Code, count, response.Error)
			}
		}
	}
	// Chat already folds system/developer into req.System; the fix must not alter it.
	chat := `{"model":"native-stream","messages":[{"role":"system","content":"first"},{"role":"developer","content":"rules"},{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"write","parameters":{"type":"object"}}}]}`
	w, count, system = post("/v1/chat/completions", chat)
	if w.Code != 200 || count != 2 || system != "first\nrules" {
		t.Fatalf("Chat developer regression: status=%d hits=%d system=%v", w.Code, count, system)
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRoutingError655NoAbsentFieldBlame(t *testing.T) {
	target := anthropicBridgeTarget()
	target.Reasoning = ReasoningSupport{Supported: true, DefaultOn: true, Mode: reasoningModeAlwaysOn, Control: reasoningControlEffortEnum}
	cases := []struct {
		name, upstream, path, body string
		target                     Target
		param                      any
	}{
		{"tool-history-without-tools", "anthropic", "/v1/responses", `{"model":"native-stream","stream":true,"input":[{"role":"user","content":"hi"},{"type":"function_call","call_id":"c1","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"}]}`, Target{Provider: "native", Model: "up"}, "input"},
		{"unrequested-default-reasoning", "openai-chat", "/anthropic/v1/messages", `{"model":"native-stream","stream":true,"max_tokens":128,"messages":[{"role":"user","content":"hi"}]}`, target, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
			defer up.Close()
			svc := nativeStreamTestService(t, up.URL, tc.upstream, []Target{tc.target})
			r := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer "+testToken)
			r.Header.Set("x-api-key", testToken)
			r.Header.Set("anthropic-version", "2023-06-01")
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			var got struct {
				Error struct {
					Code  string
					Param any
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if w.Code != 400 || hits.Load() != 0 || got.Error.Code != "unsupported_capability" || got.Error.Param != tc.param {
				t.Fatalf("status=%d hits=%d body=%s", w.Code, hits.Load(), w.Body.String())
			}
		})
	}
}

func TestRoutingError655TranslatedFunctionOutput(t *testing.T) {
	for _, tc := range []struct {
		dialect string
		target  Target
	}{
		{"anthropic", anthropicToolTarget()},
		{"openai-chat", func() Target {
			target := anthropicToolTarget()
			target.ResponsesToChat = ResponsesToChatBridge{Enabled: true, Text: true, FunctionTools: true, ToolChoice: true, Streaming: true, ValidationStatus: "passed"}
			return target
		}()},
	} {
		t.Run(tc.dialect, func(t *testing.T) {
			var hits atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
			defer up.Close()
			svc := nativeStreamTestService(t, up.URL, tc.dialect, []Target{tc.target})
			body := `{"model":"native-stream","input":[{"role":"user","content":"hi"},{"type":"function_call_output","call_id":"c1","output":[{"type":"input_text","text":"x","future_metadata":"unsupported"}]}]}`
			r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			var got struct {
				Error struct {
					Code  string
					Param any
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if w.Code != 400 || hits.Load() != 0 || got.Error.Code != "unsupported_function_call_output" || got.Error.Param != "input" {
				t.Fatalf("status=%d hits=%d body=%s", w.Code, hits.Load(), w.Body.String())
			}
		})
	}
}

func TestRoutingError655HTTPDialects(t *testing.T) {
	no := false
	for _, tc := range []struct{ name, dialect, path, body, input, cap string }{
		{"chat", "openai-chat", "/v1/chat/completions", `{"model":"native-stream","messages":[{"role":"user","content":"hi"}],"max_tokens":256,"stream":true}`, "messages", "max_tokens"},
		{"responses", "openai-responses", "/v1/responses", `{"model":"native-stream","input":"hi","max_output_tokens":256,"stream":true}`, "input", "max_output_tokens"},
		{"messages", "anthropic", "/anthropic/v1/messages", `{"model":"native-stream","messages":[{"role":"user","content":"hi"}],"max_tokens":256,"stream":true}`, "messages", "max_tokens"},
	} {
		for _, caseName := range []string{"output-cap", "context", "bytes", "stream"} {
			t.Run(tc.name+"/"+caseName, func(t *testing.T) {
				target := Target{Provider: "native", Model: "private-upstream-model"}
				code, param := "unsupported_capability", any(nil)
				switch caseName {
				case "output-cap":
					target.HonorsMaxTokens = &no
					param = tc.cap
				case "context":
					target.ContextTokens = 1
					code = "context_length_exceeded"
					param = tc.input
				case "bytes":
					target.RequestShapeSupport.MaxRequestBytes = 1
				case "stream":
					target.RequestShapeSupport.UnsupportedRequestFeatures = []string{"stream"}
					param = "stream"
				}
				var hits atomic.Int32
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
				defer up.Close()
				svc := nativeStreamTestService(t, up.URL, tc.dialect, []Target{target})
				r := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
				r.Header.Set("Authorization", "Bearer "+testToken)
				r.Header.Set("x-api-key", testToken)
				r.Header.Set("anthropic-version", "2023-06-01")
				w := httptest.NewRecorder()
				svc.Handler().ServeHTTP(w, r)
				var got struct {
					Type  string `json:"type"`
					Error struct {
						Type, Code string
						Param      any
					} `json:"error"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if w.Code != 400 || hits.Load() != 0 || got.Error.Type != "invalid_request_error" || got.Error.Code != code || got.Error.Param != param || strings.Contains(w.Body.String(), "private-upstream-model") {
					t.Fatalf("status=%d hits=%d error=%#v body=%s", w.Code, hits.Load(), got.Error, w.Body.String())
				}
				if tc.dialect == "anthropic" && got.Type != "error" {
					t.Fatalf("not Anthropic envelope: %s", w.Body.String())
				}
			})
		}
	}
}

func TestRoutingError655MixedTargetsNoGuess(t *testing.T) {
	no := false
	a := Target{Provider: "native", Model: "sensitive-upstream-one", HonorsMaxTokens: &no, ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}}
	b := Target{Provider: "native", Model: "sensitive-upstream-two", ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}, RequestShapeSupport: RequestShapeSupport{UnsupportedRequestFeatures: []string{"stream_options"}}}
	body := `{"model":"native-stream","messages":[{"role":"user","content":"hi"}],"max_tokens":256,"stream":true,"stream_options":{"include_usage":true},"tool_choice":"auto","tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}]}`
	for _, targets := range [][]Target{{a, b}, {b, a}} {
		var hits atomic.Int32
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
		svc := nativeStreamTestService(t, up.URL, "openai-chat", targets)
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		up.Close()
		var got struct {
			Error struct {
				Code  string
				Param any
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if w.Code != 400 || hits.Load() != 0 || got.Error.Code != "unsupported_capability" || got.Error.Param != nil || strings.Contains(w.Body.String(), "sensitive-upstream") {
			t.Fatalf("mixed causes misreported: status=%d hits=%d body=%s", w.Code, hits.Load(), w.Body.String())
		}
	}
}

func TestRoutingError655ExistingCapabilities(t *testing.T) {
	cases := []struct {
		name, dialect, path, body, param string
		target                           Target
	}{
		{"tools", "openai-chat", "/v1/chat/completions", `{"model":"native-stream","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}]}`, "tools", Target{Provider: "native", Model: "up"}},
		{"structured", "openai-chat", "/v1/chat/completions", `{"model":"native-stream","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema","json_schema":{"name":"schema","schema":{"type":"object"}}}}`, "response_format", Target{Provider: "native", Model: "up"}},
		{"reasoning", "openai-chat", "/v1/chat/completions", `{"model":"native-stream","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"low"}`, "reasoning", Target{Provider: "native", Model: "up"}},
		{"images", "openai-chat", "/v1/chat/completions", `{"model":"native-stream","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`, "messages", Target{Provider: "native", Model: "up"}},
		{"logprobs", "anthropic", "/v1/chat/completions", `{"model":"native-stream","messages":[{"role":"user","content":"hi"}],"logprobs":true}`, "logprobs", anthropicBridgeTarget()},
		{"audio", "openai-chat", "/v1/chat/completions", `{"model":"native-stream","messages":[{"role":"user","content":"hi"}],"modalities":["text","audio"]}`, "modalities", Target{Provider: "native", Model: "up", OutputModalities: []string{"text"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
			defer up.Close()
			svc := nativeStreamTestService(t, up.URL, tc.dialect, []Target{tc.target})
			r := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			var body struct {
				Error struct {
					Type, Code string
					Param      any
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			wantCode := "unsupported_capability"
			// Media admission rejects a group with no image-capable target
			// before routing eligibility; preserve its established code.
			if tc.name == "images" {
				wantCode = "unsupported_modality"
			}
			if w.Code != 400 || hits.Load() != 0 || body.Error.Type != "invalid_request_error" || body.Error.Code != wantCode || body.Error.Param != tc.param {
				t.Fatalf("status=%d hits=%d error=%#v", w.Code, hits.Load(), body.Error)
			}
		})
	}
}

// These cases reproduced #655 against main before changing routing behavior.
func TestRoutingError655ReportedCause(t *testing.T) {
	no := false
	cases := []struct {
		name, dialect, path, body, wantCode, wantParam string
		target                                         Target
	}{
		{"output-cap", "openai-chat", "/v1/chat/completions", `{"model":"native-stream","messages":[{"role":"user","content":"hi"}],"max_tokens":256,"stream":true,"tool_choice":"auto","tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}]}`, "unsupported_capability", "max_tokens", Target{Provider: "native", Model: "up", HonorsMaxTokens: &no, ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}}},
		{"context-limit", "openai-chat", "/v1/chat/completions", `{"model":"native-stream","messages":[{"role":"user","content":"test input"}],"stream":true}`, "context_length_exceeded", "messages", Target{Provider: "native", Model: "up", ContextTokens: 1}},
		{"unsupported-feature", "openai-chat", "/v1/chat/completions", `{"model":"native-stream","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true}}`, "unsupported_capability", "stream_options", Target{Provider: "native", Model: "up", RequestShapeSupport: RequestShapeSupport{UnsupportedRequestFeatures: []string{"stream_options"}}}},
		{"forced-tool-choice", "openai-responses", "/v1/responses", `{"model":"native-stream","input":"hi","stream":true,"tool_choice":{"type":"function","name":"read"},"tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}`, "unsupported_capability", "tool_choice", Target{Provider: "native", Model: "up", ToolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}}, RequestShapeSupport: RequestShapeSupport{UnsupportedRequestFeatures: []string{"forced_tool_choice"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(500) }))
			defer up.Close()
			svc := nativeStreamTestService(t, up.URL, tc.dialect, []Target{tc.target})
			r := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			var body struct {
				Error struct {
					Type, Code string
					Param      any
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			t.Logf("status=%d type=%s code=%s param=%v upstream=%d", w.Code, body.Error.Type, body.Error.Code, body.Error.Param, hits.Load())
			if w.Code != 400 || hits.Load() != 0 || body.Error.Type != "invalid_request_error" || body.Error.Code != tc.wantCode || body.Error.Param != tc.wantParam {
				t.Errorf("want status=400 type=invalid_request_error code=%s param=%s, no upstream", tc.wantCode, tc.wantParam)
			}
		})
	}
}

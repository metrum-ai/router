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

// These controls use local mock targets only. Messages currently uses the
// Anthropic envelope without the OpenAI code/param fields; after #655 merges
// its additional fields must be pinned here as well.
func TestRoutingBoundaryContract654(t *testing.T) {
	// Empty groups fail config validation. Mutate only this in-memory fixture to
	// exercise the defensive HTTP branch, not a deployable configuration.
	for _, tc := range []struct{ name, dialect, path, body string }{
		{"chat", "openai-chat", "/v1/chat/completions", `{"model":"native-stream","messages":[{"role":"user","content":"hi"}]}`},
		{"responses", "openai-responses", "/v1/responses", `{"model":"native-stream","input":"hi"}`},
		{"messages", "anthropic", "/anthropic/v1/messages", `{"model":"native-stream","max_tokens":128,"messages":[{"role":"user","content":"hi"}]}`},
	} {
		t.Run("empty/"+tc.name, func(t *testing.T) {
			var hits atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
			defer up.Close()
			svc := nativeStreamTestService(t, up.URL, tc.dialect, []Target{{Provider: "native", Model: "up"}})
			svc.cfg.Models["native-stream"] = ModelGroup{Strategy: "static"}
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
			if w.Code != 502 || hits.Load() != 0 {
				t.Fatalf("status=%d hits=%d body=%s", w.Code, hits.Load(), w.Body.String())
			}
			if tc.dialect == "anthropic" {
				if got.Type != "error" || got.Error.Type != "api_error" {
					t.Fatalf("bad Messages error: %s", w.Body.String())
				}
			} else if got.Error.Code != "no-eligible-target" || got.Error.Param != nil {
				t.Fatalf("bad error: %s", w.Body.String())
			}
		})
	}
	t.Run("responses-continuation", func(t *testing.T) {
		var hits atomic.Int32
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
		defer up.Close()
		svc := nativeStreamTestService(t, up.URL, "openai-chat", []Target{chatBridgeTarget()})
		r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"native-stream","input":"hi","previous_response_id":"resp_prior"}`))
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		var got struct {
			Error struct {
				Type, Code string
				Param      any
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if w.Code != 400 || hits.Load() != 0 || got.Error.Type != "invalid_request_error" || got.Error.Code != "continuation_unsupported" || got.Error.Param != "previous_response_id" {
			t.Fatalf("status=%d hits=%d body=%s", w.Code, hits.Load(), w.Body.String())
		}
	})
}

func TestCapabilityErrorContract654(t *testing.T) {
	cases := []struct {
		name, dialect, path, body, code, param string
		target                                 Target
	}{
		{"chat-tools", "openai-chat", "/v1/chat/completions", `{"model":"native-stream","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}]}`, "unsupported_capability", "tools", Target{Provider: "native", Model: "up"}},
		{"responses-tools", "openai-responses", "/v1/responses", `{"model":"native-stream","input":"hi","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}`, "unsupported_capability", "tools", Target{Provider: "native", Model: "up"}},
		{"messages-tools", "anthropic", "/anthropic/v1/messages", `{"model":"native-stream","max_tokens":128,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"read","input_schema":{"type":"object"}}]}`, "unsupported_capability", "tools", Target{Provider: "native", Model: "up"}},
		{"chat-structured", "openai-chat", "/v1/chat/completions", `{"model":"native-stream","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema","json_schema":{"name":"a","schema":{"type":"object"}}}}`, "unsupported_capability", "response_format", Target{Provider: "native", Model: "up"}},
		{"responses-structured", "openai-responses", "/v1/responses", `{"model":"native-stream","input":"hi","text":{"format":{"type":"json_schema","name":"a","schema":{"type":"object"}}}}`, "unsupported_capability", "text.format", Target{Provider: "native", Model: "up"}},
		{"chat-reasoning", "openai-chat", "/v1/chat/completions", `{"model":"native-stream","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"low"}`, "unsupported_capability", "reasoning", Target{Provider: "native", Model: "up"}},
		{"responses-reasoning", "openai-responses", "/v1/responses", `{"model":"native-stream","input":"hi","reasoning":{"effort":"low"}}`, "unsupported_capability", "reasoning", Target{Provider: "native", Model: "up"}},
		{"messages-reasoning", "anthropic", "/anthropic/v1/messages", `{"model":"native-stream","max_tokens":2048,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":1024}}`, "unsupported_capability", "reasoning", Target{Provider: "native", Model: "up"}},
		{"chat-image", "openai-chat", "/v1/chat/completions", `{"model":"native-stream","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`, "unsupported_modality", "messages", Target{Provider: "native", Model: "up"}},
		{"responses-image", "openai-responses", "/v1/responses", `{"model":"native-stream","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`, "unsupported_modality", "input", Target{Provider: "native", Model: "up"}},
		{"messages-image", "anthropic", "/anthropic/v1/messages", `{"model":"native-stream","max_tokens":128,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}`, "unsupported_modality", "messages", Target{Provider: "native", Model: "up"}},
		{"chat-forced-choice", "openai-chat", "/v1/chat/completions", `{"model":"native-stream","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}],"tool_choice":{"type":"function","function":{"name":"read"}}}`, "unsupported_capability", "tool_choice", Target{Provider: "native", Model: "up", ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}, RequestShapeSupport: RequestShapeSupport{UnsupportedRequestFeatures: []string{"forced_tool_choice"}}}},
		{"responses-forced-choice", "openai-responses", "/v1/responses", `{"model":"native-stream","input":"hi","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}],"tool_choice":{"type":"function","name":"read"}}`, "unsupported_capability", "tool_choice", Target{Provider: "native", Model: "up", ToolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}}, RequestShapeSupport: RequestShapeSupport{UnsupportedRequestFeatures: []string{"forced_tool_choice"}}}},
		{"messages-forced-choice", "anthropic", "/anthropic/v1/messages", `{"model":"native-stream","max_tokens":128,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"read","input_schema":{"type":"object"}}],"tool_choice":{"type":"tool","name":"read"}}`, "unsupported_capability", "tool_choice", Target{Provider: "native", Model: "up", ToolSupport: ToolSupport{AnthropicMessages: []string{"client_tools", "tool_choice"}}, RequestShapeSupport: RequestShapeSupport{UnsupportedRequestFeatures: []string{"forced_tool_choice"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
			defer up.Close()
			svc := nativeStreamTestService(t, up.URL, tc.dialect, []Target{tc.target})
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
			if w.Code != 400 || hits.Load() != 0 || got.Error.Type != "invalid_request_error" {
				t.Fatalf("status=%d hits=%d body=%s", w.Code, hits.Load(), w.Body.String())
			}
			if tc.dialect == "anthropic" {
				if got.Type != "error" {
					t.Fatalf("wrong Messages envelope: %s", w.Body.String())
				}
			} else if got.Error.Code != tc.code || got.Error.Param != tc.param {
				t.Fatalf("wrong code/param: %s", w.Body.String())
			}
		})
	}
}

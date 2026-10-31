// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIAnthropicLogprobsCapabilityAndFallback(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsupported", true: "native-fallback"}[fallback], func(t *testing.T) {
			anthropicCalls, chatCalls := 0, 0
			anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				anthropicCalls++
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer anthropic.Close()
			chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				chatCalls++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["logprobs"] != true {
					t.Error("logprobs request was dropped")
				}
				writeJSON(w, http.StatusOK, map[string]any{"id": "chatcmpl_log", "object": "chat.completion", "model": "chat", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "ok"}, "logprobs": map[string]any{"content": []any{}}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
			}))
			defer chat.Close()
			targets := []Target{anthropicBridgeTarget()}
			if fallback {
				targets = append(targets, Target{Provider: "native-chat", Model: "chat"})
			}
			svc := nativeStreamTestService(t, anthropic.URL, "anthropic", targets, func(cfg *Config) {
				cfg.Provider["native-chat"] = ProviderConfig{BaseURL: chat.URL, Dialect: "openai-chat", APIKey: "provider-key"}
			})
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"native-stream","messages":[{"role":"user","content":"hi"}],"logprobs":true}`))
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			if anthropicCalls != 0 {
				t.Fatal("logprobs request reached unsupported Anthropic target")
			}
			if fallback {
				if w.Code != 200 || chatCalls != 1 || !strings.Contains(w.Body.String(), `"logprobs"`) {
					t.Fatalf("fallback: %d chat=%d %s", w.Code, chatCalls, w.Body.String())
				}
				return
			}
			var body struct {
				Error struct {
					Type  string `json:"type"`
					Code  string `json:"code"`
					Param string `json:"param"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != 400 || body.Error.Type != "invalid_request_error" || body.Error.Code != "unsupported_capability" || body.Error.Param != "logprobs" {
				t.Fatalf("unsupported error: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestOpenAIAnthropicStructuredLogprobsReportsMissingCapability(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()
	target := anthropicBridgeTarget()
	target.StructuredOutput = true
	svc := nativeStreamTestService(t, upstream.URL, "anthropic", []Target{target})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"native-stream","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_object"},"logprobs":true}`))
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, r)
	var body struct {
		Error struct{ Type, Code, Param, Message string } `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || w.Code != 400 || body.Error.Type != "invalid_request_error" || body.Error.Code != "unsupported_capability" || body.Error.Param != "logprobs" || !strings.Contains(body.Error.Message, "logprobs") {
		t.Fatalf("wrong missing capability: upstream=%d status=%d body=%s", calls, w.Code, w.Body.String())
	}
}

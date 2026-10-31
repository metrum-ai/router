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

func TestChatAudioOutputSelectsAudioTarget(t *testing.T) {
	var textHits, audioHits atomic.Int32
	text := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		textHits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer text.Close()
	audio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		audioHits.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"id": "chatcmpl_audio", "object": "chat.completion", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "audio": map[string]any{"id": "audio_1", "data": "d2F2"}}, "finish_reason": "stop"}}})
	}))
	defer audio.Close()
	targets := []Target{{Provider: "native", Model: "text", OutputModalities: []string{"text"}}, {Provider: "audio", Model: "audio", OutputModalities: []string{"text", "audio"}}}
	svc := nativeStreamTestService(t, text.URL, "openai-chat", targets, func(cfg *Config) {
		cfg.Provider["audio"] = ProviderConfig{BaseURL: audio.URL, Dialect: "openai-chat", APIKey: "provider-key"}
	})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"native-stream","messages":[{"role":"user","content":"hello"}],"modalities":["text","audio"],"audio":{"voice":"alloy","format":"wav"}}`))
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK || textHits.Load() != 0 || audioHits.Load() != 1 || !strings.Contains(w.Body.String(), `"audio_1"`) {
		t.Fatalf("status=%d text_hits=%d audio_hits=%d body=%s", w.Code, textHits.Load(), audioHits.Load(), w.Body.String())
	}
}

func TestChatAudioOutputIneligibleTargets(t *testing.T) {
	for _, tc := range []struct {
		name, dialect string
		targets       []Target
	}{
		{"tool-only-audio", "openai-chat", []Target{{Provider: "native", Model: "tool-audio", ToolOnly: true, OutputModalities: []string{"audio"}}, {Provider: "native", Model: "text", OutputModalities: []string{"text"}}}},
		{"responses-without-bridge", "openai-responses", []Target{{Provider: "native", Model: "responses", OutputModalities: []string{"text", "audio"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
			defer up.Close()
			svc := nativeStreamTestService(t, up.URL, tc.dialect, tc.targets)
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"native-stream","messages":[{"role":"user","content":"hello"}],"modalities":["text","audio"]}`))
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			if w.Code != 400 || hits.Load() != 0 || !strings.Contains(w.Body.String(), `"code":"unsupported_capability"`) || !strings.Contains(w.Body.String(), `"param":"modalities"`) {
				t.Fatalf("status=%d hits=%d body=%s", w.Code, hits.Load(), w.Body.String())
			}
		})
	}
}

func TestChatAudioOutputEligibility(t *testing.T) {
	for _, tc := range []struct {
		name, dialect, request string
		output                 []string
		status                 int
	}{
		{"native-no-audio", "openai-chat", `"modalities":["text","audio"]`, []string{"text"}, 400},
		{"native-stream-no-audio", "openai-chat", `"modalities":["text","audio"],"stream":true`, []string{"text"}, 400},
		{"native-audio-parameter", "openai-chat", `"audio":{"format":"wav","voice":"alloy"}`, []string{"text"}, 400},
		{"translated-no-audio", "anthropic", `"modalities":["audio"]`, []string{"text"}, 400},
		{"translated-declared-audio", "anthropic", `"modalities":["audio"]`, []string{"text", "audio"}, 400},
		{"native-declared-audio", "openai-chat", `"modalities":["text","audio"],"audio":{"format":"wav","voice":"alloy"}`, []string{"text", "audio"}, 200},
		{"native-default-text", "openai-chat", `"modalities":["text"]`, []string{"text"}, 200},
		{"null-audio-is-absent", "openai-chat", `"modalities":["text"],"audio":null`, []string{"text"}, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			var captured map[string]any
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
					t.Error(err)
				}
				writeJSON(w, 200, map[string]any{"id": "chatcmpl_audio", "object": "chat.completion", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "hello", "audio": map[string]any{"id": "audio_1", "data": "d2F2"}}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
			}))
			defer up.Close()
			target := Target{Provider: "native", Model: "fixture", OutputModalities: tc.output, RequestShapeSupport: RequestShapeSupport{ValidationStatus: "passed", SupportedInboundDialects: []string{"openai-chat", "anthropic"}}}
			svc := nativeStreamTestService(t, up.URL, tc.dialect, []Target{target})
			body := `{"model":"native-stream","messages":[{"role":"user","content":"hello"}],` + tc.request + `}`
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", rr.Code, tc.status, rr.Body.String())
			}
			if tc.status == 400 {
				if hits.Load() != 0 || !strings.Contains(rr.Body.String(), `"code":"unsupported_capability"`) || !strings.Contains(rr.Body.String(), `"param":"modalities"`) {
					t.Fatalf("wrong rejection or upstream called: hits=%d body=%s", hits.Load(), rr.Body.String())
				}
			} else if hits.Load() != 1 || (tc.name == "native-declared-audio" && (captured["audio"] == nil || !strings.Contains(rr.Body.String(), `"audio"`))) {
				t.Fatalf("native passthrough lost audio: hits=%d body=%s", hits.Load(), rr.Body.String())
			}
		})
	}
}

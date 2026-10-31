// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGeminiGenerateContentTextGoldens(t *testing.T) {
	req, err := decodeRequest("openai-chat", []byte(`{
		"model":"text-group",
		"messages":[
			{"role":"system","content":"Answer briefly."},
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"hi"},
			{"role":"user","content":"continue"}
		],
		"max_tokens":23,
		"temperature":0.25,
		"stop":["END"]
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encodeUpstreamForTarget("gemini-generate-content", "gemini-example", req, Target{})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": "Answer briefly."}}},
		"contents": []any{
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hello"}}},
			map[string]any{"role": "model", "parts": []any{map[string]any{"text": "hi"}}},
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "continue"}}},
		},
		"generationConfig": map[string]any{
			"maxOutputTokens": float64(23),
			"temperature":     0.25,
			"stopSequences":   []any{"END"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("encoded Gemini request mismatch\n got: %#v\nwant: %#v", got, want)
	}

	rawResponse := []byte(`{
		"candidates":[{
			"content":{"role":"model","parts":[{"text":"O"},{"text":"K"}]},
			"finishReason":"STOP"
		}],
		"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":2,"totalTokenCount":9}
	}`)
	resp, err := decodeUpstreamResponse("gemini-generate-content", rawResponse, "gemini-example")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "OK" || resp.StopReason != "stop" || resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 2 || resp.Usage.TotalTokens != 9 {
		t.Fatalf("decoded Gemini response mismatch: %#v", resp)
	}

	endpoint, err := upstreamEndpoint("https://generativelanguage.googleapis.com", "gemini-generate-content", Target{Model: "gemini-example"})
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "https://generativelanguage.googleapis.com/v1beta/models/gemini-example:generateContent" {
		t.Fatalf("endpoint=%q", endpoint)
	}

	partsOnly := &IRRequest{Messages: []IRMessage{{
		Role:  "user",
		Parts: []IRContentPart{{Type: "text", Text: "part one"}, {Type: "text", Text: "part two"}},
	}}}
	encoded, err = encodeUpstreamForTarget("gemini-generate-content", "gemini-example", partsOnly, Target{})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	contents := valueAsSlice(got["contents"])
	content, _ := contents[0].(map[string]any)
	if textFromGeminiParts(valueAsSlice(content["parts"])) != "part one\npart two" {
		t.Fatalf("parts-only Gemini request mismatch: %#v", got)
	}
}

func TestGeminiGenerateContentDispatchFailsClosed(t *testing.T) {
	req, err := decodeRequest("openai-chat", []byte(`{"model":"g","messages":[{"role":"user","content":"hello"}]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := encodeUpstreamForTarget("unknown-dialect", "model", req, Target{}); err == nil {
		t.Fatal("unknown encoder dialect did not fail closed")
	}
	if _, err := decodeUpstreamResponse("unknown-dialect", []byte(`{}`), "model"); err == nil {
		t.Fatal("unknown response dialect did not fail closed")
	}
	if _, err := upstreamEndpoint("https://example.test", "unknown-dialect", Target{Model: "model"}); err == nil {
		t.Fatal("unknown endpoint dialect did not fail closed")
	}
	for name, body := range map[string]string{
		"tools": `{"model":"g","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"x"}}]}`,
		"image": `{"model":"g","messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image_url","image_url":{"url":"https://example.test/a.png"}}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			shaped, err := decodeRequest("openai-chat", []byte(body), nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := encodeUpstreamForTarget("gemini-generate-content", "model", shaped, Target{}); err == nil {
				t.Fatal("unsupported Gemini request shape was encoded")
			}
		})
	}
}

func TestGeminiCatalogOnlyAllowedButActivationRequiresExactEvidence(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Provider["gemini"] = ProviderConfig{
		BaseURL:   "https://generativelanguage.googleapis.com",
		Dialect:   "gemini-generate-content",
		APIKeyEnv: "GEMINI_API_KEY",
		Models: map[string]ProviderModel{
			"text-candidate": {
				Model:            "gemini-example",
				InputModalities:  []string{"text"},
				OutputModalities: []string{"text"},
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("catalog-only Gemini model should validate: %v", err)
	}

	cfg.Models["gemini-active"] = ModelGroup{
		Strategy: "static",
		Targets:  []Target{{Provider: "gemini", ModelRef: "text-candidate"}},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "requires activation_evidence") {
		t.Fatalf("EA-2: missing evidence error=%v", err)
	}

	model := cfg.Provider["gemini"].Models["text-candidate"]
	model.ActivationEvidence = ActivationEvidence{
		ExactModel:       "gemini-example",
		Dialect:          "gemini-generate-content",
		DirectTextPassed: true,
		RouterTextPassed: false,
		ValidatedAt:      "2026-09-09",
	}
	provider := cfg.Provider["gemini"]
	provider.Models["text-candidate"] = model
	cfg.Provider["gemini"] = provider
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "direct_text_passed and router_text_passed") {
		t.Fatalf("EA-2: incomplete staged evidence error=%v", err)
	}

	model.ActivationEvidence.RouterTextPassed = true
	provider.Models["text-candidate"] = model
	cfg.Provider["gemini"] = provider
	cfg.Models["gemini-active"] = ModelGroup{
		Strategy: "static",
		Targets:  []Target{{Provider: "gemini", ModelRef: "text-candidate"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("exact direct+router evidence should activate text target: %v", err)
	}
}

func TestGeminiUnsupportedChatRoleFailsEligibilityBeforeUpstream(t *testing.T) {
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider["gemini"] = ProviderConfig{BaseURL: upstream.URL, Dialect: "gemini-generate-content", APIKey: "provider-key"}
	cfg.Models["gemini-text"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:         "gemini",
		Model:            "gemini-example",
		InputModalities:  []string{"text"},
		OutputModalities: []string{"text"},
		ActivationEvidence: ActivationEvidence{
			ExactModel:       "gemini-example",
			Dialect:          "gemini-generate-content",
			DirectTextPassed: true,
			RouterTextPassed: true,
			ValidatedAt:      "2026-09-09",
		},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "gemini-text")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gemini-text",
		"messages":[
			{"role":"user","content":"run the prior result"},
			{"role":"tool","content":"tool result","tool_call_id":"call_1"}
		]
	}`))
	request.Header.Set("Authorization", "Bearer "+testToken)
	recorder := httptest.NewRecorder()
	svc.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"unsupported_capability"`) {
		t.Fatalf("unsupported role status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("unsupported Gemini role reached upstream %d times", upstreamCalls.Load())
	}
}

func TestGeminiGenerateContentRouterTextStage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-example:generateContent" {
			t.Fatalf("upstream path=%q", r.URL.Path)
		}
		if r.Header.Get("X-Goog-Api-Key") != "provider-key" || r.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected Gemini auth headers")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, present := body["model"]; present {
			t.Fatalf("Gemini model belongs in endpoint, not body: %#v", body)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"candidates": []map[string]any{{
				"content":      map[string]any{"parts": []map[string]any{{"text": "OK"}}},
				"finishReason": "STOP",
			}},
			"usageMetadata": map[string]any{"promptTokenCount": 3, "candidatesTokenCount": 1, "totalTokenCount": 4},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider["gemini"] = ProviderConfig{
		BaseURL: upstream.URL,
		Dialect: "gemini-generate-content",
		APIKey:  "provider-key",
	}
	cfg.Models["gemini-text"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:         "gemini",
		Model:            "gemini-example",
		InputModalities:  []string{"text"},
		OutputModalities: []string{"text"},
		ActivationEvidence: ActivationEvidence{
			ExactModel:       "gemini-example",
			Dialect:          "gemini-generate-content",
			DirectTextPassed: true,
			RouterTextPassed: true,
			ValidatedAt:      "2026-09-09",
		},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "gemini-text")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gemini-text",
		"messages":[{"role":"user","content":"Reply OK only."}],
		"max_tokens":8
	}`))
	request.Header.Set("Authorization", "Bearer "+testToken)
	recorder := httptest.NewRecorder()
	svc.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("router status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	choices := valueAsSlice(response["choices"])
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if message["content"] != "OK" || choice["finish_reason"] != "stop" {
		t.Fatalf("unexpected router response: %#v", response)
	}
}

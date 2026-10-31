// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestDocumentInputConfigValidation(t *testing.T) {
	if err := validateInputModalities([]string{"text", "document"}); err != nil {
		t.Fatal(err)
	}
	if err := validateModalities([]string{"document"}); err == nil {
		t.Fatal("document output should not be accepted")
	}
	cfg := minimalConfig(t)
	provider := cfg.Provider["mock"]
	provider.Models = map[string]ProviderModel{"doc": {Model: "doc-model", InputModalities: []string{"text", "document"}}}
	cfg.Provider["mock"] = provider
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", ModelRef: "doc"}, {Provider: "mock", Model: "direct", InputModalities: []string{"text", "document"}}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, target := range cfg.Models["default"].Targets {
		if !targetSupportsInputModalities(target, []string{"document"}) {
			t.Fatalf("document not declared: %#v", target)
		}
	}
}

func TestDocumentURLRoutesOnlyToCompatibleTarget(t *testing.T) {
	chatCalls, anthropicCalls := 0, 0
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chatCalls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer chat.Close()
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anthropicCalls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		encoded, _ := json.Marshal(body)
		if !strings.Contains(string(encoded), `"url":"https://example.test/test.pdf"`) {
			t.Errorf("native URL lost: %s", encoded)
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "msg_1", "type": "message", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "ok"}}, "stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}})
	}))
	defer anthropic.Close()
	cfg := testConfig(t, chat.URL, "provider-key", t.TempDir())
	cfg.Provider["chat-doc"] = ProviderConfig{BaseURL: chat.URL, Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Provider["anthropic-doc"] = ProviderConfig{BaseURL: anthropic.URL, Dialect: "anthropic", APIKey: "provider-key"}
	cfg.Models["doc"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "chat-doc", Model: "chat", InputModalities: []string{"text", "document"}, RequestShapeSupport: RequestShapeSupport{SupportedInboundDialects: []string{"anthropic"}, ValidationStatus: "passed"}},
		{Provider: "anthropic-doc", Model: "native", InputModalities: []string{"text", "document"}},
	}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "doc")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	body := `{"model":"doc","max_tokens":32,"messages":[{"role":"user","content":[{"type":"document","source":{"type":"url","url":"https://example.test/test.pdf"}}]}]}`
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	r.Header.Set("X-API-Key", testToken)
	r.Header.Set("Anthropic-Version", "2023-06-01")
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK || chatCalls != 0 || anthropicCalls != 1 {
		t.Fatalf("status=%d chat=%d anthropic=%d body=%s", w.Code, chatCalls, anthropicCalls, w.Body.String())
	}
}

func TestAnthropicDocumentSourceTranslation(t *testing.T) {
	for _, source := range []map[string]any{
		{"type": "base64", "media_type": "application/pdf", "data": "cGRm"},
		{"type": "url", "url": "https://example.test/document.pdf"},
	} {
		block := map[string]any{"type": "document", "source": source, "title": "test"}
		payload, err := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{block}}}})
		if err != nil {
			t.Fatal(err)
		}
		req, err := decodeRequest("anthropic", payload, http.Header{})
		if err != nil {
			t.Fatal(err)
		}
		part := req.Messages[0].Parts[0]
		if got := mediaPart(part, "anthropic"); !reflect.DeepEqual(got, block) {
			t.Fatalf("native document changed: %#v", got)
		}
		for _, dialect := range []string{"openai-chat", "openai-responses"} {
			target := Target{InputModalities: []string{"text", "document"}}
			reason := mediaTargetReason(target, req, "anthropic", dialect)
			if source["type"] == "url" && dialect == "openai-chat" {
				if reason != "unsupported_modality" {
					t.Fatalf("URL must not be routed to Chat: %q", reason)
				}
				continue
			}
			if reason != "" {
				t.Fatalf("%s unexpectedly rejected: %s", dialect, reason)
			}
			mapped := mediaPart(part, dialect)
			file := mapped
			if dialect == "openai-chat" {
				file = mapped["file"].(map[string]any)
			} else if mapped["type"] != "input_file" {
				t.Fatalf("%s: %#v", dialect, mapped)
			}
			if source["type"] == "base64" && file["file_data"] != "data:application/pdf;base64,cGRm" {
				t.Fatalf("%s: %#v", dialect, mapped)
			}
			if source["type"] == "url" && file["file_url"] != source["url"] {
				t.Fatalf("%s: %#v", dialect, mapped)
			}
		}
	}
}

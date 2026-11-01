// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponsesNoStoreBedrockMessagesHTTP(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture")
	t.Setenv("AWS_SESSION_TOKEN", "")
	var body map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = nil
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"OK"}]}},"stopReason":"end_turn","usage":{"inputTokens":5,"outputTokens":2,"totalTokens":7}}`)
	}))
	defer up.Close()
	t.Setenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME", up.URL)
	cfg := testConfig(t, up.URL, "", t.TempDir())
	cfg.Provider["bedrock"] = ProviderConfig{Dialect: "openai-chat", AuthScheme: "bedrock", BaseURL: "https://bedrock-runtime.us-east-1.amazonaws.com"}
	target := Target{Provider: "bedrock", Model: "anthropic.claude-fixture", ResponsesToChat: ResponsesToChatBridge{Enabled: true, Text: true, ValidationStatus: "passed"}, RequestShapeSupport: RequestShapeSupport{SupportedInboundDialects: []string{"openai-responses"}, ValidationStatus: "passed", UnsupportedRequestFeatures: []string{"store"}}}
	cfg.Models["bedrock-store"] = ModelGroup{Strategy: "static", Targets: []Target{target}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "bedrock-store")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	for _, store := range []string{"false", "true"} {
		r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"bedrock-store","input":"hello","store":`+store+`}`))
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		_, present := body["store"]
		if w.Code != 200 || present || !strings.Contains(w.Body.String(), "OK") {
			t.Fatalf("store=%s status=%d upstream=%#v response=%s", store, w.Code, body, w.Body.String())
		}
	}
}

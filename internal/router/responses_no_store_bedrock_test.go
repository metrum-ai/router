// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type noStoreBedrockTransport struct {
	t      *testing.T
	bodies []map[string]any
}

func (f *noStoreBedrockTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, err
	}
	f.bodies = append(f.bodies, body)
	response := `{"output":{"message":{"role":"assistant","content":[{"text":"OK"}]}},"stopReason":"end_turn","usage":{"inputTokens":5,"outputTokens":2,"totalTokens":7}}`
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewBufferString(response))}, nil
}

func TestResponsesNoStoreBedrockMessagesHTTP(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture")
	t.Setenv("AWS_SESSION_TOKEN", "")
	cfg := testConfig(t, "https://bedrock-runtime.us-east-1.amazonaws.com", "", t.TempDir())
	cfg.Provider["bedrock"] = ProviderConfig{Dialect: "openai-chat", AuthScheme: "bedrock", BaseURL: "https://bedrock-runtime.us-east-1.amazonaws.com"}
	target := Target{Provider: "bedrock", Model: "mistral.mistral-large-3-675b-instruct", Region: "us-east-1", ResponsesToChat: ResponsesToChatBridge{Enabled: true, Text: true, ValidationStatus: "passed"}, RequestShapeSupport: RequestShapeSupport{SupportedInboundDialects: []string{"openai-responses"}, ValidationStatus: "passed", UnsupportedRequestFeatures: []string{"store"}}}
	cfg.Models["bedrock-store"] = ModelGroup{Strategy: "static", Targets: []Target{target}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "bedrock-store")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	transport := &noStoreBedrockTransport{t: t}
	svc.httpClient = &http.Client{Transport: transport}
	for _, store := range []string{"false", "true"} {
		r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"bedrock-store","input":"hello","store":`+store+`}`))
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "OK") {
			t.Fatalf("store=%s status=%d response=%s", store, w.Code, w.Body.String())
		}
		if got := transport.bodies[len(transport.bodies)-1]; got["store"] != nil {
			t.Fatalf("store=%s reached Bedrock: %#v", store, got)
		}
	}
}

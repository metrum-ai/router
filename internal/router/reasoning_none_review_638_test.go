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

func TestReasoningNoneOpenAIBridgePreserved638(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tools := range []bool{false, true} {
			chat := map[string]any{"model": "m", "stream": stream, "reasoning_effort": "none", "messages": []any{map[string]any{"role": "user", "content": "hello"}}}
			responses := map[string]any{"model": "m", "stream": stream, "reasoning": map[string]any{"effort": "none"}, "input": "hello"}
			if tools {
				chat["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "read", "parameters": map[string]any{"type": "object"}}}}
				responses["tools"] = []any{map[string]any{"type": "function", "name": "read", "parameters": map[string]any{"type": "object"}}}
			}
			for _, tc := range []struct {
				caller, out string
				body        map[string]any
			}{{"openai-chat", "openai-responses", chat}, {"openai-responses", "openai-chat", responses}} {
				raw, _ := json.Marshal(tc.body)
				req, err := decodeRequest(tc.caller, raw, http.Header{})
				if err != nil {
					t.Fatal(err)
				}
				target := anthropicToolTarget()
				target.Bridges.ChatToResponses = DialectBridgeSupport{Enabled: true, Tools: true, Streaming: true}
				target.ResponsesToChat = ResponsesToChatBridge{Enabled: true, Text: true, FunctionTools: true, Streaming: true, ValidationStatus: "passed"}
				var encoded []byte
				if tc.caller == "openai-chat" {
					encoded, err = encodeChatToResponsesBridge("up", req, target, "")
				} else {
					encoded, err = encodeResponsesToChatBridge("up", req, target)
				}
				if err != nil {
					t.Fatalf("%s stream=%v tools=%v: %v", tc.caller, stream, tools, err)
				}
				var sent map[string]any
				if err = json.Unmarshal(encoded, &sent); err != nil {
					t.Fatal(err)
				}
				if tc.out == "openai-chat" && sent["reasoning_effort"] != "none" {
					t.Fatalf("lost none: %s", encoded)
				}
				if tc.out == "openai-responses" {
					reasoning, _ := sent["reasoning"].(map[string]any)
					if reasoning["effort"] != "none" {
						t.Fatalf("lost none: %s", encoded)
					}
				}
				target.Reasoning = ReasoningSupport{Supported: true, Mode: reasoningModeAlwaysOn, Control: reasoningControlEffortEnum}
				reason := requestShapeFilterReason(target, req, tc.caller, tc.out, requestTokenEstimateLogRecord{}, targetRequestShapeFit{ContextFit: true, RequestBytesFit: true, ToolSchemaFit: true})
				if reason != "reasoning-default-on-unsupported" {
					t.Fatalf("default on reason=%q", reason)
				}
			}
		}
	}
}

func TestReasoningNoneUnsafeProviderDefaults638(t *testing.T) {
	for _, out := range []string{"gemini-generate-content", "openai-chat"} {
		t.Run(out, func(t *testing.T) {
			hits := 0
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(200) }))
			defer up.Close()
			target := anthropicBridgeTarget()
			if out == "gemini-generate-content" {
				target.Reasoning = ReasoningSupport{Supported: true, Mode: reasoningModeAlwaysOn, Control: reasoningControlEffortEnum}
				target.ActivationEvidence = ActivationEvidence{ExactModel: target.Model, Dialect: out, DirectTextPassed: true, RouterTextPassed: true, ValidatedAt: "2026-09-09"}
			} else {
				target.DefaultOpenAIChatThinking = map[string]any{"type": "enabled"}
			}
			svc := nativeStreamTestService(t, up.URL, out, []Target{target})
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"native-stream","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"none"}`))
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			if w.Code < 400 || hits != 0 {
				t.Fatalf("unsafe default dispatched: status=%d hits=%d body=%s", w.Code, hits, w.Body.String())
			}
		})
	}
}

func TestReasoningNoneWhitespaceAlwaysOn638(t *testing.T) {
	for _, caller := range []string{"openai-chat", "openai-responses"} {
		for _, out := range []string{"anthropic", "openai-chat", "openai-responses"} {
			if caller == out {
				continue
			}
			t.Run(caller+"/"+out, func(t *testing.T) {
				hits := 0
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(200) }))
				defer up.Close()
				target := anthropicBridgeTarget()
				target.Reasoning = ReasoningSupport{Supported: true, Mode: " always_on ", Control: reasoningControlEffortEnum}
				target.Bridges.ChatToResponses = DialectBridgeSupport{Enabled: true}
				target.ResponsesToChat = ResponsesToChatBridge{Enabled: true, Text: true, ValidationStatus: "passed"}
				svc := nativeStreamTestService(t, up.URL, out, []Target{target})
				path, body := "/v1/chat/completions", `{"model":"native-stream","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"none"}`
				if caller == "openai-responses" {
					path = "/v1/responses"
					body = `{"model":"native-stream","input":"hello","reasoning":{"effort":"none"}}`
				}
				decoded, err := decodeRequest(caller, []byte(body), http.Header{})
				if err != nil {
					t.Fatal(err)
				}
				fit := svc.targetRequestShapeFit(target, decoded, caller, out, requestTokenEstimateLogRecord{})
				if fit.FilterReason == "" {
					t.Fatal("always-on target was eligible")
				}
				r := httptest.NewRequest("POST", path, strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+testToken)
				w := httptest.NewRecorder()
				svc.Handler().ServeHTTP(w, r)
				if w.Code < 400 || hits != 0 {
					t.Fatalf("status=%d hits=%d body=%s", w.Code, hits, w.Body.String())
				}
			})
		}
	}
}

func TestReasoningNoneShadowSkipped638(t *testing.T) {
	hits := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(200) }))
	defer up.Close()
	svc := nativeStreamTestService(t, up.URL, "openai-chat", []Target{anthropicBridgeTarget()})
	run := &shadowRunState{group: "native-stream", provider: "native", model: "up"}
	for _, tc := range []struct{ caller, body string }{
		{"openai-chat", `{"model":"native-stream","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"none"}`},
		{"openai-responses", `{"model":"native-stream","input":"hello","reasoning":{"effort":"none"}}`},
	} {
		svc.executeShadowRequest(run, tc.caller, []byte(tc.body))
	}
	if hits != 0 {
		t.Fatalf("explicit reasoning off shadow replayed: hits=%d", hits)
	}
}

func TestReasoningNoneReplicateAndBedrock638(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture")
	t.Setenv("AWS_SESSION_TOKEN", "")
	for _, provider := range []string{"replicate", "bedrock"} {
		for _, caller := range []string{"openai-chat", "openai-responses"} {
			t.Run(provider+"/"+caller, func(t *testing.T) {
				hits := 0
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(200) }))
				defer up.Close()
				t.Setenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME", up.URL)
				target := anthropicBridgeTarget()
				target.Model = "owner/model"
				target.ResponsesToChat = ResponsesToChatBridge{Enabled: true, Text: true, ValidationStatus: "passed"}
				dialect := "replicate"
				if provider == "bedrock" {
					dialect = "openai-chat"
				}
				svc := nativeStreamTestService(t, up.URL, dialect, []Target{target}, func(cfg *Config) {
					if provider == "bedrock" {
						cfg.Provider["native"] = ProviderConfig{Dialect: "openai-chat", AuthScheme: "bedrock", BaseURL: "https://bedrock-runtime.us-east-1.amazonaws.com"}
					}
				})
				path, body := "/v1/chat/completions", `{"model":"native-stream","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"none"}`
				if caller == "openai-responses" {
					path = "/v1/responses"
					body = `{"model":"native-stream","input":"hello","reasoning":{"effort":"none"}}`
				}
				decoded, err := decodeRequest(caller, []byte(body), http.Header{})
				if err != nil {
					t.Fatal(err)
				}
				fit := svc.targetRequestShapeFit(target, decoded, caller, dialect, requestTokenEstimateLogRecord{})
				if fit.FilterReason != "reasoning-disable-unsupported" {
					t.Fatalf("filter=%q", fit.FilterReason)
				}
				r := httptest.NewRequest("POST", path, strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+testToken)
				w := httptest.NewRecorder()
				svc.Handler().ServeHTTP(w, r)
				if w.Code < 400 || hits != 0 {
					t.Fatalf("status=%d hits=%d body=%s", w.Code, hits, w.Body.String())
				}
			})
		}
	}
}

func TestReasoningNoneUnsupportedFieldsAndExtraControls638(t *testing.T) {
	for _, caller := range []string{"openai-chat", "openai-responses"} {
		raw := `{"model":"m","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"none"}`
		if caller == "openai-responses" {
			raw = `{"model":"m","input":"hello","reasoning":{"effort":"none"}}`
		}
		req, err := decodeRequest(caller, []byte(raw), http.Header{})
		if err != nil {
			t.Fatal(err)
		}
		target := anthropicBridgeTarget()
		target.RequestShapeSupport.UnsupportedRequestFeatures = []string{"reasoning"}
		fit := targetRequestShapeFit{ContextFit: true, RequestBytesFit: true, ToolSchemaFit: true}
		if reason := requestShapeFilterReason(target, req, caller, caller, requestTokenEstimateLogRecord{}, fit); reason != "request-shape-unsupported-feature" {
			t.Fatalf("%s native reason=%q", caller, reason)
		}
		if reason := requestShapeFilterReason(target, req, caller, "anthropic", requestTokenEstimateLogRecord{}, fit); reason != "" {
			t.Fatalf("%s translated reason=%q", caller, reason)
		}
	}
	for _, extra := range []string{`"summary":"detailed"`, `"context":"all_turns"`} {
		raw := `{"model":"m","input":"hello","reasoning":{"effort":"none",` + extra + `}}`
		req, err := decodeRequest("openai-responses", []byte(raw), http.Header{})
		if err != nil {
			t.Fatal(err)
		}
		if !disabledResponsesReasoningHasOtherFields(req) {
			t.Fatalf("extra lost: %s", raw)
		}
		for _, out := range []string{"anthropic", "openai-chat"} {
			reason := requestShapeFilterReason(anthropicBridgeTarget(), req, "openai-responses", out, requestTokenEstimateLogRecord{}, targetRequestShapeFit{ContextFit: true, RequestBytesFit: true, ToolSchemaFit: true})
			if reason != "reasoning-controls-unsupported" {
				t.Fatalf("%s reason=%q", out, reason)
			}
		}
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// This is the pre-#655 selection predicate. Diagnostics must not alter
// routing eligibility while error attribution changes.
func TestRoutingEligibility655Parity(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()
	svc := nativeStreamTestService(t, up.URL, "openai-chat", []Target{{Provider: "native", Model: "up"}})
	no := false
	targets := []Target{{Provider: "native", Model: "up"}, {Provider: "native", Model: "up", ToolOnly: true}, {Provider: "native", Model: "up", HonorsMaxTokens: &no}, {Provider: "native", Model: "up", ContextTokens: 10}, {Provider: "native", Model: "up", RequestShapeSupport: RequestShapeSupport{MaxRequestBytes: 100}}, {Provider: "native", Model: "up", RequestShapeSupport: RequestShapeSupport{UnsupportedRequestFeatures: []string{"stream_options"}}}, {Provider: "native", Model: "up", ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}, OpenAIResponses: []string{"function", "tool_choice"}, AnthropicMessages: []string{"client_tools", "tool_choice"}}}}
	cases := []struct{ caller, body string }{
		{"openai-chat", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`},
		{"openai-chat", `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true},"max_tokens":256,"tool_choice":"auto","tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}]}`},
		{"openai-chat", `{"model":"m","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"low","modalities":["text","audio"]}`},
		{"openai-responses", `{"model":"m","input":"hi","stream":true,"tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}`},
		{"anthropic", `{"model":"m","max_tokens":128,"messages":[{"role":"user","content":"hi"}]}`},
	}
	for _, tc := range cases {
		req, err := decodeRequest(tc.caller, []byte(tc.body), nil)
		if err != nil {
			t.Fatal(err)
		}
		estimate := requestTokenEstimateFromIR(req, tc.caller, 512)
		for _, out := range []string{"openai-chat", "openai-responses", "anthropic"} {
			for _, target := range targets {
				fit := svc.targetRequestShapeFit(target, req, tc.caller, out, estimate)
				old := !(target.ForceStoreFalse && rawValuePresent(req.Raw, "previous_response_id")) && (len(req.Tools) > 0 || !target.ToolOnly) && targetSupportsCallerDialect(target, req, tc.caller, out) && (len(req.Tools) == 0 || targetSupportsToolsForCallerDialect(target, tc.caller, out)) && targetSupportsInputModalities(target, requestInputModalities(req)) && targetSupportsChatAudioOutput(target, req, tc.caller, out) && targetSupportsStructuredOutput(target, tc.caller, out, requestHasStructuredOutput(req)) && targetCanSatisfyReasoning(target, out, req) && targetHonorsExplicitMaxTokens(target, req) && fit.FilterReason == ""
				reasons := svc.targetEligibilityReasons(target, req, tc.caller, out, estimate)
				if old != (len(reasons) == 0) {
					t.Errorf("caller=%s out=%s target=%#v old=%t reasons=%v", tc.caller, out, target, old, reasons)
				}
			}
		}
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// The expected target sets are pinned by running this same test against main
// before #655 and against the candidate. All requests include tools.
func TestRoutingBridgeSelectionMatchesMain655(t *testing.T) {
	chatReq := `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}]}`
	responsesReq := `{"model":"m","input":"hi","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}`
	anthropicReq := `{"model":"m","max_tokens":128,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"read","input_schema":{"type":"object"}}]}`
	validAnthropic := RequestShapeSupport{SupportedInboundDialects: []string{"anthropic"}, ValidationStatus: "passed"}
	c2r := Target{Provider: "native", Bridges: BridgeSupport{ChatToResponses: DialectBridgeSupport{Enabled: true, Tools: true}}, ToolSupport: ToolSupport{OpenAIResponses: []string{"function"}}}
	r2c := Target{Provider: "native", ResponsesToChat: ResponsesToChatBridge{Enabled: true, Text: true, FunctionTools: true, ValidationStatus: "passed"}, ToolSupport: ToolSupport{OpenAIChat: []string{"tools"}}}
	a2c := Target{Provider: "native", RequestShapeSupport: validAnthropic, ToolSupport: ToolSupport{AnthropicMessages: []string{"client_tools"}, OpenAIChat: []string{"tools"}}}
	a2r := Target{Provider: "native", RequestShapeSupport: validAnthropic, ToolSupport: ToolSupport{AnthropicMessages: []string{"client_tools"}, OpenAIResponses: []string{"function"}}}
	for _, tc := range []struct {
		name, caller, out, body   string
		good, badBridge, badTools Target
	}{
		{"chat-to-responses", "openai-chat", "openai-responses", chatReq, c2r, func() Target { v := c2r; v.Bridges = BridgeSupport{}; return v }(), func() Target { v := c2r; v.ToolSupport = ToolSupport{}; return v }()},
		{"responses-to-chat", "openai-responses", "openai-chat", responsesReq, r2c, func() Target { v := r2c; v.ResponsesToChat = ResponsesToChatBridge{}; return v }(), func() Target { v := r2c; v.ToolSupport = ToolSupport{}; return v }()},
		{"messages-to-chat", "anthropic", "openai-chat", anthropicReq, a2c, func() Target { v := a2c; v.RequestShapeSupport = RequestShapeSupport{}; return v }(), func() Target { v := a2c; v.ToolSupport.OpenAIChat = nil; return v }()},
		{"messages-to-responses", "anthropic", "openai-responses", anthropicReq, a2r, func() Target { v := a2r; v.RequestShapeSupport = RequestShapeSupport{}; return v }(), func() Target { v := a2r; v.ToolSupport.OpenAIResponses = nil; return v }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
			defer up.Close()
			svc := nativeStreamTestService(t, up.URL, tc.out, []Target{{Provider: "native", Model: "fixture"}})
			req, err := decodeRequest(tc.caller, []byte(tc.body), nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(req.Tools) == 0 {
				t.Fatal("fixture does not exercise tools-present branch")
			}
			good1, good2, bad1, bad2 := tc.good, tc.good, tc.badBridge, tc.badTools
			good1.Model = "good-one"
			good2.Model = "good-two"
			bad1.Model = "no-bridge"
			bad2.Model = "no-tools"
			selected := svc.targetsForRequest(nil, []Target{bad1, good1, bad2, good2}, req, tc.caller)
			var models []string
			for _, target := range selected {
				models = append(models, target.Model)
			}
			want := []string{"good-one", "good-two"}
			if !reflect.DeepEqual(models, want) {
				t.Fatalf("selected=%v want=%v", models, want)
			}
		})
	}
}

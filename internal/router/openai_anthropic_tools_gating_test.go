// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOpenAIAnthropicToolBridgeRequiresEvidence(t *testing.T) {
	for _, variant := range []string{"unvalidated", "missing-anthropic-tools", "missing-caller-tools", "missing-dialect"} {
		t.Run(variant, func(t *testing.T) {
			var hits atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(500) }))
			defer up.Close()
			target := anthropicToolTarget()
			switch variant {
			case "unvalidated":
				target.RequestShapeSupport.ValidationStatus = "limited"
			case "missing-anthropic-tools":
				target.ToolSupport.AnthropicMessages = nil
			case "missing-caller-tools":
				target.ToolSupport.OpenAIChat = nil
			case "missing-dialect":
				target.RequestShapeSupport.SupportedInboundDialects = []string{"anthropic"}
			}
			svc := nativeStreamTestService(t, up.URL, "anthropic", []Target{target})
			for _, body := range []string{
				`{"model":"native-stream","messages":[{"role":"user","content":"weather"}],"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object"}}}]}`,
				`{"model":"native-stream","messages":[{"role":"tool","tool_call_id":"call_1","content":"sunny"}]}`,
			} {
				r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+testToken)
				w := httptest.NewRecorder()
				svc.Handler().ServeHTTP(w, r)
				if w.Code != 400 {
					t.Fatalf("capability gate bypassed: %d %s", w.Code, w.Body.String())
				}
			}
			if hits.Load() != 0 {
				t.Fatal("unvalidated request reached upstream")
			}
		})
	}
}

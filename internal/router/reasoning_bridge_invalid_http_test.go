// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReasoningBridgeRejectsInvalidBudgetAndRestrictedSampling(t *testing.T) {
	for _, tools := range []bool{false, true} {
		t.Run(fmt.Sprintf("tools=%t", tools), func(t *testing.T) {
			var hits atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
			defer up.Close()
			target := anthropicToolTarget()
			target.Reasoning = ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum, SupportsSummaries: true, RejectsTemperature: true, RejectsTopP: true}
			svc := nativeStreamTestService(t, up.URL, "openai-responses", []Target{target})
			tool := ""
			if tools {
				tool = `,"tools":[{"name":"read","input_schema":{"type":"object"}}]`
			}
			post := func(body string) {
				r := httptest.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+testToken)
				w := httptest.NewRecorder()
				svc.Handler().ServeHTTP(w, r)
				if w.Code != 400 || hits.Load() != 0 {
					t.Fatalf("expected zero-attempt 400, got %d %s hits=%d", w.Code, w.Body.String(), hits.Load())
				}
			}
			for _, field := range []string{"", `,"budget_tokens":0`, `,"budget_tokens":-1`, `,"budget_tokens":"bad"`, `,"budget_tokens":1.5`} {
				post(`{"model":"native-stream","messages":[{"role":"user","content":"hi"}],"max_tokens":8192,"thinking":{"type":"enabled"` + field + `}` + tool + `}`)
			}
			for _, sampling := range []string{`,"temperature":0.5`, `,"top_p":0.7`} {
				post(`{"model":"native-stream","messages":[{"role":"user","content":"hi"}],"max_tokens":8192,"thinking":{"type":"enabled","budget_tokens":1024}` + tool + sampling + `}`)
			}
			anth := anthropicToolTarget()
			anth.DefaultThinking = map[string]any{"type": "adaptive"}
			anth.Reasoning = ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlTokenBudget, RejectsTemperature: true, RejectsTopP: true}
			reverse := nativeStreamTestService(t, up.URL, "anthropic", []Target{anth})
			oaiTool := ""
			if tools {
				oaiTool = `,"tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]`
			}
			for _, sampling := range []string{`,"temperature":0.5`, `,"top_p":0.7`} {
				body := `{"model":"native-stream","input":"hi","max_output_tokens":2048,"reasoning":{"effort":"low"}` + oaiTool + sampling + `}`
				r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+testToken)
				w := httptest.NewRecorder()
				reverse.Handler().ServeHTTP(w, r)
				if w.Code != 400 || hits.Load() != 0 {
					t.Fatalf("reverse expected zero-attempt 400 got %d %s hits=%d", w.Code, w.Body.String(), hits.Load())
				}
			}
		})
	}
}

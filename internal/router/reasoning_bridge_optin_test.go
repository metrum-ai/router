// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"testing"
)

func TestReasoningBridgeRequiresPerTargetOptIn(t *testing.T) {
	for _, tc := range []struct{ caller, out, body string }{
		{"openai-responses", "anthropic", `{"model":"m","input":"hello","max_output_tokens":2048,"reasoning":{"effort":"low"}}`},
		{"anthropic", "openai-responses", `{"model":"m","messages":[{"role":"user","content":"hello"}],"max_tokens":8192,"thinking":{"type":"enabled","budget_tokens":1024}}`},
	} {
		t.Run(tc.caller+"-"+tc.out, func(t *testing.T) {
			req, err := decodeRequest(tc.caller, []byte(tc.body), http.Header{})
			if err != nil {
				t.Fatal(err)
			}
			target := anthropicBridgeTarget()
			if tc.out == "anthropic" {
				target.Reasoning = ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlTokenBudget}
			} else {
				target.Reasoning = ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum, SupportsSummaries: true}
			}
			valid := target.RequestShapeSupport
			for _, shape := range []RequestShapeSupport{{}, {SupportedInboundDialects: []string{tc.caller}, ValidationStatus: "limited"}, {SupportedInboundDialects: []string{"unknown"}, ValidationStatus: "passed"}} {
				target.RequestShapeSupport = shape
				if got := requestShapeFilterReason(target, req, tc.caller, tc.out, requestTokenEstimateLogRecord{}, targetRequestShapeFit{ContextFit: true, RequestBytesFit: true, ToolSchemaFit: true}); got != "anthropic-bridge-reasoning-unsupported" {
					t.Fatalf("shape %#v: got %q", shape, got)
				}
			}
			target.RequestShapeSupport = valid
			if got := requestShapeFilterReason(target, req, tc.caller, tc.out, requestTokenEstimateLogRecord{}, targetRequestShapeFit{ContextFit: true, RequestBytesFit: true, ToolSchemaFit: true}); got != "" {
				t.Fatalf("opted-in target rejected: %q", got)
			}
		})
	}
}

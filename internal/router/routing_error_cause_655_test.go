// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRoutingError655CommonCause(t *testing.T) {
	req := &IRRequest{MaxTokensField: "max_tokens"}
	for _, tc := range []struct {
		name        string
		reasons     [][]string
		code, param string
	}{
		{"single", [][]string{{"max-tokens-honored"}}, "unsupported_capability", "max_tokens"},
		{"shared", [][]string{{"tool-support", "max-tokens-honored"}, {"tool-support", "input-modality-image"}}, "unsupported_capability", "tools"},
		{"different", [][]string{{"max-tokens-honored"}, {"unsupported-feature-stream_options"}}, "unsupported_capability", ""},
		{"multiple", [][]string{{"max-tokens-honored", "tool-support"}, {"max-tokens-honored", "tool-support"}}, "unsupported_capability", ""},
		{"ambiguous-size", [][]string{{"request-shape-max-request-bytes", "audio-output-unsupported"}}, "unsupported_capability", ""},
		{"unknown", [][]string{{"private-provider-model-target"}}, "unsupported_capability", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, param := commonRoutingCause(tc.reasons, req, "openai-chat")
			if code != tc.code || param != tc.param {
				t.Fatalf("got %s/%s want %s/%s", code, param, tc.code, tc.param)
			}
		})
	}
}

func TestRoutingError655ReasonMappingByDialect(t *testing.T) {
	// Exercise the real error renderer for each bounded reason and dialect.
	// Full HTTP selection coverage is provided by TestRoutingError655HTTPDialects.
	svc := nativeStreamTestService(t, "http://127.0.0.1:1", "openai-chat", []Target{{Provider: "native", Model: "up"}})
	// Each bounded selection/filter reason is either mapped to an actual caller
	// field or deliberately left generic. No target-derived string is exposed.
	tests := []struct {
		reason, code, kind string
		mapped             bool
	}{
		{"continuation_unsupported", "", "", false}, {"tool-only-target", "unsupported_capability", "generic", true}, {"gemini-request-shape-unsupported", "unsupported_capability", "generic", true},
		{"dialect-unsupported", "unsupported_endpoint_for_model", "model", true}, {"request-shape-dialect-unsupported", "unsupported_endpoint_for_model", "model", true}, {"request-shape-validation-unpassed", "unsupported_endpoint_for_model", "model", true},
		{"tool-support", "unsupported_capability", "tools", true}, {"audio-output-unsupported", "unsupported_capability", "modalities", true}, {"structured-output-support", "unsupported_capability", "structured", true}, {"structured-output-unsupported", "unsupported_capability", "structured", true}, {"logprobs-unsupported", "unsupported_capability", "logprobs", true},
		{"reasoning-support", "unsupported_capability", "reasoning", true}, {"reasoning-control", "unsupported_capability", "reasoning", true}, {"reasoning-history-unsupported", "unsupported_capability", "reasoning", true}, {"reasoning-controls-unsupported", "unsupported_capability", "reasoning", true}, {"reasoning-disable-unsupported", "unsupported_capability", "reasoning", true}, {"reasoning-default-on-unsupported", "unsupported_capability", "reasoning", true},
		{"input-modality-image", "unsupported_capability", "input", true}, {"input-modality-audio", "unsupported_capability", "input", true}, {"input-modality-video", "unsupported_capability", "input", true}, {"input-modality-document", "", "", false}, {"input-modality-text", "", "", false},
		{"max-tokens-honored", "unsupported_capability", "cap", true}, {"request-shape-min-output-tokens", "unsupported_capability", "cap", true}, {"request-shape-max-output-tokens", "unsupported_capability", "cap", true}, {"request-shape-context-exceeded", "context_length_exceeded", "input", true}, {"request-shape-max-input-tokens", "context_length_exceeded", "input", true},
		{"unsupported_modality", "", "", false}, {"unsupported_block", "", "", false}, {"unsupported_parameter", "", "", false},
		{"request-shape-max-request-bytes", "unsupported_capability", "generic", true}, {"request-shape-unvalidated-large-agent-payload", "unsupported_capability", "generic", true}, {"request-shape-tool-schema-bytes", "unsupported_capability", "generic", true}, {"request-shape-required-input-modality", "", "", false}, {"request-shape-unsupported-feature", "", "", false},
		{"store-false-unsupported", "unsupported_capability", "store", true}, {"continuation-unsupported", "", "", false}, {"anthropic-bridge-unrepresentable-content", "unsupported_capability", "input", true}, {"anthropic-bridge-unsupported-request", "unsupported_capability", "input", true}, {"anthropic-bridge-server-tool-unsupported", "unsupported_capability", "tools", true}, {"anthropic-bridge-tool-choice-unsupported", "unsupported_capability", "tool_choice", true}, {"anthropic-bridge-reasoning-unsupported", "unsupported_capability", "reasoning", true}, {"anthropic-bridge-reasoning-summary-unsupported", "unsupported_capability", "reasoning", true},
		{"responses-to-chat-bridge-disabled", "unsupported_endpoint_for_model", "model", true}, {"responses-to-chat-reasoning-history-unsupported", "unsupported_capability", "reasoning", true}, {"responses-to-chat-text-unsupported", "", "", false}, {"responses-to-chat-previous-response-id", "", "", false}, {"responses-to-chat-streaming", "unsupported_capability", "stream", true}, {"responses-to-chat-image", "unsupported_capability", "input", true}, {"responses-to-chat-reasoning", "unsupported_capability", "reasoning", true}, {"responses-to-chat-structured-output", "unsupported_capability", "structured", true}, {"responses-to-chat-function-output-nontext", "unsupported_function_call_output", "responses-input", true}, {"anthropic-bridge-function-output-nontext", "unsupported_function_call_output", "responses-input", true}, {"responses-to-chat-content-unsupported", "unsupported_capability", "input", true}, {"responses-to-chat-function-call-output", "unsupported_capability", "input", true}, {"responses-to-chat-tool-result", "unsupported_capability", "input", true}, {"responses-to-chat-include", "unsupported_capability", "include", true}, {"responses-to-chat-truncation", "unsupported_capability", "truncation", true}, {"responses-to-chat-tools-unsupported", "unsupported_capability", "tools", true}, {"responses-to-chat-hosted-tools", "unsupported_capability", "tools", true}, {"responses-to-chat-tool-type", "unsupported_capability", "tools", true}, {"responses-to-chat-tool-choice", "unsupported_capability", "tool_choice", true}, {"responses-to-chat-tool-choice-null-unsupported", "unsupported_capability", "tool_choice", true},
		{"chat-to-responses-bridge-disabled", "unsupported_endpoint_for_model", "model", true}, {"chat-to-responses-text-unsupported", "", "", false}, {"chat-to-responses-image-unsupported", "unsupported_capability", "input", true}, {"chat-to-responses-tools-unsupported", "unsupported_capability", "tools", true}, {"chat-to-responses-tool-choice-unsupported", "unsupported_capability", "tool_choice", true}, {"chat-to-responses-tool-choice-null-unsupported", "unsupported_capability", "tool_choice", true}, {"chat-to-responses-parallel-tools-unsupported", "unsupported_capability", "parallel_tool_calls", true}, {"chat-to-responses-structured-output-unsupported", "unsupported_capability", "structured", true}, {"chat-to-responses-reasoning-unsupported", "unsupported_capability", "reasoning", true}, {"chat-to-responses-unsupported-field", "", "", false},
	}
	for _, feature := range []string{"tools", "tool_choice", "forced_tool_choice", "structured_output", "response_format", "previous_response_id", "function_call_output", "tool_result", "include", "truncation", "metadata", "store", "reasoning", "image", "audio", "video", "stream", "stream_options"} {
		kind := feature
		switch feature {
		case "forced_tool_choice":
			kind = "tool_choice"
		case "structured_output", "response_format":
			kind = "structured"
		case "function_call_output", "tool_result", "image", "audio", "video":
			kind = "input"
		}
		tests = append(tests, struct {
			reason, code, kind string
			mapped             bool
		}{"unsupported-feature-" + feature, "unsupported_capability", kind, true})
	}
	for _, dialect := range []string{"openai-chat", "openai-responses", "anthropic"} {
		for _, tc := range tests {
			t.Run(fmt.Sprintf("%s/%s", dialect, tc.reason), func(t *testing.T) {
				req := &IRRequest{MaxTokensField: "max_tokens"}
				if dialect == "openai-responses" {
					req.MaxTokensField = "max_output_tokens"
				}
				got, ok := publicEligibilityCause(tc.reason, req, dialect)
				if ok != tc.mapped {
					t.Fatalf("mapped=%t cause=%#v", ok, got)
				}
				param := tc.kind
				switch tc.kind {
				case "generic":
					param = ""
				case "responses-input":
					param = "input"
				case "input":
					param = "messages"
					if dialect == "openai-responses" {
						param = "input"
					}
				case "structured":
					param = "response_format"
					if dialect == "openai-responses" {
						param = "text.format"
					}
				case "cap":
					param = req.MaxTokensField
				}
				if ok && (got.code != tc.code || got.param != param) {
					t.Fatalf("got=%#v want=%s/%s", got, tc.code, param)
				}
				// Continuation requirements have a separate early error path.
				if tc.reason == "continuation_unsupported" || tc.reason == "continuation-unsupported" || tc.reason == "responses-to-chat-previous-response-id" || tc.reason == "unsupported-feature-previous_response_id" {
					return
				}
				wantCode := tc.code
				if !ok {
					wantCode, param = "unsupported_capability", ""
				}
				code, actualParam := commonRoutingCause([][]string{{tc.reason}}, req, dialect)
				w := httptest.NewRecorder()
				rc := &requestContext{id: "req_test", start: time.Now(), dialect: dialect}
				svc.writeRoutingEligibilityError(w, rc, routingEligibilityError{Model: "public-group", Dialect: dialect, Requirements: []string{"text"}, CapabilityMismatch: true, MismatchCode: code, MismatchParam: actualParam})
				var envelope struct {
					Type  string `json:"type"`
					Error struct {
						Type, Code string
						Param      any
					} `json:"error"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
					t.Fatal(err)
				}
				var wantParam any
				if param != "" {
					wantParam = param
				}
				if w.Code != 400 || envelope.Error.Type != "invalid_request_error" || envelope.Error.Code != wantCode || envelope.Error.Param != wantParam || (dialect == "anthropic" && envelope.Type != "error") {
					t.Fatalf("status=%d envelope=%#v want=%s/%v", w.Code, envelope, wantCode, wantParam)
				}
			})
		}
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "strings"

type routingErrorCause struct{ code, param string }

// publicEligibilityCause translates only bounded reason enums to public API
// fields. Unmapped/ambiguous reasons deliberately have no guessed parameter.
func publicEligibilityCause(reason string, req *IRRequest, dialect string) (routingErrorCause, bool) {
	input := "messages"
	if dialect == "openai-responses" {
		input = "input"
	}
	structured := "response_format"
	if dialect == "openai-responses" {
		structured = "text.format"
	}
	capField := req.MaxTokensField
	switch reason {
	case "dialect-unsupported", "request-shape-dialect-unsupported", "request-shape-validation-unpassed", "chat-to-responses-bridge-disabled", "responses-to-chat-bridge-disabled":
		return routingErrorCause{"unsupported_endpoint_for_model", "model"}, true
	case "tool-only-target", "gemini-request-shape-unsupported":
		return routingErrorCause{"unsupported_capability", ""}, true
	case "tool-support", "chat-to-responses-tools-unsupported", "responses-to-chat-tools-unsupported", "responses-to-chat-hosted-tools", "responses-to-chat-tool-type", "anthropic-bridge-server-tool-unsupported":
		return routingErrorCause{"unsupported_capability", "tools"}, true
	case "structured-output-support", "structured-output-unsupported", "chat-to-responses-structured-output-unsupported", "responses-to-chat-structured-output":
		return routingErrorCause{"unsupported_capability", structured}, true
	case "reasoning-support", "reasoning-control", "reasoning-controls-unsupported", "reasoning-disable-unsupported", "reasoning-default-on-unsupported", "anthropic-bridge-reasoning-unsupported", "anthropic-bridge-reasoning-summary-unsupported", "chat-to-responses-reasoning-unsupported", "responses-to-chat-reasoning", "reasoning-history-unsupported", "responses-to-chat-reasoning-history-unsupported":
		return routingErrorCause{"unsupported_capability", "reasoning"}, true
	case "logprobs-unsupported":
		return routingErrorCause{"unsupported_capability", "logprobs"}, true
	case "input-modality-image", "input-modality-audio", "input-modality-video", "responses-to-chat-image", "chat-to-responses-image-unsupported", "anthropic-bridge-unrepresentable-content", "anthropic-bridge-unsupported-request", "responses-to-chat-content-unsupported", "responses-to-chat-function-call-output", "responses-to-chat-tool-result":
		return routingErrorCause{"unsupported_capability", input}, true
	case "responses-to-chat-function-output-nontext", "anthropic-bridge-function-output-nontext":
		return routingErrorCause{"unsupported_function_call_output", "input"}, true
	case "audio-output-unsupported":
		return routingErrorCause{"unsupported_capability", "modalities"}, true
	case "max-tokens-honored", "request-shape-min-output-tokens", "request-shape-max-output-tokens":
		return routingErrorCause{"unsupported_capability", capField}, true
	case "request-shape-context-exceeded", "request-shape-max-input-tokens":
		return routingErrorCause{"context_length_exceeded", input}, true
	case "request-shape-max-request-bytes", "request-shape-unvalidated-large-agent-payload", "request-shape-tool-schema-bytes":
		return routingErrorCause{"unsupported_capability", ""}, true
	case "anthropic-bridge-tool-choice-unsupported", "responses-to-chat-tool-choice", "responses-to-chat-tool-choice-null-unsupported", "chat-to-responses-tool-choice-unsupported", "chat-to-responses-tool-choice-null-unsupported":
		return routingErrorCause{"unsupported_capability", "tool_choice"}, true
	case "chat-to-responses-parallel-tools-unsupported":
		return routingErrorCause{"unsupported_capability", "parallel_tool_calls"}, true
	case "responses-to-chat-streaming":
		return routingErrorCause{"unsupported_capability", "stream"}, true
	case "responses-to-chat-include":
		return routingErrorCause{"unsupported_capability", "include"}, true
	case "responses-to-chat-truncation":
		return routingErrorCause{"unsupported_capability", "truncation"}, true
	case "store-false-unsupported":
		return routingErrorCause{"unsupported_capability", "store"}, true
	}
	if strings.HasPrefix(reason, "unsupported-feature-") {
		switch strings.TrimPrefix(reason, "unsupported-feature-") {
		case "forced_tool_choice", "tool_choice":
			return routingErrorCause{"unsupported_capability", "tool_choice"}, true
		case "structured_output", "response_format":
			return routingErrorCause{"unsupported_capability", structured}, true
		case "function_call_output", "tool_result", "image", "audio", "video":
			return routingErrorCause{"unsupported_capability", input}, true
		case "tools", "previous_response_id", "include", "truncation", "metadata", "store", "reasoning", "stream", "stream_options":
			return routingErrorCause{"unsupported_capability", strings.TrimPrefix(reason, "unsupported-feature-")}, true
		}
	}
	return routingErrorCause{}, false
}

// commonRoutingCause returns only a cause shared by every rejected target.
// Different reasons are not reduced to a guessed caller field.
func commonRoutingCause(rejectedReasons [][]string, req *IRRequest, dialect string) (string, string) {
	var common map[routingErrorCause]bool
	for _, reasons := range rejectedReasons {
		current := map[routingErrorCause]bool{}
		for _, reason := range reasons {
			if cause, ok := publicEligibilityCause(reason, req, dialect); ok {
				current[cause] = true
			}
		}
		if common == nil {
			common = current
		} else {
			for cause := range common {
				if !current[cause] {
					delete(common, cause)
				}
			}
		}
	}
	// A shared non-field-specific blocker can make a named field incidental
	// (for example an oversized audio request). Do not blame that field.
	if common[routingErrorCause{"unsupported_capability", ""}] {
		return "unsupported_capability", ""
	}
	// Stable ordering when several independently valid shared causes exist.
	priority := []string{"unsupported_endpoint_for_model", "unsupported_function_call_output", "context_length_exceeded", "unsupported_capability"}
	for _, code := range priority {
		found := routingErrorCause{}
		count := 0
		for cause := range common {
			if cause.code == code && cause.param != "" {
				found = cause
				count++
			}
		}
		if count == 1 {
			return found.code, found.param
		}
		if count > 1 {
			return "unsupported_capability", ""
		}
	}
	return "unsupported_capability", ""
}

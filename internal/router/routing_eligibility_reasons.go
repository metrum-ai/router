// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "strings"

// targetEligibilityReasons is the single source of truth for the request
// capability filters. Reasons are bounded enums, never provider data.
func (s *Service) targetEligibilityReasons(target Target, req *IRRequest, caller, out string, estimate requestTokenEstimateLogRecord) []string {
	var reasons []string
	if target.ForceStoreFalse && rawValuePresent(req.Raw, "previous_response_id") {
		reasons = append(reasons, "continuation_unsupported")
	}
	if len(req.Tools) == 0 && target.ToolOnly {
		reasons = append(reasons, "tool-only-target")
	}
	if !targetSupportsCallerDialect(target, req, caller, out) {
		reason := "dialect-unsupported"
		switch {
		case normalizeDialect(caller) == "openai-chat" && normalizeDialect(out) == "gemini-generate-content":
			reason = "gemini-request-shape-unsupported"
		case normalizeDialect(caller) == "openai-chat" && normalizeDialect(out) == "openai-responses":
			reason = chatToResponsesBridgeFilterReason(target, req, caller, out)
		case normalizeDialect(caller) == "openai-responses" && normalizeDialect(out) == "openai-chat":
			reason = responsesToChatBridgeFilterReason(target, req, caller, out)
		case anthropicInboundDialectFilterReason(target, caller, out) != "":
			reason = anthropicInboundDialectFilterReason(target, caller, out)
		}
		if reason == "" {
			reason = "dialect-unsupported"
		}
		reasons = append(reasons, reason)
	}
	if len(req.Tools) > 0 && !targetSupportsToolsForCallerDialect(target, caller, out) {
		reasons = append(reasons, "tool-support")
	}
	for _, modality := range requestInputModalities(req) {
		if !targetSupportsInputModalities(target, []string{modality}) {
			reasons = append(reasons, "input-modality-"+safeReasonToken(modality))
		}
	}
	if !targetSupportsChatAudioOutput(target, req, caller, out) {
		reasons = append(reasons, "audio-output-unsupported")
	}
	if !targetSupportsStructuredOutput(target, caller, out, requestHasStructuredOutput(req)) {
		reasons = append(reasons, "structured-output-support")
	}
	if reason := reasoningFilterReason(target, out, req); reason != "" {
		reasons = append(reasons, reason)
	}
	if !targetHonorsExplicitMaxTokens(target, req) {
		reasons = append(reasons, "max-tokens-honored")
	}
	if reason := s.targetRequestShapeFit(target, req, caller, out, estimate).FilterReason; reason != "" {
		if reason == "request-shape-unsupported-feature" {
			// Re-evaluate each declared feature separately to preserve translated
			// exceptions such as store:false; only bounded, known names are recorded.
			reasons = append(reasons, reason)
			seen := make(map[string]bool)
			for _, raw := range target.RequestShapeSupport.UnsupportedRequestFeatures {
				feature := strings.ToLower(strings.TrimSpace(raw))
				if !knownRequestFeature(feature) || seen[feature] {
					continue
				}
				seen[feature] = true
				single := target
				single.RequestShapeSupport.UnsupportedRequestFeatures = []string{feature}
				if s.targetRequestShapeFit(single, req, caller, out, estimate).FilterReason == reason {
					reasons = append(reasons, "unsupported-feature-"+feature)
				}
			}
		} else {
			reasons = append(reasons, reason)
			if reason == "anthropic-bridge-unsupported-request" && caller == "openai-responses" && invalidResponsesFunctionOutput(req) {
				reasons = append(reasons, "anthropic-bridge-function-output-nontext")
			}
		}
	}
	unique := reasons[:0]
	seen := make(map[string]bool, len(reasons))
	for _, reason := range reasons {
		if !seen[reason] {
			seen[reason] = true
			unique = append(unique, reason)
		}
	}
	return unique
}

func knownRequestFeature(feature string) bool {
	switch feature {
	case "tools", "tool_choice", "forced_tool_choice", "structured_output", "response_format", "previous_response_id", "function_call_output", "tool_result", "include", "truncation", "metadata", "store", "reasoning", "image", "audio", "video", "stream", "stream_options":
		return true
	}
	return false
}

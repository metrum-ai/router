// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"strings"
	"time"
)

const (
	requestEstimateMethod  = "char-count"
	requestEstimateVersion = "2026-06-29"
)

type targetRequestShapeFit struct {
	Estimate                    requestTokenEstimateLogRecord
	MaxEstimatedInputTokens     int
	MinRequestedOutputTokens    int
	MaxRequestedOutputTokens    int
	MaxRequestBytes             int
	MaxToolSchemaBytes          int
	EstimatedTotalWithOutputCap int
	ToolSchemaBytes             int
	ContextHeadroomTokens       int
	ContextFit                  bool
	RequestBytesFit             bool
	ToolSchemaFit               bool
	EligibilityDecision         string
	EligibilityReason           string
	FilterReason                string
}

func (s *Service) recordRequestTokenEstimateTelemetry(rc *requestContext, req *IRRequest, callerDialect string, totalRequestBytes int) {
	if rc == nil || req == nil {
		return
	}
	rec := requestTokenEstimateFromIR(req, callerDialect, totalRequestBytes)
	rec.RequestedModel = req.Model
	rec.ResolvedGroup = req.Model
	rc.rec.TokenEstimate = &rec
}

func requestTokenEstimateFromIR(req *IRRequest, callerDialect string, totalRequestBytes int) requestTokenEstimateLogRecord {
	estimatedTotalInput := estimateTokens(req)
	var tools []map[string]any
	var raw map[string]any
	var maxTokens int
	var maxTokensField string
	if req != nil {
		tools = req.Tools
		raw = req.Raw
		maxTokens = req.MaxTokens
		maxTokensField = req.MaxTokensField
	}
	toolSchemaBytes := jsonValueLen(tools)
	estimatedToolSchema := estimateBytesAsTokens(toolSchemaBytes)
	estimatedImage := requestImageCount(req) * 256
	estimatedAudio := 0
	estimatedInput := estimatedTotalInput - estimatedToolSchema - estimatedImage - estimatedAudio
	if estimatedInput < 0 {
		estimatedInput = 0
	}
	requestedOutputCap := trafficShapeOutputReservation(req, callerDialect)
	outputField := ""
	routerDefault := false
	if req != nil {
		outputField = maxTokensField
		if requestedOutputCap > 0 && maxTokens <= 0 {
			outputField = "router_default"
			routerDefault = true
		}
	}
	confidence := "router_estimate_high"
	if requestImageCount(req) > 0 || rawContainsModality(raw, "audio") || rawContainsModality(raw, "video") {
		confidence = "router_estimate_low"
	} else if len(tools) > 0 || toolSchemaBytes > 0 {
		confidence = "router_estimate_medium"
	}
	return requestTokenEstimateLogRecord{
		TS:                            time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		InboundDialect:                callerDialect,
		EstimateMethod:                requestEstimateMethod,
		EstimateVersion:               requestEstimateVersion,
		EstimatedInputTokens:          estimatedInput,
		EstimatedToolSchemaTokens:     estimatedToolSchema,
		EstimatedImageTokens:          estimatedImage,
		EstimatedAudioTokens:          estimatedAudio,
		EstimatedTotalInputTokens:     estimatedTotalInput,
		RequestedOutputCapTokens:      requestedOutputCap,
		RequestedOutputCapField:       outputField,
		RouterDefaultOutputCapApplied: routerDefault,
		TotalReservedTokens:           estimatedTotalInput + requestedOutputCap,
		RequestBytes:                  totalRequestBytes,
		EstimateConfidenceBucket:      confidence,
	}
}

func estimateBytesAsTokens(bytes int) int {
	if bytes <= 0 {
		return 0
	}
	return bytes/4 + 1
}

func (s *Service) requestTokenEstimateForSelection(rc *requestContext, req *IRRequest, callerDialect string) requestTokenEstimateLogRecord {
	if rc != nil && rc.rec.TokenEstimate != nil {
		return *rc.rec.TokenEstimate
	}
	return requestTokenEstimateFromIR(req, callerDialect, 0)
}

func (s *Service) targetRequestShapeFit(target Target, req *IRRequest, callerDialect, outDialect string, estimate requestTokenEstimateLogRecord) targetRequestShapeFit {
	var tools []map[string]any
	if req != nil {
		tools = req.Tools
	}
	estimate = requestTokenEstimateForTargetDialect(req, outDialect, estimate)
	fit := targetRequestShapeFit{
		Estimate:                    estimate,
		MaxEstimatedInputTokens:     target.RequestShapeSupport.MaxEstimatedInputTokens,
		MinRequestedOutputTokens:    target.RequestShapeSupport.MinRequestedOutputTokens,
		MaxRequestedOutputTokens:    target.RequestShapeSupport.MaxRequestedOutputTokens,
		MaxRequestBytes:             target.RequestShapeSupport.MaxRequestBytes,
		MaxToolSchemaBytes:          target.RequestShapeSupport.MaxToolSchemaBytes,
		EstimatedTotalWithOutputCap: estimate.EstimatedTotalInputTokens + estimate.RequestedOutputCapTokens,
		ToolSchemaBytes:             jsonValueLen(tools),
		ContextFit:                  true,
		RequestBytesFit:             true,
		ToolSchemaFit:               true,
		EligibilityDecision:         "eligible",
		EligibilityReason:           "fits",
	}
	if target.ContextTokens > 0 {
		fit.ContextHeadroomTokens = target.ContextTokens - fit.EstimatedTotalWithOutputCap
		if fit.ContextHeadroomTokens < 0 {
			fit.ContextFit = false
		}
	}
	if fit.MaxRequestBytes > 0 && estimate.RequestBytes > fit.MaxRequestBytes {
		fit.RequestBytesFit = false
	}
	if fit.MaxToolSchemaBytes > 0 && fit.ToolSchemaBytes > fit.MaxToolSchemaBytes {
		fit.ToolSchemaFit = false
	}
	reason := ""
	// Bedrock currently uses a Chat-to-Converse adapter, not a native
	// Anthropic codec. Preserve main's fail-closed behavior for opaque
	// Anthropic history rather than stripping it on the way to Bedrock.
	if s.cfg != nil && normalizeDialect(callerDialect) == "anthropic" && isBedrock(s.cfg.Provider[target.Provider]) && reasoningHistoryPresent(req) {
		reason = "bedrock-opaque-reasoning-unsupported"
	}
	// Bedrock's Converse adapter consumes neither OpenAI off control, even
	// though the intermediate dialect is Chat. Do not silently drop it.
	if reason == "" && s.cfg != nil && req != nil && req.Reasoning.Disabled && strings.HasPrefix(req.Reasoning.Source, "openai_") && isBedrock(s.cfg.Provider[target.Provider]) {
		reason = "reasoning-disable-unsupported"
	}
	if reason == "" && s.cfg != nil && !s.cfg.Eligibility.implicitEnabled() && normalizeDialect(outDialect) == "anthropic" && normalizeDialect(callerDialect) != "anthropic" {
		if !stringSliceContainsNormalizedDialect(target.RequestShapeSupport.SupportedInboundDialects, callerDialect) {
			reason = "request-shape-dialect-unsupported"
		} else if !strings.EqualFold(strings.TrimSpace(target.RequestShapeSupport.ValidationStatus), "passed") {
			reason = "request-shape-validation-unpassed"
		}
	}
	if reason == "" {
		reason = requestShapeFilterReason(target, req, callerDialect, outDialect, estimate, fit)
	}
	if reason != "" {
		fit.EligibilityDecision = "skipped"
		fit.EligibilityReason = reason
		fit.FilterReason = reason
		return fit
	}
	if target.ContextTokens == 0 || fit.MaxRequestBytes == 0 || fit.MaxToolSchemaBytes == 0 {
		fit.EligibilityReason = "limit_unknown"
	}
	return fit
}

// responsesExplicitNoStore identifies only an actual boolean false.
func responsesExplicitNoStore(req *IRRequest) bool {
	if req == nil {
		return false
	}
	store, ok := req.Raw["store"].(bool)
	return ok && !store
}

func requestShapeFilterReason(target Target, req *IRRequest, callerDialect, outDialect string, estimate requestTokenEstimateLogRecord, fit targetRequestShapeFit) string {
	// Native Responses and Chat represent no-store; Messages has no stored
	// response resource. Other transports have no established equivalent.
	if normalizeDialect(callerDialect) == "openai-responses" && responsesExplicitNoStore(req) {
		switch normalizeDialect(outDialect) {
		case "openai-responses", "openai-chat", "anthropic":
		default:
			return "store-false-unsupported"
		}
	}
	// For newly accepted developer histories, stripping foreign reasoning
	// must not promote later instructions or leave no conversation.
	if callerDialect == "openai-responses" && outDialect == "anthropic" && responsesDeveloperOrderingInvalid(req) {
		return "anthropic-bridge-unrepresentable-content"
	}
	// Validate the same sanitized history that the cross-dialect encoder sees.
	// Otherwise harmless foreign reasoning items are rejected before stripping.
	clean, err := foreignReasoningHistory(req, callerDialect, outDialect)
	if err != nil {
		return "reasoning-history-unsupported"
	}
	req = clean
	support := target.RequestShapeSupport
	// Logprobs cannot be represented by the current Anthropic translation.
	if callerDialect == "openai-chat" && outDialect == "anthropic" && req != nil && boolValue(req.Raw["logprobs"]) {
		return "logprobs-unsupported"
	}
	if (callerDialect == "openai-chat" || callerDialect == "openai-responses") && outDialect == "anthropic" && requestHasStructuredOutput(req) {
		_, schema := openAIJSONSchemaForAnthropic(req)
		object := openAIJSONObjectRequest(req)
		// Tool calls have no final text to validate. Until that combined mode
		// is supported end-to-end, do not weaken the requested output guarantee.
		if (!schema && !object) || len(req.Tools) > 0 || openAIHasToolHistory(req, callerDialect) {
			return "structured-output-unsupported"
		}
	}
	if callerDialect == "openai-responses" && outDialect != "openai-responses" && disabledResponsesReasoningHasOtherFields(req) {
		return "reasoning-controls-unsupported"
	}
	if req != nil && req.Reasoning.Disabled && strings.HasPrefix(req.Reasoning.Source, "openai_") {
		// Other provider codecs have no validated off control. Never silently
		// reduce an explicit disable to an omitted provider setting.
		if outDialect != "anthropic" && outDialect != "openai-chat" && outDialect != "openai-responses" {
			return "reasoning-disable-unsupported"
		}
		// Native Chat preserves the field but must not add a contradictory
		// target-level enabled thinking object to it.
		if outDialect == "openai-chat" && len(target.DefaultOpenAIChatThinking) > 0 && !strings.EqualFold(strings.TrimSpace(stringValue(target.DefaultOpenAIChatThinking["type"])), "disabled") {
			return "reasoning-default-on-unsupported"
		}
		if callerDialect != outDialect && (outDialect == "openai-chat" || outDialect == "openai-responses") && (target.Reasoning.DefaultOn || strings.EqualFold(strings.TrimSpace(target.Reasoning.Mode), reasoningModeAlwaysOn)) {
			return "reasoning-default-on-unsupported"
		}
	}
	if outDialect == "anthropic" && (callerDialect == "openai-responses" || callerDialect == "openai-chat") {
		if anthropicImageMetadataUnsupported(req, callerDialect) || (callerDialect == "openai-responses" && invalidResponsesAnthropicMessageContent(req)) {
			return "anthropic-bridge-unrepresentable-content"
		}
	}
	if reason := mediaTargetReason(target, req, callerDialect, outDialect); reason != "" {
		return reason
	}
	if (callerDialect == "openai-chat" || callerDialect == "openai-responses") && outDialect == "anthropic" && req != nil {
		if rawValuePresent(req.Raw, "previous_response_id") {
			return "continuation-unsupported"
		}
		if openAIHasToolHistory(req, callerDialect) && !openAIAnthropicToolsBridgeEnabled(target, callerDialect, outDialect) {
			return "tool-support"
		}
		if rawKeyPresent(req.Raw, "tool_choice") && (len(req.Tools) > 0 || openAIHasToolHistory(req, callerDialect)) {
			if !openAIAnthropicToolsBridgeEnabled(target, callerDialect, outDialect) || !targetSupportsClientTools(target, callerDialect, true) || !targetSupportsClientTools(target, "anthropic", true) {
				return "anthropic-bridge-tool-choice-unsupported"
			}
		}
		if openAIAnthropicToolsBridgeEnabled(target, callerDialect, outDialect) && (len(req.Tools) > 0 || openAIHasToolHistory(req, callerDialect)) {
			if _, err := encodeOpenAIToolsToAnthropic(target.Model, callerDialect, req, target); err != nil {
				return "anthropic-bridge-unsupported-request"
			}
		}
	}
	if callerDialect == "anthropic" && outDialect != "anthropic" {
		for _, tool := range req.Tools {
			if typ := stringValue(tool["type"]); typ != "" && typ != "custom" {
				return "anthropic-bridge-server-tool-unsupported"
			}
		}
	}
	if callerDialect == "anthropic" && outDialect != "anthropic" && !anthropicToolsBridgeEnabled(target, outDialect) {
		// Continuation history still needs a tool bridge even when this turn
		// omits tool definitions (for example after tool_choice:none).
		for _, raw := range valueAsSlice(req.Raw["messages"]) {
			message, _ := raw.(map[string]any)
			for _, rawBlock := range valueAsSlice(message["content"]) {
				block, _ := rawBlock.(map[string]any)
				if typ := stringValue(block["type"]); typ == "tool_use" || typ == "tool_result" {
					return "tool-support"
				}
			}
		}
	}
	if callerDialect == "anthropic" && outDialect != "anthropic" && rawKeyPresent(req.Raw, "tool_choice") {
		choice, ok := req.Raw["tool_choice"].(map[string]any)
		if !ok || !supportsAnyCapability([]string{"auto", "any", "tool", "none"}, stringValue(choice["type"])) {
			return "anthropic-bridge-tool-choice-unsupported"
		}
		if !targetSupportsClientTools(target, "anthropic", true) || !targetSupportsClientTools(target, outDialect, true) {
			return "anthropic-bridge-tool-choice-unsupported"
		}
	}
	if anthropicStreamBridge(callerDialect, outDialect) {
		if requestRequiresReasoning(req) {
			// A target's native reasoning support alone is not permission to
			// translate it. Require an explicit validated inbound bridge even
			// when ordinary request-shape eligibility is set to implicit.
			if !stringSliceContainsNormalizedDialect(target.RequestShapeSupport.SupportedInboundDialects, callerDialect) || !strings.EqualFold(strings.TrimSpace(target.RequestShapeSupport.ValidationStatus), "passed") {
				return "anthropic-bridge-reasoning-unsupported"
			}
			if !targetCanSatisfyReasoning(target, outDialect, req) {
				return "anthropic-bridge-reasoning-unsupported"
			}
			if callerDialect == "anthropic" && outDialect == "openai-responses" && !target.Reasoning.SupportsSummaries {
				return "anthropic-bridge-reasoning-summary-unsupported"
			}
		} else if target.Reasoning.DefaultOn || strings.EqualFold(strings.TrimSpace(target.Reasoning.Mode), reasoningModeAlwaysOn) || defaultThinkingEnabled(target) {
			// Do not expose unrequested thinking or silently change the default path.
			return "anthropic-bridge-reasoning-unsupported"
		}
	}
	if reason := responsesToChatBridgeFilterReason(target, req, callerDialect, outDialect); reason != "" {
		return reason
	}
	if reason := chatToResponsesBridgeFilterReason(target, req, callerDialect, outDialect); reason != "" {
		return reason
	}
	if reason := anthropicInboundDialectFilterReason(target, callerDialect, outDialect); reason != "" {
		return reason
	}
	if len(support.SupportedInboundDialects) > 0 && !stringSliceContainsNormalizedDialect(support.SupportedInboundDialects, callerDialect) {
		return "request-shape-dialect-unsupported"
	}
	for _, modality := range support.RequiredInputModalities {
		if !stringSliceContains(requestInputModalities(req), modality) {
			return "request-shape-required-input-modality"
		}
	}
	for _, feature := range support.UnsupportedRequestFeatures {
		normalized := strings.ToLower(strings.TrimSpace(feature))
		// Translated Chat omits store for providers that reject it, relying on
		// their declared default; Messages does not have this response resource.
		if normalized == "store" && normalizeDialect(callerDialect) == "openai-responses" {
			switch normalizeDialect(outDialect) {
			case "openai-chat", "anthropic":
				continue
			case "openai-responses":
				// The native encoder forwards even null, and a force policy
				// inserts false even when the caller omitted the field.
				if target.ForceStoreFalse || (req != nil && rawKeyPresent(req.Raw, "store")) {
					return "request-shape-unsupported-feature"
				}
			}
		}
		// An explicit OpenAI none is removed only when translating to
		// Anthropic. Native/OpenAI-bridged requests still carry that field,
		// so their unsupported-feature declarations must be honored.
		if normalized == "reasoning" && outDialect == "anthropic" && req != nil && req.Reasoning.Disabled && strings.HasPrefix(req.Reasoning.Source, "openai_") {
			continue
		}
		if requestFeaturePresent(req, normalized) {
			return "request-shape-unsupported-feature"
		}
	}
	if support.SupportsLargeCodingAgentPayloads != nil && !*support.SupportsLargeCodingAgentPayloads && largeCodingAgentPayload(req, estimate) {
		return "request-shape-unvalidated-large-agent-payload"
	}
	if fit.MaxRequestBytes > 0 && estimate.RequestBytes > fit.MaxRequestBytes {
		return "request-shape-max-request-bytes"
	}
	if fit.MaxEstimatedInputTokens > 0 && estimate.EstimatedTotalInputTokens > fit.MaxEstimatedInputTokens {
		return "request-shape-max-input-tokens"
	}
	if fit.MinRequestedOutputTokens > 0 && req != nil && req.MaxTokens > 0 && estimate.RequestedOutputCapTokens < fit.MinRequestedOutputTokens {
		return "request-shape-min-output-tokens"
	}
	if fit.MaxRequestedOutputTokens > 0 && estimate.RequestedOutputCapTokens > fit.MaxRequestedOutputTokens {
		return "request-shape-max-output-tokens"
	}
	if fit.MaxToolSchemaBytes > 0 && fit.ToolSchemaBytes > fit.MaxToolSchemaBytes {
		return "request-shape-tool-schema-bytes"
	}
	if !fit.ContextFit {
		return "request-shape-context-exceeded"
	}
	_ = outDialect
	return ""
}

func stringSliceContainsAll(values, required []string) bool {
	for _, value := range required {
		if !stringSliceContains(values, value) {
			return false
		}
	}
	return true
}

func anthropicInboundDialectFilterReason(target Target, callerDialect, outDialect string) string {
	if normalizeDialect(callerDialect) != "anthropic" || normalizeDialect(outDialect) == "anthropic" {
		return ""
	}
	if !stringSliceContainsNormalizedDialect(target.RequestShapeSupport.SupportedInboundDialects, "anthropic") {
		return "request-shape-dialect-unsupported"
	}
	if strings.ToLower(strings.TrimSpace(target.RequestShapeSupport.ValidationStatus)) != "passed" {
		return "request-shape-validation-unpassed"
	}
	return ""
}

func stringSliceContainsNormalizedDialect(values []string, want string) bool {
	want = normalizeDialect(want)
	for _, value := range values {
		if normalizeDialect(value) == want {
			return true
		}
	}
	return false
}

func requestFeaturePresent(req *IRRequest, feature string) bool {
	if req == nil {
		return false
	}
	switch feature {
	case "tools":
		return len(req.Tools) > 0
	case "tool_choice":
		return rawKeyPresent(req.Raw, "tool_choice")
	case "forced_tool_choice":
		return requestHasForcedToolChoice(req)
	case "structured_output", "response_format":
		return requestHasStructuredOutput(req)
	case "previous_response_id":
		return rawValuePresent(req.Raw, "previous_response_id")
	case "function_call_output":
		_, count := countToolOutputs(req.Raw, req)
		return count > 0
	case "tool_result":
		count, _ := countToolOutputs(req.Raw, req)
		return count > 0
	case "include":
		return rawValuePresent(req.Raw, "include")
	case "truncation":
		return rawValuePresent(req.Raw, "truncation")
	case "metadata":
		return rawValuePresent(req.Raw, "metadata")
	case "store":
		return rawValuePresent(req.Raw, "store")
	case "reasoning":
		return requestRequiresReasoning(req) || rawValuePresent(req.Raw, "reasoning") || rawValuePresent(req.Raw, "thinking") || rawValuePresent(req.Raw, "reasoning_effort")
	case "image":
		return requestHasImages(req)
	case "audio":
		return rawContainsModality(req.Raw, "audio")
	case "video":
		return rawContainsModality(req.Raw, "video")
	case "stream":
		return req.Stream
	case "stream_options":
		return rawValuePresent(req.Raw, "stream_options")
	default:
		return false
	}
}

func requestTokenEstimateForTargetDialect(req *IRRequest, outDialect string, estimate requestTokenEstimateLogRecord) requestTokenEstimateLogRecord {
	outputCap := trafficShapeOutputReservation(req, outDialect)
	if outputCap == estimate.RequestedOutputCapTokens {
		return estimate
	}
	estimate.RequestedOutputCapTokens = outputCap
	estimate.TotalReservedTokens = estimate.EstimatedTotalInputTokens + outputCap
	if outputCap > 0 && (req == nil || req.MaxTokens <= 0) {
		estimate.RequestedOutputCapField = "router_default"
		estimate.RouterDefaultOutputCapApplied = true
	} else if req != nil {
		estimate.RequestedOutputCapField = req.MaxTokensField
		estimate.RouterDefaultOutputCapApplied = false
	}
	return estimate
}

func largeCodingAgentPayload(req *IRRequest, estimate requestTokenEstimateLogRecord) bool {
	if req == nil {
		return false
	}
	if estimate.RequestBytes > 256*1024 || estimate.EstimatedTotalInputTokens > 32768 {
		return true
	}
	if jsonValueLen(req.Tools) > 64*1024 || len(req.Tools) > 16 {
		return true
	}
	toolResults, functionOutputs := countToolOutputs(req.Raw, req)
	return toolResults+functionOutputs > 8
}

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
	reason := requestShapeFilterReason(target, req, callerDialect, outDialect, estimate, fit)
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

func requestShapeFilterReason(target Target, req *IRRequest, callerDialect, outDialect string, estimate requestTokenEstimateLogRecord, fit targetRequestShapeFit) string {
	support := target.RequestShapeSupport
	if anthropicStreamBridge(callerDialect, outDialect) && (requestRequiresReasoning(req) || target.Reasoning.DefaultOn || strings.EqualFold(target.Reasoning.Mode, reasoningModeAlwaysOn) || defaultThinkingEnabled(target)) {
		return "anthropic-bridge-reasoning-unsupported"
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
	if feature := firstUnsupportedRequestFeature(req, support.UnsupportedRequestFeatures); feature != "" {
		return "request-shape-unsupported-feature"
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

func firstUnsupportedRequestFeature(req *IRRequest, unsupported []string) string {
	for _, feature := range unsupported {
		normalized := strings.ToLower(strings.TrimSpace(feature))
		if requestFeaturePresent(req, normalized) {
			return normalized
		}
	}
	return ""
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

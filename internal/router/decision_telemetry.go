// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/metrum-ai/router/internal/buildinfo"
)

func (s *Service) decisionTelemetryEnabled() bool {
	return s != nil && s.cfg != nil && s.cfg.Server.DecisionTelemetry.Enabled
}

func (s *Service) recordDecisionShape(rc *requestContext, req *IRRequest, callerDialect string) {
	if !s.decisionTelemetryEnabled() || rc == nil || req == nil {
		return
	}
	addBoolFeature := func(name string, value bool) {
		rc.rec.DecisionShapeFeatures = append(rc.rec.DecisionShapeFeatures, decisionShapeFeatureLogRecord{Seq: len(rc.rec.DecisionShapeFeatures) + 1, Name: name, BoolValue: value})
	}
	addIntFeature := func(name string, value int) {
		rc.rec.DecisionShapeFeatures = append(rc.rec.DecisionShapeFeatures, decisionShapeFeatureLogRecord{Seq: len(rc.rec.DecisionShapeFeatures) + 1, Name: name, IntValue: value})
	}
	addTextFeature := func(name, value string) {
		rc.rec.DecisionShapeFeatures = append(rc.rec.DecisionShapeFeatures, decisionShapeFeatureLogRecord{Seq: len(rc.rec.DecisionShapeFeatures) + 1, Name: name, TextValue: value})
	}
	addTextFeature("caller_dialect", callerDialect)
	addBoolFeature("stream", req.Stream)
	addBoolFeature("has_tools", len(req.Tools) > 0)
	addIntFeature("tools_count", len(req.Tools))
	addBoolFeature("has_images", requestHasImages(req))
	addIntFeature("image_count", requestImageCount(req))
	addBoolFeature("structured_output", requestHasStructuredOutput(req))
	addBoolFeature("max_tokens_set", req.MaxTokens > 0)
	addTextFeature("max_tokens_field", req.MaxTokensField)
	addTextFeature("max_token_bucket", maxTokenBucket(req.MaxTokens))
	addTextFeature("input_token_bucket", contextBucket(estimateTokens(req)))
	addBoolFeature("reasoning_requested", req.Reasoning.Requested)
	addBoolFeature("reasoning_disabled", req.Reasoning.Disabled)
	addTextFeature("reasoning_kind", req.Reasoning.Kind)
	addTextFeature("reasoning_effort", req.Reasoning.Effort)
	addIntFeature("reasoning_budget_tokens", req.Reasoning.BudgetTokens)
	addTextFeature("reasoning_source", req.Reasoning.Source)
	addBoolFeature("cacheable", cacheable(req))
}

func (s *Service) recordRoutingReproducibility(rc *requestContext, groupName string, group ModelGroup) {
	if rc == nil {
		return
	}
	info := buildinfo.Current()
	rc.rec.RouterVersion = info.Version
	rc.rec.RouterBuildDate = info.BuildDate
	if s == nil || s.cfg == nil {
		return
	}
	rc.rec.RoutingConfigFingerprint = routingConfigFingerprint(s.cfg)
	rc.rec.ModelGroupConfigFingerprint = modelGroupConfigFingerprint(groupName, group)
	rc.rec.RoutingPolicyFingerprint = routingPolicyFingerprint(s.cfg, groupName, group)
	rc.rec.PricingCatalogFingerprint = pricingCatalogFingerprint(s.cfg)
}

func (s *Service) recordEligibilityTelemetry(rc *requestContext, groupName string, group ModelGroup, req *IRRequest, callerDialect string) {
	if !s.decisionTelemetryEnabled() || rc == nil || req == nil {
		return
	}
	cfg := s.cfg.Server.DecisionTelemetry
	maxCandidates := cfg.MaxCandidates
	if maxCandidates <= 0 {
		maxCandidates = 64
	}
	maxReasons := cfg.MaxFilterReasons
	if maxReasons <= 0 {
		maxReasons = 256
	}
	recordCandidates := cfg.RecordCandidates == nil || *cfg.RecordCandidates
	if !recordCandidates {
		return
	}
	estimate := s.requestTokenEstimateForSelection(rc, req, callerDialect)
	for i, target := range group.Targets {
		if len(rc.rec.DecisionCandidates) >= maxCandidates {
			break
		}
		outDialect := targetDialect(s.cfg.Provider[target.Provider], target)
		fit := s.targetRequestShapeFit(target, req, callerDialect, outDialect, estimate)
		reasons := s.requestFilterReasons(target, req, callerDialect, outDialect, estimate)
		if len(reasons) == 0 {
			reason := s.contractFilterReason(groupName, group, target, req, callerDialect, outDialect)
			if reason != "" {
				reasons = append(reasons, reason)
			}
		}
		candidateIndex := len(rc.rec.DecisionCandidates)
		rc.rec.DecisionCandidates = append(rc.rec.DecisionCandidates, decisionCandidateLogRecord{
			CandidateIndex:              candidateIndex,
			GroupTargetIndex:            i,
			Provider:                    target.Provider,
			Model:                       target.Model,
			ModelRef:                    target.ModelRef,
			Dialect:                     outDialect,
			Weight:                      target.Weight,
			ToolOnly:                    target.ToolOnly,
			ContextTokens:               target.ContextTokens,
			MaxEstimatedInputTokens:     fit.MaxEstimatedInputTokens,
			MaxRequestedOutputTokens:    fit.MaxRequestedOutputTokens,
			MaxRequestBytes:             fit.MaxRequestBytes,
			MaxToolSchemaBytes:          fit.MaxToolSchemaBytes,
			EstimatedTotalInputTokens:   fit.Estimate.EstimatedTotalInputTokens,
			RequestedOutputCapTokens:    fit.Estimate.RequestedOutputCapTokens,
			EstimatedTotalWithOutputCap: fit.EstimatedTotalWithOutputCap,
			RequestBytes:                fit.Estimate.RequestBytes,
			ToolSchemaBytes:             fit.ToolSchemaBytes,
			ContextHeadroomTokens:       fit.ContextHeadroomTokens,
			ContextFit:                  fit.ContextFit,
			RequestBytesFit:             fit.RequestBytesFit,
			ToolSchemaFit:               fit.ToolSchemaFit,
			EligibilityDecision:         fit.EligibilityDecision,
			EligibilityReason:           fit.EligibilityReason,
			InputImage:                  targetSupportsInputModalities(target, []string{"image"}),
			OutputImage:                 stringSliceContains(defaultModalities(target.OutputModalities), "image"),
			ToolSupport:                 targetSupportsTools(target, outDialect),
			ForcedToolChoice:            targetSupportsClientTools(target, outDialect, true),
			StructuredOutput:            targetSupportsStructuredOutput(target, callerDialect, outDialect, true),
			HonorsMaxTokens:             target.HonorsMaxTokens == nil || *target.HonorsMaxTokens,
			ReasoningSupport:            targetSupportsReasoning(target),
			ReasoningMode:               target.Reasoning.Mode,
			ReasoningControl:            target.Reasoning.Control,
			ReasoningDefault:            target.Reasoning.DefaultOn || defaultThinkingEnabled(target),
			ReasoningStream:             target.Reasoning.StreamBlock,
			ValidationStatus:            decisionCandidateValidationStatus(target.Validation),
			ValidationAge:               validationAgeBucket(target.Validation, time.Now().UTC()),
			Eligible:                    len(reasons) == 0,
		})
		for _, reason := range reasons {
			if len(rc.rec.DecisionFilterReasons) >= maxReasons {
				break
			}
			rc.rec.DecisionFilterReasons = append(rc.rec.DecisionFilterReasons, decisionFilterReasonLogRecord{
				Seq:            len(rc.rec.DecisionFilterReasons) + 1,
				CandidateIndex: candidateIndex,
				Stage:          filterReasonStage(reason),
				Reason:         reason,
			})
		}
	}
}

func (s *Service) requestFilterReasons(target Target, req *IRRequest, callerDialect, outDialect string, estimate requestTokenEstimateLogRecord) []string {
	requiredModalities := requestInputModalities(req)
	requiresStructuredOutput := requestHasStructuredOutput(req)
	var reasons []string
	bridge := isChatToResponsesBridge(callerDialect, outDialect, target) || isResponsesToChatBridge(callerDialect, outDialect, target)
	bridgeDialectPair := isChatToResponsesDialectPair(callerDialect, outDialect) ||
		(normalizeDialect(callerDialect) == "openai-responses" && normalizeDialect(outDialect) == "openai-chat")
	anthropicDialectReason := anthropicInboundDialectFilterReason(target, callerDialect, outDialect)
	if anthropicDialectReason != "" {
		reasons = append(reasons, anthropicDialectReason)
	}
	if len(req.Tools) == 0 {
		if target.ToolOnly {
			reasons = append(reasons, "tool-only-target")
		}
	} else {
		if !toolPassthrough(callerDialect, outDialect, req) && !bridge {
			reasons = append(reasons, "dialect-tool-passthrough")
		}
		if !targetSupportsToolsForCallerDialect(target, callerDialect, outDialect) {
			reasons = append(reasons, "tool-support")
		}
	}
	if len(req.Tools) == 0 && callerDialect != outDialect && !bridge && !bridgeDialectPair && anthropicDialectReason == "" {
		reasons = append(reasons, "dialect-unsupported")
	}
	for _, modality := range requiredModalities {
		if !targetSupportsInputModalities(target, []string{modality}) {
			reasons = append(reasons, "input-modality-"+safeReasonToken(modality))
		}
	}
	if !targetSupportsStructuredOutput(target, callerDialect, outDialect, requiresStructuredOutput) {
		reasons = append(reasons, "structured-output-support")
	}
	if reason := reasoningFilterReason(target, outDialect, req); reason != "" {
		reasons = append(reasons, reason)
	}
	if !targetHonorsExplicitMaxTokens(target, req) {
		reasons = append(reasons, "max-tokens-honored")
	}
	if reason := s.targetRequestShapeFit(target, req, callerDialect, outDialect, estimate).FilterReason; reason != "" {
		reasons = append(reasons, reason)
	}
	return reasons
}

func (s *Service) contractFilterReason(groupName string, group ModelGroup, target Target, req *IRRequest, callerDialect, outDialect string) string {
	if group.Contract == nil {
		return ""
	}
	stats := dynamicStats{}
	if s != nil && s.observations != nil {
		stats = s.observations.stats(dynamicObservationKey(groupName, target.Provider, target.Model), s.dynamicObservationRetention(groupName))
	}
	return targetPassesContract(group.Contract, target, outDialect, callerDialect, req, stats, time.Now().UTC())
}

func filterReasonStage(reason string) string {
	if strings.HasPrefix(reason, "contract-") {
		return "contract"
	}
	return "request_shape"
}

func (s *Service) recordRoutingDecisionTelemetry(rc *requestContext, dec decision) {
	if !s.decisionTelemetryEnabled() || rc == nil {
		return
	}
	selected := candidateIndexForTarget(rc.rec.DecisionCandidates, dec.Target)
	for i := range rc.rec.DecisionCandidates {
		if rc.rec.DecisionCandidates[i].CandidateIndex == selected {
			rc.rec.DecisionCandidates[i].Selected = true
			break
		}
	}
	rc.rec.RoutingDecisions = append(rc.rec.RoutingDecisions, routingDecisionLogRecord{
		Seq:                    len(rc.rec.RoutingDecisions) + 1,
		Strategy:               dec.Strategy,
		SelectedCandidateIndex: selected,
		Provider:               dec.Target.Provider,
		Model:                  dec.Target.Model,
		Dialect:                targetDialect(s.cfg.Provider[dec.Target.Provider], dec.Target),
		FallbackCount:          len(dec.Fallbacks),
		ClassLabel:             dec.ClassLabel,
	})
	rc.rec.RoutingSignals = append(rc.rec.RoutingSignals, dec.RoutingSignals...)
	if dec.ShadowRecommended != nil {
		rc.rec.RoutingSignals = append(rc.rec.RoutingSignals, routingSignalLogRecord{
			Seq:            len(rc.rec.RoutingSignals) + 1,
			Strategy:       dec.Strategy,
			SignalName:     "shadow_recommended_candidate",
			Source:         "external_policy",
			CandidateIndex: candidateIndexForTarget(rc.rec.DecisionCandidates, *dec.ShadowRecommended),
			BoolValue:      true,
		})
	}
	for i := range dec.DynamicScoreTerms {
		if dec.DynamicScoreTerms[i].CandidateIndex < 0 {
			dec.DynamicScoreTerms[i].CandidateIndex = candidateIndexForProviderModel(rc.rec.DecisionCandidates, dec.DynamicScoreTerms[i].Provider, dec.DynamicScoreTerms[i].Model)
		}
	}
	rc.rec.DynamicScoreTerms = append(rc.rec.DynamicScoreTerms, dec.DynamicScoreTerms...)
	for i := range dec.PolicyExecutions {
		dec.PolicyExecutions[i].SelectedCandidateIndex = selected
	}
	rc.rec.PolicyExecutions = append(rc.rec.PolicyExecutions, dec.PolicyExecutions...)
}

func (s *Service) recordPolicyFailureTelemetry(rc *requestContext, err routingPolicyError) {
	if !s.decisionTelemetryEnabled() || rc == nil {
		return
	}
	execution := err.Execution
	if execution.Strategy == "" {
		execution.Strategy = "external"
	}
	if execution.PolicyKind == "" {
		execution.PolicyKind = execution.Strategy
	}
	if execution.Outcome == "" {
		execution.Outcome = "error"
	}
	if execution.SelectedCandidateIndex == 0 {
		execution.SelectedCandidateIndex = -1
	}
	if execution.ErrorClass == "" {
		execution.ErrorClass = policyErrorClass(err.Err, execution.Strategy)
	}
	if execution.ErrorMessage == "" {
		execution.ErrorMessage = safePolicyExecutionMessage(execution.ErrorClass)
	}
	if execution.TerminalErrorType == "" {
		execution.TerminalErrorType = execution.ErrorClass
	}
	execution.Seq = len(rc.rec.PolicyExecutions) + 1
	rc.rec.PolicyExecutions = append(rc.rec.PolicyExecutions, execution)
}

func (s *Service) recordCacheReasonTelemetry(rc *requestContext, status, reason string, target Target) {
	if !s.decisionTelemetryEnabled() || rc == nil {
		return
	}
	recordCacheReasons := s.cfg.Server.DecisionTelemetry.RecordCacheReasons == nil || *s.cfg.Server.DecisionTelemetry.RecordCacheReasons
	if !recordCacheReasons {
		return
	}
	rc.rec.CacheReasons = append(rc.rec.CacheReasons, cacheReasonLogRecord{
		Seq:            len(rc.rec.CacheReasons) + 1,
		Status:         status,
		Reason:         reason,
		CandidateIndex: candidateIndexForTarget(rc.rec.DecisionCandidates, target),
		Provider:       target.Provider,
		Model:          target.Model,
		Dialect:        targetDialect(s.cfg.Provider[target.Provider], target),
	})
}

func (s *Service) recordFallbackTransitionTelemetry(rc *requestContext, targets []Target, failedPos, attemptIndex int, err upstreamError) {
	if !s.decisionTelemetryEnabled() || rc == nil || failedPos < 0 || failedPos+1 >= len(targets) {
		return
	}
	failed := targets[failedPos]
	fallback := targets[failedPos+1]
	rc.rec.FallbackTransitions = append(rc.rec.FallbackTransitions, fallbackTransitionLogRecord{
		Seq:                    len(rc.rec.FallbackTransitions) + 1,
		AttemptIndex:           attemptIndex,
		FailedCandidateIndex:   candidateIndexForTarget(rc.rec.DecisionCandidates, failed),
		FallbackCandidateIndex: candidateIndexForTarget(rc.rec.DecisionCandidates, fallback),
		FailedProvider:         failed.Provider,
		FailedModel:            failed.Model,
		FailedDialect:          targetDialect(s.cfg.Provider[failed.Provider], failed),
		FallbackProvider:       fallback.Provider,
		FallbackModel:          fallback.Model,
		FallbackDialect:        targetDialect(s.cfg.Provider[fallback.Provider], fallback),
		FallbackReason:         err.Class,
		ErrorClass:             err.Class,
		Retryable:              err.Retryable,
		FallbackSucceeded:      false,
	})
}

func markFallbackTransitionSucceeded(rc *requestContext, attemptIndex int) {
	if rc == nil {
		return
	}
	for i := len(rc.rec.FallbackTransitions) - 1; i >= 0; i-- {
		if rc.rec.FallbackTransitions[i].AttemptIndex == attemptIndex-1 {
			rc.rec.FallbackTransitions[i].FallbackSucceeded = true
			return
		}
	}
}

func simpleStrategyRankingTelemetry(strategy string, targets []Target, providers map[string]ProviderConfig) []dynamicScoreTermLogRecord {
	out := make([]dynamicScoreTermLogRecord, 0, len(targets))
	for rank, target := range targets {
		termName := "configured_order"
		scoreName := "configured_order"
		value := float64(len(targets) - rank)
		switch strategy {
		case "weighted":
			termName = "configured_weight"
			scoreName = "configured_weight"
			value = float64(target.Weight)
		case "latency":
			termName = "configured_rpm_rank"
			scoreName = "configured_rpm"
			value = float64(target.RPM)
		case "cost":
			termName = "configured_cost_rank"
			scoreName = "configured_cost"
			value = float64(target.Cost)
		}
		out = append(out, dynamicScoreTermLogRecord{
			Seq:              len(out) + 1,
			CandidateIndex:   -1,
			Rank:             rank + 1,
			Provider:         target.Provider,
			Model:            target.Model,
			Dialect:          targetDialect(providers[target.Provider], target),
			TermName:         termName,
			ScoreName:        scoreName,
			Value:            value,
			FinalScore:       value,
			ValueBucket:      scoreValueBucket(value),
			FinalScoreBucket: scoreValueBucket(value),
			Selected:         rank == 0,
		})
	}
	return out
}

func policyOutputRankingTelemetry(strategy string, target Target, fallbacks []Target, providers map[string]ProviderConfig) []dynamicScoreTermLogRecord {
	targets := append([]Target{target}, fallbacks...)
	out := make([]dynamicScoreTermLogRecord, 0, len(targets))
	for rank, tgt := range targets {
		termName := "policy_output_primary"
		if rank > 0 {
			termName = "policy_output_fallback"
		}
		value := float64(len(targets) - rank)
		out = append(out, dynamicScoreTermLogRecord{
			Seq:              len(out) + 1,
			CandidateIndex:   -1,
			Rank:             rank + 1,
			Provider:         tgt.Provider,
			Model:            tgt.Model,
			Dialect:          targetDialect(providers[tgt.Provider], tgt),
			TermName:         termName,
			ScoreName:        "policy_output_rank",
			Value:            value,
			FinalScore:       value,
			ValueBucket:      scoreValueBucket(value),
			FinalScoreBucket: scoreValueBucket(value),
			Selected:         rank == 0,
		})
	}
	return out
}

func policyFailure(group, strategy, kind, terminal string, err error, durationMS int64, eligibleTargets, allTargets int) routingPolicyError {
	class := policyErrorClass(err, strategy)
	if terminal != "" {
		class = terminal
	}
	message := ""
	if err != nil {
		message = err.Error()
	}
	callerMessage := message
	if strategy == "script" {
		callerMessage = safePolicyExecutionMessage(class)
	}
	return routingPolicyError{
		Group:   group,
		Message: callerMessage,
		Err:     err,
		Execution: policyExecutionLogRecord{
			Seq:                    1,
			Strategy:               strategy,
			PolicyKind:             kind,
			Outcome:                "error",
			DurationMS:             durationMS,
			EligibleTargetCount:    eligibleTargets,
			AllTargetCount:         allTargets,
			SelectedCandidateIndex: -1,
			ErrorClass:             class,
			ErrorMessage:           safePolicyExecutionMessage(class),
			TerminalErrorType:      class,
		},
	}
}

func safePolicyExecutionMessage(class string) string {
	switch {
	case strings.Contains(class, "timeout"):
		return "routing policy timed out before selection"
	case strings.Contains(class, "invalid-target"):
		return "routing policy returned a target outside the eligible set"
	case strings.Contains(class, "no-target"):
		return "routing policy produced no usable target"
	case strings.Contains(class, "invalid-response"):
		return "routing policy returned an invalid response"
	case strings.Contains(class, "http-error"):
		return "routing policy request failed"
	case strings.Contains(class, "load"):
		return "routing policy failed to load"
	case strings.Contains(class, "runtime"):
		return "routing policy runtime error before selection"
	default:
		return "routing policy failed before selection"
	}
}

func policyErrorClass(err error, strategy string) string {
	if err == nil {
		return strings.Trim(strings.ToLower(strategy)+"-policy-error", "-")
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "timed out") || strings.Contains(text, "timeout"):
		return strings.Trim(strings.ToLower(strategy)+"-policy-timeout", "-")
	case strings.Contains(text, "targetindex") || strings.Contains(text, "target index") || strings.Contains(text, "invalid target") || strings.Contains(text, "outside"):
		return strings.Trim(strings.ToLower(strategy)+"-policy-invalid-target", "-")
	case strings.Contains(text, "no target") || strings.Contains(text, "missing target"):
		return strings.Trim(strings.ToLower(strategy)+"-policy-no-target", "-")
	case strings.Contains(text, "not json") || strings.Contains(text, "invalid json"):
		return strings.Trim(strings.ToLower(strategy)+"-policy-invalid-response", "-")
	case strings.Contains(text, "http ") || strings.Contains(text, "request failed") || strings.Contains(text, "host ") || strings.Contains(text, "egress"):
		return strings.Trim(strings.ToLower(strategy)+"-policy-http-error", "-")
	case strings.Contains(text, "run ") || strings.Contains(text, "route(ctx)") || strings.Contains(text, "typescript"):
		return strings.Trim(strings.ToLower(strategy)+"-policy-runtime-error", "-")
	default:
		return strings.Trim(strings.ToLower(strategy)+"-policy-error", "-")
	}
}

func candidateIndexForTarget(candidates []decisionCandidateLogRecord, target Target) int {
	for _, candidate := range candidates {
		if candidate.Provider == target.Provider && candidate.Model == target.Model && candidate.ModelRef == target.ModelRef {
			return candidate.CandidateIndex
		}
	}
	return -1
}

func candidateIndexForProviderModel(candidates []decisionCandidateLogRecord, provider, model string) int {
	for _, candidate := range candidates {
		if candidate.Provider == provider && candidate.Model == model {
			return candidate.CandidateIndex
		}
	}
	return -1
}

func routingConfigFingerprint(cfg *Config) string {
	if cfg == nil {
		return ""
	}
	providers := map[string]any{}
	for name, provider := range cfg.Provider {
		providers[name] = map[string]any{
			"dialect":     normalizeDialect(provider.Dialect),
			"auth_scheme": normalizeAuthScheme(provider.AuthScheme),
			"model_count": len(provider.Models),
		}
	}
	groups := map[string]any{}
	for name, group := range cfg.Models {
		groups[name] = redactedModelGroupFingerprintPayload(name, group, false)
	}
	return canonicalFingerprint(map[string]any{
		"default_model_group": cfg.Server.DefaultModelGroup,
		"providers":           providers,
		"models":              groups,
	})
}

func modelGroupConfigFingerprint(groupName string, group ModelGroup) string {
	return canonicalFingerprint(redactedModelGroupFingerprintPayload(groupName, group, false))
}

func routingPolicyFingerprint(cfg *Config, groupName string, group ModelGroup) string {
	payload := map[string]any{
		"group":    groupName,
		"strategy": strings.ToLower(strings.TrimSpace(group.Strategy)),
	}
	switch strings.ToLower(strings.TrimSpace(group.Strategy)) {
	case "dynamic_score":
		payload["dynamic_score"] = group.RoutingPolicy.DynamicScore
	case "script":
		payload["script_sha256"] = scriptContentFingerprint(cfg, group.Script)
		payload["script_http"] = map[string]any{
			"enabled":            group.ScriptHTTP.Enabled,
			"allow_hosts_count":  len(group.ScriptHTTP.AllowHosts),
			"allow_http":         group.ScriptHTTP.AllowHTTP,
			"timeout_ms":         group.ScriptHTTP.TimeoutMS,
			"max_response_bytes": group.ScriptHTTP.MaxResponseBytes,
			"headers_count":      len(group.ScriptHTTP.Headers),
		}
	case "external":
		payload["external_policy"] = map[string]any{
			"method":             strings.ToUpper(strings.TrimSpace(group.ExternalPolicy.Method)),
			"mode":               defaultString(strings.ToLower(strings.TrimSpace(group.ExternalPolicy.Mode)), "enforce"),
			"allow_hosts_count":  len(group.ExternalPolicy.AllowHosts),
			"allow_http":         group.ExternalPolicy.AllowHTTP,
			"timeout_ms":         group.ExternalPolicy.TimeoutMS,
			"max_response_bytes": group.ExternalPolicy.MaxResponseBytes,
			"include_request":    group.ExternalPolicy.IncludeRequest,
			"on_error":           strings.ToLower(strings.TrimSpace(group.ExternalPolicy.OnError)),
			"headers_count":      len(group.ExternalPolicy.Headers),
		}
	default:
		payload["target_count"] = len(group.Targets)
	}
	return canonicalFingerprint(payload)
}

func pricingCatalogFingerprint(cfg *Config) string {
	if cfg == nil {
		return ""
	}
	providers := map[string]any{}
	for providerName, provider := range cfg.Provider {
		models := map[string]any{}
		for modelRef, model := range provider.Models {
			models[modelRef] = map[string]any{
				"model":                                    model.Model,
				"input_price_per_million_usd":              model.InputPricePerMillionUSD,
				"output_price_per_million_usd":             model.OutputPricePerMillionUSD,
				"cached_input_price_per_million_usd":       model.CachedInputPricePerMillionUSD,
				"image_input_price_per_million_tokens_usd": model.ImageInputPricePerMillionTokensUSD,
				"image_input_price_per_image_usd":          model.ImageInputPricePerImageUSD,
				"pricing_source":                           model.PricingSource,
				"pricing_updated_at":                       model.PricingUpdatedAt,
			}
		}
		providers[providerName] = models
	}
	return canonicalFingerprint(providers)
}

func redactedModelGroupFingerprintPayload(groupName string, group ModelGroup, includePolicy bool) map[string]any {
	targets := make([]map[string]any, 0, len(group.Targets))
	for _, target := range group.Targets {
		targets = append(targets, map[string]any{
			"provider":                           target.Provider,
			"model":                              target.Model,
			"model_ref":                          target.ModelRef,
			"dialect":                            normalizeDialect(target.Dialect),
			"region":                             target.Region,
			"weight":                             target.Weight,
			"tool_only":                          target.ToolOnly,
			"rpm":                                target.RPM,
			"cost":                               target.Cost,
			"tier":                               target.Tier,
			"tags":                               append([]string(nil), target.Tags...),
			"context_tokens":                     target.ContextTokens,
			"input_modalities":                   append([]string(nil), target.InputModalities...),
			"output_modalities":                  append([]string(nil), target.OutputModalities...),
			"tool_support":                       target.ToolSupport,
			"honors_max_tokens":                  target.HonorsMaxTokens,
			"input_price_per_million_usd":        target.InputPricePerMillionUSD,
			"output_price_per_million_usd":       target.OutputPricePerMillionUSD,
			"cached_input_price_per_million_usd": target.CachedInputPricePerMillionUSD,
			"image_input_price_per_million_tokens_usd": target.ImageInputPricePerMillionTokensUSD,
			"image_input_price_per_image_usd":          target.ImageInputPricePerImageUSD,
			"pricing_source":                           target.PricingSource,
			"pricing_updated_at":                       target.PricingUpdatedAt,
			"validation_status":                        validationStatusForFingerprint(target.Validation),
			"validation_workload":                      validationWorkloadForFingerprint(target.Validation),
			"validation_quality_score":                 validationQualityForFingerprint(target.Validation),
			"validation_pass_rate":                     validationPassRateForFingerprint(target.Validation),
		})
	}
	payload := map[string]any{
		"group":              groupName,
		"strategy":           strings.ToLower(strings.TrimSpace(group.Strategy)),
		"attempt_timeout_ms": group.AttemptTimeoutMS,
		"targets":            targets,
		"contract":           group.Contract,
		"pii_filter": map[string]any{
			"enabled":                      group.PIIFilter.Enabled,
			"mode":                         normalizePIIFilterMode(group.PIIFilter),
			"fail_on_match":                group.PIIFilter.FailOnMatch,
			"restore_response_configured":  group.PIIFilter.RestoreResponse != nil,
			"max_replacements_per_request": group.PIIFilter.MaxReplacementsPerRequest,
			"rule_count":                   len(group.PIIFilter.Rules),
			"apply_to_image_urls":          group.PIIFilter.ApplyTo.ImageURLs,
		},
	}
	if includePolicy {
		payload["routing_policy"] = group.RoutingPolicy
	}
	return payload
}

func validationStatusForFingerprint(validation *TargetValidation) string {
	if validation == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(validation.Status))
}

func validationWorkloadForFingerprint(validation *TargetValidation) string {
	if validation == nil {
		return ""
	}
	return strings.TrimSpace(validation.Workload)
}

func validationQualityForFingerprint(validation *TargetValidation) float64 {
	if validation == nil {
		return 0
	}
	return validation.QualityScore
}

func validationPassRateForFingerprint(validation *TargetValidation) float64 {
	if validation == nil {
		return 0
	}
	return validation.PassRate
}

func scriptContentFingerprint(cfg *Config, scriptPath string) string {
	resolved := strings.TrimSpace(scriptPath)
	if resolved == "" {
		return ""
	}
	if cfg != nil && cfg.baseDir != "" && !strings.HasPrefix(resolved, "/") {
		resolved = cfg.baseDir + "/" + resolved
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return "unavailable"
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func canonicalFingerprint(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func decisionCandidateValidationStatus(validation *TargetValidation) string {
	if validation == nil {
		return "missing"
	}
	return defaultString(strings.ToLower(strings.TrimSpace(validation.Status)), "missing")
}

func cacheBypassReason(req *IRRequest) string {
	if req == nil {
		return "cache-request-missing"
	}
	switch {
	case req.Stream:
		return "cache-streaming"
	case req.NoCache:
		return "cache-request-no-cache"
	case len(req.Tools) > 0:
		return "cache-tool-request"
	case requestHasImages(req):
		return "cache-image-request"
	case requestHasStructuredOutput(req):
		return "cache-structured-output"
	case req.Temperature != nil && *req.Temperature > 0:
		return "cache-temperature"
	default:
		return "cache-eligible"
	}
}

func safeReasonToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

func safeOptionalReasonToken(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return safeReasonToken(value)
}

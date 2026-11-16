// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"strings"
	"time"
)

type contractFilterResult struct {
	targets []Target
	reason  string
}

func (s *Service) targetsForContract(groupName string, group ModelGroup, targets []Target, req *IRRequest, callerDialect string) contractFilterResult {
	if group.Contract == nil && group.VirtualModel == nil {
		return contractFilterResult{targets: targets}
	}
	now := time.Now().UTC()
	out := make([]Target, 0, len(targets))
	firstReason := ""
	for _, target := range targets {
		outDialect := ""
		if s != nil && s.cfg != nil {
			outDialect = targetDialect(s.cfg.Provider[target.Provider], target)
		}
		stats := dynamicStats{}
		if s != nil && s.observations != nil {
			stats = s.observations.stats(dynamicObservationKey(groupName, target.Provider, target.Model), s.dynamicObservationRetention(groupName))
		}
		reason := virtualModelTargetReason(group.VirtualModel, target)
		if reason == "" {
			reason = targetPassesContract(group.Contract, target, outDialect, callerDialect, req, stats, now)
		}
		if reason == "" {
			out = append(out, target)
			continue
		}
		if firstReason == "" {
			firstReason = reason
		}
	}
	return contractFilterResult{targets: out, reason: firstReason}
}

func virtualModelTargetReason(config *VirtualModelConfig, target Target) string {
	if config == nil {
		return ""
	}
	if target.Validation == nil || !strings.EqualFold(strings.TrimSpace(target.Validation.Status), "passed") || target.Validation.Harness != "eval-infra" {
		return "virtual-model-no-eval-vector"
	}
	vector, ok := target.Validation.Capabilities[config.TaskFamily]
	if !ok || vector.TaskFamily != config.TaskFamily || vector.EvalRunID == "" || vector.EvalReportSHA256 == "" {
		return "virtual-model-no-eval-vector"
	}
	if config.MinQualityScore != nil && vector.QualityScore < *config.MinQualityScore {
		return "virtual-model-quality-floor"
	}
	if config.MinToolReliability != nil && (vector.ToolReliability == nil || *vector.ToolReliability < *config.MinToolReliability) {
		return "virtual-model-tool-reliability"
	}
	if vector.ContextTokens < config.MinContextTokens {
		return "virtual-model-context-floor"
	}
	if config.MaxP95LatencyMS != nil && vector.P95LatencyMS > *config.MaxP95LatencyMS {
		return "virtual-model-latency-ceiling"
	}
	if config.MaxCostUSD != nil && vector.MeanCostUSD > *config.MaxCostUSD {
		return "virtual-model-cost-ceiling"
	}
	return ""
}

func targetPassesContract(contract *ModelGroupContract, target Target, outDialect, callerDialect string, req *IRRequest, stats dynamicStats, now time.Time) string {
	if contract == nil {
		return ""
	}
	if len(contract.SupportedAPIShapes) > 0 && !contractSupportsAPIShape(contract, callerDialect) {
		return "contract-required-api-shape"
	}
	caps := contract.RequiredCaps
	if caps.Tools && !targetSupportsClientTools(target, outDialect, false) {
		return "contract-required-tools"
	}
	if caps.ForcedToolChoice && !targetSupportsClientTools(target, outDialect, true) {
		return "contract-required-tools"
	}
	if caps.StructuredOutputs && !targetSupportsStructuredOutput(target, callerDialect, outDialect, true) {
		return "contract-required-structured-outputs"
	}
	if caps.Reasoning && !targetSupportsReasoning(target) {
		return "contract-required-reasoning"
	}
	if len(caps.InputModalities) > 0 && !targetSupportsInputModalities(target, caps.InputModalities) {
		return "contract-required-modality"
	}
	if len(caps.OutputModalities) > 0 && !targetSupportsOutputModalities(target, caps.OutputModalities) {
		return "contract-required-modality"
	}
	if caps.MinContextTokens > 0 && target.ContextTokens < caps.MinContextTokens {
		return "contract-required-context"
	}
	if caps.HonorsMaxTokensWhenCallerCapped && req != nil && req.MaxTokens > 0 && !targetHonorsExplicitMaxTokens(target, req) {
		return "contract-required-max-tokens"
	}
	qf := contract.QualityFloor
	if len(qf.RequireTags) > 0 && !targetHasAllTags(target, qf.RequireTags) {
		return "contract-quality-floor"
	}
	if len(qf.AllowedValidationStatus) > 0 {
		if target.Validation == nil {
			return "contract-no-validated-target"
		}
		if !stringSliceContainsFold(qf.AllowedValidationStatus, target.Validation.Status) {
			return "contract-no-validated-target"
		}
	}
	if qf.MinEvalQualityScore != nil {
		if target.Validation == nil {
			return "contract-no-validated-target"
		}
		if target.Validation.QualityScore < *qf.MinEvalQualityScore {
			return "contract-quality-floor"
		}
	}
	if qf.MinEvalPassRate != nil {
		if target.Validation == nil {
			return "contract-no-validated-target"
		}
		if target.Validation.PassRate < *qf.MinEvalPassRate {
			return "contract-quality-floor"
		}
	}
	if qf.MaxEvalAgeDays > 0 {
		if target.Validation == nil || strings.TrimSpace(target.Validation.ValidatedAt) == "" {
			return "contract-no-validated-target"
		}
		validatedAt, err := time.Parse("2006-01-02", strings.TrimSpace(target.Validation.ValidatedAt))
		if err != nil {
			return "contract-no-validated-target"
		}
		if now.Sub(validatedAt) > time.Duration(qf.MaxEvalAgeDays)*24*time.Hour {
			return "contract-validation-expired"
		}
	}
	thresholds := DynamicScoreThresholds{
		MaxErrorRate:             contract.OperationalTargets.MaxErrorRate,
		MaxTimeoutRate:           contract.OperationalTargets.MaxTimeoutRate,
		MaxP95LatencyMS:          contract.OperationalTargets.MaxP95LatencyMS,
		MinOutputTokensPerSecond: contract.OperationalTargets.MinOutputTokensPerSecond,
	}
	if !dynamicPassesThresholds(stats, thresholds) {
		return "contract-operational-threshold"
	}
	return ""
}

func contractSupportsAPIShape(contract *ModelGroupContract, callerDialect string) bool {
	for _, shape := range contract.SupportedAPIShapes {
		if normalizeContractAPIShape(shape) == callerDialect {
			return true
		}
	}
	return false
}

func targetSupportsOutputModalities(target Target, required []string) bool {
	targetModalities := defaultModalities(target.OutputModalities)
	for _, req := range required {
		if !stringSliceContains(targetModalities, req) {
			return false
		}
	}
	return true
}

func stringSliceContainsFold(values []string, want string) bool {
	want = strings.ToLower(strings.TrimSpace(want))
	for _, value := range values {
		if strings.ToLower(strings.TrimSpace(value)) == want {
			return true
		}
	}
	return false
}

func contractWorkloadLabel(contract *ModelGroupContract) string {
	if contract == nil || !contract.Reporting.ExposeWorkloadLabels || len(contract.IntendedWorkloads) == 0 {
		return ""
	}
	return strings.TrimSpace(contract.IntendedWorkloads[0])
}

func validationAgeBucket(validation *TargetValidation, now time.Time) string {
	if validation == nil || strings.TrimSpace(validation.ValidatedAt) == "" {
		return "missing"
	}
	validatedAt, err := time.Parse("2006-01-02", strings.TrimSpace(validation.ValidatedAt))
	if err != nil {
		return "invalid"
	}
	days := int(now.Sub(validatedAt).Hours() / 24)
	switch {
	case days < 0:
		return "future"
	case days <= 7:
		return "0-7d"
	case days <= 30:
		return "8-30d"
	case days <= 90:
		return "31-90d"
	default:
		return "90d+"
	}
}

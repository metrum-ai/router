// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"math"
	"strings"
)

const (
	reasoningModeOptIn    = "opt_in"
	reasoningModeAlwaysOn = "always_on"

	reasoningControlEffortEnum  = "effort_enum"
	reasoningControlTokenBudget = "token_budget"
)

func detectReasoningIntent(dialect string, raw map[string]any) ReasoningIntent {
	switch dialect {
	case "anthropic":
		thinking, _ := raw["thinking"].(map[string]any)
		if len(thinking) == 0 {
			return ReasoningIntent{}
		}
		typ := strings.ToLower(strings.TrimSpace(stringValue(thinking["type"])))
		if typ == "disabled" {
			return ReasoningIntent{Disabled: true, Source: "anthropic_thinking", Kind: "disabled"}
		}
		if typ == "adaptive" {
			return ReasoningIntent{Requested: true, Source: "anthropic_thinking", Kind: "adaptive"}
		}
		if typ == "enabled" {
			budget, _ := numberAsInt(thinking["budget_tokens"])
			return ReasoningIntent{Requested: true, Source: "anthropic_thinking", Kind: "token_budget", BudgetTokens: budget}
		}
	case "openai-chat":
		if effort, ok := normalizeReasoningEffort(stringValue(raw["reasoning_effort"])); ok {
			return ReasoningIntent{Requested: true, Source: "openai_chat_reasoning_effort", Kind: "effort", Effort: effort}
		}
	case "openai-responses":
		reasoning, _ := raw["reasoning"].(map[string]any)
		if len(reasoning) == 0 {
			return ReasoningIntent{}
		}
		intent := ReasoningIntent{Requested: true, Source: "openai_responses_reasoning", Kind: "effort"}
		if effort, ok := normalizeReasoningEffort(stringValue(reasoning["effort"])); ok {
			intent.Effort = effort
		}
		if summary := strings.TrimSpace(stringValue(reasoning["summary"])); summary != "" {
			intent.Summary = summary
		}
		return intent
	}
	return ReasoningIntent{}
}

func normalizeReasoningEffort(value string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return "", false
	}
	return normalized, true
}

func requestRequiresReasoning(req *IRRequest) bool {
	return req != nil && req.Reasoning.Requested && !req.Reasoning.Disabled
}

func targetSupportsReasoning(target Target) bool {
	return target.Reasoning.Supported || strings.EqualFold(target.Reasoning.Mode, reasoningModeAlwaysOn) || defaultThinkingEnabled(target)
}

func defaultThinkingEnabled(target Target) bool {
	if len(target.DefaultThinking) == 0 {
		return false
	}
	return !strings.EqualFold(strings.TrimSpace(stringValue(target.DefaultThinking["type"])), "disabled")
}

func targetCanSatisfyReasoning(target Target, outDialect string, req *IRRequest) bool {
	if !requestRequiresReasoning(req) {
		return true
	}
	if !targetSupportsReasoning(target) {
		return false
	}
	foreign := (req.Reasoning.Source == "anthropic_thinking" && (outDialect == "openai-chat" || outDialect == "openai-responses")) || (strings.HasPrefix(req.Reasoning.Source, "openai_") && outDialect == "anthropic")
	if foreign {
		if target.Reasoning.RejectsTemperature && rawValuePresent(req.Raw, "temperature") {
			return false
		}
		if target.Reasoning.RejectsTopP && rawValuePresent(req.Raw, "top_p") {
			return false
		}
		if req.Reasoning.Source == "anthropic_thinking" {
			if req.Reasoning.Kind == "token_budget" && !validForeignThinkingBudget(req.Raw) {
				return false
			}
			if _, err := bridgeReasoningEffort(req.Reasoning); err != nil {
				return false
			}
		}
	}
	control := strings.ToLower(strings.TrimSpace(target.Reasoning.Control))
	switch outDialect {
	case "anthropic":
		if control != reasoningControlTokenBudget && !defaultThinkingEnabled(target) {
			return false
		}
		if strings.HasPrefix(req.Reasoning.Source, "openai_") {
			_, _, err := bridgeAnthropicReasoning(req.Reasoning, target, effectiveMaxTokens(req, 1024))
			return err == nil
		}
		return reasoningBudgetForTarget(req.Reasoning, target) > 0
	case "openai-responses", "openai-chat":
		return control == reasoningControlEffortEnum || control == reasoningControlTokenBudget
	default:
		return false
	}
}

// A translated Anthropic enabled budget is a positive integer JSON number.
// Do not coerce strings, fractions, zero or missing values into an effort.
func validForeignThinkingBudget(raw map[string]any) bool {
	thinking, _ := raw["thinking"].(map[string]any)
	switch budget := thinking["budget_tokens"].(type) {
	case float64:
		return budget > 0 && budget == math.Trunc(budget) && budget < float64(math.MaxInt)
	case int:
		return budget > 0
	case int64:
		return budget > 0 && uint64(budget) <= uint64(math.MaxInt)
	default:
		return false
	}
}

func reasoningFilterReason(target Target, outDialect string, req *IRRequest) string {
	if !requestRequiresReasoning(req) {
		return ""
	}
	if !targetSupportsReasoning(target) {
		return "reasoning-support"
	}
	if !targetCanSatisfyReasoning(target, outDialect, req) {
		return "reasoning-control"
	}
	return ""
}

func reasoningEffortForTarget(intent ReasoningIntent, target Target) string {
	if effort := strings.TrimSpace(intent.Effort); effort != "" {
		return strings.ToLower(effort)
	}
	if intent.BudgetTokens > 0 {
		return reasoningBudgetToEffort(intent.BudgetTokens)
	}
	return "medium"
}

func reasoningBudgetForTarget(intent ReasoningIntent, target Target) int {
	budget := intent.BudgetTokens
	if budget <= 0 {
		budget = reasoningEffortToBudget(intent.Effort)
	}
	if budget <= 0 {
		budget = 8192
	}
	if target.Reasoning.MinBudgetTokens > 0 && budget < target.Reasoning.MinBudgetTokens {
		budget = target.Reasoning.MinBudgetTokens
	}
	if target.Reasoning.MaxBudgetTokens > 0 && budget > target.Reasoning.MaxBudgetTokens {
		budget = target.Reasoning.MaxBudgetTokens
	}
	return budget
}

func reasoningEffortToBudget(effort string) int {
	switch effort {
	case "low":
		return 2048
	case "high":
		return 24576
	default:
		return 8192
	}
}

func reasoningBudgetToEffort(tokens int) string {
	switch {
	case tokens <= 4096:
		return "low"
	case tokens <= 16384:
		return "medium"
	default:
		return "high"
	}
}

func validateReasoningSupport(rs ReasoningSupport) error {
	mode := strings.ToLower(strings.TrimSpace(rs.Mode))
	control := strings.ToLower(strings.TrimSpace(rs.Control))
	if !rs.Supported {
		if mode != "" || control != "" || rs.DefaultOn || rs.MinBudgetTokens != 0 || rs.MaxBudgetTokens != 0 || rs.BudgetMustBeLessThanMaxTokens || rs.StreamBlock != "" || rs.RejectsMaxTokens || rs.RejectsTemperature || rs.RejectsTopP || rs.SupportsSummaries {
			return fmt.Errorf("unsupported reasoning target must not set reasoning details")
		}
		return nil
	}
	switch mode {
	case reasoningModeOptIn, reasoningModeAlwaysOn:
	default:
		return fmt.Errorf("mode must be opt_in or always_on")
	}
	switch control {
	case reasoningControlEffortEnum, reasoningControlTokenBudget:
	default:
		return fmt.Errorf("control must be effort_enum or token_budget")
	}
	if rs.MinBudgetTokens < 0 || rs.MaxBudgetTokens < 0 {
		return fmt.Errorf("budget token bounds cannot be negative")
	}
	if rs.MaxBudgetTokens > 0 && rs.MinBudgetTokens > rs.MaxBudgetTokens {
		return fmt.Errorf("max_budget_tokens must be >= min_budget_tokens")
	}
	if rs.BudgetMustBeLessThanMaxTokens && control != reasoningControlTokenBudget {
		return fmt.Errorf("budget_must_be_less_than_max_tokens requires token_budget control")
	}
	switch strings.ToLower(strings.TrimSpace(rs.StreamBlock)) {
	case "", "thinking", "reasoning_summary", "none":
	default:
		return fmt.Errorf("stream_block must be thinking, reasoning_summary, or none")
	}
	return nil
}

func reasoningSupportEmpty(rs ReasoningSupport) bool {
	return !rs.Supported && rs.Mode == "" && rs.Control == "" && !rs.DefaultOn && rs.MinBudgetTokens == 0 && rs.MaxBudgetTokens == 0 && !rs.BudgetMustBeLessThanMaxTokens && rs.StreamBlock == "" && !rs.RejectsMaxTokens && !rs.RejectsTemperature && !rs.RejectsTopP && !rs.SupportsSummaries
}

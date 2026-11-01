// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"strings"
)

// bridgeReasoningEffort converts foreign controls into the deliberately
// coarse bridge buckets. It does not imply equivalent provider semantics.
func bridgeReasoningEffort(intent ReasoningIntent) (string, error) {
	switch intent.Kind {
	case "adaptive":
		return "medium", nil
	case "token_budget":
		if intent.BudgetTokens <= 0 {
			return "", fmt.Errorf("thinking budget must be positive")
		}
		switch {
		case intent.BudgetTokens <= 1024:
			return "low", nil
		case intent.BudgetTokens <= 4096:
			return "medium", nil
		default:
			return "high", nil
		}
	case "effort":
		switch strings.ToLower(strings.TrimSpace(intent.Effort)) {
		case "", "medium":
			return "medium", nil
		case "low":
			return "low", nil
		case "high":
			return "high", nil
		default:
			return "", fmt.Errorf("reasoning effort has no supported bridge mapping")
		}
	default:
		return "", fmt.Errorf("reasoning control has no supported bridge mapping")
	}
}

// bridgeAnthropicReasoning maps OpenAI effort only. Adaptive is used solely
// when the target explicitly configures adaptive as its default thinking mode.
// For budget-based thinking, a too-small maximum or declared target bounds
// fails closed rather than silently sending an invalid or larger token cap.
func bridgeAnthropicReasoning(intent ReasoningIntent, target Target, maxTokens int) (map[string]any, string, error) {
	effort, err := bridgeReasoningEffort(intent)
	if err != nil {
		return nil, "", err
	}
	if strings.EqualFold(strings.TrimSpace(stringValue(target.DefaultThinking["type"])), "adaptive") {
		return map[string]any{"type": "adaptive"}, effort, nil
	}
	budget := map[string]int{"low": 1024, "medium": 4096, "high": 16384}[effort]
	if maxTokens <= 0 {
		return nil, "", fmt.Errorf("missing max_tokens for thinking budget")
	}
	if budget >= maxTokens {
		budget = maxTokens - 1
	}
	if target.Reasoning.MaxBudgetTokens > 0 && budget > target.Reasoning.MaxBudgetTokens {
		budget = target.Reasoning.MaxBudgetTokens
	}
	minimum := 1024
	if target.Reasoning.MinBudgetTokens > minimum {
		minimum = target.Reasoning.MinBudgetTokens
	}
	if budget < minimum {
		return nil, "", fmt.Errorf("max_tokens or target limits cannot fit a valid thinking budget")
	}
	return map[string]any{"type": "enabled", "budget_tokens": budget}, "", nil
}

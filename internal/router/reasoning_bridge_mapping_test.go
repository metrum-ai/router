// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "testing"

func TestBridgeReasoningEffortMapping(t *testing.T) {
	for _, tc := range []struct {
		intent ReasoningIntent
		want   string
	}{
		{ReasoningIntent{Kind: "adaptive"}, "medium"},
		{ReasoningIntent{Kind: "token_budget", BudgetTokens: 1024}, "low"},
		{ReasoningIntent{Kind: "token_budget", BudgetTokens: 1025}, "medium"},
		{ReasoningIntent{Kind: "token_budget", BudgetTokens: 4096}, "medium"},
		{ReasoningIntent{Kind: "token_budget", BudgetTokens: 4097}, "high"},
		{ReasoningIntent{Kind: "effort", Effort: "high"}, "high"},
	} {
		got, err := bridgeReasoningEffort(tc.intent)
		if err != nil || got != tc.want {
			t.Fatalf("intent=%+v got=%q err=%v want=%q", tc.intent, got, err, tc.want)
		}
	}
	for _, intent := range []ReasoningIntent{{Kind: "token_budget"}, {Kind: "effort", Effort: "xhigh"}, {Kind: "invalid"}} {
		if _, err := bridgeReasoningEffort(intent); err == nil {
			t.Fatalf("accepted invalid intent %+v", intent)
		}
	}
}

func TestBridgeAnthropicReasoningMapping(t *testing.T) {
	for _, tc := range []struct {
		effort string
		max    int
		budget int
	}{
		{"low", 20000, 1024},
		{"medium", 20000, 4096},
		{"high", 20000, 16384},
		{"high", 4096, 4095},
	} {
		got, _, err := bridgeAnthropicReasoning(ReasoningIntent{Kind: "effort", Effort: tc.effort}, Target{}, tc.max)
		if err != nil || got["type"] != "enabled" || got["budget_tokens"] != tc.budget {
			t.Fatalf("effort=%s max=%d got=%v err=%v", tc.effort, tc.max, got, err)
		}
	}
	for _, max := range []int{0, 1024} {
		if _, _, err := bridgeAnthropicReasoning(ReasoningIntent{Kind: "effort", Effort: "low"}, Target{}, max); err == nil {
			t.Fatalf("max=%d accepted invalid budget", max)
		}
	}
	adaptive := Target{DefaultThinking: map[string]any{"type": "adaptive"}}
	got, effort, err := bridgeAnthropicReasoning(ReasoningIntent{Kind: "effort", Effort: "high"}, adaptive, 1)
	if err != nil || got["type"] != "adaptive" || effort != "high" {
		t.Fatalf("adaptive mapping=%v effort=%s err=%v", got, effort, err)
	}
}

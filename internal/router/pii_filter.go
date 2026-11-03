// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type piiPlaceholder struct {
	Placeholder string
	Original    string
	RuleName    string
}

type piiFilterResult struct {
	Applied       bool
	Mode          string
	Replacements  int
	LimitExceeded bool
	Placeholders  []piiPlaceholder
	RuleCounts    map[string]int
	Warnings      []string
}

type compiledPIIRule struct {
	name   string
	prefix string
	re     *regexp.Regexp
}

func applyPIIFilter(req *IRRequest, cfg PIIFilterConfig, caller ...string) (piiFilterResult, error) {
	result := piiFilterResult{RuleCounts: map[string]int{}}
	if req == nil || !cfg.Enabled {
		return result, nil
	}
	mode := normalizePIIFilterMode(cfg)
	result.Mode = mode
	rules, err := compilePIIRules(cfg.Rules)
	if err != nil {
		return result, err
	}
	limit := cfg.MaxReplacementsPerRequest
	if limit <= 0 {
		limit = 200
	}
	state := &piiRedactionState{
		rules:          rules,
		limit:          limit,
		result:         &result,
		placeholderFor: map[string]string{},
	}
	apply := normalizePIIApplyTo(cfg.ApplyTo)
	// Snapshot and redact every instruction text this caller can transmit before
	// the individual raw values are filtered: a match may span parts or messages
	// even though no single raw value contains it (#645).
	callerDialect := ""
	if len(caller) > 0 {
		callerDialect = caller[0]
	}
	redactTargetInstructions(req, callerDialect, apply, state)
	if apply.system {
		req.System = state.redact(req.System)
	}
	if apply.responsesInput {
		if hasImageParts(req.InputParts) {
			for i := range req.InputParts {
				if req.InputParts[i].Type == "text" {
					req.InputParts[i].Text = state.redact(req.InputParts[i].Text)
				}
				if req.InputParts[i].Type == "image" && apply.imageURLs {
					req.InputParts[i].ImageURL = state.redact(req.InputParts[i].ImageURL)
				}
			}
			req.Input = textFromIRParts(req.InputParts)
		} else {
			req.Input = state.redact(req.Input)
			for i := range req.InputParts {
				if req.InputParts[i].Type == "text" {
					req.InputParts[i].Text = req.Input
				}
			}
		}
	}
	// A caller's `input` array is the message list for Responses requests, so the
	// responses_input scope governs those messages directly.
	responsesInputMessages := apply.responsesInput && normalizeDialect(callerDialect) == "openai-responses"
	for i := range req.Messages {
		role := strings.ToLower(strings.TrimSpace(req.Messages[i].Role))
		// Instruction-role messages are instruction text: the system scope governs
		// them even when the messages scope is disabled (#645).
		instructionRole := apply.system && (role == "system" || role == "developer")
		if !apply.messages && !instructionRole && !responsesInputMessages {
			continue
		}
		if hasImageParts(req.Messages[i].Parts) {
			for j := range req.Messages[i].Parts {
				if req.Messages[i].Parts[j].Type == "text" {
					req.Messages[i].Parts[j].Text = state.redact(req.Messages[i].Parts[j].Text)
				}
				if req.Messages[i].Parts[j].Type == "image" && apply.imageURLs {
					req.Messages[i].Parts[j].ImageURL = state.redact(req.Messages[i].Parts[j].ImageURL)
				}
			}
			req.Messages[i].Content = textFromIRParts(req.Messages[i].Parts)
		} else {
			req.Messages[i].Content = state.redact(req.Messages[i].Content)
			for j := range req.Messages[i].Parts {
				if req.Messages[i].Parts[j].Type == "text" {
					req.Messages[i].Parts[j].Text = req.Messages[i].Content
				}
			}
		}
	}
	if req.Raw != nil {
		req.Raw = redactRawPayload(req.Raw, state, apply)
	}
	if result.Replacements > 0 {
		result.Applied = true
	}
	if result.Applied && (mode == "fail_on_match" || cfg.FailOnMatch) {
		return result, piiFilterBlockedError{}
	}
	if result.LimitExceeded {
		return result, piiFilterBlockedError{}
	}
	return result, nil
}

func textFromIRParts(parts []IRContentPart) string {
	var values []string
	for _, part := range parts {
		if part.Type == "text" && part.Text != "" {
			values = append(values, part.Text)
		}
	}
	return strings.Join(values, "\n")
}

func restorePIIPlaceholders(resp *IRResponse, result piiFilterResult) {
	if resp == nil || !result.Applied || len(result.Placeholders) == 0 {
		return
	}
	resp.Text = restorePIIText(resp.Text, result.Placeholders)
	if resp.Raw != nil {
		if raw, ok := restorePIIRaw(resp.Raw, result.Placeholders).(map[string]any); ok {
			resp.Raw = raw
		}
	}
}

func piiRestoreEnabled(cfg PIIFilterConfig) bool {
	if !cfg.Enabled {
		return false
	}
	if cfg.RestoreResponse != nil {
		return *cfg.RestoreResponse
	}
	return normalizePIIFilterMode(cfg) == "redact_and_restore"
}

func normalizePIIFilterMode(cfg PIIFilterConfig) string {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode == "" {
		if cfg.FailOnMatch {
			return "fail_on_match"
		}
		if cfg.RestoreResponse != nil && *cfg.RestoreResponse {
			return "redact_and_restore"
		}
		return "redact_only"
	}
	return mode
}

func normalizePlaceholderPrefix(prefix string) string {
	prefix = strings.ToUpper(strings.TrimSpace(prefix))
	var b strings.Builder
	for _, r := range prefix {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-':
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func compilePIIRules(rules []PIIFilterRule) ([]compiledPIIRule, error) {
	out := make([]compiledPIIRule, 0, len(rules))
	for _, rule := range rules {
		re, err := regexp.Compile(rule.Expression)
		if err != nil {
			return nil, err
		}
		out = append(out, compiledPIIRule{
			name:   strings.TrimSpace(rule.Name),
			prefix: normalizePlaceholderPrefix(rule.PlaceholderPrefix),
			re:     re,
		})
	}
	return out, nil
}

type normalizedPIIApplyTo struct {
	system         bool
	messages       bool
	responsesInput bool
	toolResults    bool
	imageURLs      bool
}

func normalizePIIApplyTo(in PIIFilterApplyTo) normalizedPIIApplyTo {
	return normalizedPIIApplyTo{
		system:         boolDefault(in.System, true),
		messages:       boolDefault(in.Messages, true),
		responsesInput: boolDefault(in.ResponsesInput, true),
		toolResults:    boolDefault(in.ToolResults, true),
		imageURLs:      in.ImageURLs,
	}
}

func boolDefault(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}

type piiRedactionState struct {
	rules          []compiledPIIRule
	limit          int
	result         *piiFilterResult
	placeholderFor map[string]string
}

func (s *piiRedactionState) redact(text string) string {
	if text == "" || s == nil || s.result == nil {
		return text
	}
	for _, rule := range s.rules {
		if text == "" {
			return text
		}
		text = rule.re.ReplaceAllStringFunc(text, func(match string) string {
			if match == "" {
				return match
			}
			placeholderKey := rule.name + "\x00" + match
			if placeholder := s.placeholderFor[placeholderKey]; placeholder != "" {
				return placeholder
			}
			if s.result.Replacements >= s.limit {
				s.result.LimitExceeded = true
				s.result.Warnings = appendWarning(s.result.Warnings, "pii-filter-replacement-limit-reached")
				return match
			}
			s.result.RuleCounts[rule.name]++
			placeholder := fmt.Sprintf("[%s_%d]", rule.prefix, s.result.RuleCounts[rule.name])
			s.placeholderFor[placeholderKey] = placeholder
			s.result.Replacements++
			s.result.Placeholders = append(s.result.Placeholders, piiPlaceholder{
				Placeholder: placeholder,
				Original:    match,
				RuleName:    rule.name,
			})
			return placeholder
		})
	}
	if s.result.LimitExceeded {
		s.result.Warnings = appendWarning(s.result.Warnings, "pii-filter-replacement-limit-reached")
	}
	return text
}

func redactRawPayload(raw map[string]any, state *piiRedactionState, apply normalizedPIIApplyTo) map[string]any {
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		out[k] = redactRawValue(v, rawPIIContext{key: strings.ToLower(k)}, state, apply)
	}
	return out
}

type rawPIIContext struct {
	key           string
	inContent     bool
	inToolResult  bool
	inInstruction bool // Inside a system or developer message/item.
	inInput       bool // Inside the caller's `input` array.
}

func redactRawValue(v any, ctx rawPIIContext, state *piiRedactionState, apply normalizedPIIApplyTo) any {
	switch x := v.(type) {
	case string:
		if shouldRedactRawString(ctx, apply) {
			return state.redact(x)
		}
		return x
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			child := ctx
			if ctx.key == "content" || ctx.key == "input" {
				child.inContent = true
			}
			if ctx.key == "input" {
				child.inInput = true
			}
			// An Anthropic `system` or Responses `instructions` array carries
			// instruction text in its blocks.
			if ctx.key == "system" || ctx.key == "instructions" {
				child.inInstruction = true
			}
			out[i] = redactRawValue(item, child, state, apply)
		}
		return out
	case map[string]any:
		role := strings.ToLower(strings.TrimSpace(stringValue(x["role"])))
		childToolResult := ctx.inToolResult || role == "tool"
		childInstruction := ctx.inInstruction || role == "system" || role == "developer"
		out := make(map[string]any, len(x))
		for k, value := range x {
			key := strings.ToLower(k)
			child := rawPIIContext{
				key:           key,
				inContent:     ctx.inContent,
				inToolResult:  childToolResult,
				inInstruction: childInstruction || key == "system" || key == "instructions",
				inInput:       ctx.inInput || key == "input",
			}
			if key == "content" || key == "input" || key == "text" {
				child.inContent = true
			}
			out[k] = redactRawValue(value, child, state, apply)
		}
		return out
	default:
		return v
	}
}

func shouldRedactRawString(ctx rawPIIContext, apply normalizedPIIApplyTo) bool {
	switch ctx.key {
	case "system", "instructions":
		return apply.system
	case "input":
		return apply.responsesInput
	case "text":
		return apply.messages || apply.responsesInput || (apply.toolResults && ctx.inToolResult) ||
			(apply.system && ctx.inInstruction) || (apply.responsesInput && ctx.inInput)
	case "content":
		if ctx.inToolResult {
			return apply.toolResults
		}
		// Instruction-role content is instruction text: system scope governs it even
		// when the value sits in a message/item `content` field. Content inside the
		// caller's Responses `input` array is governed by responses_input.
		return apply.messages || (apply.system && ctx.inInstruction) || (apply.responsesInput && ctx.inInput)
	case "image_url", "url":
		return apply.imageURLs
	case "id", "tool_call_id", "call_id", "name", "model", "role", "type":
		return false
	default:
		return ctx.inContent && (apply.messages || apply.responsesInput || (apply.toolResults && ctx.inToolResult) ||
			(apply.system && ctx.inInstruction) || (apply.responsesInput && ctx.inInput))
	}
}

func restorePIIText(text string, placeholders []piiPlaceholder) string {
	if text == "" || len(placeholders) == 0 {
		return text
	}
	for i := len(placeholders) - 1; i >= 0; i-- {
		p := placeholders[i]
		text = strings.ReplaceAll(text, p.Placeholder, p.Original)
	}
	return text
}

func restorePIIRaw(v any, placeholders []piiPlaceholder) any {
	switch x := v.(type) {
	case string:
		return restorePIIText(x, placeholders)
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = restorePIIRaw(item, placeholders)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, value := range x {
			out[k] = restorePIIRaw(value, placeholders)
		}
		return out
	default:
		return v
	}
}

func piiRuleCount(result piiFilterResult) int {
	return len(result.RuleCounts)
}

func piiRuleNames(result piiFilterResult) []string {
	names := make([]string, 0, len(result.RuleCounts))
	for name, count := range result.RuleCounts {
		if count > 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

type piiFilterBlockedError struct{}

func (piiFilterBlockedError) Error() string {
	return "pii-filter-blocked"
}

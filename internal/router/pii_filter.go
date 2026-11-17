// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
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
	state         *piiRedactionState
}

type compiledPIIRule struct {
	name     string
	prefix   string
	re       *regexp.Regexp
	entropy  bool
	pemBlock bool
}

var (
	builtinPIIRulesOnce sync.Once
	builtinPIIRules     []compiledPIIRule
)

func (s *piiRedactionState) redactMatch(rule compiledPIIRule, match string) string {
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
}

func (s *piiRedactionState) redactPEMPrivateKeys(text string, rule compiledPIIRule) string {
	dashes := strings.Repeat("-", 5)
	beginMarker := dashes + "BEGIN "
	var out strings.Builder
	cursor := 0
	for cursor < len(text) {
		rel := strings.Index(text[cursor:], beginMarker)
		if rel < 0 {
			out.WriteString(text[cursor:])
			break
		}
		begin := cursor + rel
		labelStart := begin + len(beginMarker)
		labelEndRel := strings.Index(text[labelStart:], dashes)
		if labelEndRel < 0 {
			out.WriteString(text[cursor:])
			break
		}
		labelEnd := labelStart + labelEndRel
		label := text[labelStart:labelEnd]
		if !isPrivateKeyPEMLabel(label) {
			out.WriteString(text[cursor : begin+len(beginMarker)])
			cursor = begin + len(beginMarker)
			continue
		}
		blockStart := labelEnd + len(dashes)
		nextRel := strings.Index(text[blockStart:], beginMarker)
		blockLimit := len(text)
		if nextRel >= 0 {
			blockLimit = blockStart + nextRel
		}
		endMarker := dashes + "END " + label + dashes
		matchEndRel := strings.Index(text[blockStart:blockLimit], endMarker)
		blockEnd := blockLimit
		if matchEndRel >= 0 {
			blockEnd = blockStart + matchEndRel + len(endMarker)
		}
		out.WriteString(text[cursor:begin])
		out.WriteString(s.redactMatch(rule, text[begin:blockEnd]))
		cursor = blockEnd
	}
	return out.String()
}

func isPrivateKeyPEMLabel(label string) bool {
	prefix := strings.TrimSuffix(label, "PRIVATE KEY")
	if label != "PRIVATE KEY" && prefix == label {
		return false
	}
	if prefix != "" && !strings.HasSuffix(prefix, " ") {
		return false
	}
	for _, r := range strings.TrimSuffix(prefix, " ") {
		if r != ' ' && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
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
	if cfg.SecretsEnabled {
		rules = append(rules, compiledBuiltinSecretRules()...)
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
	result.state = state
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

func compiledBuiltinSecretRules() []compiledPIIRule {
	builtinPIIRulesOnce.Do(func() {
		builtinPIIRules, _ = compilePIIRules(builtinSecretRules())
		for i := range builtinPIIRules {
			if builtinPIIRules[i].name == "private_key" {
				builtinPIIRules[i].pemBlock = true
			}
		}
	})
	return builtinPIIRules
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

// builtinSecretRules is deliberately small and bounded. Go's RE2 engine keeps
// every expression linear in the size of the request.
func builtinSecretRules() []PIIFilterRule {
	dashes := strings.Repeat("-", 5)
	labels := []string{"RSA PRIVATE KEY", "EC PRIVATE KEY", "OPENSSH PRIVATE KEY", "DSA PRIVATE KEY", "ENCRYPTED PRIVATE KEY", "PRIVATE KEY"}
	pemExpressions := make([]string, 0, len(labels))
	for _, label := range labels {
		pemExpressions = append(pemExpressions, dashes+"BEGIN "+label+dashes+`[\s\S]*?`+dashes+"END "+label+dashes)
	}
	return []PIIFilterRule{
		{Name: "private_key", Expression: strings.Join(pemExpressions, "|"), PlaceholderPrefix: "SECRET_PRIVATE_KEY"},
		{Name: "aws_access_key", Expression: `\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`, PlaceholderPrefix: "SECRET_AWS"},
		{Name: "github_token", Expression: `\b(?:gh[pousr]_[A-Za-z0-9]{30,256}|github_pat_[A-Za-z0-9_]{40,256})\b`, PlaceholderPrefix: "SECRET_GITHUB"},
		{Name: "slack_token", Expression: `\bxox[baprs]-[A-Za-z0-9-]{20,256}\b`, PlaceholderPrefix: "SECRET_SLACK"},
		{Name: "stripe_secret", Expression: `\bsk_(?:live|test)_[A-Za-z0-9]{16,256}\b`, PlaceholderPrefix: "SECRET_STRIPE"},
		{Name: "openai_secret", Expression: `\bsk-(?:proj|svcacct)-[A-Za-z0-9_-]{24,256}\b`, PlaceholderPrefix: "SECRET_PROVIDER"},
		{Name: "openai_legacy_secret", Expression: `\bsk-[A-Za-z0-9]{32,256}\b`, PlaceholderPrefix: "SECRET_PROVIDER"},
		{Name: "groq_secret", Expression: `\bgsk_[A-Za-z0-9]{32,256}\b`, PlaceholderPrefix: "SECRET_PROVIDER"},
		{Name: "openrouter_secret", Expression: `\bsk-or-v1-[A-Fa-f0-9]{64}\b`, PlaceholderPrefix: "SECRET_PROVIDER"},
		{Name: "anthropic_secret", Expression: `\bsk-ant-api03-[A-Za-z0-9_-]{24,256}\b`, PlaceholderPrefix: "SECRET_PROVIDER"},
		{Name: "google_api_key", Expression: `\bAIza[0-9A-Za-z_-]{35}\b`, PlaceholderPrefix: "SECRET_PROVIDER"},
		{Name: "router_token", Expression: `\brtr_metrum_[A-Za-z0-9_-]{12,256}\b`, PlaceholderPrefix: "SECRET_ROUTER"},
		{Name: "jwt", Expression: `\beyJ[A-Za-z0-9_-]{8,1000}[A-Za-z0-9_-]{0,1000}[A-Za-z0-9_-]{0,1000}[A-Za-z0-9_-]{0,1000}[A-Za-z0-9_-]{0,93}\.[A-Za-z0-9_-]{8,1000}[A-Za-z0-9_-]{0,1000}[A-Za-z0-9_-]{0,1000}[A-Za-z0-9_-]{0,1000}[A-Za-z0-9_-]{0,96}\.[A-Za-z0-9_-]{8,1000}[A-Za-z0-9_-]{0,1000}[A-Za-z0-9_-]{0,1000}[A-Za-z0-9_-]{0,1000}[A-Za-z0-9_-]{0,96}\b`, PlaceholderPrefix: "SECRET_JWT"},
		{Name: "bearer_token", Expression: `(?i)\bAuthorization\s*:\s*Bearer\s+[A-Za-z0-9._~+/-]{16,256}={0,2}`, PlaceholderPrefix: "SECRET_BEARER"},
		{Name: "credential_assignment", Expression: `(?i)\b(?:api[_-]?key|access[_-]?token|client[_-]?secret)\s*[:=]\s*["']?[A-Za-z0-9_./+=-]{16,256}|\bpassword\s*[:=]\s*["']?[A-Za-z0-9_./+=-]{24,256}`, PlaceholderPrefix: "SECRET_CREDENTIAL"},
	}
}

func redactPIIResponse(resp *IRResponse, state *piiRedactionState, apply normalizedPIIApplyTo) {
	if resp == nil || state == nil {
		return
	}
	resp.Text = state.redact(resp.Text)
	if resp.Raw != nil {
		resp.Raw = redactRawPayload(resp.Raw, state, apply)
	}
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
		if rule.pemBlock {
			text = s.redactPEMPrivateKeys(text, rule)
			continue
		}
		text = rule.re.ReplaceAllStringFunc(text, func(match string) string {
			return s.redactMatch(rule, match)
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
	key             string
	inContent       bool
	inToolResult    bool
	inToolArguments bool
	inToolUse       bool
	inInstruction   bool // Inside a system or developer message/item.
	inInput         bool // Inside the caller's `input` array.
}

func redactRawValue(v any, ctx rawPIIContext, state *piiRedactionState, apply normalizedPIIApplyTo) any {
	switch x := v.(type) {
	case string:
		if ctx.inToolArguments && ctx.key == "arguments" && shouldRedactToolArguments(ctx, apply) {
			redacted, _, err := redactToolArgumentJSON(x, state)
			if err != nil {
				markPIIRedactionFailedClosed(state)
				return x
			}
			return redacted
		}
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
	case []map[string]any:
		out := make([]map[string]any, len(x))
		for i, item := range x {
			redacted, _ := redactRawValue(item, ctx, state, apply).(map[string]any)
			out[i] = redacted
		}
		return out
	case map[string]any:
		if ctx.inToolArguments && (ctx.key == "input" || ctx.key == "arguments") && shouldRedactToolArguments(ctx, apply) {
			redacted, _ := redactToolArgumentValue(x, "", state)
			if out, ok := redacted.(map[string]any); ok {
				return out
			}
			return x
		}
		role := strings.ToLower(strings.TrimSpace(stringValue(x["role"])))
		itemType := strings.ToLower(strings.TrimSpace(stringValue(x["type"])))
		childToolResult := ctx.inToolResult || role == "tool"
		childInstruction := ctx.inInstruction || role == "system" || role == "developer"
		out := make(map[string]any, len(x))
		for k, value := range x {
			key := strings.ToLower(k)
			child := rawPIIContext{
				key:          key,
				inContent:    ctx.inContent,
				inToolResult: childToolResult,
				inToolArguments: ctx.inToolArguments ||
					(ctx.key == "tool_calls" && key == "function") ||
					((ctx.key == "function_call" || itemType == "function_call") && key == "arguments") ||
					((ctx.key == "tool_use" || itemType == "tool_use") && key == "input"),
				inToolUse:     ctx.inToolUse || ctx.key == "tool_use" || itemType == "tool_use",
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
	case "text", "output_text":
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

func shouldRedactToolArguments(ctx rawPIIContext, apply normalizedPIIApplyTo) bool {
	if ctx.inToolUse {
		return apply.messages
	}
	if ctx.inInput {
		return apply.responsesInput
	}
	return apply.messages
}

// redactToolArgumentJSON decodes a tool's JSON before matching so JSON
// escaping cannot hide credential prefixes or punctuation. It checks each
// string value with its object key in key=value form and scans keys directly.
// If nothing changes, the original encoding and whitespace are retained.
func redactToolArgumentJSON(value string, state *piiRedactionState) (string, bool, error) {
	if value == "" || state == nil || state.result == nil {
		return value, false, nil
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return redactMalformedToolArgumentJSON(value, state)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return redactMalformedToolArgumentJSON(value, state)
	}
	redacted, changed := redactToolArgumentValue(decoded, "", state)
	if !changed {
		return value, false, nil
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	err := encoder.Encode(redacted)
	if err != nil {
		return value, false, err
	}
	return strings.TrimSuffix(encoded.String(), "\n"), true, nil
}

func redactMalformedToolArgumentJSON(value string, state *piiRedactionState) (string, bool, error) {
	equalsForm := normalizeMalformedToolArgumentJSON(value)
	afterEquals := state.redact(equalsForm)
	equalsChanged := afterEquals != equalsForm
	colonForm := normalizeMalformedToolArgumentJSONWithDelimiter(value, ": ")
	if equalsChanged {
		// Preserve replacements from the first pass, while changing the remaining
		// property assignments to header form for rules that require a colon.
		colonForm = malformedToolArgumentAuthorizationAssignment.ReplaceAllString(afterEquals, "$1: ")
	}
	redacted := state.redact(colonForm)
	if redacted != colonForm {
		return redacted, true, nil
	}
	if equalsChanged {
		return afterEquals, true, nil
	}
	return value, false, nil
}

var malformedToolArgumentProperty = regexp.MustCompile(`"([^"\\]+)"\s*:\s*"`)
var malformedToolArgumentAuthorizationAssignment = regexp.MustCompile(`(?i)(\bAuthorization\s*)=`)

// normalizeMalformedToolArgumentJSON decodes each JSON string token it can,
// even when the containing argument object is truncated, then turns property
// boundaries into the key=value form expected by credential-assignment rules.
func normalizeMalformedToolArgumentJSON(value string) string {
	return normalizeMalformedToolArgumentJSONWithDelimiter(value, "=")
}

func normalizeMalformedToolArgumentJSONWithDelimiter(value, delimiter string) string {
	var decoded strings.Builder
	for i := 0; i < len(value); {
		if value[i] != '"' {
			decoded.WriteByte(value[i])
			i++
			continue
		}
		start := i
		i++
		closed := false
		for i < len(value) {
			if value[i] == '\\' {
				if i+1 >= len(value) {
					i++
					break
				}
				i += 2
				continue
			}
			if value[i] == '"' {
				i++
				closed = true
				break
			}
			i++
		}
		quoted := value[start:i]
		if !closed {
			quoted += `"`
		}
		var text string
		if err := json.Unmarshal([]byte(quoted), &text); err == nil {
			decoded.WriteByte('"')
			decoded.WriteString(text)
			decoded.WriteByte('"')
		} else {
			decoded.WriteString(value[start:i])
		}
	}
	return malformedToolArgumentProperty.ReplaceAllString(decoded.String(), "$1"+delimiter)
}

func redactToolArgumentValue(value any, parentKey string, state *piiRedactionState) (any, bool) {
	switch v := value.(type) {
	case string:
		redacted := state.redact(v)
		changed := redacted != v
		if parentKey != "" {
			for _, delimiter := range []string{"=", ": "} {
				pair := parentKey + delimiter + redacted
				redactedPair := state.redact(pair)
				if redactedPair == pair {
					continue
				}
				prefix := parentKey + delimiter
				if strings.HasPrefix(redactedPair, prefix) {
					redacted = strings.TrimPrefix(redactedPair, prefix)
				} else if separator := strings.IndexByte(redactedPair, '='); separator >= 0 && !strings.Contains(parentKey, "=") {
					redacted = redactedPair[separator+1:]
				} else {
					redacted = redactedPair
				}
				changed = true
				break
			}
		}
		return redacted, changed
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(v))
		changed := false
		for _, key := range keys {
			redactedKey := state.redact(key)
			redactedValue, valueChanged := redactToolArgumentValue(v[key], key, state)
			out[redactedKey] = redactedValue
			changed = changed || redactedKey != key || valueChanged
		}
		return out, changed
	case []any:
		out := make([]any, len(v))
		changed := false
		for i, child := range v {
			redacted, childChanged := redactToolArgumentValue(child, parentKey, state)
			out[i] = redacted
			changed = changed || childChanged
		}
		return out, changed
	default:
		return value, false
	}
}

func markPIIRedactionFailedClosed(state *piiRedactionState) {
	if state == nil || state.result == nil {
		return
	}
	state.result.LimitExceeded = true
	state.result.Warnings = appendWarning(state.result.Warnings, "pii-filter-tool-argument-encoding-failed")
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
	case []map[string]any:
		out := make([]map[string]any, len(x))
		for i, item := range x {
			out[i], _ = restorePIIRaw(item, placeholders).(map[string]any)
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

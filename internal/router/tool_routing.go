// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// defaultToolRoutingWindowSeconds is the default reliability window. Slice C
// uses it for scoring; observe mode records the value so a later enablement
// does not change the window an operator validated.
const defaultToolRoutingWindowSeconds = 900

// toolValidationLogRecord is the per-attempt tool-call telemetry. It exists only
// for groups with a tool_routing block.
type toolValidationLogRecord struct {
	Mode               string         `json:"mode"`
	Calls              int            `json:"calls"`
	Valid              int            `json:"valid"`
	Invalid            int            `json:"invalid"`
	FirstFailure       string         `json:"first_failure,omitempty"`
	FirstFailureTool   string         `json:"first_failure_tool,omitempty"`
	UnsupportedKeyword bool           `json:"unsupported_schema_keyword,omitempty"`
	ClassCounts        map[string]int `json:"class_counts,omitempty"`
	Action             string         `json:"action"`
}

// toolAwareGroup returns the group's tool-routing policy when the group opted
// in. Every caller guards on the second return value, so a group without the
// block takes the existing path with one map lookup and no allocation.
func (s *Service) toolAwareGroup(name string) (ToolRoutingConfig, bool) {
	if s == nil || s.cfg == nil || name == "" {
		return ToolRoutingConfig{}, false
	}
	group, ok := s.cfg.Models[name]
	if !ok || !group.ToolRouting.configured() || !strings.EqualFold(group.Strategy, "tool_aware") {
		return ToolRoutingConfig{}, false
	}
	return group.ToolRouting, true
}

// toolValidationOptionsForRequest derives the validation policy for one
// request: the group's configured checks plus the caller's forced-tool shape.
func toolValidationOptionsForRequest(cfg ToolRoutingConfig, req *IRRequest) toolValidationOptions {
	options := cfg.validationOptions()
	if req == nil {
		return options
	}
	options.ForcedToolNames = forcedToolNames(req)
	return options
}

// forcedToolNames returns the tool names a caller forced or required. It covers
// the OpenAI Chat/Responses and Anthropic tool_choice shapes; auto, none and an
// absent choice return nil.
func forcedToolNames(req *IRRequest) []string {
	if req == nil || req.Raw == nil {
		return nil
	}
	choice, present := req.Raw["tool_choice"]
	if !present || choice == nil {
		return nil
	}
	switch typed := choice.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "required", "any":
			return offeredToolNames(req.Tools)
		default:
			return nil
		}
	case map[string]any:
		if name := stringValue(typed["name"]); name != "" {
			return []string{name}
		}
		if kind := strings.ToLower(strings.TrimSpace(stringValue(typed["type"]))); kind == "any" || kind == "required" {
			return offeredToolNames(req.Tools)
		}
		if function, ok := typed["function"].(map[string]any); ok {
			if name := stringValue(function["name"]); name != "" {
				return []string{name}
			}
		}
		return nil
	default:
		return nil
	}
}

func offeredToolNames(tools []map[string]any) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if name, _ := toolNameAndSchema(tool); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	return names
}

// recordToolValidation observes one successful attempt on a tool_aware group.
// Observe mode never changes the response, the attempt outcome or the caller
// bytes; it only annotates the attempt record and emits a trace event.
func (s *Service) recordToolValidation(rc *requestContext, groupName string, req *IRRequest, resp *IRResponse, attempt *attemptLogRecord) {
	cfg, ok := s.toolAwareGroup(groupName)
	if !ok || attempt == nil || resp == nil {
		return
	}
	if resp.Streamed && len(resp.ToolCalls) == 0 {
		// Streamed tool-call deltas are not assembled in this release, so a
		// streamed response carries no call view. Recording a failure here would
		// penalize a target for something the router cannot observe, so the
		// attempt is marked as observed-only and never fed to the reliability
		// store.
		attempt.ToolValidation = &toolValidationLogRecord{Mode: cfg.mode(), Action: "observed_streaming"}
		return
	}
	calls := resp.ToolCalls
	options := toolValidationOptionsForRequest(cfg, req)
	validation := validateToolCalls(calls, req.Tools, resp.StopReason, options)
	record := &toolValidationLogRecord{
		Mode:               cfg.mode(),
		Calls:              validation.Calls,
		Valid:              validation.Valid,
		Invalid:            validation.Invalid,
		FirstFailure:       validation.FirstFailure,
		FirstFailureTool:   safeLoggedToolName(validation.FirstFailureTool),
		UnsupportedKeyword: validation.UnsupportedKeyword,
		ClassCounts:        validation.classCounts(),
		Action:             "observed",
	}
	attempt.ToolValidation = record
	if s.toolReliability != nil {
		s.toolReliability.observe(
			toolReliabilityKey(groupName, attempt.Provider, attempt.Model),
			validation,
			toolReliabilityWindow(cfg),
			time.Now().UTC(),
		)
	}
	if validation.Invalid > 0 && rc != nil {
		rc.trace(
			"tool_call_invalid",
			sanitizeToolValidationTrace(validation.FirstFailure, validation.FirstFailureTool),
			Target{Provider: attempt.Provider, Model: attempt.Model, Dialect: attempt.Dialect},
			attempt.Index,
			attempt.StatusCode,
			validation.FirstFailure,
			false,
			attempt.DurationMS,
		)
	}
}

// maxLoggedToolNameBytes bounds a caller-supplied tool name in telemetry. The
// name comes from the request, so it is truncated on a rune boundary and
// stripped of control characters before it reaches a log, trace or metric.
const maxLoggedToolNameBytes = 64

func safeLoggedToolName(name string) string {
	if name == "" {
		return ""
	}
	var builder strings.Builder
	for _, char := range name {
		if char < 0x20 || char == 0x7f {
			continue
		}
		if builder.Len()+utf8.RuneLen(char) > maxLoggedToolNameBytes {
			break
		}
		builder.WriteRune(char)
	}
	return builder.String()
}

func sanitizeToolValidationTrace(class, tool string) string {
	safeClass := strings.Map(func(char rune) rune {
		if char < 0x20 || char == 0x7f {
			return -1
		}
		return char
	}, class)
	name := safeLoggedToolName(tool)
	if name == "" {
		return sanitizeToolValidationTraceClass(safeClass)
	}
	return sanitizeToolValidationTraceClass(safeClass) + " tool=" + name
}

func sanitizeToolValidationTraceClass(class string) string {
	if len(class) > maxLoggedToolNameBytes {
		return class[:maxLoggedToolNameBytes]
	}
	return class
}

// recoverToolCall applies the bounded recovery policy to a completed,
// non-streaming response whose tool calls failed validation. It retries the
// same target first, then escalates through the remaining eligible targets, and
// never runs after the first client byte. It returns the response to deliver
// (the last one received, or the original when the budget is spent) and the
// updated attempt accounting. Upstream transport errors during recovery keep
// the last usable response instead of replacing it with an error.
func (s *Service) recoverToolCall(ctx context.Context, rc *requestContext, caller *callerRuntime, callerDialect string, req *IRRequest, dec decision, resp *IRResponse, attempts int, fallbackUsed bool) (*IRResponse, int, bool) {
	cfg, ok := s.toolAwareGroup(dec.GroupName)
	if !ok || cfg.mode() != toolRoutingModeEnforce || resp == nil {
		return resp, attempts, fallbackUsed
	}
	options := toolValidationOptionsForRequest(cfg, req)
	validation := validateToolCalls(resp.ToolCalls, req.Tools, resp.StopReason, options)
	if validation.Invalid == 0 {
		return resp, attempts, fallbackUsed
	}
	markAttemptToolAction(rc, attempts, "delivered_invalid")
	if req.Stream {
		// Streamed tool-call bytes are already on the wire by the time the call is
		// complete, so a retry cannot replace them. This is the stated limit of
		// this release: streamed responses are observed, never retried.
		markAttemptToolAction(rc, attempts, "observed_streaming")
		if rc != nil {
			rc.trace("tool_recovery_skipped", "streaming response already delivered", dec.Target, attempts, http.StatusOK, validation.FirstFailure, false, 0)
		}
		return resp, attempts, fallbackUsed
	}

	targets := append([]Target{dec.Target}, dec.Fallbacks...)
	if len(targets) == 0 {
		return resp, attempts, fallbackUsed
	}
	// Retry the target that actually served this response, not the primary: a
	// fallback attempt must be retried where it ran and escalated from there.
	index := servedTargetIndex(rc, attempts, targets)
	// The group/caller spend ceiling also bounds recovery: a target the request
	// may not escalate to is not retried into.
	ceiling := effectiveSpendCeiling(caller, s.cfg.Models[dec.GroupName])
	allowed := map[string]bool{}
	if ceiling.configured() {
		kept, filtered := filterTargetsBySpendCeiling(targets, req, ceiling)
		for _, target := range kept {
			allowed[targetKey(target)] = true
		}
		if len(filtered) > 0 && rc != nil {
			rc.trace("tool_recovery_ceiling_filtered", fmt.Sprintf("%d target(s) excluded by spend_ceiling", len(filtered)), dec.Target, attempts, http.StatusOK, spendCeilingReason, false, 0)
		}
	}
	deadline := ctx
	retriesLeft := cfg.Recovery.MaxRetriesSameTarget
	escalationsLeft := cfg.Recovery.MaxEscalations
	last := resp
	for {
		if err := deadline.Err(); err != nil {
			if rc != nil {
				rc.trace("tool_recovery_deadline", "request deadline reached during tool recovery", targets[index], attempts, http.StatusOK, "deadline_exceeded", true, 0)
			}
			return last, attempts, fallbackUsed
		}
		action := ""
		switch {
		case retriesLeft > 0:
			if !targetAllowed(allowed, ceiling, targets[index]) {
				if rc != nil {
					rc.trace("tool_recovery_ceiling_blocked", "retry target exceeds spend ceiling", targets[index], attempts, http.StatusOK, spendCeilingReason, false, 0)
				}
				retriesLeft = 0
				continue
			}
			retriesLeft--
			action = "retried"
		case escalationsLeft > 0 && index+1 < len(targets):
			next := targets[index+1]
			if !targetAllowed(allowed, ceiling, next) {
				if rc != nil {
					rc.trace("tool_recovery_ceiling_blocked", "escalation target exceeds spend ceiling", next, attempts, http.StatusOK, spendCeilingReason, false, 0)
				}
				escalationsLeft = 0
				continue
			}
			index++
			escalationsLeft--
			action = "escalated"
		default:
			return last, attempts, fallbackUsed
		}
		attemptIndex := len(rc.rec.AttemptsDetail) + 1
		next, attempt, err := s.callOne(ctx, nil, rc, callerDialect, req, dec.GroupName, targets[index], attemptIndex)
		if err != nil {
			if attempt.ErrorMessage != "" {
				attempt.ErrorMessage = s.sanitizeDiagnosticError(attempt.ErrorMessage)
			}
			rc.rec.AttemptsDetail = append(rc.rec.AttemptsDetail, attempt)
			attempts = attemptIndex
			if rc != nil {
				rc.trace("tool_recovery_stopped", err.Error(), targets[index], attemptIndex, attempt.StatusCode, attempt.ErrorClass, false, attempt.DurationMS)
			}
			return last, attempts, fallbackUsed
		}
		attempt.Selected = false
		s.recordToolValidation(rc, dec.GroupName, req, next, &attempt)
		if attempt.ToolValidation != nil {
			attempt.ToolValidation.Action = action
		}
		rc.rec.AttemptsDetail = append(rc.rec.AttemptsDetail, attempt)
		attempts = attemptIndex
		// The previous response is discarded by this attempt, so its usage is
		// recorded for cost reporting.
		recordDiscardedUsage(rc, attempts-1, last)
		if rc != nil {
			rc.rec.DiscardedUsage = addUsage(rc.rec.DiscardedUsage, last.Usage)
		}
		last = next
		nextValidation := validateToolCalls(next.ToolCalls, req.Tools, next.StopReason, options)
		if nextValidation.Invalid == 0 {
			markSelectedAttempt(rc, attemptIndex)
			if index > 0 {
				fallbackUsed = true
			}
			if rc != nil {
				rc.trace("tool_recovery_succeeded", action, targets[index], attemptIndex, http.StatusOK, "", false, attempt.DurationMS)
			}
			return next, attempts, fallbackUsed
		}
	}
}

// markSelectedAttempt clears the selected flag on every attempt and sets it on
// the one that is actually delivered.
func markSelectedAttempt(rc *requestContext, index int) {
	if rc == nil {
		return
	}
	for position := range rc.rec.AttemptsDetail {
		rc.rec.AttemptsDetail[position].Selected = rc.rec.AttemptsDetail[position].Index == index
	}
}

func markAttemptToolAction(rc *requestContext, attemptIndex int, action string) {
	if rc == nil {
		return
	}
	for position := range rc.rec.AttemptsDetail {
		if rc.rec.AttemptsDetail[position].Index != attemptIndex {
			continue
		}
		if rc.rec.AttemptsDetail[position].ToolValidation == nil {
			rc.rec.AttemptsDetail[position].ToolValidation = &toolValidationLogRecord{Mode: toolRoutingModeEnforce}
		}
		rc.rec.AttemptsDetail[position].ToolValidation.Action = action
		return
	}
}

func recordDiscardedUsage(rc *requestContext, attemptIndex int, resp *IRResponse) {
	if rc == nil || resp == nil {
		return
	}
	for position := range rc.rec.AttemptsDetail {
		if rc.rec.AttemptsDetail[position].Index != attemptIndex {
			continue
		}
		rc.rec.AttemptsDetail[position].InputTokens = resp.Usage.InputTokens
		rc.rec.AttemptsDetail[position].OutputTokens = resp.Usage.OutputTokens
		return
	}
}

func targetKey(target Target) string {
	return target.Provider + "|" + target.Model + "|" + target.Dialect
}

// targetAllowed reports whether recovery may retry or escalate to a target. An
// unconfigured ceiling allows every target.
func targetAllowed(allowed map[string]bool, ceiling SpendCeilingConfig, target Target) bool {
	if !ceiling.configured() {
		return true
	}
	return allowed[targetKey(target)]
}

// servedTargetIndex maps the index of the attempt that produced the response
// back to its position in the delivered target order, so recovery retries the
// target that actually served it.
func servedTargetIndex(rc *requestContext, attemptIndex int, targets []Target) int {
	if rc == nil || attemptIndex <= 0 {
		return 0
	}
	var served attemptLogRecord
	found := false
	for _, attempt := range rc.rec.AttemptsDetail {
		if attempt.Index == attemptIndex {
			served = attempt
			found = true
			break
		}
	}
	if !found {
		return 0
	}
	for position, target := range targets {
		if target.Provider != served.Provider || target.Model != served.Model {
			continue
		}
		if served.Dialect != "" && target.Dialect != "" && target.Dialect != served.Dialect {
			continue
		}
		return position
	}
	return 0
}

// addUsage returns left+right token totals. Optional counters are summed only
// when both sides report them, keeping "unreported" distinct from zero.
func addUsage(left, right Usage) Usage {
	sum := Usage{
		InputTokens:                   left.InputTokens + right.InputTokens,
		OutputTokens:                  left.OutputTokens + right.OutputTokens,
		TotalTokens:                   left.TotalTokens + right.TotalTokens,
		InputImageTokens:              left.InputImageTokens + right.InputImageTokens,
		UpstreamReportedInputCostUSD:  left.UpstreamReportedInputCostUSD + right.UpstreamReportedInputCostUSD,
		UpstreamReportedOutputCostUSD: left.UpstreamReportedOutputCostUSD + right.UpstreamReportedOutputCostUSD,
		UpstreamReportedTotalCostUSD:  left.UpstreamReportedTotalCostUSD + right.UpstreamReportedTotalCostUSD,
	}
	if sum.TotalTokens == 0 {
		sum.TotalTokens = sum.InputTokens + sum.OutputTokens
	}
	addOptionalInt := func(left, right *int) *int {
		if left == nil && right == nil {
			return nil
		}
		var value int
		if left != nil {
			value += *left
		}
		if right != nil {
			value += *right
		}
		return &value
	}
	sum.CachedInputTokens = addOptionalInt(left.CachedInputTokens, right.CachedInputTokens)
	sum.CacheWriteInputTokens = addOptionalInt(left.CacheWriteInputTokens, right.CacheWriteInputTokens)
	sum.ReasoningTokens = addOptionalInt(left.ReasoningTokens, right.ReasoningTokens)
	return sum
}

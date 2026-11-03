// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
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

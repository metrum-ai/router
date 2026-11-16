// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

var errMCPRedactionBounds = errors.New("MCP payload exceeds redaction bounds")

func mcpPIIFilter(cfg *Config, modelGroup string) PIIFilterConfig {
	if cfg == nil || strings.TrimSpace(modelGroup) == "" {
		return PIIFilterConfig{}
	}
	group, ok := cfg.Models[strings.TrimSpace(modelGroup)]
	if !ok {
		return PIIFilterConfig{}
	}
	return group.PIIFilter
}

func newMCPPIIRedactionState(cfg PIIFilterConfig) (*piiRedactionState, normalizedPIIApplyTo, error) {
	apply := normalizePIIApplyTo(cfg.ApplyTo)
	result := &piiFilterResult{RuleCounts: map[string]int{}, Mode: normalizePIIFilterMode(cfg)}
	if !cfg.Enabled {
		return nil, apply, nil
	}
	rules, err := compilePIIRules(cfg.Rules)
	if err != nil {
		return nil, apply, err
	}
	if cfg.SecretsEnabled {
		rules = append(rules, compiledBuiltinSecretRules()...)
	}
	limit := cfg.MaxReplacementsPerRequest
	if limit <= 0 {
		limit = 200
	}
	state := &piiRedactionState{rules: rules, limit: limit, result: result, placeholderFor: map[string]string{}}
	result.state = state
	return state, apply, nil
}

func validateMCPRedactionShape(value any, depth int, budget *int) error {
	if depth > maxFirewallArgumentDepth {
		return errMCPRedactionBounds
	}
	*budget--
	if *budget < 0 {
		return errMCPRedactionBounds
	}
	switch typed := value.(type) {
	case []any:
		for _, child := range typed {
			if err := validateMCPRedactionShape(child, depth+1, budget); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, child := range typed {
			if err := validateMCPRedactionShape(child, depth+1, budget); err != nil {
				return err
			}
		}
	}
	return nil
}

func redactMCPValue(value any, state *piiRedactionState, failOnMatch bool) (any, error) {
	if state == nil || state.result == nil {
		return value, nil
	}
	budget := maxFirewallArgumentNodes
	if err := validateMCPRedactionShape(value, 0, &budget); err != nil {
		return nil, err
	}
	before := state.result.Replacements
	redacted, _ := redactToolArgumentValue(value, "", state)
	if state.result.LimitExceeded || (failOnMatch && state.result.Replacements > before) {
		return nil, piiFilterBlockedError{}
	}
	return redacted, nil
}

func mcpApprovalResult(status, approvalID string) map[string]any {
	payload := map[string]string{"status": status}
	if approvalID != "" {
		payload["approval_id"] = approvalID
	}
	encoded, _ := json.Marshal(payload)
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": string(encoded)}},
		"isError": status != "approval_pending",
	}
}

func (s *Service) mcpFirewallDecision(cfg *Config, caller *callerRuntime, modelGroup, tool string, arguments map[string]any) (any, bool) {
	if s == nil || cfg == nil || caller == nil || s.toolFirewall == nil || s.toolApprovals == nil {
		return nil, false
	}
	policy := cfg.Server.ToolFirewall
	s.toolFirewall.syncPolicyWithApprovals(policy, s.toolApprovals)
	if strings.TrimSpace(policy.Mode) == "" || strings.EqualFold(strings.TrimSpace(policy.Mode), toolFirewallOff) {
		return nil, false
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return mcpApprovalResult("denied", ""), true
	}
	calls := []IRToolCall{{Name: tool, Arguments: string(encoded), Dialect: "mcp", Kind: "mcp_call"}}
	fingerprint, ok := toolCallApprovalFingerprint(caller.cfg.ID, calls)
	if !ok {
		return mcpApprovalResult("denied", ""), true
	}
	approvalID, approvalState := s.toolApprovals.lookup(fingerprint)
	// MCP session identifiers are client-controlled. Use one bounded sequence
	// scope per authenticated caller so callers cannot evade a hold by changing
	// the header between calls.
	req := &IRRequest{Messages: []IRMessage{{Role: "user", Content: "mcp-caller:" + caller.cfg.ID}}}
	outcome := s.toolFirewall.checkWithTruncation(policy, caller.cfg.ID, modelGroup, req, calls, false, approvalState == "release")
	rc := &requestContext{s: s, id: requestID(), caller: caller, method: http.MethodPost, pathTemplate: "/mcp"}
	disposition := s.resolveToolFirewallDecision(rc, caller.cfg.ID, fingerprint, approvalID, approvalState, outcome, policy)
	if disposition.pending {
		return mcpApprovalResult("approval_pending", disposition.approvalID), true
	}
	if disposition.blocked {
		return mcpApprovalResult("denied", ""), true
	}
	return nil, false
}

func mcpRedactMCPToolInput(cfg *Config, modelGroup string, arguments map[string]any) (map[string]any, error) {
	filter := mcpPIIFilter(cfg, modelGroup)
	state, apply, err := newMCPPIIRedactionState(filter)
	if err != nil {
		return nil, errors.New("invalid MCP PII filter")
	}
	if !filter.Enabled || !apply.messages {
		return arguments, nil
	}
	value, redactErr := redactMCPValue(arguments, state, normalizePIIFilterMode(filter) == "fail_on_match" || filter.FailOnMatch)
	if redactErr != nil {
		return nil, redactErr
	}
	redactedArguments, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("MCP arguments must be an object")
	}
	return redactedArguments, nil
}

func mcpRedactToolResult(cfg *Config, modelGroup string, result any) (any, error) {
	filter := mcpPIIFilter(cfg, modelGroup)
	state, apply, err := newMCPPIIRedactionState(filter)
	if err != nil {
		return nil, errors.New("invalid MCP PII filter")
	}
	if !filter.Enabled || !apply.toolResults {
		return result, nil
	}
	return redactMCPValue(result, state, normalizePIIFilterMode(filter) == "fail_on_match" || filter.FailOnMatch)
}

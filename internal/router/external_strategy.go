// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const externalPolicyDefaultTimeoutMS = 500

type externalPolicyStrategy struct {
	cfg            ExternalPolicyConfig
	client         *http.Client
	feedbackClient *http.Client
	feedbackCtx    context.Context
	feedbackCancel context.CancelFunc
	feedbackWG     sync.WaitGroup
}

func newExternalPolicyStrategy(cfg ExternalPolicyConfig) *externalPolicyStrategy {
	timeout := time.Duration(cfg.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = externalPolicyDefaultTimeoutMS * time.Millisecond
	}
	feedbackCtx, feedbackCancel := context.WithCancel(context.Background())
	strat := &externalPolicyStrategy{
		cfg:            cfg,
		client:         newEgressHTTPClient(timeout, cfg.AllowHosts, cfg.AllowHTTP, "external policy"),
		feedbackCtx:    feedbackCtx,
		feedbackCancel: feedbackCancel,
	}
	if cfg.Feedback.Enabled {
		strat.feedbackClient = newExternalPolicyFeedbackClient(cfg)
	}
	return strat
}

type externalPolicyInput struct {
	Group           string                      `json:"group"`
	Request         *IRRequest                  `json:"request,omitempty"`
	Context         requestSummary              `json:"context"`
	Contract        *scriptContract             `json:"contract,omitempty"`
	Requirements    []string                    `json:"requirements"`
	Targets         []scriptTarget              `json:"targets"`
	AllTargets      []scriptTarget              `json:"allTargets,omitempty"`
	Caller          *scriptCaller               `json:"caller,omitempty"`
	Text            string                      `json:"text,omitempty"`
	InputModalities []string                    `json:"inputModalities"`
	VerifierHint    *externalPolicyVerifierHint `json:"verifierHint,omitempty"`
	Now             string                      `json:"now"`
}

type requestSummary struct {
	Model               string            `json:"model,omitempty"`
	Dialect             string            `json:"dialect,omitempty"`
	EstimatedTokens     int               `json:"estimatedTokens"`
	TextChars           int               `json:"textChars"`
	SystemChars         int               `json:"systemChars,omitempty"`
	InputChars          int               `json:"inputChars,omitempty"`
	InputPartCount      int               `json:"inputPartCount,omitempty"`
	MessageCount        int               `json:"messageCount,omitempty"`
	MessageTextChars    int               `json:"messageTextChars,omitempty"`
	MessagePartCount    int               `json:"messagePartCount,omitempty"`
	ImageCount          int               `json:"imageCount,omitempty"`
	ToolCount           int               `json:"toolCount,omitempty"`
	HasTools            bool              `json:"hasTools"`
	HasStructuredOutput bool              `json:"hasStructuredOutput"`
	MaxTokens           int               `json:"maxTokens,omitempty"`
	MaxTokensField      string            `json:"maxTokensField,omitempty"`
	TemperatureSet      bool              `json:"temperatureSet"`
	Stream              bool              `json:"stream"`
	StopCount           int               `json:"stopCount,omitempty"`
	MetadataKeys        []string          `json:"metadataKeys,omitempty"`
	Reasoning           *reasoningSummary `json:"reasoning,omitempty"`
	ConversationKey     string            `json:"conversationKey,omitempty"`
	ConversationKeySrc  string            `json:"conversationKeySource,omitempty"`
}

type reasoningSummary struct {
	Requested    bool   `json:"requested"`
	Disabled     bool   `json:"disabled,omitempty"`
	Kind         string `json:"kind,omitempty"`
	Effort       string `json:"effort,omitempty"`
	BudgetTokens int    `json:"budgetTokens,omitempty"`
	Source       string `json:"source,omitempty"`
}

type routingPolicyError struct {
	Group     string
	Message   string
	Err       error
	Execution policyExecutionLogRecord
}

func (e routingPolicyError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return "routing policy failed"
}

func (e routingPolicyError) Unwrap() error {
	return e.Err
}

func (s *externalPolicyStrategy) Pick(ctx context.Context, group string, req *IRRequest, contract *ModelGroupContract, eligibleTargets, allTargets []Target, providers map[string]ProviderConfig, caller *callerRuntime, tokenID, callerDialect string) (decision, error) {
	start := time.Now()
	callerID := ""
	if caller != nil {
		callerID = caller.cfg.ID
	}
	conversationKey := buildExternalPolicyConversationKey(s.cfg.ConversationKey, callerID, group, req)
	mode := strings.ToLower(strings.TrimSpace(s.cfg.Mode))
	if mode == "" {
		mode = "enforce"
	}
	if mode == "baseline" {
		dec := externalBaselineDecision(group, eligibleTargets, "baseline", nil, 0, len(allTargets))
		dec.ConversationKey = conversationKey
		return dec, nil
	}
	dec, err := s.pick(ctx, group, req, contract, eligibleTargets, allTargets, providers, caller, tokenID, callerDialect)
	if mode == "shadow" {
		if err != nil {
			baseline := externalBaselineDecision(group, eligibleTargets, "shadow_error", err, time.Since(start).Milliseconds(), len(allTargets))
			baseline.ConversationKey = conversationKey
			return baseline, nil
		}
		baseline := externalBaselineDecision(group, eligibleTargets, "shadow_recommended", nil, time.Since(start).Milliseconds(), len(allTargets))
		recommended := dec.Target
		baseline.ShadowRecommended = &recommended
		baseline.ConversationKey = conversationKey
		if dec.ClassLabel != nil {
			baseline.PolicyExecutions[0].ClassLabel = dec.ClassLabel
		}
		return baseline, nil
	}
	if err == nil {
		if dec.ConversationKey == "" {
			dec.ConversationKey = conversationKey
		}
		dec.PolicyExecutions = append(dec.PolicyExecutions, policyExecutionLogRecord{
			Seq:                    len(dec.PolicyExecutions) + 1,
			Strategy:               "external",
			PolicyKind:             "external",
			Outcome:                "selected",
			DurationMS:             time.Since(start).Milliseconds(),
			EligibleTargetCount:    len(eligibleTargets),
			AllTargetCount:         len(allTargets),
			SelectedCandidateIndex: dec.TargetIndex,
			FallbackCount:          len(dec.Fallbacks),
			ClassLabel:             dec.ClassLabel,
		})
		return dec, nil
	}
	if strings.EqualFold(strings.TrimSpace(s.cfg.OnError), "fallback") {
		label := "external-policy:fallback"
		errorClass := policyErrorClass(err, "external")
		return decision{
			Target:          eligibleTargets[0],
			Fallbacks:       eligibleTargets[1:],
			ClassLabel:      &label,
			Strategy:        "external",
			GroupName:       group,
			TargetIndex:     0,
			ConversationKey: conversationKey,
			PolicyExecutions: []policyExecutionLogRecord{{
				Seq:                    1,
				Strategy:               "external",
				PolicyKind:             "external",
				Outcome:                "fallback",
				DurationMS:             time.Since(start).Milliseconds(),
				EligibleTargetCount:    len(eligibleTargets),
				AllTargetCount:         len(allTargets),
				SelectedCandidateIndex: 0,
				FallbackCount:          len(eligibleTargets) - 1,
				ClassLabel:             &label,
				ErrorClass:             errorClass,
				ErrorMessage:           safePolicyExecutionMessage(errorClass),
				TerminalErrorType:      "external-policy-fallback",
			}},
		}, nil
	}
	return decision{}, policyFailure(group, "external", "external", "", err, time.Since(start).Milliseconds(), len(eligibleTargets), len(allTargets))
}

func externalBaselineDecision(group string, eligibleTargets []Target, outcome string, policyErr error, durationMS int64, allTargetCount int) decision {
	label := "external-policy:" + outcome
	execution := policyExecutionLogRecord{
		Seq:                    1,
		Strategy:               "external",
		PolicyKind:             "external",
		Outcome:                outcome,
		DurationMS:             durationMS,
		EligibleTargetCount:    len(eligibleTargets),
		AllTargetCount:         allTargetCount,
		SelectedCandidateIndex: 0,
		FallbackCount:          len(eligibleTargets) - 1,
		ClassLabel:             &label,
	}
	if policyErr != nil {
		execution.ErrorClass = policyErrorClass(policyErr, "external")
		execution.ErrorMessage = safePolicyExecutionMessage(execution.ErrorClass)
		execution.TerminalErrorType = "external-policy-shadow-error"
	}
	return decision{
		Target:           eligibleTargets[0],
		Fallbacks:        eligibleTargets[1:],
		ClassLabel:       &label,
		Strategy:         "external",
		GroupName:        group,
		TargetIndex:      0,
		PolicyExecutions: []policyExecutionLogRecord{execution},
	}
}

func (s *externalPolicyStrategy) pick(ctx context.Context, group string, req *IRRequest, contract *ModelGroupContract, eligibleTargets, allTargets []Target, providers map[string]ProviderConfig, caller *callerRuntime, tokenID, callerDialect string) (decision, error) {
	callerID := ""
	if caller != nil {
		callerID = caller.cfg.ID
	}
	summary := buildRequestSummary(req, callerDialect)
	summary.ConversationKey = buildExternalPolicyConversationKey(s.cfg.ConversationKey, callerID, group, req)
	summary.ConversationKeySrc = conversationKeySource(req)
	input := externalPolicyInput{
		Group:           group,
		Context:         summary,
		Contract:        buildScriptContract(contract),
		Requirements:    routingRequirements(req, callerDialect),
		Targets:         buildScriptTargets(eligibleTargets, providers),
		Caller:          buildScriptCaller(caller, tokenID),
		InputModalities: requestInputModalities(req),
		Now:             time.Now().UTC().Format(time.RFC3339),
	}
	if s.cfg.IncludeRequest {
		input.Request = req
		input.Text = requestText(req)
	}
	hint, err := extractExternalPolicyVerifierHint(s.cfg.VerifierHints, req)
	if err != nil {
		return decision{}, fmt.Errorf("external policy verifier hint: %w", err)
	}
	input.VerifierHint = hint
	body, err := json.Marshal(input)
	if err != nil {
		return decision{}, err
	}
	raw, err := s.call(ctx, body)
	if err != nil {
		return decision{}, err
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return decision{}, fmt.Errorf("external policy response is not JSON")
	}
	out, err := exportDecisionOutput(payload, "external policy")
	if err != nil {
		return decision{}, err
	}
	primary, err := resolveScriptTarget(out, eligibleTargets)
	if err != nil {
		return decision{}, err
	}
	fallbacks, err := resolveScriptFallbacks(out, primary, eligibleTargets)
	if err != nil {
		return decision{}, err
	}
	var classLabel *string
	if out.ClassLabel != "" {
		classLabel = safePolicyClassLabel(out.ClassLabel)
	}
	return decision{
		Target:          eligibleTargets[primary],
		Fallbacks:       fallbacks,
		ClassLabel:      classLabel,
		Strategy:        "external",
		GroupName:       group,
		TargetIndex:     primary,
		ConversationKey: summary.ConversationKey,
	}, nil
}

func (s *externalPolicyStrategy) call(ctx context.Context, body []byte) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	u, err := url.Parse(s.cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid external_policy.url")
	}
	if err := validateEgressURL(u, s.cfg.AllowHosts, s.cfg.AllowHTTP, "external policy"); err != nil {
		return nil, err
	}
	method := strings.ToUpper(strings.TrimSpace(s.cfg.Method))
	if method == "" {
		method = http.MethodPost
	}
	if method != http.MethodGet && method != http.MethodPost {
		return nil, fmt.Errorf("external_policy method %s is not allowed", method)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build external policy request: %w", err)
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Content-Type", "application/json")
	for name, value := range s.cfg.Headers {
		if !scriptConfigHeaderAllowed(name) {
			return nil, fmt.Errorf("configured external_policy header %s is not allowed", name)
		}
		httpReq.Header.Set(name, value)
	}
	resp, err := s.client.Do(httpReq)
	if err != nil {
		return nil, auditSafeHTTPError(err, "external policy request failed")
	}
	defer resp.Body.Close()
	limit := s.cfg.MaxResponseBytes
	if limit <= 0 {
		limit = 64 << 10
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("external policy response exceeds max_response_bytes")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("external policy returned HTTP %d", resp.StatusCode)
	}
	return raw, nil
}

func buildRequestSummary(req *IRRequest, callerDialect string) requestSummary {
	if req == nil {
		return requestSummary{Dialect: callerDialect, EstimatedTokens: 1}
	}
	messageTextChars, messagePartCount := canonicalMessageTextChars(req.Messages)
	inputPartCount := 0
	if len(req.Messages) == 0 {
		inputPartCount = len(req.InputParts)
	}
	metadataKeys := make([]string, 0, len(req.Metadata))
	for key := range req.Metadata {
		metadataKeys = append(metadataKeys, key)
	}
	sort.Strings(metadataKeys)
	inputChars := canonicalInputTextChars(req)
	textChars := len(req.System) + inputChars + messageTextChars
	return requestSummary{
		Model:               req.Model,
		Dialect:             callerDialect,
		EstimatedTokens:     estimateSummaryTokens(req, textChars),
		TextChars:           textChars,
		SystemChars:         len(req.System),
		InputChars:          inputChars,
		InputPartCount:      inputPartCount,
		MessageCount:        len(req.Messages),
		MessageTextChars:    messageTextChars,
		MessagePartCount:    messagePartCount,
		ImageCount:          requestImageCount(req),
		ToolCount:           len(req.Tools),
		HasTools:            len(req.Tools) > 0,
		HasStructuredOutput: requestHasStructuredOutput(req),
		MaxTokens:           req.MaxTokens,
		MaxTokensField:      req.MaxTokensField,
		TemperatureSet:      req.Temperature != nil,
		Stream:              req.Stream,
		StopCount:           len(req.Stop),
		MetadataKeys:        metadataKeys,
		Reasoning:           buildReasoningSummary(req.Reasoning),
	}
}

func buildReasoningSummary(intent ReasoningIntent) *reasoningSummary {
	if !intent.Requested && !intent.Disabled {
		return nil
	}
	return &reasoningSummary{
		Requested:    intent.Requested,
		Disabled:     intent.Disabled,
		Kind:         intent.Kind,
		Effort:       intent.Effort,
		BudgetTokens: intent.BudgetTokens,
		Source:       intent.Source,
	}
}

func canonicalMessageTextChars(messages []IRMessage) (int, int) {
	textChars := 0
	partCount := 0
	for _, msg := range messages {
		if len(msg.Parts) == 0 {
			textChars += len(msg.Content)
			continue
		}
		for _, part := range msg.Parts {
			partCount++
			textChars += len(part.Text)
		}
	}
	return textChars, partCount
}

func canonicalInputTextChars(req *IRRequest) int {
	if req == nil || len(req.Messages) > 0 {
		return 0
	}
	if len(req.InputParts) > 0 {
		return contentPartTextChars(req.InputParts)
	}
	return len(req.Input)
}

func contentPartTextChars(parts []IRContentPart) int {
	textChars := 0
	for _, part := range parts {
		textChars += len(part.Text)
	}
	return textChars
}

func estimateSummaryTokens(req *IRRequest, textChars int) int {
	chars := textChars
	if req != nil {
		chars += requestImageCount(req) * 1024
	}
	if chars == 0 {
		return 1
	}
	return chars/4 + 1
}

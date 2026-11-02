// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/metrum-ai/router/internal/buildinfo"
)

// maxEmbeddingsBodyBytes bounds the inbound body before parsing. Embedding
// requests are text batches; they do not need chat-sized payloads.
const maxEmbeddingsBodyBytes = 8 << 20

// maxEmbeddingInputs bounds one request so a caller cannot ask for an
// unbounded number of vectors in a single admission.
const maxEmbeddingInputs = 2048

// maxEmbeddingInputBytes bounds each individual input string.
const maxEmbeddingInputBytes = 1 << 16

var (
	errEmbeddingInput                 = errors.New("invalid embedding input")
	errEmbeddingDimensions            = errors.New("invalid embedding dimensions")
	errEmbeddingUnsupportedGroup      = errors.New("model group does not serve embeddings")
	errEmbeddingDimensionsRequired    = errors.New("embedding dimensions are required")
	errEmbeddingDimensionsUnsupported = errors.New("embedding dimensions are not served")
)

// embeddingsRequest is the supported OpenAI-compatible subset. Unknown fields
// are ignored so client SDK extras do not fail, but every field that changes
// routing, cost or output shape is validated.
type embeddingsRequest struct {
	Model          string          `json:"model"`
	Input          json.RawMessage `json:"input"`
	Dimensions     int             `json:"dimensions"`
	EncodingFormat string          `json:"encoding_format"`

	// texts holds one entry per requested vector. tokens records that the input
	// was pre-tokenized, which the PII policy cannot inspect.
	texts       []string
	tokens      [][]int
	tokenArrays bool
}

func (r *embeddingsRequest) inputCount() int {
	if r.tokenArrays {
		return len(r.tokens)
	}
	return len(r.texts)
}

// parseEmbeddingsRequest validates shape, bounds and encoding before any
// upstream call. It never echoes input text in errors.
func parseEmbeddingsRequest(body []byte) (*embeddingsRequest, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, errEmbeddingInput
	}
	var req embeddingsRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&req); err != nil {
		return nil, errEmbeddingInput
	}
	if err := ensureNoTrailingJSON(decoder); err != nil {
		return nil, errEmbeddingInput
	}
	switch strings.ToLower(strings.TrimSpace(req.EncodingFormat)) {
	case "", "float", "base64":
	default:
		return nil, errEmbeddingInput
	}
	if req.Dimensions < 0 || req.Dimensions > maxEmbeddingDimensions {
		return nil, errEmbeddingDimensions
	}
	if err := req.parseInput(); err != nil {
		return nil, err
	}
	return &req, nil
}

func ensureNoTrailingJSON(decoder *json.Decoder) error {
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return errEmbeddingInput
	}
	return nil
}

func (r *embeddingsRequest) parseInput() error {
	raw := bytes.TrimSpace(r.Input)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return errEmbeddingInput
	}
	switch raw[0] {
	case '"':
		var single string
		if err := json.Unmarshal(raw, &single); err != nil {
			return errEmbeddingInput
		}
		if err := validateEmbeddingText(single); err != nil {
			return err
		}
		r.texts = []string{single}
		return nil
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return errEmbeddingInput
		}
		if len(items) == 0 || len(items) > maxEmbeddingInputs {
			return errEmbeddingInput
		}
		first := bytes.TrimSpace(items[0])
		if len(first) == 0 {
			return errEmbeddingInput
		}
		switch first[0] {
		case '"':
			texts := make([]string, 0, len(items))
			for _, item := range items {
				var text string
				if err := json.Unmarshal(item, &text); err != nil {
					return errEmbeddingInput
				}
				if err := validateEmbeddingText(text); err != nil {
					return err
				}
				texts = append(texts, text)
			}
			r.texts = texts
			return nil
		case '[':
			arrays := make([][]int, 0, len(items))
			for _, item := range items {
				var tokens []int
				if err := json.Unmarshal(item, &tokens); err != nil {
					return errEmbeddingInput
				}
				if len(tokens) == 0 {
					return errEmbeddingInput
				}
				arrays = append(arrays, tokens)
			}
			r.tokens, r.tokenArrays = arrays, true
			return nil
		default:
			// A single token array is one input.
			var tokens []int
			if err := json.Unmarshal(raw, &tokens); err != nil {
				return errEmbeddingInput
			}
			if len(tokens) == 0 {
				return errEmbeddingInput
			}
			r.tokens, r.tokenArrays = [][]int{tokens}, true
			return nil
		}
	default:
		return errEmbeddingInput
	}
}

func validateEmbeddingText(text string) error {
	if strings.TrimSpace(text) == "" || len(text) > maxEmbeddingInputBytes || !utf8.ValidString(text) {
		return errEmbeddingInput
	}
	return nil
}

// embeddingInputTokenEstimate approximates prompt tokens for admission
// reservation only. Settlement uses the upstream-reported count when present.
// embeddingGroupTargets returns the configured targets eligible for one
// embeddings request. Omitted dimensions use the configured default; without
// one the request fails closed rather than assuming a dimension.
func embeddingGroupTargets(group ModelGroup, requestedDimensions int) ([]Target, int, error) {
	if len(group.Targets) == 0 || !targetOutputsEmbeddings(group.Targets[0]) {
		return nil, 0, errEmbeddingUnsupportedGroup
	}
	dimensions := requestedDimensions
	if dimensions <= 0 {
		defaults := map[int]bool{}
		for _, target := range group.Targets {
			if target.DefaultEmbeddingDimensions > 0 {
				defaults[target.DefaultEmbeddingDimensions] = true
			}
		}
		if len(defaults) != 1 {
			return nil, 0, errEmbeddingDimensionsRequired
		}
		for value := range defaults {
			dimensions = value
		}
	}
	var eligible []Target
	for _, target := range group.Targets {
		if !targetOutputsEmbeddings(target) {
			continue
		}
		for _, dims := range target.EmbeddingDimensions {
			if dims == dimensions {
				eligible = append(eligible, target)
				break
			}
		}
	}
	if len(eligible) == 0 {
		return nil, 0, errEmbeddingDimensionsUnsupported
	}
	return eligible, dimensions, nil
}

func (r *embeddingsRequest) inputTokenEstimate() int {
	if r.tokenArrays {
		total := 0
		for _, tokens := range r.tokens {
			total += len(tokens)
		}
		if total > 0 {
			return total
		}
		return 1
	}
	total := 0
	for _, text := range r.texts {
		total += utf8.RuneCountInString(text)/4 + 1
	}
	if total > 0 {
		return total
	}
	return 1
}

// applyEmbeddingsPII redacts textual input and fails closed on token arrays,
// which cannot be inspected for policy matches.
func applyEmbeddingsPII(req *embeddingsRequest, cfg PIIFilterConfig) (piiFilterResult, error) {
	result := piiFilterResult{RuleCounts: map[string]int{}}
	if !cfg.Enabled {
		return result, nil
	}
	result.Mode = normalizePIIFilterMode(cfg)
	if req.tokenArrays {
		return result, piiFilterBlockedError{}
	}
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
	for i := range req.texts {
		req.texts[i] = state.redact(req.texts[i])
	}
	if result.Replacements > 0 {
		result.Applied = true
	}
	if result.Applied && (result.Mode == "fail_on_match" || cfg.FailOnMatch) {
		return result, piiFilterBlockedError{}
	}
	if result.LimitExceeded {
		return result, piiFilterBlockedError{}
	}
	return result, nil
}

// embeddingsUpstreamBody rebuilds the upstream request from validated fields so
// no unvalidated caller field reaches the provider.
func (r *embeddingsRequest) upstreamBody(model string) ([]byte, error) {
	payload := map[string]any{"model": model}
	if r.tokenArrays {
		if len(r.tokens) == 1 {
			payload["input"] = r.tokens[0]
		} else {
			payload["input"] = r.tokens
		}
	} else if len(r.texts) == 1 {
		payload["input"] = r.texts[0]
	} else {
		payload["input"] = r.texts
	}
	if r.Dimensions > 0 {
		payload["dimensions"] = r.Dimensions
	}
	if r.EncodingFormat != "" {
		payload["encoding_format"] = r.EncodingFormat
	}
	return json.Marshal(payload)
}

type embeddingsUpstreamResponse struct {
	Object string `json:"object"`
	Data   []struct {
		Object    string          `json:"object"`
		Index     int             `json:"index"`
		Embedding json.RawMessage `json:"embedding"`
	} `json:"data"`
	Model string `json:"model"`
	Usage *struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// handleEmbeddings serves POST /v1/embeddings over OpenAI-compatible upstreams.
// It is a dedicated path: no chat translation, no response or semantic cache.
func (s *Service) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	rc, ok := s.begin(w, r, "openai-embeddings")
	if !ok {
		return
	}
	rc.rec.Endpoint = "embeddings"
	rc.rec.Cache = "bypass"
	if !s.prepareRouterVia(w, r, rc) {
		return
	}
	conc := s.quota.AcquireConcurrency(rc.caller)
	if !conc.OK {
		s.writeAdmissionError(w, rc, conc)
		return
	}
	bodyHeld := true
	defer func() {
		if bodyHeld {
			s.quota.Release(rc.caller)
		}
	}()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxEmbeddingsBodyBytes))
	if err != nil {
		s.writeError(w, rc, http.StatusBadRequest, "invalid-body")
		return
	}
	req, err := parseEmbeddingsRequest(body)
	if err != nil {
		code := "invalid-request"
		if errors.Is(err, errEmbeddingDimensions) {
			code = "unsupported_capability"
		}
		rc.trace("embeddings_invalid_request", err.Error(), Target{}, 0, http.StatusBadRequest, code, false, 0)
		s.writeError(w, rc, http.StatusBadRequest, code)
		return
	}
	if req.Model == "" {
		req.Model = s.cfg.Server.DefaultModelGroup
	}
	if req.Model == "" {
		s.writeError(w, rc, http.StatusBadRequest, "missing-model")
		return
	}
	group, exists := s.cfg.Models[req.Model]
	if !exists || !rc.caller.allow[req.Model] {
		s.writeError(w, rc, http.StatusNotFound, "model_not_found")
		return
	}
	rc.rec.RequestedModel = req.Model
	targets, dimensions, err := embeddingGroupTargets(group, req.Dimensions)
	if err != nil {
		reason := "embedding-group-mismatch"
		switch {
		case errors.Is(err, errEmbeddingDimensionsRequired):
			reason = "embedding-dimensions-required"
		case errors.Is(err, errEmbeddingDimensionsUnsupported):
			reason = "embedding-dimensions-unsupported"
		}
		rc.trace("embeddings_route_rejected", reason, Target{}, 0, http.StatusBadRequest, "unsupported_capability", false, 0)
		s.writeError(w, rc, http.StatusBadRequest, "unsupported_capability")
		return
	}
	var compatible []Target
	for _, target := range targets {
		provider := s.cfg.Provider[target.Provider]
		if normalizeDialect(targetDialect(provider, target)) != "openai-chat" || isBedrock(provider) || normalizeDialect(provider.Dialect) != "openai-chat" {
			continue
		}
		compatible = append(compatible, target)
	}
	if len(compatible) == 0 {
		rc.trace("embeddings_route_rejected", "no openai-compatible embedding target", Target{}, 0, http.StatusBadRequest, "unsupported_capability", false, 0)
		s.writeError(w, rc, http.StatusBadRequest, "unsupported_capability")
		return
	}
	// Release the body-buffering slot before admission, mirroring chat.
	s.quota.Release(rc.caller)
	bodyHeld = false
	ad := s.quota.Admit(rc.caller, 0)
	if !ad.OK {
		s.writeAdmissionError(w, rc, ad)
		return
	}
	defer s.quota.Release(rc.caller)
	rc.rec.QuotaState = ad.QuotaState
	rc.rec.KeyState = ad.KeyState
	if ad.WarningText != "" {
		addRouterWarningHeaders(w.Header(), ad.WarningText)
		rc.rec.Warnings = append(rc.rec.Warnings, ad.WarningText)
	}
	piiResult, err := applyEmbeddingsPII(req, group.PIIFilter)
	if err != nil {
		var blocked piiFilterBlockedError
		if errors.As(err, &blocked) {
			rc.rec.PIIFilterMode = piiResult.Mode
			rc.rec.PIIFilterReplacements = piiResult.Replacements
			rc.trace("pii_filter_blocked", "pii_filter blocked embedding input", Target{}, 0, http.StatusBadRequest, "pii-filter-blocked", false, 0)
			s.writeError(w, rc, http.StatusBadRequest, "pii-filter-blocked")
			return
		}
		s.writeError(w, rc, http.StatusBadGateway, "pii-filter-failed")
		return
	}
	if piiResult.Applied {
		rc.rec.PIIFilterApplied = true
		rc.rec.PIIFilterMode = piiResult.Mode
		rc.rec.PIIFilterReplacements = piiResult.Replacements
		rc.rec.PIIFilterRuleCount = piiRuleCount(piiResult)
		rc.rec.Warnings = appendWarning(rc.rec.Warnings, "pii-filter-applied")
		rc.rec.Warnings = append(rc.rec.Warnings, piiResult.Warnings...)
	}
	resAd := s.quota.ReserveTokens(rc.caller, req.inputTokenEstimate())
	if !resAd.OK {
		s.writeAdmissionError(w, rc, resAd)
		return
	}
	defer s.quota.ReleaseReservation(rc.caller, resAd.Reservation)
	rc.rec.QuotaState = resAd.QuotaState
	rc.rec.KeyState = resAd.KeyState
	if resAd.WarningText != "" {
		addRouterWarningHeaders(w.Header(), resAd.WarningText)
		rc.rec.Warnings = append(rc.rec.Warnings, resAd.WarningText)
	}
	if shapeCfg, shapeScope, enabled := resolveTrafficShapeConfig(s.cfg.Server.TrafficShape, rc.caller.cfg); enabled {
		shapeRes := s.trafficShape.Admit(r.Context(), trafficShapeRequest{
			CallerID:             rc.caller.cfg.ID,
			ModelGroup:           req.Model,
			Config:               shapeCfg,
			Scope:                shapeScope,
			Phase:                trafficShapeBucketRequestStart,
			EstimatedInputTokens: req.inputTokenEstimate(),
			TotalReservedTokens:  req.inputTokenEstimate(),
			IncludeRequestStart:  true,
		})
		recordTrafficShapeResult(rc, shapeRes)
		if shapeRes.Decision == trafficShapeDecisionRejected {
			s.writeTrafficShapeError(w, rc, req.Model, shapeRes)
			return
		}
	}
	target := compatible[0]
	rc.rec.ResolvedGroup = req.Model
	rc.rec.Strategy = group.Strategy
	s.applyServingTargetRecord(rc, target)
	data, usage, usageSource, err := s.callEmbeddingsUpstream(r.Context(), w, rc, req, target, dimensions)
	if err != nil {
		var upstream upstreamError
		if errors.As(err, &upstream) && upstream.StatusCode > 0 && upstream.StatusCode < 500 {
			s.writeError(w, rc, upstream.StatusCode, "upstream-rejected")
			return
		}
		if errors.As(err, &upstream) && upstream.TimedOut {
			s.writeError(w, rc, http.StatusGatewayTimeout, "upstream-timeout")
			return
		}
		s.writeError(w, rc, http.StatusBadGateway, "upstream-failed")
		return
	}
	quotaState, keyState, qerr := s.quota.RecordTokens(rc.caller, resAd.Reservation, usage)
	if qerr != nil {
		s.writeError(w, rc, http.StatusServiceUnavailable, "quota-state-error")
		return
	}
	rc.rec.QuotaState, rc.rec.KeyState = quotaState, keyState
	rc.rec.Usage = usage
	rc.rec.UsageSource = usageSource
	if usageSource == "estimated" {
		rc.rec.Warnings = appendWarning(rc.rec.Warnings, "usage-estimated")
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Metrum-Usage-Source", usageSource)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
	s.finish(rc, http.StatusOK, nil)
}

// callEmbeddingsUpstream issues one bounded attempt and validates the vector
// response shape before any bytes reach the caller.
func (s *Service) callEmbeddingsUpstream(ctx context.Context, w http.ResponseWriter, rc *requestContext, req *embeddingsRequest, target Target, dimensions int) ([]byte, Usage, string, error) {
	provider := s.cfg.Provider[target.Provider]
	body, err := req.upstreamBody(target.Model)
	if err != nil {
		return nil, Usage{}, "", upstreamError{Class: "encode_error", Err: err}
	}
	endpoint, err := embeddingsEndpoint(provider.BaseURL)
	if err != nil {
		return nil, Usage{}, "", upstreamError{Class: "encode_error", Err: err}
	}
	attemptCtx := ctx
	if ms := s.attemptTimeoutMS(rc.rec.RequestedModel, target); ms > 0 {
		var cancel context.CancelFunc
		attemptCtx, cancel = context.WithTimeout(ctx, time.Duration(ms)*time.Millisecond)
		defer cancel()
	}
	upstream, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, Usage{}, "", upstreamError{Class: "encode_error", Err: err}
	}
	upstream.Header.Set("Content-Type", "application/json")
	upstream.Header.Set("User-Agent", "metrum-ai-router/"+buildinfo.Version)
	for name, value := range provider.Headers {
		upstream.Header.Set(name, value)
	}
	forwardRouterVia(upstream.Header, rc.via)
	if provider.APIKey != "" {
		upstream.Header.Set("Authorization", "Bearer "+provider.APIKey)
	}
	start := time.Now()
	resp, err := s.httpClient.Do(upstream)
	ms := durationMillis(time.Since(start))
	rc.rec.UpstreamMS = &ms
	rc.rec.Attempts = 1
	if err != nil {
		classified := classifyContextError(err)
		rc.rec.AttemptsDetail = append(rc.rec.AttemptsDetail, attemptLogRecord{
			Index: 1, TS: formatUsageTime(time.Now().UTC()), Provider: target.Provider, Model: target.Model,
			Dialect: "openai", EndpointHost: endpointHost(provider.BaseURL), DurationMS: ms,
			ErrorClass: classified.Class, Retryable: classified.Retryable, TimedOut: classified.TimedOut, Selected: true,
		})
		return nil, Usage{}, "", upstreamError{Class: classified.Class, Message: "embedding upstream call failed", Retryable: classified.Retryable, TimedOut: classified.TimedOut, Err: err}
	}
	defer resp.Body.Close()
	limit := int64(s.cfg.Server.Upstream.MaxResponseBytes)
	if limit <= 0 {
		limit = 32 << 20
	}
	payload, readErr := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if readErr != nil || int64(len(payload)) > limit {
		return nil, Usage{}, "", upstreamError{Class: "response_too_large", Message: "embedding upstream response exceeded the size limit"}
	}
	attempt := attemptLogRecord{
		Index: 1, TS: formatUsageTime(time.Now().UTC()), Provider: target.Provider, Model: target.Model,
		Dialect: "openai", EndpointHost: endpointHost(provider.BaseURL), DurationMS: ms, StatusCode: resp.StatusCode, Selected: true,
	}
	if resp.StatusCode >= 400 {
		class := "upstream_rejected"
		if resp.StatusCode >= 500 {
			class = "upstream_failed"
		}
		attempt.ErrorClass = class
		rc.rec.AttemptsDetail = append(rc.rec.AttemptsDetail, attempt)
		status := resp.StatusCode
		if status >= 500 {
			status = 0
		}
		return nil, Usage{}, "", upstreamError{Class: class, StatusCode: status, Message: "embedding upstream rejected the request"}
	}
	rc.rec.AttemptsDetail = append(rc.rec.AttemptsDetail, attempt)
	var parsed embeddingsUpstreamResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, Usage{}, "", upstreamError{Class: "invalid_response", Message: "embedding upstream returned invalid JSON"}
	}
	if err := validateEmbeddingsUpstreamResponse(&parsed, req.inputCount(), dimensions); err != nil {
		return nil, Usage{}, "", upstreamError{Class: "invalid_response", Message: err.Error()}
	}
	usage := Usage{}
	usageSource := "estimated"
	if parsed.Usage != nil && parsed.Usage.PromptTokens >= 0 && parsed.Usage.TotalTokens >= 0 && (parsed.Usage.PromptTokens > 0 || parsed.Usage.TotalTokens > 0) {
		usage.InputTokens = parsed.Usage.PromptTokens
		usage.TotalTokens = parsed.Usage.TotalTokens
		if usage.TotalTokens == 0 {
			usage.TotalTokens = usage.InputTokens
		}
		usageSource = "reported"
	} else {
		usage.InputTokens = req.inputTokenEstimate()
		usage.TotalTokens = usage.InputTokens
	}
	// Report the caller-facing model identity without changing what served.
	if public := rc.callerModel(target.Model); public != target.Model {
		payload, err = rewriteEmbeddingsModel(payload, public)
		if err != nil {
			return nil, Usage{}, "", upstreamError{Class: "invalid_response", Message: "embedding upstream returned invalid JSON"}
		}
	}
	return payload, usage, usageSource, nil
}

func validateEmbeddingsUpstreamResponse(parsed *embeddingsUpstreamResponse, inputs, dimensions int) error {
	if len(parsed.Data) != inputs {
		return fmt.Errorf("embedding response returned %d vectors for %d inputs", len(parsed.Data), inputs)
	}
	seen := make([]bool, inputs)
	for _, item := range parsed.Data {
		if item.Index < 0 || item.Index >= inputs || seen[item.Index] {
			return fmt.Errorf("embedding response has an invalid or repeated index")
		}
		seen[item.Index] = true
		if len(bytes.TrimSpace(item.Embedding)) == 0 {
			return fmt.Errorf("embedding response is missing a vector")
		}
		if dimensions > 0 {
			var vector []float64
			if err := json.Unmarshal(item.Embedding, &vector); err != nil {
				// base64 encodings are opaque here; dimension checks need float vectors.
				continue
			}
			if len(vector) != dimensions {
				return fmt.Errorf("embedding response has %d dimensions, expected %d", len(vector), dimensions)
			}
		}
	}
	return nil
}

func rewriteEmbeddingsModel(payload []byte, model string) ([]byte, error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}
	body["model"] = encoded
	return json.Marshal(body)
}

// embeddingsEndpoint derives the provider embeddings URL from the configured
// base URL, preserving any version suffix.
func embeddingsEndpoint(base string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(base), "/")
	if trimmed == "" {
		return "", fmt.Errorf("provider base URL is empty")
	}
	return trimmed + "/embeddings", nil
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/metrum-ai/router/internal/buildinfo"
)

func (s *Service) writeEmbeddingError(w http.ResponseWriter, rc *requestContext, status int, code, param string) {
	if rc != nil {
		rc.trace("request_error", code, Target{}, 0, status, code, false, 0)
		rc.rec.Status, rc.rec.Error, rc.rec.ErrorClass, rc.rec.ErrorMessage = status, &code, code, code
		s.finish(rc, status, &code)
	}
	var p any
	if param != "" {
		p = param
	}
	typ := "invalid_request_error"
	if status == http.StatusTooManyRequests {
		typ = "rate_limit_error"
	} else if status >= 500 {
		typ = "server_error"
	}
	writeJSON(w, status, map[string]any{"error": map[string]any{"type": typ, "code": code, "param": p, "message": code}})
}

func (s *Service) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	if !s.identifiersAvailable() {
		s.writeUnavailableIdentifiers(w, "openai-chat")
		return
	}
	rc, ok := s.begin(w, r, "openai-chat")
	if !ok {
		return
	}
	if !s.prepareRouterVia(w, r, rc) {
		return
	}
	conc := s.quota.AcquireConcurrency(rc.caller)
	if !conc.OK {
		s.writeAdmissionError(w, rc, conc)
		return
	}
	held := true
	defer func() {
		if held {
			s.quota.Release(rc.caller)
		}
	}()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		s.writeEmbeddingError(w, rc, 400, "invalid_request", "input")
		return
	}
	req, param, err := decodeEmbeddingInput(body)
	if err != nil {
		s.writeEmbeddingError(w, rc, 400, "invalid_request", param)
		return
	}
	// Canonicalize before forwarding so duplicate JSON members cannot bypass
	// authorization, validation, or PII filtering on a different parser.
	body, err = json.Marshal(req.Raw)
	if err != nil {
		s.writeEmbeddingError(w, rc, 400, "invalid_request", "")
		return
	}
	if req.Model == "" {
		req.Model = s.cfg.Server.DefaultModelGroup
	}
	if req.Model == "" {
		s.writeEmbeddingError(w, rc, 400, "missing-model", "model")
		return
	}
	group, exists := s.cfg.Models[req.Model]
	if !exists || !rc.caller.allow[req.Model] {
		reason := "unknown-model"
		if exists {
			reason = "model-not-allowed"
		}
		s.writeModelNotFound(w, rc, req.Model, reason)
		return
	}
	rc.rec.RequestedModel = req.Model
	if !isEmbeddingGroup(group) {
		s.writeEmbeddingError(w, rc, 400, "unsupported_endpoint_for_model", "model")
		return
	}
	// Token IDs cannot be inspected for PII without the target tokenizer.
	if req.TokenIDs && group.PIIFilter.Enabled {
		s.writeEmbeddingError(w, rc, 400, "pii-filter-token-input-unsupported", "input")
		return
	}
	total := 0
	for _, count := range req.Items {
		total += count
	}
	s.quota.Release(rc.caller)
	held = false
	shapeCfg, shapeScope, shapeEnabled := resolveTrafficShapeConfig(s.cfg.Server.TrafficShape, rc.caller.cfg)
	if shapeEnabled {
		result := s.trafficShape.Admit(r.Context(), trafficShapeRequest{CallerID: rc.caller.cfg.ID, ModelGroup: req.Model, Config: shapeCfg, Scope: shapeScope, Phase: trafficShapeBucketRequestStart, EstimatedInputTokens: total, TotalReservedTokens: total, IncludeRequestStart: true})
		recordTrafficShapeResult(rc, result)
		if result.Decision == trafficShapeDecisionRejected {
			s.writeTrafficShapeError(w, rc, req.Model, result)
			return
		}
	}
	ad := s.quota.Admit(rc.caller, 0)
	if !ad.OK {
		s.writeAdmissionError(w, rc, ad)
		return
	}
	defer s.quota.Release(rc.caller)
	rc.rec.QuotaState, rc.rec.KeyState = ad.QuotaState, ad.KeyState
	if ad.WarningText != "" {
		addRouterWarningHeaders(w.Header(), ad.WarningText)
		rc.rec.Warnings = append(rc.rec.Warnings, ad.WarningText)
	}
	ir := &IRRequest{Model: req.Model, Raw: req.Raw}
	pii, err := applyPIIFilter(ir, group.PIIFilter)
	if err != nil {
		var blocked piiFilterBlockedError
		if errors.As(err, &blocked) {
			s.writeError(w, rc, 400, "pii-filter-blocked")
		} else {
			s.writeError(w, rc, 502, "pii-filter-failed")
		}
		return
	}
	if pii.Applied {
		rc.rec.PIIFilterApplied = true
		rc.rec.PIIFilterMode = pii.Mode
		rc.rec.PIIFilterReplacements = pii.Replacements
		rc.rec.PIIFilterRuleCount = piiRuleCount(pii)
		body, err = json.Marshal(ir.Raw)
		if err != nil {
			s.writeEmbeddingError(w, rc, 400, "invalid_request", "input")
			return
		}
		filtered, _, parseErr := decodeEmbeddingInput(body)
		if parseErr != nil {
			s.writeEmbeddingError(w, rc, 400, "invalid_request", "input")
			return
		}
		// Admission accounts for the larger of caller and redacted estimates.
		for i, count := range filtered.Items {
			if count > req.Items[i] {
				total += count - req.Items[i]
				req.Items[i] = count
			}
		}
	}
	candidates := make([]Target, 0, len(group.Targets))
	dimEligible := false
	for _, target := range group.Targets {
		provider := s.cfg.Provider[target.Provider]
		dialect := targetDialect(provider, target)
		if isBedrock(provider) || (dialect != "openai-chat" && dialect != "openai-responses") {
			continue
		}
		if req.Dimensions > 0 {
			found := false
			for _, dim := range target.EmbeddingDimensions {
				if dim == req.Dimensions {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		dimEligible = true
		fits := true
		for _, count := range req.Items {
			if target.ContextTokens > 0 && count > target.ContextTokens {
				fits = false
				break
			}
		}
		if fits {
			candidates = append(candidates, target)
		}
	}
	if len(candidates) == 0 {
		if req.Dimensions > 0 && !dimEligible {
			s.writeEmbeddingError(w, rc, 400, "unsupported_dimensions", "dimensions")
		} else {
			s.writeEmbeddingError(w, rc, 400, "unsupported_embedding_input", "input")
		}
		return
	}
	if strings.EqualFold(group.Strategy, "weighted") {
		candidates = weightedOrder(candidates)
	}
	res := s.quota.ReserveTokens(rc.caller, total)
	if !res.OK {
		s.writeAdmissionError(w, rc, res)
		return
	}
	defer s.quota.ReleaseReservation(rc.caller, res.Reservation)
	rc.rec.QuotaState, rc.rec.KeyState = res.QuotaState, res.KeyState
	if res.WarningText != "" {
		addRouterWarningHeaders(w.Header(), res.WarningText)
		rc.rec.Warnings = append(rc.rec.Warnings, res.WarningText)
	}
	if shapeEnabled {
		result := s.trafficShape.Admit(r.Context(), trafficShapeRequest{CallerID: rc.caller.cfg.ID, ModelGroup: req.Model, Config: shapeCfg, Scope: shapeScope, Phase: "token_reservation", EstimatedInputTokens: total, TotalReservedTokens: total, IncludeTokenReservations: true})
		recordTrafficShapeResult(rc, result)
		if result.Decision == trafficShapeDecisionRejected {
			s.writeTrafficShapeError(w, rc, req.Model, result)
			return
		}
	}
	rc.rec.ResolvedGroup, rc.rec.Strategy = req.Model, group.Strategy
	shape := shapeReservationInput{EstimatedInputTokens: total, TotalReservedTokens: total}
	var retryAfter time.Duration
	var lastStatus int
	var lastData []byte
	var lastRetryHeader string
	var lastTarget Target
	admitted := false
	for i, target := range candidates {
		shapeAd := s.admitTrafficShape(rc, req.Model, target, shape)
		if !shapeAd.OK {
			if shapeAd.RetryAfter > retryAfter {
				retryAfter = shapeAd.RetryAfter
			}
			continue
		}
		admitted = true
		s.applyServingTargetRecord(rc, target)
		rc.rec.FallbackUsed = i > 0
		var retryHeader string
		data, status, callErr := s.callEmbeddingUpstream(r.Context(), rc, target, body, &retryHeader)
		lastStatus, lastData, lastRetryHeader, lastTarget = status, data, retryHeader, target
		if callErr != nil && status >= 200 && status < 300 {
			// A successful provider response may already be billed. Never retry it.
			usage := Usage{InputTokens: total, TotalTokens: total}
			if !s.settleEmbeddingUsage(w, rc, res.Reservation, usage, "estimated") {
				return
			}
			s.writeError(w, rc, 502, "upstream-failed")
			return
		}
		if callErr != nil || status == 429 || status >= 500 {
			if status == 429 || (status >= 500 && status != http.StatusLoopDetected) {
				classified := classifyUpstreamStatus(status, data)
				classified.RetryAfter = parseRetryAfterHeader(retryHeader, time.Now().UTC())
				s.recordAdaptiveTrafficBackoff(rc, req.Model, target, classified, shape)
			}
			if status == http.StatusLoopDetected {
				s.writeError(w, rc, http.StatusLoopDetected, "router-hop-limit")
				return
			}
			if i+1 < len(candidates) && r.Context().Err() == nil {
				continue
			}
			if status == 429 {
				s.writeEmbeddingRateLimit(w, rc, target, data, retryHeader)
				return
			}
			s.writeError(w, rc, 502, "upstream-failed")
			return
		}
		if status < 200 || status >= 300 {
			if status < 400 {
				status = 502
			}
			s.writeEmbeddingError(w, rc, status, "upstream-rejected", "")
			return
		}
		var response map[string]any
		valid := json.Unmarshal(data, &response) == nil && response != nil
		usage := Usage{InputTokens: total, TotalTokens: total}
		source := "estimated"
		if valid {
			if fields, ok := response["usage"].(map[string]any); ok {
				if n, ok := embeddingInt(fields["prompt_tokens"]); ok {
					usage.InputTokens = n
					usage.TotalTokens = n
					source = "reported"
				}
			}
		}
		if !s.settleEmbeddingUsage(w, rc, res.Reservation, usage, source) {
			return
		}
		if !valid {
			s.writeError(w, rc, 502, "upstream-failed")
			return
		}
		if !s.cfg.Server.Identifiers.passthrough() {
			data, err = countTokensModelBody(data, req.Model)
			if err != nil {
				s.writeError(w, rc, 502, "upstream-failed")
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
		s.finish(rc, http.StatusOK, nil)
		return
	}
	if !admitted {
		s.writeUpstreamCapacityThrottledError(w, rc, req.Model, "openai-chat", upstreamCapacityThrottledError{RetryAfter: retryAfter, TargetCount: len(candidates)})
		return
	}
	if lastStatus == 429 {
		s.writeEmbeddingRateLimit(w, rc, lastTarget, lastData, lastRetryHeader)
		return
	}
	s.writeError(w, rc, 502, "upstream-failed")
}

func (s *Service) writeEmbeddingRateLimit(w http.ResponseWriter, rc *requestContext, target Target, body []byte, retryHeader string) {
	provider := s.cfg.Provider[target.Provider]
	if provider.UpstreamKind == "router" && trustedRouterRateLimit(http.StatusTooManyRequests, body, classifyUpstreamStatus(http.StatusTooManyRequests, body).Class) {
		if value := validTrustedRetryAfterHeader(retryHeader); value != "" {
			w.Header().Set("Retry-After", value)
		}
	}
	s.writeEmbeddingError(w, rc, http.StatusTooManyRequests, "upstream-rejected", "")
}

func (s *Service) settleEmbeddingUsage(w http.ResponseWriter, rc *requestContext, reservation *quotaReservation, usage Usage, source string) bool {
	quotaState, keyState, err := s.quota.RecordTokens(rc.caller, reservation, usage)
	if err != nil {
		s.writeError(w, rc, 503, "quota-state-error")
		return false
	}
	rc.rec.QuotaState, rc.rec.KeyState, rc.rec.Usage, rc.rec.UsageSource = quotaState, keyState, usage, source
	return true
}

func (s *Service) callEmbeddingUpstream(ctx context.Context, rc *requestContext, target Target, body []byte, retryHeader *string) ([]byte, int, error) {
	provider := s.cfg.Provider[target.Provider]
	body, err := countTokensModelBody(body, target.Model)
	if err != nil {
		return nil, 0, err
	}
	u, err := url.Parse(provider.BaseURL)
	if err != nil {
		return nil, 0, err
	}
	basePath := strings.TrimRight(u.Path, "/")
	for _, suffix := range []string{"/chat/completions", "/responses", "/embeddings"} {
		if strings.HasSuffix(basePath, suffix) {
			basePath = strings.TrimSuffix(basePath, suffix)
			break
		}
	}
	u.Path = joinPath(basePath, "/embeddings")
	if ms := s.attemptTimeoutMS(rc.rec.RequestedModel, target); ms > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(ms)*time.Millisecond)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "metrum-ai-router/"+buildinfo.Version)
	for name, value := range provider.Headers {
		req.Header.Set(name, value)
	}
	forwardRouterVia(req.Header, rc.via)
	if provider.APIKey != "" {
		switch upstreamAuthScheme(provider, "openai-chat") {
		case "x-api-key":
			req.Header.Set("X-API-Key", provider.APIKey)
		case "x-goog-api-key":
			req.Header.Set("X-Goog-Api-Key", provider.APIKey)
		default:
			req.Header.Set("Authorization", "Bearer "+provider.APIKey)
		}
	}
	start := time.Now()
	attempt := attemptLogRecord{Index: len(rc.rec.AttemptsDetail) + 1, TS: start.UTC().Format(time.RFC3339Nano), Provider: target.Provider, Model: target.Model, Dialect: targetDialect(provider, target), EndpointHost: u.Hostname(), RequestBytes: int64(len(body)), AttemptTimeoutMS: s.attemptTimeoutMS(rc.rec.RequestedModel, target)}
	defer func() {
		attempt.DurationMS = durationMillis(time.Since(start))
		rc.rec.AttemptsDetail = append(rc.rec.AttemptsDetail, attempt)
	}()
	resp, err := s.httpClient.Do(req)
	ms := durationMillis(time.Since(start))
	rc.rec.UpstreamMS = &ms
	rc.rec.Attempts++
	if err != nil {
		attempt.ErrorClass = "upstream-failed"
		attempt.Retryable = true
		return nil, 0, err
	}
	attempt.StatusCode = resp.StatusCode
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		attempt.ErrorClass = "upstream-rejected"
		attempt.Retryable = resp.StatusCode == 429 || resp.StatusCode >= 500
	}
	defer resp.Body.Close()
	*retryHeader = resp.Header.Get("Retry-After")
	limit := int64(s.cfg.Server.Upstream.MaxResponseBytes)
	if limit <= 0 {
		limit = 32 << 20
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	attempt.ResponseBytes = int64(len(data))
	if err != nil {
		attempt.ErrorClass = "upstream-failed"
		return nil, resp.StatusCode, err
	}
	if int64(len(data)) > limit {
		attempt.ErrorClass = "upstream-failed"
		return nil, resp.StatusCode, errors.New("response exceeds limit")
	}
	attempt.Selected = resp.StatusCode >= 200 && resp.StatusCode < 300
	return data, resp.StatusCode, nil
}

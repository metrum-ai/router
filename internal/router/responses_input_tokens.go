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
	"time"

	"github.com/metrum-ai/router/internal/buildinfo"
)

// Responses input counting is a free upstream operation, but remains subject
// to caller authentication, model authorization, request rate and concurrency.
func (s *Service) handleResponsesInputTokens(w http.ResponseWriter, r *http.Request) {
	if !s.identifiersAvailable() {
		s.writeUnavailableIdentifiers(w, "openai-responses")
		return
	}
	rc, ok := s.begin(w, r, "openai-responses")
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
		s.writeError(w, rc, http.StatusBadRequest, "invalid-body")
		return
	}
	body, pinnedResponse, ok := s.resolveContinuationBody(w, rc, body)
	if !ok {
		return
	}
	body, err = decodeIdentifierFieldsWithPinnedResponse(body, s.identifiers, pinnedResponse != nil)
	if err != nil {
		s.writeError(w, rc, http.StatusBadRequest, "invalid-tool-call-id")
		return
	}
	req, err := decodeRequest("openai-responses", body, r.Header)
	if err != nil {
		s.writeError(w, rc, http.StatusBadRequest, "invalid-request")
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
		reason := "unknown-model"
		if exists {
			reason = "model-not-allowed"
		}
		s.writeModelNotFound(w, rc, req.Model, reason)
		return
	}
	rc.rec.RequestedModel = req.Model
	if isEmbeddingGroup(group) {
		s.writeError(w, rc, http.StatusBadRequest, "unsupported_endpoint_for_model")
		return
	}
	// Stored references use the recorded target, never ordinary failover.
	if rawValuePresent(req.Raw, "previous_response_id") && pinnedResponse == nil {
		s.writeUnsupportedEndpoint(w, r, rc, false)
		return
	}
	if pinnedResponse != nil {
		if pinnedResponse.ModelGroup != req.Model {
			s.writeError(w, rc, http.StatusNotFound, "response_not_found")
			return
		}
		target, available := s.affinityTarget(pinnedResponse)
		if !available {
			s.writeError(w, rc, http.StatusConflict, "response_target_unavailable")
			return
		}
		group.Targets = []Target{target}
		group.Strategy = "static"
	}
	native := group
	native.Targets = nil
	for _, target := range group.Targets {
		provider := s.cfg.Provider[target.Provider]
		if targetDialect(provider, target) == "openai-responses" && !isBedrock(provider) {
			native.Targets = append(native.Targets, target)
		}
	}
	if len(native.Targets) == 0 {
		s.writeUnsupportedEndpoint(w, r, rc, false)
		return
	}
	if reason := s.mediaAdmission(native, req, "openai-responses"); reason != "" {
		s.writeError(w, rc, http.StatusBadRequest, reason)
		return
	}
	// Release the body-buffering slot before request shaping can queue.
	s.quota.Release(rc.caller)
	held = false
	shapeCfg, shapeScope, shapeEnabled := resolveTrafficShapeConfig(s.cfg.Server.TrafficShape, rc.caller.cfg)
	if shapeEnabled {
		shapeRes := s.trafficShape.Admit(r.Context(), trafficShapeRequest{
			CallerID: rc.caller.cfg.ID, ModelGroup: req.Model, Config: shapeCfg,
			Scope: shapeScope, Phase: trafficShapeBucketRequestStart, IncludeRequestStart: true,
		})
		recordTrafficShapeResult(rc, shapeRes)
		if shapeRes.Decision == trafficShapeDecisionRejected {
			s.writeTrafficShapeError(w, rc, req.Model, shapeRes)
			return
		}
	}
	ad := s.quota.Admit(rc.caller, 0)
	if !ad.OK {
		s.writeAdmissionError(w, rc, ad)
		return
	}
	defer s.quota.Release(rc.caller)
	rc.rec.QuotaState, rc.rec.KeyState, rc.rec.UsageSource = ad.QuotaState, ad.KeyState, "none"
	if ad.WarningText != "" {
		addRouterWarningHeaders(w.Header(), ad.WarningText)
		rc.rec.Warnings = append(rc.rec.Warnings, ad.WarningText)
	}
	pii, err := applyPIIFilter(req, group.PIIFilter)
	if err != nil {
		var blocked piiFilterBlockedError
		if errors.As(err, &blocked) {
			s.writeError(w, rc, http.StatusBadRequest, "pii-filter-blocked")
		} else {
			s.writeError(w, rc, http.StatusBadGateway, "pii-filter-failed")
		}
		return
	}
	if pii.Applied {
		rc.rec.PIIFilterApplied = true
		rc.rec.PIIFilterMode = pii.Mode
		rc.rec.PIIFilterReplacements = pii.Replacements
		rc.rec.PIIFilterRuleCount = piiRuleCount(pii)
		body, err = json.Marshal(req.Raw)
		if err != nil {
			s.writeError(w, rc, http.StatusBadRequest, "invalid-request")
			return
		}
	}
	dec, err := s.pick(rc, req.Model, native, req, "openai-responses", rc.caller, rc.rec.TokenID)
	if err != nil {
		var eligibility routingEligibilityError
		if errors.As(err, &eligibility) {
			s.writeRoutingEligibilityError(w, rc, eligibility)
		} else {
			s.writeError(w, rc, http.StatusBadGateway, "routing-failed")
		}
		return
	}
	// Free counts still consume upstream request-rate capacity, but do not
	// reserve generation tokens. Try only the already-eligible native targets.
	candidates := append([]Target{dec.Target}, dec.Fallbacks...)
	var target Target
	var chosen bool
	var retryAfter time.Duration
	for i, candidate := range candidates {
		shape := s.admitTrafficShape(rc, req.Model, candidate, shapeReservationInput{})
		if shape.OK {
			target, chosen = candidate, true
			rc.rec.FallbackUsed = i > 0
			break
		}
		if shape.RetryAfter > retryAfter {
			retryAfter = shape.RetryAfter
		}
	}
	if !chosen {
		s.writeUpstreamCapacityThrottledError(w, rc, req.Model, "openai-responses", upstreamCapacityThrottledError{RetryAfter: retryAfter, TargetCount: len(candidates)})
		return
	}
	provider := s.cfg.Provider[target.Provider]
	rc.rec.ResolvedGroup, rc.rec.Strategy = req.Model, group.Strategy
	s.applyServingTargetRecord(rc, target)
	body, err = countTokensModelBody(body, target.Model)
	if err != nil {
		s.writeError(w, rc, http.StatusBadRequest, "invalid-request")
		return
	}
	endpoint, err := upstreamEndpoint(provider.BaseURL, "openai-responses", target)
	if err != nil {
		s.writeError(w, rc, http.StatusBadGateway, "upstream-failed")
		return
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		s.writeError(w, rc, http.StatusBadGateway, "upstream-failed")
		return
	}
	u.Path += "/input_tokens"
	endpoint = u.String()
	ctx := r.Context()
	if ms := s.attemptTimeoutMS(req.Model, target); ms > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(ms)*time.Millisecond)
		defer cancel()
	}
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		s.writeError(w, rc, http.StatusBadGateway, "upstream-failed")
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	upstream.Header.Set("User-Agent", "metrum-ai-router/"+buildinfo.Version)
	for name, value := range provider.Headers {
		upstream.Header.Set(name, value)
	}
	forwardRouterVia(upstream.Header, rc.via)
	if provider.APIKey != "" {
		switch upstreamAuthScheme(provider, "openai-responses") {
		case "x-api-key":
			upstream.Header.Set("X-API-Key", provider.APIKey)
		case "x-goog-api-key":
			upstream.Header.Set("X-Goog-Api-Key", provider.APIKey)
		default:
			upstream.Header.Set("Authorization", "Bearer "+provider.APIKey)
		}
	}
	start := time.Now()
	resp, err := s.httpClient.Do(upstream)
	ms := durationMillis(time.Since(start))
	rc.rec.UpstreamMS = &ms
	rc.rec.Attempts = 1
	if err != nil {
		s.writeError(w, rc, http.StatusBadGateway, "upstream-failed")
		return
	}
	defer resp.Body.Close()
	limit := int64(s.cfg.Server.Upstream.MaxResponseBytes)
	if limit <= 0 {
		limit = 32 << 20
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		s.writeError(w, rc, http.StatusBadGateway, "upstream-failed")
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		status, code := resp.StatusCode, "upstream-rejected"
		if status == http.StatusLoopDetected {
			code = "router-hop-limit"
		} else if status < 400 || status >= 500 {
			status, code = http.StatusBadGateway, "upstream-failed"
		}
		if retry := resp.Header.Get("Retry-After"); retry != "" {
			w.Header().Set("Retry-After", retry)
		}
		s.writeError(w, rc, status, code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(data)
	s.finish(rc, resp.StatusCode, nil)
}

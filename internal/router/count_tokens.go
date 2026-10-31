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
	"strings"
	"time"

	"github.com/metrum-ai/router/internal/buildinfo"
)

func countTokensEndpoint(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	switch {
	case strings.HasSuffix(u.Path, "/v1/messages/count_tokens"):
	case strings.HasSuffix(u.Path, "/v1/messages"):
		u.Path += "/count_tokens"
	case strings.HasSuffix(u.Path, "/v1"):
		u.Path += "/messages/count_tokens"
	default:
		u.Path = joinPath(u.Path, "/v1/messages/count_tokens")
	}
	return u.String(), nil
}

// Preserve every unrelated field and byte, including cache controls, multimodal
// blocks and provider extensions. Only the public model alias is replaced.
func countTokensModelBody(body []byte, model string) ([]byte, error) {
	root, err := parseIDJSON(body)
	if err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(model)
	if field := root.field("model"); field != nil {
		return applyIDReplacements(body, []idReplacement{{field.start, field.end, encoded}}), nil
	}
	if root.fields == nil {
		return nil, fmt.Errorf("count_tokens requires an object")
	}
	addition := append([]byte(`"model":`), encoded...)
	if len(root.fields) > 0 {
		addition = append([]byte(","), addition...)
	}
	return applyIDReplacements(body, []idReplacement{{root.end - 1, root.end - 1, addition}}), nil
}

func (s *Service) serveCountTokens(w http.ResponseWriter, r *http.Request, rc *requestContext, req *IRRequest, body []byte) {
	group := s.cfg.Models[req.Model]
	dec, err := s.pick(rc, req.Model, group, req, "anthropic", rc.caller, rc.rec.TokenID)
	if err != nil {
		s.writeError(w, rc, http.StatusBadGateway, "routing-failed")
		return
	}
	target := dec.Target
	provider := s.cfg.Provider[target.Provider]
	rc.rec.ResolvedGroup = req.Model
	rc.rec.Strategy = group.Strategy
	s.applyServingTargetRecord(rc, target)
	// Bedrock uses a different CountTokens protocol; do not send an Anthropic
	// HTTP endpoint or provider credentials to an unsupported transport.
	if targetDialect(provider, target) != "anthropic" || isBedrock(provider) {
		w.Header().Set("X-Metrum-Usage-Source", "estimated")
		writeJSON(w, http.StatusOK, map[string]any{"input_tokens": estimateTokens(req)})
		s.finish(rc, http.StatusOK, nil)
		return
	}
	body, err = countTokensModelBody(body, target.Model)
	if err != nil {
		s.writeError(w, rc, http.StatusBadRequest, "invalid-request")
		return
	}
	endpoint, err := countTokensEndpoint(provider.BaseURL)
	if err != nil {
		s.writeError(w, rc, http.StatusBadGateway, "upstream-failed")
		return
	}
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
	if upstream.Header.Get("Anthropic-Version") == "" {
		upstream.Header.Set("Anthropic-Version", "2023-06-01")
	}
	upstream.Header.Del("Anthropic-Beta")
	if beta := permittedBetas(strings.Join(r.Header.Values("Anthropic-Beta"), ","), target.AllowedBetas); beta != "" {
		upstream.Header.Set("Anthropic-Beta", beta)
	}
	if provider.APIKey != "" {
		switch upstreamAuthScheme(provider, "anthropic") {
		case "x-api-key":
			upstream.Header.Set("X-API-Key", provider.APIKey)
		case "x-goog-api-key":
			upstream.Header.Set("X-Goog-Api-Key", provider.APIKey)
		case "replicate":
			upstream.Header.Set("Authorization", "Bearer "+provider.APIKey)
			upstream.Header.Set("Prefer", "wait=60")
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
	if retry := resp.Header.Get("Retry-After"); retry != "" {
		w.Header().Set("Retry-After", retry)
	}
	if resp.StatusCode >= 400 {
		// Count-token errors are still caller-facing Anthropic errors. Preserve
		// provider 4xx status, but never forward a body that may echo secrets.
		status := resp.StatusCode
		code := "upstream-rejected"
		if status == http.StatusLoopDetected {
			code = "router-hop-limit"
		} else if status >= 500 {
			status = http.StatusBadGateway
			code = "upstream-failed"
		}
		s.writeError(w, rc, status, code)
		return
	}
	if contentType := resp.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		w.Header().Set("X-Metrum-Usage-Source", "reported")
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(data)
	s.finish(rc, resp.StatusCode, nil)
}

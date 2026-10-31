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
	"strconv"
	"strings"
	"time"

	"github.com/metrum-ai/router/internal/buildinfo"
)

func (s *Service) ownedResponse(ctx context.Context, id, owner string) (*responseAffinityRecord, error) {
	if s.usage == nil || s.usage.db == nil {
		return nil, errors.New("affinity database unavailable")
	}
	db := s.usage.db.WithContext(ctx)
	row, err := lookupOwnedResponseAffinity(db, id, owner, time.Now().UTC())
	if err != nil || row != nil {
		return row, err
	}
	// POST enqueues before exposing its ID. A barrier on a miss makes an
	// immediately-following GET observe accepted writes without delaying POST.
	if s.usage.writer != nil {
		flushCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if err := s.usage.writer.flush(flushCtx); err != nil {
			return nil, err
		}
		return lookupOwnedResponseAffinity(db, id, owner, time.Now().UTC())
	}
	return nil, nil
}

// resolveContinuationBody checks ownership on the exact caller-visible ID
// before identifier decoding, and substitutes only the recorded upstream ID.
func (s *Service) resolveContinuationBody(w http.ResponseWriter, rc *requestContext, body []byte) ([]byte, *responseAffinityRecord, bool) {
	if s.usage == nil {
		return body, nil, true
	}
	root, err := parseIDJSON(body)
	if err != nil || root == nil {
		return body, nil, true
	}
	node := root.field("previous_response_id")
	if node == nil || string(body[node.start:node.end]) == "null" {
		return body, nil, true
	}
	id := node.value()
	if id == "" || len(id) > 2048 {
		s.writeError(w, rc, http.StatusNotFound, "response_not_found")
		return nil, nil, false
	}
	row, err := s.ownedResponse(rc.ctx, id, rc.caller.cfg.ID)
	if err != nil {
		s.writeError(w, rc, http.StatusServiceUnavailable, "response_affinity_unavailable")
		return nil, nil, false
	}
	if row == nil {
		s.writeError(w, rc, http.StatusNotFound, "response_not_found")
		return nil, nil, false
	}
	replacement, _ := json.Marshal(row.UpstreamResponseID)
	return applyIDReplacements(body, []idReplacement{{start: node.start, end: node.end, value: replacement}}), row, true
}

func (s *Service) settleObservedBackground(ctx context.Context, row *responseAffinityRecord, response map[string]any) error {
	if row == nil || !row.Background || row.UsageSettled {
		return nil
	}
	status, terminal := terminalBackgroundStatus(stringValue(response["status"]))
	usage, reported := reportedBackgroundUsage(response)
	if !terminal {
		return nil
	}
	if !reported {
		if s.usage.writer != nil {
			s.usage.writer.settlementUnreported.Add(1)
		}
		return nil
	}
	changed, err := settleBackgroundResponse(ctx, s.usage.db, row.ResponseID, row.CallerID, status, usage, time.Now().UTC())
	if err != nil {
		if s.usage.writer != nil {
			s.usage.writer.settlementFailures.Add(1)
		}
		return err
	}
	if changed {
		row.UsageSettled = true
	}
	return nil
}

func (s *Service) affinityTarget(row *responseAffinityRecord) (Target, bool) {
	if row == nil {
		return Target{}, false
	}
	group, ok := s.cfg.Models[row.ModelGroup]
	if !ok {
		return Target{}, false
	}
	for _, target := range group.Targets {
		provider, ok := s.cfg.Provider[target.Provider]
		if !ok || provider.APIKey == "" || target.ForceStoreFalse || targetDialect(provider, target) != "openai-responses" || isBedrock(provider) {
			continue
		}
		key, err := responseAffinityTargetKey(target.Provider, provider, target)
		if err == nil && key == row.TargetKey {
			return target, true
		}
	}
	return Target{}, false
}

type storedStreamIdentifiers struct {
	base                  IdentifierTransform
	upstreamID, visibleID string
}

func (t storedStreamIdentifiers) Encode(id string) string {
	if id == t.upstreamID {
		return t.visibleID
	}
	if t.base != nil {
		return t.base.Encode(id)
	}
	return id
}
func (t storedStreamIdentifiers) Decode(id string) (string, error) {
	if id == t.visibleID {
		return t.upstreamID, nil
	}
	if t.base != nil {
		return t.base.Decode(id)
	}
	return id, nil
}
func (t storedStreamIdentifiers) KeyIDs() (string, string) {
	if t.base != nil {
		return t.base.KeyIDs()
	}
	return "", ""
}

func safeResponsePathID(id string) bool {
	if len(id) == 0 || len(id) > 2048 || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' || r == '~') {
			return false
		}
	}
	return true
}

func responseOperationQuery(r *http.Request, operation string, ids IdentifierTransform) (url.Values, error) {
	out := url.Values{}
	for key, values := range r.URL.Query() {
		if key == "include[]" {
			if (operation != "get" && operation != "input_items") || len(values) > 16 {
				return nil, errors.New("invalid query")
			}
			for _, value := range values {
				if len(value) > 256 {
					return nil, errors.New("invalid query")
				}
				out.Add(key, value)
			}
			continue
		}
		if len(values) != 1 {
			return nil, errors.New("invalid query")
		}
		v := values[0]
		switch key {
		case "stream":
			if operation != "get" || (v != "true" && v != "false") {
				return nil, errors.New("invalid query")
			}
		case "starting_after":
			if operation != "get" {
				return nil, errors.New("invalid query")
			}
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				return nil, errors.New("invalid query")
			}
		case "limit":
			if operation != "input_items" {
				return nil, errors.New("invalid query")
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 100 {
				return nil, errors.New("invalid query")
			}
		case "order":
			if operation != "input_items" || (v != "asc" && v != "desc") {
				return nil, errors.New("invalid query")
			}
		case "after", "before":
			if operation != "input_items" || len(v) > 2048 {
				return nil, errors.New("invalid query")
			}
			if ids != nil && strings.HasPrefix(v, "mr_") {
				var err error
				v, err = ids.Decode(v)
				if err != nil {
					return nil, errors.New("invalid query")
				}
			}
		default:
			return nil, errors.New("invalid query")
		}
		out.Add(key, v)
	}
	return out, nil
}

func (s *Service) handleStoredResponse(w http.ResponseWriter, r *http.Request) {
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
	if s.usage == nil {
		s.writeUnsupportedEndpoint(w, r, rc, false)
		return
	}
	operation := "get"
	if r.Method == http.MethodDelete {
		operation = "delete"
	} else if strings.HasSuffix(r.URL.Path, "/input_items") {
		operation = "input_items"
	} else if strings.HasSuffix(r.URL.Path, "/cancel") {
		operation = "cancel"
	}
	rc.rec.Endpoint = "responses." + operation
	ad := s.quota.AdmitStoredOperation(rc.caller)
	if !ad.OK {
		s.writeAdmissionError(w, rc, ad)
		return
	}
	defer s.quota.Release(rc.caller)
	row, err := s.ownedResponse(r.Context(), r.PathValue("id"), rc.caller.cfg.ID)
	if err != nil {
		s.writeError(w, rc, http.StatusServiceUnavailable, "response_affinity_unavailable")
		return
	}
	if row == nil || !rc.caller.allow[row.ModelGroup] {
		s.writeError(w, rc, http.StatusNotFound, "response_not_found")
		return
	}
	if operation == "delete" && row.Background && !row.UsageSettled {
		// A completed POST may have queued settlement immediately after the
		// affinity insert. Observe that work before refusing the delete.
		if s.usage.writer != nil {
			flushCtx, cancel := context.WithTimeout(r.Context(), time.Second)
			flushErr := s.usage.writer.flush(flushCtx)
			cancel()
			if flushErr != nil {
				s.writeError(w, rc, http.StatusServiceUnavailable, "background_settlement_unavailable")
				return
			}
			row, err = lookupOwnedResponseAffinity(s.usage.db.WithContext(r.Context()), row.ResponseID, rc.caller.cfg.ID, time.Now().UTC())
			if err != nil {
				s.writeError(w, rc, http.StatusServiceUnavailable, "response_affinity_unavailable")
				return
			}
			if row == nil {
				s.writeError(w, rc, http.StatusNotFound, "response_not_found")
				return
			}
		}
		if !row.UsageSettled {
			// Preserve the upstream object until reported usage can be reconciled.
			s.writeError(w, rc, http.StatusConflict, "background_usage_unsettled")
			return
		}
	}
	target, ok := s.affinityTarget(row)
	if !ok || !safeResponsePathID(row.UpstreamResponseID) {
		s.writeError(w, rc, http.StatusConflict, "response_target_unavailable")
		return
	}
	query, err := responseOperationQuery(r, operation, s.identifiers)
	if err != nil {
		s.writeError(w, rc, http.StatusBadRequest, "invalid-request")
		return
	}
	// openai-go's GetStreaming sets stream in a JSON GET body. Normalize that
	// SDK form to the standard query parameter without forwarding other data.
	if operation == "get" && r.Body != nil {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 16<<10+1))
		if err != nil || len(raw) > 16<<10 {
			s.writeError(w, rc, http.StatusBadRequest, "invalid-request")
			return
		}
		if len(bytes.TrimSpace(raw)) > 0 {
			var body map[string]json.RawMessage
			if json.Unmarshal(raw, &body) != nil || body == nil {
				s.writeError(w, rc, http.StatusBadRequest, "invalid-request")
				return
			}
			for key, value := range body {
				if key != "stream" {
					s.writeError(w, rc, http.StatusBadRequest, "invalid-request")
					return
				}
				var stream bool
				if json.Unmarshal(value, &stream) != nil || (query.Has("stream") && query.Get("stream") != strconv.FormatBool(stream)) {
					s.writeError(w, rc, http.StatusBadRequest, "invalid-request")
					return
				}
				query.Set("stream", strconv.FormatBool(stream))
			}
		}
	}
	provider := s.cfg.Provider[target.Provider]
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
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + row.UpstreamResponseID
	if operation == "cancel" || operation == "input_items" {
		u.Path += "/" + operation
	}
	u.RawQuery = query.Encode()
	ctx := r.Context()
	if ms := s.attemptTimeoutMS(row.ModelGroup, target); ms > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(ms)*time.Millisecond)
		defer cancel()
	}
	up, err := http.NewRequestWithContext(ctx, r.Method, u.String(), nil)
	if err != nil {
		s.writeError(w, rc, http.StatusBadGateway, "upstream-failed")
		return
	}
	up.Header.Set("User-Agent", "metrum-ai-router/"+buildinfo.Version)
	for name, value := range provider.Headers {
		up.Header.Set(name, value)
	}
	forwardRouterVia(up.Header, rc.via)
	if provider.APIKey != "" {
		switch upstreamAuthScheme(provider, "openai-responses") {
		case "x-api-key":
			up.Header.Set("X-API-Key", provider.APIKey)
		case "x-goog-api-key":
			up.Header.Set("X-Goog-Api-Key", provider.APIKey)
		default:
			up.Header.Set("Authorization", "Bearer "+provider.APIKey)
		}
	}
	rc.rec.RequestedModel = row.ModelGroup
	rc.rec.ResolvedGroup = row.ModelGroup
	s.applyServingTargetRecord(rc, target)
	shape := s.admitTrafficShape(rc, row.ModelGroup, target, shapeReservationInput{})
	if !shape.OK {
		s.writeUpstreamCapacityThrottledError(w, rc, row.ModelGroup, "openai-responses", upstreamCapacityThrottledError{RetryAfter: shape.RetryAfter, TargetCount: 1})
		return
	}
	start := time.Now()
	response, err := s.httpClient.Do(up)
	ms := durationMillis(time.Since(start))
	rc.rec.UpstreamMS = &ms
	rc.rec.Attempts = 1
	if err != nil {
		s.writeError(w, rc, http.StatusBadGateway, "upstream-failed")
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode == http.StatusTooManyRequests {
			if value := retryAfterHeaderValue(parseRetryAfterHeader(response.Header.Get("Retry-After"), time.Now().UTC())); value != "" {
				w.Header().Set("Retry-After", value)
			}
		}
		status, code := response.StatusCode, "upstream-rejected"
		if status == 508 {
			code = "router-hop-limit"
		} else if status < 400 || status >= 500 {
			status, code = http.StatusBadGateway, "upstream-failed"
		}
		s.writeError(w, rc, status, code)
		return
	}
	if query.Get("stream") == "true" {
		if !contentTypeIsSSE(response.Header) {
			s.writeError(w, rc, http.StatusBadGateway, "upstream-failed")
			return
		}
		limit := int64(s.cfg.Server.Upstream.MaxResponseBytes)
		if limit <= 0 {
			limit = 32 << 20
		}
		if row.Background {
			rc.responseTerminal = func(object map[string]any) error {
				if stringValue(object["id"]) != row.UpstreamResponseID {
					return errors.New("background response id mismatch")
				}
				return s.settleObservedBackground(ctx, row, object)
			}
		}
		result, streamErr := proxyResponsesSSE(ctx, w, response.Body, "openai-responses", target.Model, limit, rc, storedStreamIdentifiers{base: s.identifiers, upstreamID: row.UpstreamResponseID, visibleID: row.ResponseID})
		rc.responseTerminal = nil
		if streamErr != nil {
			if !result.Committed {
				s.writeError(w, rc, http.StatusBadGateway, "upstream-failed")
			} else {
				code := "upstream-failed"
				s.finish(rc, http.StatusBadGateway, &code)
			}
			return
		}
		s.finish(rc, http.StatusOK, nil)
		return
	}
	data, oversized, err := s.readUpstreamSuccessBody(response.Body)
	if err != nil || oversized {
		s.writeError(w, rc, http.StatusBadGateway, "upstream-failed")
		return
	}
	var body map[string]any
	if operation == "delete" && len(bytes.TrimSpace(data)) == 0 {
		body = map[string]any{"id": row.UpstreamResponseID, "object": "response.deleted", "deleted": true}
	} else if json.Unmarshal(data, &body) != nil || body == nil {
		s.writeError(w, rc, http.StatusBadGateway, "upstream-failed")
		return
	}
	if operation != "input_items" {
		if stringValue(body["id"]) != row.UpstreamResponseID {
			s.writeError(w, rc, http.StatusBadGateway, "upstream-failed")
			return
		}
		if row.Background && (operation == "get" || operation == "cancel") {
			if err := s.settleObservedBackground(ctx, row, body); err != nil {
				s.writeError(w, rc, http.StatusServiceUnavailable, "background_settlement_unavailable")
				return
			}
		}
		body["id"] = row.ResponseID
	}
	if operation == "get" || operation == "cancel" {
		responseObj := &IRResponse{RawResponse: true, Raw: body}
		rewriteNativeResponsesObject(responseObj, s.identifiers)
		body["id"] = row.ResponseID
		if public := rc.publicModel(); public != "" && body["model"] != nil {
			body["model"] = public
		}
	}
	if operation == "input_items" && s.identifiers != nil {
		for _, key := range []string{"first_id", "last_id"} {
			if id, ok := body[key].(string); ok && id != "" {
				body[key] = s.identifiers.Encode(id)
			}
		}
		if items, ok := body["data"].([]any); ok {
			for _, value := range items {
				if item, ok := value.(map[string]any); ok {
					for _, key := range []string{"id", "call_id"} {
						if id, ok := item[key].(string); ok && id != "" {
							item[key] = s.identifiers.Encode(id)
						}
					}
				}
			}
		}
	}
	status := ""
	if operation == "delete" {
		status = "deleted"
	} else if operation == "cancel" {
		status = "cancelled"
	} else if operation == "get" && stringValue(body["status"]) == "completed" {
		status = "completed"
	}
	if status != "" {
		changed, err := updateOwnedResponseAffinityStatus(s.usage.db.WithContext(ctx), row.ResponseID, rc.caller.cfg.ID, status, time.Now().UTC())
		if err != nil || !changed {
			s.writeError(w, rc, http.StatusServiceUnavailable, "response_affinity_unavailable")
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.StatusCode)
	_ = json.NewEncoder(w).Encode(body)
	s.finish(rc, response.StatusCode, nil)
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Stored Responses operations (#533, second slice): GET a stored response, list
// its input items and DELETE it by forwarding to the account that holds it.
//
// Ownership, expiry and target checks happen before any upstream call. A
// missing, expired or other-caller id returns the same 404; a removed or
// incompatible target returns 409 response_target_unavailable. With affinity
// disabled the routes keep the documented 501 unsupported_endpoint behavior.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/metrum-ai/router/internal/buildinfo"
)

type storedResponseOperation int

const (
	storedResponseGet storedResponseOperation = iota
	storedResponseDelete
	storedResponseInputItems
	storedResponseCancel
)

func (op storedResponseOperation) method() string {
	switch op {
	case storedResponseDelete:
		return http.MethodDelete
	case storedResponseCancel:
		return http.MethodPost
	default:
		return http.MethodGet
	}
}

func (op storedResponseOperation) suffix() string {
	switch op {
	case storedResponseInputItems:
		return "/input_items"
	case storedResponseCancel:
		return "/cancel"
	default:
		return ""
	}
}

func (op storedResponseOperation) traceAction() string {
	switch op {
	case storedResponseDelete:
		return "stored_response_delete"
	case storedResponseInputItems:
		return "stored_response_input_items"
	case storedResponseCancel:
		return "stored_response_cancel"
	default:
		return "stored_response_get"
	}
}

func (s *Service) handleStoredResponseGet(w http.ResponseWriter, r *http.Request) {
	s.handleStoredResponseOperation(w, r, storedResponseGet)
}

func (s *Service) handleStoredResponseDelete(w http.ResponseWriter, r *http.Request) {
	s.handleStoredResponseOperation(w, r, storedResponseDelete)
}

func (s *Service) handleStoredResponseInputItems(w http.ResponseWriter, r *http.Request) {
	s.handleStoredResponseOperation(w, r, storedResponseInputItems)
}

func (s *Service) handleStoredResponseCancel(w http.ResponseWriter, r *http.Request) {
	s.handleStoredResponseOperation(w, r, storedResponseCancel)
}

func (s *Service) handleStoredResponseOperation(w http.ResponseWriter, r *http.Request, op storedResponseOperation) {
	// Disabled affinity keeps the v4.6.0 unsupported-endpoint contract exactly.
	if s.responsesAffinity == nil {
		s.handleUnsupportedOpenAIEndpoint(w, r)
		return
	}
	if !s.identifiersAvailable() {
		s.writeUnavailableIdentifiers(w, "openai-responses")
		return
	}
	rc, ok := s.begin(w, r, "openai-responses")
	if !ok {
		return
	}
	// One admission and one release: Admit accounts for the in-flight slot and
	// the request count, so a second AcquireConcurrency/Release pair would
	// double-count the slot for this caller.
	ad := s.quota.Admit(rc.caller, 0)
	if !ad.OK {
		s.writeAdmissionError(w, rc, ad)
		return
	}
	defer s.quota.Release(rc.caller)
	rc.rec.QuotaState = ad.QuotaState
	rc.rec.KeyState = ad.KeyState

	row, target, groupName, err := s.resolveStoredResponse(r, rc)
	if err != nil {
		s.writeResponsesAffinityError(w, rc, err)
		return
	}
	if row.State == "deleted" || row.State == "expired" {
		s.writeResponsesAffinityError(w, rc, affinityClientError{status: http.StatusNotFound, code: "response_not_found", message: "The response was not found."})
		return
	}
	upstreamID, err := s.decryptAffinityUpstreamID(row)
	if err != nil {
		s.writeResponsesAffinityError(w, rc, affinityClientError{status: http.StatusServiceUnavailable, code: "response_affinity_unavailable", message: "Stored response affinity is unavailable."})
		return
	}
	rc.rec.RequestedModel = groupName
	rc.rec.ResolvedGroup = groupName
	rc.rec.TargetProvider = target.Provider
	rc.rec.TargetModel = target.Model
	rc.trace(op.traceAction(), "stored response operation", *target, 0, 0, "", false, 0)

	switch op {
	case storedResponseGet:
		rc.rec.Endpoint = "responses.get"
	case storedResponseDelete:
		rc.rec.Endpoint = "responses.delete"
	case storedResponseInputItems:
		rc.rec.Endpoint = "responses.input_items"
	case storedResponseCancel:
		rc.rec.Endpoint = "responses.cancel"
	}
	if op == storedResponseGet && strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("stream")), "true") {
		s.forwardStoredResponseStream(w, r, rc, row, *target, groupName, upstreamID)
		return
	}
	s.forwardStoredResponseUnary(w, r, rc, op, row, *target, groupName, upstreamID)
}

// resolveStoredResponse performs the ownership, expiry and target checks that
// must happen before any provider dispatch.
func (s *Service) resolveStoredResponse(r *http.Request, rc *requestContext) (*responseAffinityRecord, *Target, string, error) {
	owner, err := s.callerAffinityOwner(rc, s.callerTokenForAffinity(r))
	if err != nil {
		return nil, nil, "", affinityClientError{status: http.StatusServiceUnavailable, code: "response_affinity_unavailable", message: "Stored response affinity is unavailable."}
	}
	presented := strings.TrimSpace(r.PathValue("id"))
	if presented == "" {
		return nil, nil, "", affinityClientError{status: http.StatusNotFound, code: "response_not_found", message: "The response was not found."}
	}
	upstreamID := presented
	if s.identifiers != nil && strings.HasPrefix(presented, "mr_") {
		decoded, decodeErr := s.identifiers.Decode(presented)
		if decodeErr != nil {
			// A malformed caller id is indistinguishable from an unknown one.
			return nil, nil, "", affinityClientError{status: http.StatusNotFound, code: "response_not_found", message: "The response was not found."}
		}
		upstreamID = decoded
	}
	row, err := s.responsesAffinity.lookup(owner, upstreamID, time.Now().UTC())
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, "", affinityClientError{status: http.StatusNotFound, code: "response_not_found", message: "The response was not found."}
		}
		return nil, nil, "", affinityClientError{status: http.StatusServiceUnavailable, code: "response_affinity_unavailable", message: "Stored response lookup is unavailable."}
	}
	// Authorization still applies: a token that lost access to the group that
	// holds the response cannot reach it, and gets the same 404 as an unknown
	// id rather than a distinct forbidden response.
	if rc == nil || rc.caller == nil || !rc.caller.allow[row.ModelGroup] {
		return nil, nil, "", affinityClientError{status: http.StatusNotFound, code: "response_not_found", message: "The response was not found."}
	}
	group, ok := s.cfg.Models[row.ModelGroup]
	if !ok || !s.responsesAffinity.enabledFor(row.ModelGroup) {
		return nil, nil, "", affinityClientError{status: http.StatusConflict, code: "response_target_unavailable", message: "The model group that holds this stored response is no longer available."}
	}
	target, ok := s.affinityTargetForRow(row, group)
	if !ok {
		return nil, nil, "", affinityClientError{status: http.StatusConflict, code: "response_target_unavailable", message: "The upstream target that holds this stored response is no longer available."}
	}
	return row, target, row.ModelGroup, nil
}

// storedResponseEndpoint builds the provider endpoint for one stored resource.
// The recorded account identity is the only target considered.
func (s *Service) storedResponseEndpoint(target Target, upstreamID string, op storedResponseOperation) (string, error) {
	provider := s.cfg.Provider[target.Provider]
	endpoint, err := upstreamEndpoint(provider.BaseURL, "openai-responses", target)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + url.PathEscape(upstreamID) + op.suffix()
	u.RawQuery = ""
	return u.String(), nil
}

// storedResponseQuery maps caller-visible pagination cursors back to provider
// ids. A malformed cursor is an unknown id, not a provider error.
func (s *Service) storedResponseQuery(r *http.Request, op storedResponseOperation) (string, error) {
	if r == nil || r.URL == nil {
		return "", nil
	}
	query := r.URL.Query()
	if op == storedResponseInputItems && s.identifiers != nil {
		for _, key := range []string{"after", "before"} {
			value := strings.TrimSpace(query.Get(key))
			if !strings.HasPrefix(value, "mr_") {
				continue
			}
			decoded, err := s.identifiers.Decode(value)
			if err != nil {
				return "", affinityClientError{status: http.StatusNotFound, code: "response_not_found", message: "The response was not found."}
			}
			query.Set(key, decoded)
		}
	}
	return query.Encode(), nil
}

func (s *Service) storedResponseHTTPRequest(ctx context.Context, r *http.Request, op storedResponseOperation, target Target, upstreamID string) (*http.Request, error) {
	endpoint, err := s.storedResponseEndpoint(target, upstreamID, op)
	if err != nil {
		return nil, err
	}
	query, err := s.storedResponseQuery(r, op)
	if err != nil {
		return nil, err
	}
	if query != "" {
		u, err := url.Parse(endpoint)
		if err != nil {
			return nil, err
		}
		u.RawQuery = query
		endpoint = u.String()
	}
	req, err := http.NewRequestWithContext(ctx, op.method(), endpoint, nil)
	if err != nil {
		return nil, err
	}
	provider := s.cfg.Provider[target.Provider]
	req.Header.Set("User-Agent", "metrum-ai-router/"+buildinfo.Version)
	for name, value := range provider.Headers {
		req.Header.Set(name, value)
	}
	if provider.APIKey != "" {
		switch upstreamAuthScheme(provider, "openai-responses") {
		case "x-api-key":
			req.Header.Set("X-API-Key", provider.APIKey)
		case "x-goog-api-key":
			req.Header.Set("X-Goog-Api-Key", provider.APIKey)
		case "replicate":
			req.Header.Set("Authorization", "Bearer "+provider.APIKey)
		default:
			req.Header.Set("Authorization", "Bearer "+provider.APIKey)
		}
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

func (s *Service) storedResponseContext(parent context.Context, groupName string, target Target) (context.Context, context.CancelFunc) {
	if ms := s.attemptTimeoutMS(groupName, target); ms > 0 {
		return context.WithTimeout(parent, time.Duration(ms)*time.Millisecond)
	}
	if ms := s.cfg.Server.Upstream.TimeoutMS; ms > 0 {
		return context.WithTimeout(parent, time.Duration(ms)*time.Millisecond)
	}
	return context.WithCancel(parent)
}

func (s *Service) forwardStoredResponseUnary(w http.ResponseWriter, r *http.Request, rc *requestContext, op storedResponseOperation, row *responseAffinityRecord, target Target, groupName, upstreamID string) {
	ctx, cancel := s.storedResponseContext(r.Context(), groupName, target)
	defer cancel()
	upReq, err := s.storedResponseHTTPRequest(ctx, r, op, target, upstreamID)
	if err != nil {
		var clientErr affinityClientError
		if errors.As(err, &clientErr) {
			s.writeResponsesAffinityError(w, rc, clientErr)
			return
		}
		s.writeError(w, rc, http.StatusBadGateway, "upstream-failed")
		return
	}
	resp, err := s.httpClient.Do(upReq)
	if err != nil {
		s.writeUpstreamOffline(w, rc, err)
		return
	}
	defer resp.Body.Close()
	limit := int64(s.cfg.Server.Upstream.MaxResponseBytes)
	if limit <= 0 {
		limit = 32 << 20
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		s.writeError(w, rc, http.StatusBadGateway, "upstream-response-too-large")
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.writeStoredResponseUpstreamStatus(w, rc, op, row, target, resp.StatusCode)
		return
	}
	switch op {
	case storedResponseDelete:
		s.markAffinityDeleted(rc, row, "confirmed")
	case storedResponseGet, storedResponseCancel:
		s.recordStoredResponseStatus(rc, row, raw)
	}
	body := s.rewriteStoredResponseBody(raw, row.ModelGroup)
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/json"
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(resp.StatusCode)
	if len(body) > 0 {
		_, _ = w.Write(body)
	}
	rc.rec.Status = resp.StatusCode
	s.finish(rc, resp.StatusCode, nil)
}

func (s *Service) forwardStoredResponseStream(w http.ResponseWriter, r *http.Request, rc *requestContext, row *responseAffinityRecord, target Target, groupName, upstreamID string) {
	ctx, cancel := s.storedResponseContext(r.Context(), groupName, target)
	defer cancel()
	upReq, err := s.storedResponseHTTPRequest(ctx, r, storedResponseGet, target, upstreamID)
	if err != nil {
		var clientErr affinityClientError
		if errors.As(err, &clientErr) {
			s.writeResponsesAffinityError(w, rc, clientErr)
			return
		}
		s.writeError(w, rc, http.StatusBadGateway, "upstream-failed")
		return
	}
	upReq.Header.Set("Accept", "text/event-stream")
	resp, err := s.httpClient.Do(upReq)
	if err != nil {
		s.writeUpstreamOffline(w, rc, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.writeStoredResponseUpstreamStatus(w, rc, storedResponseGet, row, target, resp.StatusCode)
		return
	}
	if !contentTypeIsSSE(resp.Header) {
		s.writeError(w, rc, http.StatusBadGateway, "upstream-protocol-error")
		return
	}
	limit := int64(s.cfg.Server.Upstream.MaxResponseBytes)
	if limit <= 0 {
		limit = 32 << 20
	}
	rc.rec.Stream = true
	rc.rec.TargetProvider = target.Provider
	rc.rec.TargetModel = target.Model
	result, streamErr := proxyNativeSSE(ctx, w, resp.Body, "openai-responses", target.Model, limit, rc, s.identifiers)
	if streamErr == nil && result.Response != nil {
		switch result.Response.StopReason {
		case "completed":
			s.recordAffinityState(rc, row, "completed")
		case "cancelled":
			s.recordAffinityState(rc, row, "cancelled")
		}
	}
	if streamErr != nil {
		classified := classifyError(streamErr)
		rc.trace("stored_response_stream_error", "stored response stream ended with an error", target, 0, 0, classified.Class, !result.Committed, 0)
		if !result.Committed {
			// Nothing reached the caller: return a JSON error, not an empty 200.
			code, status := upstreamFailureResponse(classified, rc)
			s.writeError(w, rc, status, code)
			return
		}
	}
	rc.rec.Status = http.StatusOK
	s.finish(rc, http.StatusOK, nil)
}

// writeStoredResponseUpstreamStatus maps a provider rejection without leaking
// the provider body. A 404 is the same not-found the router returns for an
// unknown id.
func (s *Service) writeStoredResponseUpstreamStatus(w http.ResponseWriter, rc *requestContext, op storedResponseOperation, row *responseAffinityRecord, target Target, status int) {
	switch status {
	case http.StatusNotFound:
		if op == storedResponseDelete {
			// The provider no longer holds it; record the local tombstone but
			// still report not-found to the caller.
			s.markAffinityDeleted(rc, row, "not_found")
		}
		s.writeResponsesAffinityError(w, rc, affinityClientError{status: http.StatusNotFound, code: "response_not_found", message: "The response was not found."})
	case http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusConflict:
		// The request itself is not acceptable at the provider (for example a
		// cancel of an already completed response): keep the client error class
		// instead of reporting a router/upstream failure.
		s.writeError(w, rc, status, "upstream-rejected")
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusPaymentRequired:
		s.writeError(w, rc, http.StatusServiceUnavailable, "upstream-access-denied")
	case http.StatusTooManyRequests:
		s.writeError(w, rc, http.StatusServiceUnavailable, "upstream-rate-limited")
	default:
		s.writeError(w, rc, http.StatusBadGateway, "upstream-failed")
	}
	rc.trace("stored_response_upstream_error", "provider rejected the stored response operation", target, 0, status, "upstream-rejected", false, 0)
}

func (s *Service) writeUpstreamOffline(w http.ResponseWriter, rc *requestContext, err error) {
	rc.trace("stored_response_upstream_error", "provider request failed", Target{}, 0, 0, "upstream-unreachable", true, 0)
	_ = err
	s.writeError(w, rc, http.StatusBadGateway, "upstream-failed")
}

func (s *Service) recordStoredResponseStatus(rc *requestContext, row *responseAffinityRecord, raw []byte) {
	var body struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return
	}
	switch strings.ToLower(strings.TrimSpace(body.Status)) {
	case "completed":
		s.recordAffinityState(rc, row, "completed")
	case "cancelled":
		s.recordAffinityState(rc, row, "cancelled")
	}
}

func (s *Service) recordAffinityState(rc *requestContext, row *responseAffinityRecord, state string) {
	if s.usage == nil || row == nil {
		return
	}
	now := formatUsageTime(time.Now().UTC())
	// Deletion wins: a concurrent GET that finishes after a DELETE must not
	// resurrect a tombstoned row.
	if err := s.usage.db.Model(&responseAffinityRecord{}).Where("id = ? AND state NOT IN ?", row.ID, []string{"deleted", "expired"}).Updates(map[string]any{"state": state, "updated_at": now}).Error; err != nil && rc != nil {
		rc.trace("response_affinity_state_update_failed", "stored response state update failed", Target{}, 0, 0, "db-error", false, 0)
	}
}

func (s *Service) markAffinityDeleted(rc *requestContext, row *responseAffinityRecord, upstreamState string) {
	if s.usage == nil || row == nil {
		return
	}
	now := formatUsageTime(time.Now().UTC())
	if err := s.usage.db.Model(&responseAffinityRecord{}).Where("id = ?", row.ID).Updates(map[string]any{
		"state":                 "deleted",
		"tombstoned_at":         now,
		"updated_at":            now,
		"upstream_delete_state": upstreamState,
	}).Error; err != nil && rc != nil {
		rc.trace("response_affinity_state_update_failed", "stored response tombstone update failed", Target{}, 0, 0, "db-error", false, 0)
	}
}

// rewriteStoredResponseBody applies the caller-facing id mapping and the
// configured model identity to a unary stored-response payload. It edits the
// original bytes in place (offsets from parseIDJSON), so numbers, escaping and
// all untouched fields stay exactly as the provider sent them.
func (s *Service) rewriteStoredResponseBody(raw []byte, groupName string) []byte {
	publicModel := ""
	if s.cfg.Server.Responses.ModelIdentity == ModelIdentityRequestedGroup {
		publicModel = groupName
	}
	return rewriteResponsesJSONBytes(raw, s.identifiers, publicModel)
}

func rewriteResponsesJSONBytes(raw []byte, ids IdentifierTransform, publicModel string) []byte {
	if (ids == nil && publicModel == "") || len(raw) == 0 {
		return raw
	}
	root, err := parseIDJSON(raw)
	if err != nil {
		return raw
	}
	var changes []idReplacement
	replaceString := func(node *idJSONNode, value string) {
		if node == nil || node.text == value {
			return
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return
		}
		changes = append(changes, idReplacement{node.start, node.end, encoded})
	}
	encode := func(node *idJSONNode) {
		if ids == nil || node == nil || node.text == "" || strings.HasPrefix(node.text, "mr_") {
			return
		}
		replaceString(node, ids.Encode(node.text))
	}
	encode(root.field("id"))
	encode(root.field("previous_response_id"))
	encode(root.field("item_id"))
	encode(root.field("response_id"))
	encode(root.field("first_id"))
	encode(root.field("last_id"))
	if publicModel != "" {
		replaceString(root.field("model"), publicModel)
	}
	if response := root.field("response"); response != nil {
		encode(response.field("id"))
		encode(response.field("previous_response_id"))
		if publicModel != "" {
			replaceString(response.field("model"), publicModel)
		}
	}
	for _, list := range []string{"output", "data"} {
		items := root.field(list)
		if items == nil {
			continue
		}
		for _, item := range items.items {
			encode(item.field("id"))
			encode(item.field("call_id"))
			encode(item.field("item_id"))
			encode(item.field("response_id"))
		}
	}
	if len(changes) == 0 {
		return raw
	}
	return applyIDReplacements(raw, changes)
}

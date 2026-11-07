// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// RFC 9457 problem documents for the admin control plane (metrum-ai/router#811).
//
// Every response the admin listener produces with status >= 400 is an
// application/problem+json document. The shape is fixed:
//
//	{ "type": "urn:metrum-router:problem:<code>", "title", "status",
//	  "code", "detail", "instance", "request_id", ...extensions }
//
// `type` is a stable URN rather than a web page, so a client can never depend
// on a documentation URL that moves. `code` is the last segment of `type` and
// is the value clients switch on.
//
// The document never carries request content: Detail is prose from this file's
// catalogue or from a handler that already produces operator-safe text, and no
// header, token, claim, verifier, path from configuration or upstream text is
// ever copied into it. RequestID is server-generated; a client-supplied request
// ID is never trusted or echoed.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

const (
	adminProblemTypePrefix = "urn:metrum-router:problem:"
	// adminRequestIDHeader carries the server-generated correlation ID on
	// every admin-plane response.
	adminRequestIDHeader = "X-Request-Id"
)

// AdminProblem is one RFC 9457 problem document. Extension members are typed
// fields rather than a map so the generated OpenAPI schema can describe them.
type AdminProblem struct {
	Type              string `json:"type"`
	Title             string `json:"title"`
	Status            int    `json:"status"`
	Code              string `json:"code"`
	Detail            string `json:"detail,omitempty"`
	Instance          string `json:"instance,omitempty"`
	RequestID         string `json:"request_id"`
	CurrentRevision   *int64 `json:"current_revision,omitempty"`
	CurrentETag       string `json:"current_etag,omitempty"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"`
}

type adminProblemSpec struct {
	Status int
	Title  string
}

var (
	adminProblemCodesMu sync.RWMutex
	adminProblemCodes   = map[string]adminProblemSpec{
		// Layer-generated codes.
		"invalid-request":          {http.StatusBadRequest, "Invalid request"},
		"unknown-query-parameter":  {http.StatusBadRequest, "Unknown query parameter"},
		"invalid-limit":            {http.StatusBadRequest, "Invalid limit"},
		"invalid-cursor":           {http.StatusBadRequest, "Invalid cursor"},
		"invalid-if-match":         {http.StatusBadRequest, "Invalid If-Match"},
		"invalid-idempotency-key":  {http.StatusBadRequest, "Invalid Idempotency-Key"},
		"not-found":                {http.StatusNotFound, "Resource not found"},
		"conflict":                 {http.StatusConflict, "Conflict"},
		"idempotency-key-conflict": {http.StatusConflict, "Idempotency-Key conflict"},
		"idempotent-request-in-doubt": {
			http.StatusConflict, "Earlier request with this Idempotency-Key did not complete",
		},
		"precondition-failed":   {http.StatusPreconditionFailed, "Precondition failed"},
		"precondition-required": {http.StatusPreconditionRequired, "If-Match is required"},
		"rate-limited":          {http.StatusTooManyRequests, "Rate limit exceeded"},
		"authentication-throttled": {
			http.StatusTooManyRequests, "Too many authentication failures",
		},
		"unavailable":    {http.StatusServiceUnavailable, "Service unavailable"},
		"internal-error": {http.StatusInternalServerError, "Internal error"},

		// Authentication codes owned by #814 (carried verbatim).
		"authentication_required":    {http.StatusUnauthorized, "Authentication required"},
		"invalid_credential":         {http.StatusUnauthorized, "Invalid credential"},
		"unsupported_authentication": {http.StatusUnauthorized, "Unsupported authentication"},
		"sender_constraint_failed":   {http.StatusUnauthorized, "Sender constraint failed"},
		"emergency_inactive":         {http.StatusUnauthorized, "Emergency credential is not active"},
		"admin-browser-auth-unsupported": {
			http.StatusUnauthorized, "Browser authentication is not supported",
		},

		// Authorization and policy codes owned by #812/#813 (carried verbatim).
		"admin-forbidden":           {http.StatusForbidden, "Forbidden"},
		"privilege-escalation":      {http.StatusForbidden, "Privilege escalation refused"},
		"last-policy-admin":         {http.StatusConflict, "Last policy administrator"},
		"invalid-policy":            {http.StatusUnprocessableEntity, "Invalid policy"},
		"authorization-unavailable": {http.StatusServiceUnavailable, "Authorization unavailable"},
		"policy-stale":              {http.StatusServiceUnavailable, "Policy revision is stale"},

		// Listener codes owned by #809 (rendered through this writer).
		"admin-not-found":          {http.StatusNotFound, "Not found"},
		"admin-method-not-allowed": {http.StatusMethodNotAllowed, "Method not allowed"},
		"admin-cors-rejected":      {http.StatusMethodNotAllowed, "Cross-origin request refused"},
		"admin-payload-too-large":  {http.StatusRequestEntityTooLarge, "Payload too large"},
		"admin-tls-required":       {http.StatusForbidden, "TLS is required"},
		"admin-proxy-untrusted":    {http.StatusForbidden, "Untrusted proxy"},
		"admin-metrics-forbidden":  {http.StatusForbidden, "Metrics forbidden"},
	}
)

// RegisterAdminProblemCode adds or replaces one problem code. #809, #812,
// #813, #815 and #816 register their codes here instead of hand-writing error
// bodies, so the whole admin plane shares one document format.
func RegisterAdminProblemCode(code string, status int, title string) {
	code = strings.TrimSpace(code)
	if !validAdminProblemCode(code) || status < 400 || status > 599 || strings.TrimSpace(title) == "" {
		return
	}
	adminProblemCodesMu.Lock()
	defer adminProblemCodesMu.Unlock()
	adminProblemCodes[code] = adminProblemSpec{Status: status, Title: title}
}

// AdminProblemCodes returns the registered code catalogue, for documentation
// and conformance tests.
func AdminProblemCodes() map[string]adminProblemSpec {
	adminProblemCodesMu.RLock()
	defer adminProblemCodesMu.RUnlock()
	out := make(map[string]adminProblemSpec, len(adminProblemCodes))
	for code, spec := range adminProblemCodes {
		out[code] = spec
	}
	return out
}

// validAdminProblemCode accepts the two spellings in use across lanes
// (lower-kebab from #811/#812/#809 and snake_case from #814) and nothing that
// could break a `type` URN or a client switch statement.
func validAdminProblemCode(code string) bool {
	if code == "" || len(code) > 64 {
		return false
	}
	for _, r := range code {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// AdminProblemFor builds a problem document from the catalogue. An unknown code
// is a programming error, so it is reported as internal-error rather than
// leaking an unregistered code.
func AdminProblemFor(r *http.Request, status int, code string) AdminProblem {
	return AdminProblemForRequestID(r, status, code, AdminRequestIDFrom(r))
}

// AdminProblemForRequestID is AdminProblemFor with an explicit request ID, for
// callers that already resolved one.
func AdminProblemForRequestID(r *http.Request, status int, code string, requestID string) AdminProblem {
	if !validAdminProblemCode(code) {
		code, status = "internal-error", http.StatusInternalServerError
	}
	adminProblemCodesMu.RLock()
	spec, ok := adminProblemCodes[code]
	if !ok {
		code, status = "internal-error", http.StatusInternalServerError
		spec = adminProblemCodes[code]
	}
	adminProblemCodesMu.RUnlock()
	if status == 0 {
		status = spec.Status
	}
	problem := AdminProblem{
		Type:      adminProblemTypePrefix + code,
		Title:     spec.Title,
		Status:    status,
		Code:      code,
		RequestID: requestID,
	}
	if r != nil && r.URL != nil {
		problem.Instance = r.URL.Path
	}
	return problem
}

type adminRequestIDContextKey struct{}

// WithAdminRequestID stores the request ID the admin plane assigned.
func WithAdminRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, adminRequestIDContextKey{}, id)
}

// AdminRequestID returns the admin-plane request ID for r, generating one when
// the listener has not assigned one yet (unit tests, direct handler calls).
func AdminRequestID(r *http.Request) string {
	if r == nil {
		return requestID()
	}
	if id, ok := r.Context().Value(adminRequestIDContextKey{}).(string); ok && id != "" {
		return id
	}
	return requestID()
}

// AdminRequestIDFrom is AdminRequestID without generating a new ID; it returns
// "" when none is assigned.
func AdminRequestIDFrom(r *http.Request) string {
	if r == nil {
		return ""
	}
	id, _ := r.Context().Value(adminRequestIDContextKey{}).(string)
	return id
}

// WriteAdminProblem writes a problem document. It is the only error writer the
// admin listener uses: #809, #811, #812, #813, #815 and #816 all route through
// it so no lane can drift into its own error shape.
func WriteAdminProblem(w http.ResponseWriter, r *http.Request, problem AdminProblem) {
	if w == nil {
		return
	}
	if problem.Status == 0 {
		problem.Status = http.StatusInternalServerError
	}
	if problem.Code == "" || problem.Type == "" {
		filled := AdminProblemForRequestID(r, problem.Status, "internal-error", problem.RequestID)
		filled.Detail = problem.Detail
		problem = filled
	}
	if problem.RequestID == "" {
		problem.RequestID = AdminRequestID(r)
	}
	if problem.Title == "" {
		problem.Title = http.StatusText(problem.Status)
	}
	// Defence in depth: no lane can leak a credential through a detail string.
	// Every problem detail is bounded and run through the same redactor the
	// diagnostics surface uses.
	problem.Detail = truncateSafeDetail(redactDiagnosticSecrets(problem.Detail))
	problem.Title = truncateSafeDetail(redactDiagnosticSecrets(problem.Title))
	header := w.Header()
	header.Set("Content-Type", "application/problem+json")
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	if problem.RequestID != "" {
		header.Set(adminRequestIDHeader, problem.RequestID)
	}
	if problem.RetryAfterSeconds > 0 {
		header.Set("Retry-After", fmt.Sprintf("%d", problem.RetryAfterSeconds))
	}
	w.WriteHeader(problem.Status)
	_ = json.NewEncoder(w).Encode(problem)
}

// writeAdminProblemCode is the short form used across the contract layer.
func writeAdminProblemCode(w http.ResponseWriter, r *http.Request, status int, code, detail string) AdminProblem {
	problem := AdminProblemFor(r, status, code)
	problem.Detail = detail
	WriteAdminProblem(w, r, problem)
	return problem
}

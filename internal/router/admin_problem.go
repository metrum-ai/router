// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
)

// AdminProblem is the RFC 9457 problem document every admin-listener response
// with status >= 400 uses. The contract lane (#811) owns the published
// renderer; this local implementation exists so the listener can ship before
// #811 merges and carries the same fields, the same catalogue and the same
// wire shape, so swapping it is a file deletion.
type AdminProblem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Code      string `json:"code"`
	Detail    string `json:"detail,omitempty"`
	Instance  string `json:"instance,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

type adminProblemCode struct {
	status int
	title  string
	detail string
}

var (
	adminProblemCodesMu sync.RWMutex
	adminProblemCodes   = map[string]adminProblemCode{
		"admin-not-found":            {http.StatusNotFound, "Not found", "The requested admin resource does not exist on this listener."},
		"admin-method-not-allowed":   {http.StatusMethodNotAllowed, "Method not allowed", "The admin operation does not accept this HTTP method."},
		"admin-cors-rejected":        {http.StatusMethodNotAllowed, "CORS is not supported", "The admin listener is machine-to-machine only and does not answer CORS preflights."},
		"admin-payload-too-large":    {http.StatusRequestEntityTooLarge, "Payload too large", "The request body exceeds admin.limits.max_body_bytes."},
		"admin-tls-required":         {http.StatusForbidden, "TLS required", "The admin listener accepts this peer only over a trusted HTTPS hop."},
		"admin-proxy-untrusted":      {http.StatusForbidden, "Untrusted proxy", "The peer is not a configured trusted reverse proxy for the admin listener."},
		"unsupported_authentication": {http.StatusUnauthorized, "Unsupported authentication", "The admin listener accepts machine credentials only."},
		"admin-metrics-forbidden":    {http.StatusForbidden, "Metrics forbidden", "The authenticated identity has no metrics read grant."},
		"admin-internal-error":       {http.StatusInternalServerError, "Internal error", "The admin listener could not complete the request."},
	}
)

// adminRequestIDHeader carries the server-generated correlation ID on every
// admin-plane response.
const adminRequestIDHeader = "X-Request-Id"

// RegisterAdminProblemCode adds or replaces a catalogue entry. Other lanes
// register their codes through #811's renderer once it lands; the listener only
// owns the codes above.
func RegisterAdminProblemCode(code string, status int, title string) {
	adminProblemCodesMu.Lock()
	defer adminProblemCodesMu.Unlock()
	entry := adminProblemCodes[code]
	entry.status = status
	entry.title = title
	adminProblemCodes[code] = entry
}

// AdminProblemFor looks a code up in the catalogue. Unknown codes fall back to
// the supplied status with a constant detail, so an unregistered code can never
// echo request text.
func AdminProblemFor(r *http.Request, status int, code string) AdminProblem {
	adminProblemCodesMu.RLock()
	entry, ok := adminProblemCodes[code]
	adminProblemCodesMu.RUnlock()
	if !ok {
		entry = adminProblemCode{status: status, title: http.StatusText(status)}
	}
	if entry.status == 0 {
		entry.status = status
	}
	problem := AdminProblem{
		Type:      "urn:metrum-router:problem:" + code,
		Title:     entry.title,
		Status:    entry.status,
		Code:      code,
		Detail:    entry.detail,
		RequestID: AdminRequestIDFrom(r),
	}
	if r != nil && r.URL != nil {
		problem.Instance = r.URL.Path
	}
	return problem
}

type adminRequestIDContextKey struct{}

// WithAdminRequestID stores the request ID the admin listener assigned.
func WithAdminRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, adminRequestIDContextKey{}, id)
}

// AdminRequestID returns the admin-plane request ID for r, generating one when
// the listener has not assigned one yet.
func AdminRequestID(r *http.Request) string {
	if r == nil {
		return ""
	}
	if id, ok := r.Context().Value(adminRequestIDContextKey{}).(string); ok && id != "" {
		return id
	}
	return newAdminRequestID()
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

// WriteAdminProblem renders the problem document with the admin-listener
// headers. The request ID is server-generated; a client-supplied X-Request-Id
// is never echoed.
func WriteAdminProblem(w http.ResponseWriter, r *http.Request, problem AdminProblem) {
	if w == nil {
		return
	}
	if problem.RequestID == "" {
		problem.RequestID = AdminRequestID(r)
	}
	if problem.RequestID == "" {
		problem.RequestID = newAdminRequestID()
	}
	header := w.Header()
	header.Set("Content-Type", "application/problem+json")
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set(adminRequestIDHeader, problem.RequestID)
	status := problem.Status
	if status == 0 {
		status = http.StatusInternalServerError
	}
	body, err := json.Marshal(problem)
	if err != nil {
		body = []byte(`{"type":"urn:metrum-router:problem:admin-internal-error","title":"Internal error","status":500,"code":"admin-internal-error"}`)
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func newAdminRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "req_unavailable"
	}
	return "req_" + hex.EncodeToString(raw[:])
}

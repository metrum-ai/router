// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Cursor pagination, typed filters and sorts for the /admin/v1 contract
// (ACP-013/014).
//
// The cursor codec is the same HMAC-SHA256 construction the admin reports
// already use (encodeAdminReportCursor): base64url(payload) + "." +
// base64url(mac). The payload additionally carries a fingerprint of the
// normalised filter+sort set, so a cursor cannot be replayed against a
// different query and silently return a wrong page.
//
// Paginated responses carry their navigation in headers (Link rel="next",
// X-Total-Count when the handler computes it), never in the body: /admin/v1
// must return byte-identical resource bodies to the legacy route for the same
// effective query.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// AdminV1DefaultLimit and AdminV1MaxLimit are the contract defaults; the
	// configured values live in admin.api.pagination and are passed in.
	AdminV1DefaultLimit = 50
	AdminV1MaxLimit     = 500
)

// AdminPaginationConfig is the admin.api.pagination block.
type AdminPaginationConfig struct {
	DefaultLimit int `yaml:"default_limit" json:"defaultLimit"`
	MaxLimit     int `yaml:"max_limit" json:"maxLimit"`
}

func (c AdminPaginationConfig) normalized() AdminPaginationConfig {
	out := c
	if out.DefaultLimit <= 0 {
		out.DefaultLimit = AdminV1DefaultLimit
	}
	if out.MaxLimit <= 0 {
		out.MaxLimit = AdminV1MaxLimit
	}
	if out.DefaultLimit > out.MaxLimit {
		out.DefaultLimit = out.MaxLimit
	}
	return out
}

// adminCursorPayload is one decoded cursor. Endpoint carries the operation ID,
// Fingerprint the hash of the normalised filter+sort set, and Values the sort
// key tuple of the last row on the previous page.
type adminCursorPayload struct {
	Version     int      `json:"v"`
	Endpoint    string   `json:"e"`
	Sort        string   `json:"s"`
	Direction   string   `json:"d"`
	Fingerprint string   `json:"f,omitempty"`
	Values      []string `json:"vls"`
}

// AdminCursorCodec signs and verifies opaque pagination cursors.
type AdminCursorCodec struct{ key []byte }

// NewAdminCursorCodec returns a codec bound to key. A nil key yields a usable
// codec with an unguessable per-process key, matching today's report cursors
// (which are process-scoped because the key is random per process).
func NewAdminCursorCodec(key []byte) *AdminCursorCodec {
	if len(key) == 0 {
		var fresh [32]byte
		if _, err := rand.Read(fresh[:]); err != nil {
			copy(fresh[:], []byte(strconv.FormatInt(time.Now().UnixNano(), 10)))
		}
		return &AdminCursorCodec{key: fresh[:]}
	}
	cp := make([]byte, len(key))
	copy(cp, key)
	return &AdminCursorCodec{key: cp}
}

// Encode signs a cursor payload.
func (c *AdminCursorCodec) Encode(payload adminCursorPayload) (string, error) {
	if c == nil || len(c.key) == 0 {
		return "", fmt.Errorf("cursor codec is not initialised")
	}
	payload.Version = 1
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// Decode verifies and decodes a cursor.
func (c *AdminCursorCodec) Decode(raw string) (adminCursorPayload, error) {
	if c == nil || len(c.key) == 0 {
		return adminCursorPayload{}, fmt.Errorf("cursor codec is not initialised")
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return adminCursorPayload{}, fmt.Errorf("malformed cursor")
	}
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write([]byte(parts[0]))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(got, want) {
		return adminCursorPayload{}, fmt.Errorf("cursor signature mismatch")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return adminCursorPayload{}, fmt.Errorf("malformed cursor")
	}
	var payload adminCursorPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return adminCursorPayload{}, fmt.Errorf("malformed cursor")
	}
	if payload.Version != 1 || payload.Endpoint == "" || payload.Sort == "" || payload.Direction == "" {
		return adminCursorPayload{}, fmt.Errorf("malformed cursor")
	}
	return payload, nil
}

// adminPageRequest is the parsed pagination request for one operation.
type adminPageRequest struct {
	Limit     int
	Cursor    adminCursorPayload
	HasCursor bool
	Sort      string
	Direction string
	Filters   map[string]string
}

// adminQueryParameter describes one accepted query parameter of an operation.
type adminQueryParameter struct {
	Name        string
	Description string
	// Enum restricts the accepted values; empty means any short scalar.
	Enum []string
	// Required parameters must be present.
	Required bool
}

// adminParsePage validates the query string against the operation's declared
// parameter set and parses limit/cursor/sort/direction. Every rejection is a
// 400 problem document; nothing reaches the handler unvalidated.
func adminParsePage(r *http.Request, op Operation, cfg AdminPaginationConfig, codec *AdminCursorCodec) (adminPageRequest, *AdminProblem) {
	cfg = cfg.normalized()
	page := adminPageRequest{Limit: cfg.DefaultLimit, Direction: "asc", Filters: map[string]string{}}
	query := r.URL.Query()

	allowed := map[string]adminQueryParameter{}
	for _, param := range op.Query {
		allowed[param.Name] = param
	}
	if op.Pagination != nil {
		for _, name := range []string{"limit", "cursor", "sort", "direction"} {
			if _, ok := allowed[name]; !ok {
				allowed[name] = adminQueryParameter{Name: name}
			}
		}
	}
	for name := range query {
		if _, ok := allowed[name]; !ok {
			problem := AdminProblemFor(r, http.StatusBadRequest, "unknown-query-parameter")
			problem.Detail = fmt.Sprintf("query parameter %q is not supported by this operation", name)
			return page, &problem
		}
	}
	for name, param := range allowed {
		if param.Required && strings.TrimSpace(query.Get(name)) == "" {
			problem := AdminProblemFor(r, http.StatusBadRequest, "invalid-request")
			problem.Detail = fmt.Sprintf("query parameter %q is required", name)
			return page, &problem
		}
	}
	for name, param := range allowed {
		if len(param.Enum) == 0 {
			continue
		}
		if value := strings.TrimSpace(query.Get(name)); value != "" && !adminValueInEnum(value, param.Enum) {
			problem := AdminProblemFor(r, http.StatusBadRequest, "invalid-request")
			problem.Detail = fmt.Sprintf("query parameter %q has an unsupported value", name)
			return page, &problem
		}
	}
	for name := range allowed {
		if strings.TrimSpace(query.Get(name)) != "" {
			page.Filters[name] = strings.TrimSpace(query.Get(name))
		}
	}

	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			problem := AdminProblemFor(r, http.StatusBadRequest, "invalid-limit")
			problem.Detail = "limit must be a positive integer"
			return page, &problem
		}
		if parsed > cfg.MaxLimit {
			problem := AdminProblemFor(r, http.StatusBadRequest, "invalid-limit")
			problem.Detail = fmt.Sprintf("limit must not exceed %d", cfg.MaxLimit)
			return page, &problem
		}
		page.Limit = parsed
	}

	if op.Pagination != nil {
		if page.Sort = strings.TrimSpace(query.Get("sort")); page.Sort == "" {
			page.Sort = op.Pagination.DefaultSort
		}
		if !adminValueInEnum(page.Sort, op.Pagination.Sorts) {
			problem := AdminProblemFor(r, http.StatusBadRequest, "invalid-request")
			problem.Detail = "unsupported sort key"
			return page, &problem
		}
		if raw := strings.TrimSpace(query.Get("direction")); raw != "" {
			if !adminValueInEnum(strings.ToLower(raw), []string{"asc", "desc"}) {
				problem := AdminProblemFor(r, http.StatusBadRequest, "invalid-request")
				problem.Detail = "direction must be asc or desc"
				return page, &problem
			}
			page.Direction = strings.ToLower(raw)
		}
		raw := strings.TrimSpace(query.Get("cursor"))
		if raw == "" {
			return page, nil
		}
		if codec == nil {
			problem := AdminProblemFor(r, http.StatusBadRequest, "invalid-cursor")
			problem.Detail = "cursor is not supported for this operation"
			return page, &problem
		}
		payload, err := codec.Decode(raw)
		if err != nil || payload.Endpoint != op.ID || payload.Sort != page.Sort || payload.Direction != page.Direction ||
			payload.Fingerprint != adminFilterFingerprint(op.ID, page.Filters, page.Sort, page.Direction) {
			problem := AdminProblemFor(r, http.StatusBadRequest, "invalid-cursor")
			problem.Detail = "cursor does not match this operation, filter set or sort"
			return page, &problem
		}
		page.Cursor = payload
		page.HasCursor = true
	}
	return page, nil
}

func adminValueInEnum(value string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, candidate := range allowed {
		if candidate == value {
			return true
		}
	}
	return false
}

// adminFilterFingerprint binds a cursor to the query it was produced for.
func adminFilterFingerprint(operationID string, filters map[string]string, sortKey, direction string) string {
	names := make([]string, 0, len(filters))
	for name := range filters {
		if name == "limit" || name == "cursor" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n%s\n", operationID, sortKey, direction)
	for _, name := range names {
		fmt.Fprintf(h, "%s=%s\n", name, filters[name])
	}
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:12])
}

// adminNextLink builds the Link header value for a next page.
func adminNextLink(r *http.Request, page adminPageRequest, next adminCursorPayload, codec *AdminCursorCodec) string {
	if codec == nil {
		return ""
	}
	encoded, err := codec.Encode(next)
	if err != nil {
		return ""
	}
	query := url.Values{}
	for name, value := range page.Filters {
		if name == "cursor" || name == "limit" {
			continue
		}
		query.Set(name, value)
	}
	query.Set("limit", strconv.Itoa(page.Limit))
	query.Set("cursor", encoded)
	path := "/"
	if r != nil && r.URL != nil {
		path = r.URL.Path
	}
	return "<" + path + "?" + query.Encode() + `>; rel="next"`
}

// adminSetPaginationHeaders publishes page navigation without touching the body.
func adminSetPaginationHeaders(w http.ResponseWriter, r *http.Request, page adminPageRequest, next *adminCursorPayload, codec *AdminCursorCodec, totalCount *int64) {
	if next != nil {
		if link := adminNextLink(r, page, *next, codec); link != "" {
			w.Header().Set("Link", link)
		}
	}
	if totalCount != nil {
		w.Header().Set("X-Total-Count", strconv.FormatInt(*totalCount, 10))
	}
	w.Header().Set("X-Page-Limit", strconv.Itoa(page.Limit))
}

// adminCursorInt64 and adminCursorString decode cursor tuple members with an
// explicit error instead of silently paginating from zero.
func adminCursorInt64(payload adminCursorPayload, index int) (int64, error) {
	if index >= len(payload.Values) {
		return 0, fmt.Errorf("cursor value missing")
	}
	return strconv.ParseInt(payload.Values[index], 10, 64)
}

// adminRetryAfterSeconds rounds a cooldown up to whole seconds, minimum 1.
func adminRetryAfterSeconds(after time.Duration) int {
	if after <= 0 {
		return 1
	}
	seconds := int(math.Ceil(after.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	return seconds
}

// AdminPage is the parsed pagination of the current request, available to a
// collection handler so it can apply the limit, the keyset filter and the next-page
// link without knowing the cursor codec.
type AdminPage struct {
	Limit     int
	Sort      string
	Direction string
	HasCursor bool
	Cursor    adminCursorPayload

	writer  http.ResponseWriter
	request *http.Request
	codec   *AdminCursorCodec
	op      Operation
}

// AdminPageFrom returns the page of the current request, if it paginates.
func AdminPageFrom(ctx context.Context) (*AdminPage, bool) {
	page, ok := ctx.Value(adminPageContextKey{}).(*AdminPage)
	return page, ok && page != nil
}

type adminPageContextKey struct{}

// WithAdminPage records the parsed page for the handler.
func WithAdminPage(ctx context.Context, page *AdminPage) context.Context {
	return context.WithValue(ctx, adminPageContextKey{}, page)
}

// LimitOr returns the requested limit, or fallback when the request does not
// paginate.
func (p *AdminPage) LimitOr(fallback int) int {
	if p == nil || p.Limit <= 0 {
		return fallback
	}
	return p.Limit
}

// Descending reports whether the page walks the keys in descending order.
func (p *AdminPage) Descending() bool { return p != nil && p.Direction == "desc" }

// CursorValues returns the keyset tuple of the cursor, or nil for the first page.
func (p *AdminPage) CursorValues() []string {
	if p == nil || !p.HasCursor {
		return nil
	}
	return p.Cursor.Values
}

// PublishNext emits the Link rel="next" header for the last row on this page.
func (p *AdminPage) PublishNext(values ...string) {
	if p == nil || p.writer == nil || p.codec == nil {
		return
	}
	page := adminPageRequest{Limit: p.Limit, Sort: p.Sort, Direction: p.Direction, Filters: map[string]string{}}
	next := adminCursorPayload{
		Endpoint:    p.op.ID,
		Sort:        p.Sort,
		Direction:   p.Direction,
		Fingerprint: adminFilterFingerprint(p.op.ID, nil, p.Sort, p.Direction),
		Values:      values,
	}
	adminSetPaginationHeaders(p.writer, p.request, page, &next, p.codec, nil)
}

// adminPageContext builds the handler-visible page for an operation.
func adminPageContext(w http.ResponseWriter, r *http.Request, op Operation, page adminPageRequest, codec *AdminCursorCodec) *AdminPage {
	if op.Pagination == nil {
		return nil
	}
	return &AdminPage{
		Limit: page.Limit, Sort: page.Sort, Direction: page.Direction,
		HasCursor: page.HasCursor, Cursor: page.Cursor,
		writer: w, request: r, codec: codec, op: op,
	}
}

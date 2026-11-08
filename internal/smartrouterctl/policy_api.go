// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package smartrouterctl

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// PolicyAPIClient reuses the shared admin transport without legacy endpoint or
// redirect fallback. Other machine transports belong to the shared client.
type PolicyAPIClient struct{ admin *AdminAPIClient }
type PolicyCallOptions struct {
	IfMatch        string
	IdempotencyKey string
	Against        string
	Cursor         string
	PageSize       int
}

func NewPolicyAPIClient(endpoint, keyFile string) (*PolicyAPIClient, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		endpoint = strings.TrimSpace(os.Getenv("ROUTER_URL"))
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, fmt.Errorf("invalid policy API endpoint")
	}
	if parsed.Scheme != "https" {
		ip := net.ParseIP(parsed.Hostname())
		if parsed.Scheme != "http" || (parsed.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return nil, fmt.Errorf("policy API endpoint requires HTTPS except on loopback")
		}
	}
	token, err := resolvePolicyCredential(keyFile)
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(token, " \t\r\n") {
		return nil, fmt.Errorf("invalid admin credential")
	}
	// Default Go transport may replay Idempotency-Key requests after a lost
	// response on reused connections. Policy calls deliberately use fresh
	// HTTP/1 connections, eliminating that retry branch and HTTP/2 stream
	// replay. Keep the server-visible idempotency key for explicit retries.
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("policy client requires standard HTTP transport")
	}
	transport := base.Clone()
	transport.DisableKeepAlives = true
	transport.ForceAttemptHTTP2 = false
	transport.Protocols = new(http.Protocols)
	transport.Protocols.SetHTTP1(true)
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	// Clone initializes the source's HTTP2 defaults before copying TLS. Limit
	// ALPN explicitly as well: Protocols alone does not clear inherited h2.
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	client := &AdminAPIClient{BaseURL: strings.TrimRight(endpoint, "/"), Token: token, HTTP: &http.Client{Transport: transport, Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	return &PolicyAPIClient{admin: client}, nil
}
func resolvePolicyCredential(file string) (string, error) {
	file = strings.TrimSpace(file)
	if file == "" && strings.TrimSpace(os.Getenv("ROUTER_ADMIN_KEY")) != "" {
		return ResolveAdminCredential("")
	}
	if file == "" {
		file = strings.TrimSpace(os.Getenv("ROUTER_ADMIN_KEY_FILE"))
	}
	if file == "" {
		return ResolveAdminCredential("")
	}
	before, err := os.Lstat(file)
	if err != nil {
		return "", fmt.Errorf("admin key file unavailable")
	}
	if !before.Mode().IsRegular() || before.Mode().Perm() != 0600 {
		return "", fmt.Errorf("admin key file must be a regular mode-0600 file")
	}
	f, err := os.Open(file)
	if err != nil {
		return "", fmt.Errorf("admin key file unavailable")
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Mode().Perm() != 0600 {
		return "", fmt.Errorf("admin key file changed or has unsafe permissions")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil || len(raw) > 16384 {
		return "", fmt.Errorf("admin key file unreadable or oversized")
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("admin key file is empty")
	}
	return token, nil
}

// Call dispatches only the approved policy-set operations. Callers supply
// explicit revision preconditions; mutation replay keys use shared generation.
type PolicyAPIResponse struct {
	Body json.RawMessage `json:"data"`
	ETag string          `json:"etag,omitempty"`
	Link string          `json:"link,omitempty"`
}
type PolicyAPIError struct {
	Status int
	Code   string
}

func (e *PolicyAPIError) Error() string {
	return fmt.Sprintf("policy API HTTP %d: %s", e.Status, e.Code)
}

func (c *PolicyAPIClient) Call(operation, id string, body json.RawMessage, opts PolicyCallOptions) ([]byte, error) {
	result, err := c.CallWithMetadata(operation, id, body, opts)
	if err != nil {
		return nil, err
	}
	return result.Body, nil
}
func (c *PolicyAPIClient) CallWithMetadata(operation, id string, body json.RawMessage, opts PolicyCallOptions) (*PolicyAPIResponse, error) {
	if c == nil || c.admin == nil {
		return nil, fmt.Errorf("policy API client unavailable")
	}
	method, path := "GET", "/admin/v1/policy-sets"
	if operation != "list" && operation != "draft" {
		if strings.TrimSpace(id) == "" || id == "." || id == ".." {
			return nil, fmt.Errorf("policy set ID required")
		}
		path += "/" + url.PathEscape(id)
	}
	mutation := false
	precondition := false
	q := url.Values{}
	switch operation {
	case "list":
		if opts.PageSize != 0 {
			if opts.PageSize < 1 {
				return nil, fmt.Errorf("invalid page size")
			}
			q.Set("limit", fmt.Sprint(opts.PageSize))
		}
		if opts.Cursor != "" {
			q.Set("cursor", opts.Cursor)
		}
	case "get":
	case "draft":
		method = "POST"
		mutation = true
		precondition = true
	case "validate":
		method = "POST"
		path += "/validate"
	case "diff":
		path += "/diff"
		if strings.TrimSpace(opts.Against) == "" {
			return nil, fmt.Errorf("comparison policy set required")
		}
		q.Set("against", opts.Against)
	case "activate", "rollback":
		method = "POST"
		path += "/" + operation
		mutation = true
		precondition = true
	case "replace":
		method = "PUT"
		mutation = true
		precondition = true
	case "delete":
		method = "DELETE"
		mutation = true
		precondition = true
	default:
		return nil, fmt.Errorf("unsupported policy operation")
	}
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	headers := map[string]string{}
	if precondition {
		if strings.TrimSpace(opts.IfMatch) == "" {
			return nil, fmt.Errorf("explicit If-Match revision required")
		}
		headers["If-Match"] = opts.IfMatch
	}
	if mutation {
		key := opts.IdempotencyKey
		if key == "" {
			key = NewIdempotencyKey()
		}
		if key == "" {
			return nil, fmt.Errorf("could not generate idempotency key")
		}
		headers["Idempotency-Key"] = key
	}
	var value any
	if len(body) > 0 {
		if !json.Valid(body) {
			return nil, fmt.Errorf("invalid policy JSON body")
		}
		value = body
	}
	raw, status, responseHeaders, err := c.admin.DoWithHeaders(method, path, value, headers)
	if err != nil || status < 200 || status >= 300 {
		code := "request-failed"
		if apiErr, ok := err.(*AdminAPIError); ok {
			var problem struct {
				Code string `json:"code"`
				Type string `json:"type"`
			}
			if json.Unmarshal([]byte(apiErr.Raw), &problem) == nil {
				candidate := problem.Code
				if candidate == "" {
					candidate = problem.Type
					if i := strings.LastIndexAny(candidate, "/:"); i >= 0 {
						candidate = candidate[i+1:]
					}
				}
				switch candidate {
				case "policy-stale", "authorization-unavailable", "admin-forbidden", "privilege-escalation", "last-policy-admin", "invalid-policy", "policy-set-immutable", "policy-set-not-found", "policy-record-not-found", "precondition-required", "precondition-failed", "invalid-cursor", "invalid-limit", "invalid-body", "rate-limited", "idempotency-conflict":
					code = candidate
				}
			}
		}
		return nil, &PolicyAPIError{Status: status, Code: code}
	}
	return &PolicyAPIResponse{Body: raw, ETag: responseHeaders.Get("ETag"), Link: responseHeaders.Get("Link")}, nil
}

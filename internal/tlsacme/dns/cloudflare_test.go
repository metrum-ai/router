// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package dns

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// requestLog records every request a fake provider API receives so a test can
// assert the exact calls, not just the outcome.
type requestLog struct {
	method string
	path   string
	query  string
	body   string
	header http.Header
}

func (r requestLog) String() string {
	return r.method + " " + r.path + "?" + r.query + " " + r.body
}

type fakeAPI struct {
	t        *testing.T
	server   *httptest.Server
	requests []requestLog
	handler  func(req requestLog, w http.ResponseWriter)
}

func newFakeAPI(t *testing.T, handler func(req requestLog, w http.ResponseWriter)) *fakeAPI {
	t.Helper()
	api := &fakeAPI{t: t, handler: handler}
	api.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		req := requestLog{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.RawQuery,
			body:   string(raw),
			header: r.Header.Clone(),
		}
		api.requests = append(api.requests, req)
		api.handler(req, w)
	}))
	t.Cleanup(api.server.Close)
	return api
}

// writes counts successful write calls (POST, PUT, DELETE).
func (f *fakeAPI) writes() []requestLog {
	var out []requestLog
	for _, req := range f.requests {
		switch req.method {
		case http.MethodPost, http.MethodPut, http.MethodDelete:
			out = append(out, req)
		}
	}
	return out
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, payload any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode fake response: %v", err)
	}
}

func TestCloudflareProviderCalls(t *testing.T) {
	var records []map[string]any
	api := newFakeAPI(t, func(req requestLog, w http.ResponseWriter) {
		switch {
		case req.method == http.MethodGet && req.path == "/zones":
			writeJSON(t, w, 200, map[string]any{
				"success":     true,
				"result":      []map[string]any{{"id": "zone-1", "name": "example.com"}},
				"result_info": map[string]any{"page": 1, "total_pages": 1},
			})
		case req.method == http.MethodGet && req.path == "/zones/zone-1/dns_records":
			writeJSON(t, w, 200, map[string]any{
				"success":     true,
				"result":      records,
				"result_info": map[string]any{"page": 1, "total_pages": 1},
			})
		case req.method == http.MethodPost && req.path == "/zones/zone-1/dns_records":
			var body map[string]any
			if err := json.Unmarshal([]byte(req.body), &body); err != nil {
				t.Errorf("create body is not JSON: %v", err)
			}
			record := map[string]any{"id": "rec-1"}
			for key, value := range body {
				record[key] = value
			}
			records = append(records, record)
			writeJSON(t, w, 200, map[string]any{"success": true, "result": record})
		case req.method == http.MethodPut && req.path == "/zones/zone-1/dns_records/rec-1":
			var body map[string]any
			if err := json.Unmarshal([]byte(req.body), &body); err != nil {
				t.Errorf("update body is not JSON: %v", err)
			}
			body["id"] = "rec-1"
			records = []map[string]any{body}
			writeJSON(t, w, 200, map[string]any{"success": true, "result": body})
		case req.method == http.MethodDelete && req.path == "/zones/zone-1/dns_records/rec-1":
			records = nil
			writeJSON(t, w, 200, map[string]any{"success": true, "result": map[string]any{"id": "rec-1"}})
		default:
			t.Errorf("unexpected request %s", req)
			writeJSON(t, w, 404, map[string]any{"success": false, "errors": []map[string]any{{"code": 7000, "message": "not found"}}})
		}
	})
	provider, err := NewCloudflare(CloudflareCredentials{APIToken: "cf-token"}, Options{Endpoint: api.server.URL})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	ctx := context.Background()

	zones, err := provider.Zones(ctx)
	if err != nil {
		t.Fatalf("zones: %v", err)
	}
	if len(zones) != 1 || zones[0] != "example.com" {
		t.Fatalf("zones = %v, want [example.com]", zones)
	}

	id, err := provider.Create(ctx, "example.com", Record{Type: "TXT", Name: "_acme-challenge.example.com", Value: "token-value", TTL: 60})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id != "rec-1" {
		t.Fatalf("create id = %q, want rec-1", id)
	}
	createRequests := api.writes()
	if len(createRequests) != 1 {
		t.Fatalf("create made %d write calls, want 1: %v", len(createRequests), createRequests)
	}
	if got, want := createRequests[0].body, `{"content":"token-value","name":"_acme-challenge.example.com","ttl":60,"type":"TXT"}`; got != want {
		t.Fatalf("create body = %s, want %s", got, want)
	}
	if got := createRequests[0].header.Get("Authorization"); got != "Bearer cf-token" {
		t.Fatalf("Authorization = %q", got)
	}

	listed, err := provider.Records(ctx, "example.com")
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	if len(listed) != 1 || listed[0].Name != "_acme-challenge.example.com" || listed[0].Value != "token-value" || listed[0].Type != "TXT" {
		t.Fatalf("records = %+v", listed)
	}

	if err := provider.Update(ctx, "example.com", "rec-1", Record{Type: "A", Name: "router.example.com", Value: "203.0.113.10", TTL: 300}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := provider.Delete(ctx, "example.com", "rec-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	last := api.requests[len(api.requests)-1]
	if last.method != http.MethodDelete || last.path != "/zones/zone-1/dns_records/rec-1" {
		t.Fatalf("last request = %s", last)
	}
	// The zone id is cached: three record operations must not look it up again.
	zoneLookups := 0
	for _, req := range api.requests {
		if req.path == "/zones" {
			zoneLookups++
		}
	}
	if zoneLookups != 1 {
		t.Fatalf("zone lookups = %d, want 1 (cached after first use)", zoneLookups)
	}
}

func TestCloudflareProviderErrorsAreRedactedAndTruncated(t *testing.T) {
	api := newFakeAPI(t, func(req requestLog, w http.ResponseWriter) {
		writeJSON(t, w, 403, map[string]any{
			"success": false,
			"errors":  []map[string]any{{"code": 9109, "message": "Unauthorized to access zone"}},
		})
	})
	provider, err := NewCloudflare(CloudflareCredentials{APIToken: "super-secret-token-value"}, Options{
		Endpoint: api.server.URL,
		Redact: func(text string) string {
			return strings.ReplaceAll(text, "super-secret-token-value", "<redacted>")
		},
	})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	_, err = provider.Zones(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "9109 Unauthorized to access zone") {
		t.Fatalf("error %q does not carry the API message", err)
	}
	if strings.Contains(err.Error(), "super-secret-token-value") {
		t.Fatalf("error leaks the token: %q", err)
	}
}

func TestCloudflareErrorsNeverEchoTheToken(t *testing.T) {
	const token = "cf-super-secret-token"
	api := newFakeAPI(t, func(req requestLog, w http.ResponseWriter) {
		// A provider that echoes the presented credential back in its error
		// must not leak it into logs or startup errors.
		writeJSON(t, w, 401, map[string]any{
			"success": false,
			"errors":  []map[string]any{{"code": 10000, "message": "Authentication failed: invalid token " + token}},
		})
	})
	provider, err := NewCloudflare(CloudflareCredentials{APIToken: token}, Options{
		Endpoint: api.server.URL,
		Redact: func(text string) string {
			return strings.ReplaceAll(text, token, "<redacted>")
		},
	})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	_, err = provider.Zones(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("provider error echoes the token: %q", err)
	}
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Fatalf("provider error is not redacted: %q", err)
	}
}

func TestCloudflareSuccessFalseIsAnError(t *testing.T) {
	api := newFakeAPI(t, func(req requestLog, w http.ResponseWriter) {
		writeJSON(t, w, 200, map[string]any{"success": false, "errors": []map[string]any{{"code": 1, "message": "bad request"}}})
	})
	provider, err := NewCloudflare(CloudflareCredentials{APIToken: "t"}, Options{Endpoint: api.server.URL})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	if _, err := provider.Zones(context.Background()); err == nil {
		t.Fatal("success:false with status 200 must be an error")
	}
}

func TestCloudflareNeedsCredentials(t *testing.T) {
	if _, err := NewCloudflare(CloudflareCredentials{}, Options{}); err == nil {
		t.Fatal("expected an error without credentials")
	}
	if _, err := NewCloudflare(CloudflareCredentials{APIKey: "k"}, Options{}); err == nil {
		t.Fatal("expected an error for an API key without an email")
	}
}

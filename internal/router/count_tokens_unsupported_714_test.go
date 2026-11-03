// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Issue #714: a target whose upstream serves Messages but not the count_tokens
// endpoint must still answer a count request. The router returns its local
// estimate, marked as such, while real provider failures keep their classes.

func countTokensTestService(t *testing.T, upstreamURL string, mutate func(*Config)) *Service {
	t.Helper()
	cfg := testConfig(t, upstreamURL, "provider-key", t.TempDir())
	provider := cfg.Provider["mock"]
	provider.Dialect = "anthropic"
	cfg.Provider["mock"] = provider
	if mutate != nil {
		mutate(cfg)
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func postCountTokens(svc *Service) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/v1/messages/count_tokens", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("X-API-Key", testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	return rr
}

func TestCountTokensDeclaredUnsupportedTargetEstimates(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("declared-unsupported target must not be called: %s", r.URL.Path)
	}))
	defer up.Close()
	svc := countTokensTestService(t, up.URL, func(cfg *Config) {
		no := false
		group := cfg.Models["default"]
		group.Targets[0].CountTokens = &no
		cfg.Models["default"] = group
	})
	rr := postCountTokens(svc)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200 estimate", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-Metrum-Usage-Source"); got != "estimated" {
		t.Fatalf("usage source=%q, want estimated", got)
	}
	if !strings.Contains(rr.Body.String(), `"input_tokens"`) {
		t.Fatalf("body=%s, want input_tokens", rr.Body.String())
	}
}

// TestCountTokensUpstreamNotFoundEstimates pins the gateway case: a plain
// missing-route 404 body is not an Anthropic typed error, so the router answers
// with its local estimate instead of an opaque upstream failure.
func TestCountTokensUpstreamNotFoundEstimates(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"gateway-json", `{"error":{"message":"Not Found","code":404}}`},
		{"empty-body", ``},
		{"plain-text", `404 page not found`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer up.Close()
			svc := countTokensTestService(t, up.URL, nil)
			rr := postCountTokens(svc)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s, want a local estimate", rr.Code, rr.Body.String())
			}
			if got := rr.Header().Get("X-Metrum-Usage-Source"); got != "estimated" {
				t.Fatalf("usage source=%q, want estimated", got)
			}
			if strings.Contains(rr.Body.String(), "Not Found") {
				t.Fatalf("body=%s, must not echo the provider body", rr.Body.String())
			}
		})
	}
}

// TestCountTokensTypedProviderNotFoundSurfaces pins the other side of the same
// 404: the provider's own typed not_found_error means an unknown or
// misconfigured model, which must keep its existing class and never become a
// silent estimate.
func TestCountTokensTypedProviderNotFoundSurfaces(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"unknown-model", `{"type":"error","error":{"type":"not_found_error","message":"model: claude-does-not-exist"}}`},
		{"typed-uppercase", `{"type":"error","error":{"type":"NOT_FOUND_ERROR","message":"model: claude-does-not-exist"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer up.Close()
			svc := countTokensTestService(t, up.URL, nil)
			rr := postCountTokens(svc)
			if rr.Code != http.StatusNotFound {
				t.Fatalf("status=%d body=%s, want the provider 404 class preserved", rr.Code, rr.Body.String())
			}
			if got := rr.Header().Get("X-Metrum-Usage-Source"); got == "estimated" {
				t.Fatal("a typed provider 404 must not be reported as an estimate")
			}
			if strings.Contains(rr.Body.String(), "claude-does-not-exist") {
				t.Fatalf("body=%s, must not echo the provider body", rr.Body.String())
			}
		})
	}
}

func TestAnthropicTypedNotFoundErrorDetection(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"anthropic-typed", `{"type":"error","error":{"type":"not_found_error","message":"model: x"}}`, true},
		{"anthropic-typed-with-details", `{"type":"error","error":{"type":"not_found_error","message":"model: x","details":null},"request_id":"req_1"}`, true},
		{"gateway-untyped", `{"error":{"message":"Not Found","code":404}}`, false},
		{"error-only-type", `{"error":{"type":"not_found_error"}}`, false},
		{"other-typed-error", `{"type":"error","error":{"type":"invalid_request_error","message":"x"}}`, false},
		{"not-json", `404 page not found`, false},
		{"empty", ``, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := anthropicTypedNotFoundError([]byte(tc.body)); got != tc.want {
				t.Fatalf("body=%q: got %t want %t", tc.body, got, tc.want)
			}
		})
	}
}

func TestCountTokensRealProviderFailuresKeepTheirClass(t *testing.T) {
	for _, tc := range []struct{ upstream, want int }{{400, 400}, {401, 401}, {429, 429}, {500, 502}} {
		t.Run(http.StatusText(tc.upstream), func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.upstream)
				_, _ = w.Write([]byte(`{"error":{"message":"provider detail"}}`))
			}))
			defer up.Close()
			svc := countTokensTestService(t, up.URL, nil)
			rr := postCountTokens(svc)
			if rr.Code != tc.want {
				t.Fatalf("status=%d body=%s, want %d", rr.Code, rr.Body.String(), tc.want)
			}
			if got := rr.Header().Get("X-Metrum-Usage-Source"); got == "estimated" {
				t.Fatal("a provider failure must not be reported as an estimate")
			}
		})
	}
}

func TestCountTokensNativeTargetReportsProviderCount(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/messages/count_tokens") {
			t.Errorf("path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":1234}`))
	}))
	defer up.Close()
	svc := countTokensTestService(t, up.URL, nil)
	rr := postCountTokens(svc)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "1234") {
		t.Fatalf("status=%d body=%s, want the provider count", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-Metrum-Usage-Source"); got != "reported" {
		t.Fatalf("usage source=%q, want reported", got)
	}
}

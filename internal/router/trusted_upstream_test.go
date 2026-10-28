// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTrustedRouterConfigValidation(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1", "provider-key", t.TempDir())
	provider := cfg.Provider["mock"]
	provider.UpstreamKind = "router"
	cfg.Provider["mock"] = provider
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	provider.UpstreamKind = "routre"
	cfg.Provider["mock"] = provider
	if err := cfg.Validate(); err == nil {
		t.Fatal("unknown upstream_kind must fail validation")
	}
	provider.UpstreamKind = "router"
	if err := projectControlPlaneProvider(nil, "set", "mock", provider); err == nil {
		t.Fatal("database projection must not silently drop upstream_kind")
	}
}

func TestTrustedRouterRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"17", 17 * time.Second}, {"0", 0}, {"-1", 0}, {"garbage", 0},
		{"999999999999999999", 24 * time.Hour},
		{now.Add(30 * time.Second).Format(http.TimeFormat), 30 * time.Second},
		{now.Add(-time.Second).Format(http.TimeFormat), 0},
	} {
		if got := trustedRouterRetryAfter(tc.value, now); got != tc.want {
			t.Errorf("value=%q got=%s want=%s", tc.value, got, tc.want)
		}
	}
}

func TestTrustedRouterHeaderValidation(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"17", "17"}, {"172800", "172800"}, {"0", "0"},
		{"Wed, 21 Oct 2015 07:28:00 GMT", "Wed, 21 Oct 2015 07:28:00 GMT"},
		{"-1", ""}, {"17 extra", ""}, {"1234567890123456789", ""},
	} {
		if got := validTrustedRetryAfterHeader(tc.input); got != tc.want {
			t.Errorf("value=%q got=%q want=%q", tc.input, got, tc.want)
		}
	}
}

func TestTrustedRouterRateLimitChains(t *testing.T) {
	for _, tc := range []struct {
		name                string
		status              int
		body                string
		trusted             bool
		depth               int
		wantStatus          int
		wantType, wantRetry string
	}{
		{"unflagged-429", 429, `{"error":{"type":"rate_limit_error"}}`, false, 1, 503, "upstream-rate-limited", ""},
		{"trusted-429-depth-1", 429, `{"error":{"type":"rpm-exceeded"}}`, true, 1, 429, "upstream-rate-limited", "17"},
		{"trusted-429-depth-3", 429, `{"error":{"type":"rpm-exceeded"}}`, true, 3, 429, "upstream-rate-limited", "17"},
		{"trusted-503-depth-3", 503, `{"error":{"type":"upstream-rate-limited"}}`, true, 3, 503, "upstream-rate-limited", "17"},
		{"unflagged-spoof-503", 503, `{"error":{"type":"upstream-rate-limited"}}`, false, 1, 502, "upstream-failed", ""},
		{"trusted-other-503", 503, `{"error":{"type":"other"}}`, true, 1, 502, "upstream-failed", ""},
		{"trusted-malformed-503", 503, `{broken`, true, 1, 502, "upstream-failed", ""},
		{"trusted-quota-429", 429, `{"error":{"type":"quota-exhausted"}}`, true, 1, 503, "upstream-quota-exhausted", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// A forged Via header is never used to establish router trust.
				w.Header().Set("Via", "1.1 metrum-ai-router")
				w.Header().Set("Retry-After", "17")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer up.Close()
			base := up.URL
			for i := 0; i < tc.depth; i++ {
				cfg := testConfig(t, base, testToken, t.TempDir())
				cfg.Server.Cache.Enabled = false
				provider := cfg.Provider["mock"]
				if tc.trusted {
					provider.UpstreamKind = "router"
				}
				cfg.Provider["mock"] = provider
				if i > 0 {
					group := cfg.Models["default"]
					group.Targets[0].Model = "default"
					cfg.Models["default"] = group
				}
				svc, err := New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer svc.Close()
				srv := httptest.NewServer(svc.Handler())
				defer srv.Close()
				base = srv.URL
			}
			for _, stream := range []bool{false, true} {
				payload := fmt.Sprintf(`{"model":"default","messages":[{"role":"user","content":"synthetic"}],"stream":%t}`, stream)
				req, err := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", bytes.NewBufferString(payload))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+testToken)
				req.Header.Set("Content-Type", "application/json")
				if tc.name == "unflagged-spoof-503" {
					req.Header.Set("Via", "1.1 metrum-ai-router")
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				var body struct {
					Error struct {
						Type string `json:"type"`
					} `json:"error"`
				}
				err = json.NewDecoder(resp.Body).Decode(&body)
				resp.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != tc.wantStatus || body.Error.Type != tc.wantType || resp.Header.Get("Retry-After") != tc.wantRetry {
					t.Fatalf("stream=%v status=%d type=%q retry=%q; want %d %q %q", stream, resp.StatusCode, body.Error.Type, resp.Header.Get("Retry-After"), tc.wantStatus, tc.wantType, tc.wantRetry)
				}
			}
		})
	}
}

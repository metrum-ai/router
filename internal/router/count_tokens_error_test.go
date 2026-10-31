// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCountTokensUpstreamErrorsAreAnthropicAndSanitized(t *testing.T) {
	for _, tc := range []struct{ upstream, want int }{{400, 400}, {404, 404}, {429, 429}, {500, 502}} {
		t.Run(http.StatusText(tc.upstream), func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.upstream)
				_, _ = w.Write([]byte(`{"error":{"message":"private provider detail"}}`))
			}))
			defer up.Close()
			cfg := testConfig(t, up.URL, "provider-key", t.TempDir())
			provider := cfg.Provider["mock"]
			provider.Dialect = "anthropic"
			cfg.Provider["mock"] = provider
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			req := httptest.NewRequest("POST", "/v1/messages/count_tokens", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set("X-API-Key", testToken)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != tc.want || !strings.Contains(rr.Body.String(), `"type":"error"`) || !strings.Contains(rr.Body.String(), `"request_id"`) || rr.Header().Get("Request-Id") == "" || strings.Contains(rr.Body.String(), "private provider detail") {
				t.Fatalf("%d %s", rr.Code, rr.Body.String())
			}
		})
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpstreamClientStatusIsPreservedAndSanitized(t *testing.T) {
	for _, tc := range []struct {
		upstream, caller int
		code             string
	}{
		{400, 400, "upstream-rejected"}, {404, 404, "upstream-rejected"},
		{413, 413, "upstream-rejected"}, {422, 422, "upstream-rejected"},
		{401, 503, "upstream-access-denied"}, {402, 503, "upstream-access-denied"},
		{403, 503, "upstream-access-denied"}, {408, 504, "upstream-timeout"},
		{429, 503, "upstream-rate-limited"},
	} {
		t.Run(fmt.Sprint(tc.upstream), func(t *testing.T) {
			status := tc.upstream
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"message":"private upstream details"}}`))
			}))
			defer up.Close()
			cfg := testConfig(t, up.URL, "provider-key", t.TempDir())
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != tc.caller || strings.Contains(rr.Body.String(), "private upstream details") || strings.Contains(rr.Body.String(), "provider-key") {
				t.Fatalf("want %d, got %d: %s", tc.caller, rr.Code, rr.Body.String())
			}
			var body map[string]map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["error"]["code"] != tc.code {
				t.Fatalf("want code %s: %s", tc.code, rr.Body.String())
			}
			for _, field := range []string{"message", "type", "param", "code"} {
				if _, ok := body["error"][field]; !ok {
					t.Fatalf("missing %s: %s", field, rr.Body.String())
				}
			}
		})
	}
}

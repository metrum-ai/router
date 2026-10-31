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
	for _, status := range []int{400, 401, 403, 404, 408, 413, 422, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
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
			if rr.Code != status || strings.Contains(rr.Body.String(), "private upstream details") || strings.Contains(rr.Body.String(), "provider-key") {
				t.Fatalf("want %d, got %d: %s", status, rr.Code, rr.Body.String())
			}
			var body map[string]map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"message", "type", "param", "code"} {
				if _, ok := body["error"][field]; !ok {
					t.Fatalf("missing %s: %s", field, rr.Body.String())
				}
			}
		})
	}
}

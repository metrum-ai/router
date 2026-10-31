// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponsesInputTokensHonorsRequestShaping(t *testing.T) {
	for _, scope := range []string{"caller", "provider"} {
		t.Run(scope, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"input_tokens":1}`) }))
			defer up.Close()
			cfg := testConfig(t, up.URL, "provider-key", t.TempDir())
			p := cfg.Provider["mock"]
			p.Dialect = "openai-responses"
			shape := TrafficShapeConfig{RequestStartPerSec: 0.01, RequestBurst: 1}
			if scope == "caller" {
				enabled := true
				shape.Enabled = &enabled
				cfg.Callers[0].TrafficShape = shape
			} else {
				p.TrafficShape = shape
			}
			cfg.Provider["mock"] = p
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			request := func() *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", "/v1/responses/input_tokens", strings.NewReader(`{"model":"default","input":"hi"}`))
				r.Header.Set("Authorization", "Bearer "+testToken)
				w := httptest.NewRecorder()
				svc.Handler().ServeHTTP(w, r)
				return w
			}
			if w := request(); w.Code != 200 {
				t.Fatalf("first: %d %s", w.Code, w.Body.String())
			}
			w := request()
			if w.Code != 429 && w.Code != 503 {
				t.Fatalf("request shaping bypassed: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

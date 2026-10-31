// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelGetOnlyReturnsAllowedModels(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Models["private"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "private-model"}}}
	cfg.Models["accounts/x/models/y"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "slash-model"}}}
	cfg.Models["accounts/x/models/private"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "private-slash-model"}}}
	cfg.Callers[0].Allow = []string{"default", "accounts/x/models/y"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	for _, tc := range []struct {
		model  string
		status int
	}{{"default", 200}, {"accounts/x/models/y", 200}, {"accounts/x/models/private", 404}, {"accounts/x/models/missing", 404}, {"private", 404}, {"missing", 404}} {
		req := httptest.NewRequest(http.MethodGet, "/v1/models/"+tc.model, nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != tc.status {
			t.Fatalf("%s: status=%d body=%s", tc.model, rr.Code, rr.Body.String())
		}
		if tc.status == 200 && !strings.Contains(rr.Body.String(), `"id":"`+tc.model+`"`) {
			t.Fatalf("model: %s", rr.Body.String())
		}
		if tc.status == 404 && !strings.Contains(rr.Body.String(), `"code":"model_not_found"`) {
			t.Fatalf("error: %s", rr.Body.String())
		}
	}
	escaped := httptest.NewRequest(http.MethodGet, "/v1/models/accounts%2Fx%2Fmodels%2Fy", nil)
	escaped.Header.Set("Authorization", "Bearer "+testToken)
	escapedResult := httptest.NewRecorder()
	svc.Handler().ServeHTTP(escapedResult, escaped)
	if escapedResult.Code != http.StatusOK || !strings.Contains(escapedResult.Body.String(), `"id":"accounts/x/models/y"`) {
		t.Fatalf("escaped slash: status=%d body=%s", escapedResult.Code, escapedResult.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/models/default", nil)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", rr.Code)
	}
}

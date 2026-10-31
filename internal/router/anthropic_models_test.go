// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicModelCatalogPaginationAndLookup(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Models["alpha"] = cfg.Models["default"]
	cfg.Models["zeta"] = cfg.Models["default"]
	cfg.Models["private"] = cfg.Models["default"]
	cfg.Callers[0].Allow = []string{"alpha", "default", "zeta"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	request := func(path, auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if auth == "header" {
			req.Header.Set("Anthropic-Version", "2023-06-01")
			req.Header.Set("Authorization", "Bearer "+testToken)
		} else {
			req.Header.Set("X-API-Key", testToken)
		}
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		return rr
	}
	for _, tc := range []struct {
		path, first string
		more        bool
	}{
		{"/v1/models?limit=1", "alpha", true},
		{"/v1/models?limit=1&after_id=alpha", "default", true},
		{"/v1/models?limit=1&before_id=zeta", "default", true},
	} {
		rr := request(tc.path, "key")
		var b struct {
			Data    []map[string]any
			HasMore bool   `json:"has_more"`
			FirstID string `json:"first_id"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &b); err != nil {
			t.Fatal(err)
		}
		if rr.Code != 200 || len(b.Data) != 1 || b.FirstID != tc.first || b.HasMore != tc.more || b.Data[0]["type"] != "model" || b.Data[0]["created_at"] == "" {
			t.Fatalf("%s: %d %s", tc.path, rr.Code, rr.Body.String())
		}
	}
	for _, model := range []string{"private", "missing"} {
		rr := request("/v1/models/"+model, "header")
		if rr.Code != 404 || !strings.Contains(rr.Body.String(), `"type":"not_found_error"`) || strings.Contains(rr.Body.String(), "private-model") {
			t.Fatalf("%s: %d %s", model, rr.Code, rr.Body.String())
		}
	}
	rr := request("/v1/models/default", "header")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"type":"model"`) {
		t.Fatalf("%d %s", rr.Code, rr.Body.String())
	}
	rr = request("/v1/models?limit=0", "key")
	if rr.Code != 400 {
		t.Fatalf("invalid limit: %d %s", rr.Code, rr.Body.String())
	}
}

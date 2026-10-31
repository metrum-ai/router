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

func TestAnthropicModelRequestDetection(t *testing.T) {
	for _, tc := range []struct {
		name, version, key, authorization string
		setVersion, want                  bool
	}{
		{"empty-version-present", "", "", "Bearer token", true, true},
		{"version-and-bearer", "2023-06-01", "", "Bearer token", true, true},
		{"key-only", "", "token", "", false, true},
		{"both-auth-headers", "", "token", "Bearer token", false, false},
		{"bearer-only", "", "", "Bearer token", false, false},
		{"no-auth", "", "", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			if tc.setVersion {
				req.Header.Set("Anthropic-Version", tc.version)
			}
			if tc.key != "" {
				req.Header.Set("X-API-Key", tc.key)
			}
			if tc.authorization != "" {
				req.Header.Set("Authorization", tc.authorization)
			}
			if got := isAnthropicModelRequest(req); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

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
		switch auth {
		case "header":
			req.Header.Set("Anthropic-Version", "2023-06-01")
			req.Header.Set("Authorization", "Bearer "+testToken)
		case "both":
			req.Header.Set("X-API-Key", testToken)
			req.Header.Set("Authorization", "Bearer "+testToken)
		case "bearer":
			req.Header.Set("Authorization", "Bearer "+testToken)
		default:
			req.Header.Set("X-API-Key", testToken)
		}
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		return rr
	}
	for _, tc := range []struct {
		path, first string
		more        bool
		count       int
	}{
		{"/v1/models?limit=1", "alpha", true, 1},
		{"/v1/models?limit=1&after_id=alpha", "default", true, 1},
		{"/v1/models?limit=1&before_id=zeta", "default", true, 1},
		{"/v1/models?after_id=zeta", "", false, 0},
		{"/v1/models?before_id=alpha", "", false, 0},
	} {
		rr := request(tc.path, "key")
		var b struct {
			Data    []map[string]any
			HasMore bool    `json:"has_more"`
			FirstID *string `json:"first_id"`
			LastID  *string `json:"last_id"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &b); err != nil {
			t.Fatal(err)
		}
		if rr.Code != 200 || len(b.Data) != tc.count || b.HasMore != tc.more {
			t.Fatalf("%s: %d %s", tc.path, rr.Code, rr.Body.String())
		}
		if tc.count == 0 {
			if b.FirstID != nil || b.LastID != nil {
				t.Fatalf("empty page cursors: %s", rr.Body.String())
			}
			continue
		}
		if b.FirstID == nil || *b.FirstID != tc.first || b.LastID == nil || b.Data[0]["type"] != "model" || b.Data[0]["created_at"] == "" {
			t.Fatalf("%s: %s", tc.path, rr.Body.String())
		}
	}
	for _, path := range []string{"/v1/models?limit=0", "/v1/models?limit=1001", "/v1/models?limit=bad", "/v1/models?after_id=private", "/v1/models?before_id=alpha&after_id=zeta"} {
		rr := request(path, "key")
		if rr.Code != 400 || !strings.Contains(rr.Body.String(), `"type":"invalid_request_error"`) || rr.Header().Get("Request-Id") == "" {
			t.Fatalf("%s: %d %s", path, rr.Code, rr.Body.String())
		}
	}
	for _, model := range []string{"private", "missing"} {
		rr := request("/v1/models/"+model, "header")
		if rr.Code != 404 || !strings.Contains(rr.Body.String(), `"type":"not_found_error"`) || rr.Header().Get("Request-Id") == "" || strings.Contains(rr.Body.String(), model) {
			t.Fatalf("%s: %d %s", model, rr.Code, rr.Body.String())
		}
	}
	for _, auth := range []string{"key", "header"} {
		rr := request("/v1/models/default", auth)
		if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"type":"model"`) {
			t.Fatalf("%s: %d %s", auth, rr.Code, rr.Body.String())
		}
	}
	for _, auth := range []string{"bearer", "both"} {
		rr := request("/v1/models", auth)
		if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"object":"list"`) {
			t.Fatalf("%s: %d %s", auth, rr.Code, rr.Body.String())
		}
	}
}

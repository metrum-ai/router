// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUnavailableIdentifierTransformUsesCallerDialectError(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	svc.cfg.Server.Identifiers.Mode = "transform"
	svc.identifiers = nil
	for _, tc := range []struct {
		path, want string
		anthropic  bool
	}{
		{"/v1/messages", `"type":"error"`, true},
		{"/v1/messages/count_tokens", `"type":"error"`, true},
		{"/v1/chat/completions", `"code":"identifier-transform-unavailable"`, false},
	} {
		req := httptest.NewRequest("POST", tc.path, strings.NewReader(`{}`))
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), tc.want) || rr.Header().Get("X-Request-Id") == "" || (tc.anthropic && rr.Header().Get("Request-Id") == "") {
			t.Fatalf("%s: %d %s", tc.path, rr.Code, rr.Body.String())
		}
	}
}

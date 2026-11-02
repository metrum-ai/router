// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestContentProvenanceUnsupportedNoDispatch(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusOK) }))
	defer upstream.Close()
	svc, err := New(testConfig(t, upstream.URL, "provider-key", t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	for _, tc := range []struct {
		name, token string
		status      int
	}{{"missing", "", http.StatusUnauthorized}, {"invalid", "bad-token", http.StatusUnauthorized}, {"valid", testToken, http.StatusNotImplemented}} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/content_provenance_checks", strings.NewReader(`{"input":"test"}`))
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.status == http.StatusNotImplemented {
				var body struct {
					Error struct {
						Type    string `json:"type"`
						Code    string `json:"code"`
						Param   any    `json:"param"`
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body.Error.Type != "invalid_request_error" || body.Error.Code != "unsupported_endpoint" || body.Error.Param != nil || !strings.Contains(body.Error.Message, "/v1/content_provenance_checks") {
					t.Fatalf("unexpected body %s", w.Body.String())
				}
			}
		})
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("unexpected upstream dispatch: %d", got)
	}
}

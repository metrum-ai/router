// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAdminMountedFreshnessUsesTrustedOperationMetadata(t *testing.T) {
	s := freshnessGateService(t, "db", "postgres") // Dialect classification only, no SQL/network.
	p := Principal{Subject: "operator", Kind: "mtls"}
	specs := []Operation{
		{ID: "readProbe", Method: "GET", Path: "/admin/v1/read", RateClass: AdminRateClassRead, Object: "admin:config", Action: "read", Domain: AdminDomainGlobal, NoSideEffect: true, HandlerKey: "read"},
		{ID: "validateProbe", Method: "POST", Path: "/admin/v1/validate", RateClass: AdminRateClassWrite, Object: "admin:config", Action: "write", Domain: AdminDomainGlobal, NoSideEffect: true, HandlerKey: "validate"},
		{ID: "mutationProbe", Method: "POST", Path: "/admin/v1/write", RateClass: AdminRateClassWrite, Object: "admin:config", Action: "write", Domain: AdminDomainGlobal, HandlerKey: "write"},
		{ID: "mutationGetProbe", Method: "GET", Path: "/admin/v1/mutating-get", RateClass: AdminRateClassWrite, Object: "admin:config", Action: "write", Domain: AdminDomainGlobal, HandlerKey: "write"},
	}
	registry, err := NewAdminV1Registry(specs)
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	mux := http.NewServeMux()
	err = RegisterAdminV1(mux, AdminV1Deps{Registry: registry, Authenticate: &fakeAuthenticator{principal: p}, Authorize: s.Authorize,
		Denials: &recordingDenialSink{}, CredentialEvents: &recordingCredentialSink{},
		Handlers: map[string]http.HandlerFunc{
			"read": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }, "validate": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }, "write": func(w http.ResponseWriter, r *http.Request) { writes++; w.WriteHeader(200) },
		}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		want         int
	}{{"GET", "/admin/v1/read", 200}, {"POST", "/admin/v1/validate", 200}, {"POST", "/admin/v1/write", 503}, {"GET", "/admin/v1/mutating-get", 503}} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("X-Policy-Fresh", "true")
		r.Header.Set("X-Admin-Read-Only", "true")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s %s=%d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
		if tc.want == 503 && (!strings.Contains(w.Body.String(), `"code":"policy-stale"`) || !strings.Contains(w.Header().Get("Content-Type"), "application/problem+json")) {
			t.Errorf("unsafe freshness problem: %s %s", w.Header().Get("Content-Type"), w.Body.String())
		}
	}
	if writes != 0 {
		t.Fatal("unknown-age authority reached mutation handler")
	}
	s.policyFreshness.observeSuccess(1, time.Now())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/admin/v1/write", nil))
	if w.Code != 200 || writes != 1 {
		t.Fatalf("successful authoritative observation did not enable mutation: %d/%d %s", w.Code, writes, w.Body.String())
	}
}

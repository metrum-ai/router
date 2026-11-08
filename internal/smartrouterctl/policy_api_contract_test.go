// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package smartrouterctl

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPolicyClientUsesSharedPaginationAndSafeProblems(t *testing.T) {
	t.Setenv("ROUTER_ADMIN_KEY", "test-policy-client-credential")
	t.Setenv("ROUTER_ADMIN_KEY_FILE", "")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			if r.URL.Query().Get("limit") != "2" || r.URL.Query().Get("cursor") != "opaque" || r.URL.Query().Has("page_size") {
				t.Errorf("noncanonical pagination query=%s", r.URL.RawQuery)
			}
			w.Header().Set("ETag", "\"7\"")
			w.Header().Set("Link", "</admin/v1/policy-sets?cursor=next>; rel=\"next\"")
			w.Write([]byte(`{"items":[]}`))
			return
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(503)
		w.Write([]byte(`{"type":"urn:router:admin:policy-stale","code":"policy-stale","detail":"private SQL test-policy-client-credential"}`))
	}))
	defer server.Close()
	client, err := NewPolicyAPIClient(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.CallWithMetadata("list", "", nil, PolicyCallOptions{PageSize: 2, Cursor: "opaque"})
	if err != nil {
		t.Fatal(err)
	}
	if response.ETag != "\"7\"" || !strings.Contains(response.Link, "cursor=next") || !json.Valid(response.Body) {
		t.Fatalf("metadata lost: %+v", response)
	}
	_, err = client.Call("get", "one", nil, PolicyCallOptions{})
	var problem *PolicyAPIError
	if !errors.As(err, &problem) || problem.Code != "policy-stale" || problem.Status != 503 {
		t.Fatalf("safe problem lost: %v", err)
	}
	if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "test-policy-client-credential") {
		t.Fatal("private problem detail leaked")
	}
}
func TestPolicyClientRequiresExplicitMutationRevision(t *testing.T) {
	t.Setenv("ROUTER_ADMIN_KEY", "test-policy-client-credential")
	t.Setenv("ROUTER_ADMIN_KEY_FILE", "")
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.EscapedPath() != "/admin/v1/policy-sets/a%2Fb/activate" || r.Method != "POST" || r.Header.Get("If-Match") != "\"9\"" || r.Header.Get("Idempotency-Key") != "retry-key" {
			t.Errorf("wrong mutation: %s %s %v", r.Method, r.URL.EscapedPath(), r.Header)
		}
		w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client, err := NewPolicyAPIClient(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call("activate", "a/b", nil, PolicyCallOptions{}); err == nil || hits != 0 {
		t.Fatal("mutation without revision dispatched")
	}
	if _, err := client.Call("activate", "a/b", nil, PolicyCallOptions{IfMatch: "\"9\"", IdempotencyKey: "retry-key"}); err != nil {
		t.Fatal(err)
	}
}

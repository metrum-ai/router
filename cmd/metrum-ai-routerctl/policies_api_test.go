// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPoliciesCommandCanonicalReadAndWritePreconditions(t *testing.T) {
	t.Setenv("ROUTER_ADMIN_KEY", "test-policy-command-key")
	t.Setenv("ROUTER_ADMIN_KEY_FILE", "")
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Method != "GET" || r.URL.Path != "/admin/v1/policy-sets" || r.URL.Query().Get("limit") != "2" {
			t.Errorf("wrong request %s %s", r.Method, r.URL)
		}
		w.Header().Set("ETag", "\"7\"")
		w.Header().Set("Link", "</admin/v1/policy-sets?cursor=opaque>; rel=\"next\"")
		w.Write([]byte(`{"items":[]}`))
	}))
	defer server.Close()
	var out bytes.Buffer
	if err := runPoliciesCommand([]string{"list", "--router-url", server.URL, "--limit", "2"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "opaque") || !strings.Contains(out.String(), "etag") {
		t.Fatal("pagination/precondition metadata lost")
	}
	out.Reset()
	if err := runPoliciesCommand([]string{"activate", "--set-id", "one", "--router-url", server.URL}, &out); err == nil || hits != 1 {
		t.Fatal("missing revision mutation reached server")
	}
}

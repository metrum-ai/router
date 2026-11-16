// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package smartrouterctl

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminClientEndpointAndRedirectSafety(t *testing.T) {
	t.Setenv("ROUTER_ADMIN_KEY", "test-admin-key")
	t.Setenv("ROUTER_URL", "")
	for _, endpoint := range []string{"http://example.invalid", "http://192.0.2.4", "https://user:pass@example.invalid"} {
		if _, err := NewAdminAPIClient(endpoint, ""); err == nil {
			t.Errorf("unsafe endpoint accepted: %s", endpoint)
		}
	}
	client, err := NewAdminAPIClient("", "")
	if err != nil || client.BaseURL != "http://127.0.0.1:8081" {
		t.Fatalf("default endpoint = %v, err=%v", client, err)
	}

	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("Authorization") != "Bearer test-admin-key" {
			t.Error("missing admin credential")
		}
		http.Redirect(w, r, "/unexpected", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err = NewAdminAPIClient(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	_, status, err := client.Do(http.MethodGet, "/admin/v1/config/groups", nil, nil)
	if err == nil || status != http.StatusTemporaryRedirect || hits != 1 {
		t.Fatalf("redirect followed: status=%d hits=%d err=%v", status, hits, err)
	}
	if strings.Contains(err.Error(), "test-admin-key") {
		t.Fatal("credential leaked in redirect error")
	}
}

func TestAdminClientFailureHints(t *testing.T) {
	t.Setenv("ROUTER_ADMIN_KEY", "test-admin-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	client, err := NewAdminAPIClient(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = client.Do(http.MethodGet, "/admin/v1/config/groups", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "admin.enabled") || !strings.Contains(err.Error(), "admin.listen") || !strings.Contains(err.Error(), "ROUTER_ADMIN_KEY") {
		t.Fatalf("unauthorized error lacks setup hints: %v", err)
	}
}

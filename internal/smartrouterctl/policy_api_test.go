// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package smartrouterctl

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPolicyAPIClientUnixSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "policy.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer policy-socket-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"items":[]}`))
	})}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })
	t.Setenv("ROUTER_ADMIN_KEY", "policy-socket-key")
	t.Setenv("ROUTER_URL", "")
	client, err := NewPolicyAPIClientWithSocket("", "", socket)
	if err != nil {
		t.Fatal(err)
	}
	body, err := client.Call("list", "", nil, PolicyCallOptions{})
	if err != nil || string(body) != `{"items":[]}` {
		t.Fatalf("policy socket response=%s err=%v", body, err)
	}
	if _, err := NewPolicyAPIClientWithSocket("http://localhost", "", socket); err == nil {
		t.Fatal("accepted both URL and socket")
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyClientCredentialAndEndpointSafety(t *testing.T) {
	t.Setenv("ROUTER_ADMIN_KEY", "")
	t.Setenv("ROUTER_ADMIN_KEY_FILE", "")
	path := filepath.Join(t.TempDir(), "admin.key")
	if err := os.WriteFile(path, []byte("test-only-policy-key"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"http://example.invalid", "https://user:password@example.invalid", "https://example.invalid?key=private", "https://example.invalid/#private"} {
		if _, err := NewPolicyAPIClient(endpoint, path); err == nil {
			t.Fatalf("unsafe endpoint accepted: %s", endpoint)
		}
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPolicyAPIClient("https://example.invalid", path); err == nil {
		t.Fatal("world-readable key file accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link.key")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPolicyAPIClient("https://example.invalid", link); err == nil {
		t.Fatal("symlink key file accepted")
	}
	if _, err := NewPolicyAPIClient("https://example.invalid", path); err != nil {
		t.Fatal(err)
	}
}
func TestPolicyClientDoesNotFollowRedirect(t *testing.T) {
	const token = "test-only-policy-key"
	t.Setenv("ROUTER_ADMIN_KEY", token)
	t.Setenv("ROUTER_ADMIN_KEY_FILE", "")
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing machine credential")
		}
		http.Redirect(w, r, "/unexpected", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := NewPolicyAPIClient(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Call("get", "one", nil, PolicyCallOptions{})
	if err == nil || hits != 1 {
		t.Fatalf("redirect was followed: hits=%d err=%v", hits, err)
	}
	if strings.Contains(err.Error(), token) {
		t.Fatal("credential appeared in error")
	}
}

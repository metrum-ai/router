// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package smartrouterctl

import (
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAdminAPIClientUnixSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets are unavailable on Windows")
	}
	socket := filepath.Join(t.TempDir(), "admin.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer socket-test-key" {
			http.Error(w, "unauthorized", 401)
			return
		}
		if r.URL.Path == "/slow" {
			time.Sleep(50 * time.Millisecond)
		}
		w.WriteHeader(204)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	t.Setenv("ROUTER_ADMIN_KEY", "socket-test-key")
	t.Setenv("ROUTER_URL", "")
	client, err := NewAdminAPIClientWithSocket("", "", socket)
	if err != nil {
		t.Fatal(err)
	}
	_, status, err := client.Do(http.MethodGet, "/admin/v1/config/status", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status != 204 {
		t.Fatalf("status=%d, want 204", status)
	}
	if _, err := NewAdminAPIClientWithSocket("http://localhost", "", socket); err == nil {
		t.Fatal("accepted both URL and socket")
	}
	client.HTTP.Timeout = time.Millisecond
	if _, _, err := client.Do(http.MethodGet, "/slow", nil, nil); err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("socket client timeout error=%v, want deadline exceeded", err)
	}
}

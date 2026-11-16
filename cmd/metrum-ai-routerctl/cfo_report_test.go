// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestCFOReportCommand(t *testing.T) {
	t.Setenv("ROUTER_ADMIN_KEY", "rtr_admin_fake")
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer rtr_admin_fake" {
			t.Error("missing admin credential")
		}
		paths = append(paths, r.URL.RequestURI())
		if strings.HasSuffix(r.URL.Path, "/export.csv") {
			_, _ = w.Write([]byte("section,key\nsummary,all\n"))
			return
		}
		_, _ = w.Write([]byte(`{"summary":{}}`))
	}))
	defer server.Close()
	var out bytes.Buffer
	if err := runCFOReport([]string{"--router-url", server.URL, "--since", "24h", "--baseline", "fixture"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != `{"summary":{}}` || len(paths) != 1 || paths[0] != "/admin/v1/reports/cfo?baseline=fixture&since=24h" {
		t.Fatalf("output=%q paths=%v", out.String(), paths)
	}
	out.Reset()
	if err := runCFOReport([]string{"--router-url", server.URL, "--format", "csv"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "summary,all") || paths[1] != "/admin/v1/reports/cfo/export.csv" {
		t.Fatalf("output=%q paths=%v", out.String(), paths)
	}
	out.Reset()
	if err := runCFOReport([]string{"--router-url", server.URL, "--format", "markdown"}, &out); err != nil {
		t.Fatal(err)
	}
	if paths[2] != "/admin/v1/reports/cfo/export.md" {
		t.Fatalf("wrong Markdown endpoint: %v", paths)
	}
	if err := runCFOReport([]string{"--router-url", server.URL, "--reports-prefix", "/admin/v1/reports/../elsewhere"}, &out); err == nil {
		t.Fatal("accepted unsafe prefix")
	}
}

func TestCFOReportRequiresExplicitEndpoint(t *testing.T) {
	t.Setenv("ROUTER_URL", "")
	t.Setenv("ROUTER_ADMIN_KEY", "rtr_admin_fake")
	err := runCFOReport(nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--router-url, ROUTER_URL, or --socket") {
		t.Fatalf("missing endpoint error=%v", err)
	}
}

func TestCFOReportUnixSocketOption(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "admin.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer rtr_admin_fake" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"summary":{}}`))
	})}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })
	t.Setenv("ROUTER_ADMIN_KEY", "rtr_admin_fake")
	var out bytes.Buffer
	if err := runCFOReport([]string{"--socket", socket}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != `{"summary":{}}` {
		t.Fatalf("socket report response=%q", out.String())
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCFORouterURLNeverLeaksCredentials(t *testing.T) {
	for _, raw := range []string{"https://user:secret@host%zz", "https://user:secret@example.com", "http:user:secret@host"} {
		err := runCFOReport([]string{"--router-url", raw}, &bytes.Buffer{})
		if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), raw) {
			t.Fatalf("unsafe URL error: %v", err)
		}
		t.Setenv("ROUTER_URL", raw)
		err = runCFOReport(nil, &bytes.Buffer{})
		if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), raw) {
			t.Fatalf("unsafe environment URL error: %v", err)
		}
	}
}

func TestCFOReportDoesNotFollowRedirects(t *testing.T) {
	t.Setenv("ROUTER_ADMIN_KEY", "rtr_admin_fake")
	redirected := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			redirected = true
			return
		}
		http.Redirect(w, r, "/redirected", http.StatusFound)
	}))
	defer server.Close()
	if err := runCFOReport([]string{"--router-url", server.URL}, &bytes.Buffer{}); err == nil {
		t.Fatal("accepted redirect")
	}
	if redirected {
		t.Fatal("followed redirect")
	}
}

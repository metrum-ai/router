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
	if out.String() != `{"summary":{}}` || len(paths) != 1 || paths[0] != "/admin/reports/api/cfo?baseline=fixture&since=24h" {
		t.Fatalf("output=%q paths=%v", out.String(), paths)
	}
	out.Reset()
	if err := runCFOReport([]string{"--router-url", server.URL, "--format", "csv"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "summary,all") || paths[1] != "/admin/reports/cfo/export.csv" {
		t.Fatalf("output=%q paths=%v", out.String(), paths)
	}
	out.Reset()
	if err := runCFOReport([]string{"--router-url", server.URL, "--format", "markdown"}, &out); err != nil {
		t.Fatal(err)
	}
	if paths[2] != "/admin/reports/cfo/export.md" {
		t.Fatalf("wrong Markdown endpoint: %v", paths)
	}
	if err := runCFOReport([]string{"--router-url", server.URL, "--reports-prefix", "/admin/reports/../elsewhere"}, &out); err == nil {
		t.Fatal("accepted unsafe prefix")
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

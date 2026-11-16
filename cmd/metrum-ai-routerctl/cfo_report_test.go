// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/metrum-ai/router/internal/router"
	"github.com/metrum-ai/router/internal/smartrouterctl"
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
	client, err := smartrouterctl.NewAdminAPIClient("", "")
	if err != nil || client.BaseURL != "http://127.0.0.1:8081" {
		t.Fatalf("default endpoint client=%+v err=%v", client, err)
	}
}

func TestCFOReportUnixSocketOption(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets are unavailable on Windows")
	}
	socket := filepath.Join(t.TempDir(), "admin.sock")
	plane, err := router.NewAdminPlane(&router.AdminPlaneConfig{Enabled: true, UnixSocket: router.AdminUnixSocketConfig{Path: socket, Mode: "0600"}}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer rtr_admin_fake" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"summary":{}}`))
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := plane.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plane.Shutdown(context.Background()) })
	t.Setenv("ROUTER_ADMIN_KEY", "rtr_admin_fake")
	t.Setenv("ROUTER_URL", "invalid-but-ignored-for-socket")
	var out bytes.Buffer
	if err := runCFOReport([]string{"--socket", socket}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != `{"summary":{}}` {
		t.Fatalf("socket report response=%q", out.String())
	}
	if err := plane.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type socketCLIAuthenticator struct{}

func (socketCLIAuthenticator) Authenticate(context.Context, *http.Request) (router.Principal, error) {
	return router.Principal{Subject: "adminkey:socket-cli", Kind: "adminkey", CredentialID: "socket-cli"}, nil
}

func TestRouterctlSocketReachesRealAdminHandler(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets are unavailable on Windows")
	}
	config := writeSecurityDoctorConfig(t)
	cfg, err := router.LoadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "admin.sock")
	cfg.Admin.Enabled = true
	cfg.Admin.Listen = ""
	cfg.Admin.UnixSocket = router.AdminUnixSocketConfig{Path: socket, Mode: "0600"}
	cfg.Server.AdminAuth.Authorization = router.AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, adminkey:socket-cli, *, admin:config, read"}}
	svc, err := router.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	svc.SetAdminAuthenticator(socketCLIAuthenticator{})
	var mountErr error
	svc.RegisterAdminRoutes(func(mux *router.AdminMux) { mountErr = svc.MountAdminV1(mux, svc.AdminAuthenticator(), svc.Authorize) })
	if mountErr != nil {
		t.Fatal(mountErr)
	}
	plane, err := router.NewAdminPlane(&cfg.Admin, svc.AdminHandler())
	if err != nil {
		t.Fatal(err)
	}
	if err := plane.Start(); err != nil {
		t.Fatal(err)
	}
	defer plane.Shutdown(context.Background())
	t.Setenv("ROUTER_ADMIN_KEY", "routerctl-socket-test")
	var out bytes.Buffer
	oldStdout := os.Stdout
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writeEnd
	groupsCommand([]string{"list", "--socket", socket})
	_ = writeEnd.Close()
	os.Stdout = oldStdout
	if _, err := out.ReadFrom(readEnd); err != nil {
		t.Fatal(err)
	}
	_ = readEnd.Close()
	if !strings.Contains(out.String(), "groups") && !strings.Contains(out.String(), "items") {
		t.Fatalf("unexpected real routerctl response: %q", out.String())
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

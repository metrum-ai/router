// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReportsCFOWiredBinaryFailureHints(t *testing.T) {
	const token = "rtr_admin_binary_test_key"
	binary := buildRouterctlWithTimeout(t, "150ms")
	baseEnv := envWithoutDatabaseCredentials(token)
	run := func(routerURL string) (string, error) {
		cmd := exec.Command(binary, "reports", "cfo", "--router-url", routerURL)
		cmd.Env = baseEnv
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	assertHints := func(name, routerURL string) {
		t.Helper()
		output, err := run(routerURL)
		if err == nil || !strings.Contains(output, "admin.enabled") || !strings.Contains(output, "admin.listen") || !strings.Contains(output, "ROUTER_ADMIN_KEY") {
			t.Fatalf("%s output=%q err=%v, want admin setup hints", name, output, err)
		}
	}

	t.Run("unauthorized", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()
		assertHints("401", server.URL)
	})
	t.Run("unreachable", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := listener.Addr().String()
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		assertHints("unreachable", "http://"+address)
	})
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			time.Sleep(time.Second)
		}))
		defer server.Close()
		output, err := run(server.URL)
		if err == nil || !strings.Contains(output, "deadline exceeded") || !strings.Contains(output, "admin.listen") {
			t.Fatalf("timeout output=%q err=%v", output, err)
		}
	})
}

func buildRouterctlWithTimeout(t *testing.T, timeout string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "metrum-ai-routerctl")
	ldflags := "-X=github.com/metrum-ai/router/internal/smartrouterctl.adminAPIRequestTimeoutText=" + timeout
	if output, err := exec.Command("go", "build", "-ldflags", ldflags, "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build routerctl: %v: %s", err, output)
	}
	return binary
}

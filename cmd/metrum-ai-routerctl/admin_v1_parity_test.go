// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOnlineCommandsUseAdminV1WithoutDatabaseCredentials(t *testing.T) {
	const token = "rtr_admin_test_only_parity_key"
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q, want admin bearer credential", got)
		}
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	binary := filepath.Join(dir, "metrum-ai-routerctl")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build routerctl: %v: %s", err, output)
	}
	yamlPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(yamlPath, []byte("state_path: state.json\nproviders: {}\nmodels: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{
		{"config", "diff", "--api", "--router-url", server.URL},
		{"config", "import", "--api", "--router-url", server.URL, "--yaml", yamlPath, "--name", "demo"},
		{"config", "export", "--api", "--router-url", server.URL},
		{"config", "activate", "--api", "--router-url", server.URL, "--set-id", "set-1"},
		{"config", "rollback", "--api", "--router-url", server.URL, "--set-id", "set-1"},
		{"groups", "list", "--router-url", server.URL},
		{"groups", "show", "--router-url", server.URL, "default"},
		{"groups", "history", "--router-url", server.URL, "default"},
		{"groups", "set-targets", "--router-url", server.URL, "--target", "openai/model", "default"},
		{"groups", "add-target", "--router-url", server.URL, "--target", "openai/model", "default"},
		{"groups", "remove-target", "--router-url", server.URL, "--target", "openai/model", "default"},
		{"groups", "rollback", "--router-url", server.URL, "default"},
		{"groups", "shadow", "start", "--router-url", server.URL, "--target", "openai/model", "default"},
		{"groups", "shadow", "status", "--router-url", server.URL, "default"},
		{"groups", "shadow", "promote", "--router-url", server.URL, "default"},
		{"groups", "shadow", "stop", "--router-url", server.URL, "default"},
		{"reports", "cfo", "--router-url", server.URL},
		{"reports", "cfo", "--router-url", server.URL, "--format", "csv"},
		{"reports", "cfo", "--router-url", server.URL, "--format", "markdown"},
	}
	want := []string{
		"GET /admin/v1/config/diff", "POST /admin/v1/config/sets", "GET /admin/v1/config/sets/active",
		"POST /admin/v1/config/sets/set-1/activate", "POST /admin/v1/config/rollback",
		"GET /admin/v1/config/groups", "GET /admin/v1/config/groups/default",
		"GET /admin/v1/config/groups/default/history", "PUT /admin/v1/config/groups/default/targets",
		"POST /admin/v1/config/groups/default/targets", "DELETE /admin/v1/config/groups/default/targets/openai/model",
		"POST /admin/v1/config/groups/default/rollback", "POST /admin/v1/config/groups/default/shadow",
		"GET /admin/v1/config/groups/default/shadow", "POST /admin/v1/config/groups/default/shadow/promote",
		"DELETE /admin/v1/config/groups/default/shadow", "GET /admin/v1/reports/cfo",
		"GET /admin/v1/reports/cfo/export.csv", "GET /admin/v1/reports/cfo/export.md",
	}
	for _, args := range commands {
		cmd := exec.Command(binary, args...)
		cmd.Dir = dir
		cmd.Env = envWithoutDatabaseCredentials(token)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("routerctl %v: %v: %s", args, err, output)
		}
		if strings.Contains(string(output), token) {
			t.Fatalf("routerctl %v leaked admin credential", args)
		}
	}
	if len(requests) != len(want) {
		t.Fatalf("got %d online requests, want %d: %v", len(requests), len(want), requests)
	}
	for i := range want {
		if requests[i] != want[i] {
			t.Errorf("request %d = %q, want %q", i, requests[i], want[i])
		}
	}
}

func envWithoutDatabaseCredentials(token string) []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(key)
		if strings.Contains(upper, "DSN") || strings.Contains(upper, "DATABASE_URL") || strings.Contains(upper, "DB_PASSWORD") {
			continue
		}
		env = append(env, item)
	}
	return append(env, "ROUTER_ADMIN_KEY="+token, "ROUTER_URL=")
}

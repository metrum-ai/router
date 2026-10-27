// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metrum-ai/router/internal/smartrouterctl"
)

// TestAdminAPIClientErrorMapping checks the typed error mapping against a
// stub Admin API server (the full HTTP flow against a live router service is
// covered by the e2e suite in internal/router).
func TestAdminAPIClientErrorMapping(t *testing.T) {
	server := newStubAdminServer(t)
	defer server.Close()

	client, err := smartrouterctl.NewAdminAPIClient(server.URL, "")
	if err == nil {
		t.Fatal("missing credential must fail")
	}
	t.Setenv("ROUTER_ADMIN_KEY", "rtr_admin_test_only_key_material")
	client, err = smartrouterctl.NewAdminAPIClient(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	_, code, err := client.Do("GET", "/admin/config/groups", nil, nil)
	if err == nil || code != 403 {
		t.Fatalf("expected typed 403, got code=%d err=%v", code, err)
	}
	apiErr, ok := err.(*smartrouterctl.AdminAPIError)
	if !ok || apiErr.Type != "config-forbidden" {
		t.Fatalf("error type not mapped: %+v", err)
	}
	if strings.Contains(apiErr.Raw, "rtr_admin_test_only_key_material") || strings.Contains(apiErr.Error(), "rtr_admin_test_only_key_material") {
		t.Fatal("admin token leaked in error")
	}
}

func TestParseGroupTargetFlag(t *testing.T) {
	provider, ref, err := smartrouterctl.ParseGroupTargetFlag("openai/gpt-6.1-terra")
	if err != nil || provider != "openai" || ref != "gpt-6.1-terra" {
		t.Fatalf("simple: %q %q %v", provider, ref, err)
	}
	provider, ref, err = smartrouterctl.ParseGroupTargetFlag("fireworks/accounts/fireworks/models/deepseek-v4p1-flash")
	if err != nil || provider != "fireworks" || ref != "accounts/fireworks/models/deepseek-v4p1-flash" {
		t.Fatalf("slashed ref: %q %q %v", provider, ref, err)
	}
	if _, _, err := smartrouterctl.ParseGroupTargetFlag("noslash"); err == nil {
		t.Fatal("invalid target accepted")
	}
}

func TestGroupsCommandMissingCredentialFailsClosed(t *testing.T) {
	os.Unsetenv("ROUTER_ADMIN_KEY")
	os.Unsetenv("ROUTER_ADMIN_KEY_FILE")
	out, err := exec.Command("go", "run", ".", "groups", "list", "--router-url", "http://127.0.0.1:1").CombinedOutput()
	if err == nil {
		t.Fatalf("groups list without credential succeeded: %s", out)
	}
	if !strings.Contains(string(out), "admin credential required") {
		t.Fatalf("unexpected error: %s", out)
	}
	if strings.Contains(string(out), "token-out") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestAdminkeysGenerateConfigSnippet(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "a.key")
	out, err := exec.Command("go", "run", ".", "adminkeys", "generate", "--key-id", "ops-x", "--token-out", keyPath).CombinedOutput()
	if err != nil {
		t.Fatalf("generate: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "key_hash_env: ROUTER_ADMIN_KEY_OPS_X_HASH") {
		t.Fatalf("snippet missing env name: %s", out)
	}
}

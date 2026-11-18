// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSecurityConsoleReleaseInstructionsEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("release instructions require bash")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("release instructions require curl")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "routerctl")
	build := exec.Command("go", "build", "-p=2", "-o", binary, "../../cmd/metrum-ai-routerctl")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build routerctl: %v: %s", err, output)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"fixture","model":"mock-model","choices":[{"index":0,"message":{"role":"assistant","content":"restored"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "fixture-provider-key", dir)
	cfg.Admin.Enabled = true
	cfg.Admin.Listen = "127.0.0.1:0"
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.AdminReports = AdminReportsConfig{Enabled: true, Security: AdminSecurityReportsConfig{Enabled: true}}
	t.Setenv("CONSOLE_RELEASE_PASSWORD_HASH", mustBcryptHash(t, "fixture-password"))
	key, err := GenerateAdminAPIKey("console-release", nil, callerTokenHashMinIterations)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONSOLE_RELEASE_KEY_HASH", key.TokenHash)
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{Enabled: true, AllowInsecureHTTP: true, Users: []AdminBasicAuthUser{
		{Username: "operator", Subject: "basic:operator", Domain: "*", PasswordHashEnv: "CONSOLE_RELEASE_PASSWORD_HASH"},
	}}
	cfg.Server.AdminAuth.APIKeys = []AdminAPIKeyConfig{{KeyID: key.KeyID, KeyHashEnv: "CONSOLE_RELEASE_KEY_HASH", Domain: "*", Permissions: []string{"config:read", "admin:containment:contain"}}}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{
		"p, basic:operator, *, admin:reports, read|drilldown",
		"p, basic:operator, *, admin:security_reports, read",
		"p, basic:operator, *, admin:containment, contain",
		"p, adminkey:console-release, *, admin:config, read",
		"p, adminkey:console-release, *, admin:containment, contain",
	}}
	dbPath := filepath.Join(dir, "config.sqlite")
	runner, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	applyConfigControlPlaneMigrationsForTest(t, runner)
	cfg.Server.ConfigSource = ConfigSourceConfig{Mode: "database", Driver: "sqlite", Path: dbPath, RuntimeScope: "fixture"}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	setID, err := ImportCanonicalYAML(runner.db, "fixture", "console-release", raw, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := ActivateConfigSet(runner.db, "fixture", setID, "fixture"); err != nil {
		t.Fatal(err)
	}
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	serving := httptest.NewServer(svc.Handler())
	defer serving.Close()
	admin := httptest.NewServer(svc.AdminHandler())
	defer admin.Close()

	infer := func(t *testing.T, want int) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, serving.URL+"/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"fixture"}]}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		if response.StatusCode != want {
			t.Fatalf("inference status=%d want=%d: %s", response.StatusCode, want, body)
		}
	}
	suspend := func(t *testing.T) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, serving.URL+"/admin/reports/api/security/csrf-token", nil)
		req.SetBasicAuth("operator", "fixture-password")
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var csrf struct {
			Token string `json:"csrf_token"`
		}
		err = json.NewDecoder(response.Body).Decode(&csrf)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK || csrf.Token == "" {
			t.Fatalf("CSRF status=%d decode=%v", response.StatusCode, err)
		}
		req, _ = http.NewRequest(http.MethodPost, serving.URL+"/admin/reports/api/security/containment", strings.NewReader(`{"caller_id":"alice","action":"suspend","reason":"operator-reviewed"}`))
		req.SetBasicAuth("operator", "fixture-password")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", serving.URL)
		req.Header.Set("X-CSRF-Token", csrf.Token)
		for _, cookie := range response.Cookies() {
			req.AddCookie(cookie)
		}
		response, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("console suspend status=%d: %s", response.StatusCode, body)
		}
		infer(t, http.StatusForbidden)
	}
	template, err := os.ReadFile("admindist/web/src/components/release-instructions.txt")
	if err != nil {
		t.Fatal(err)
	}
	instructions := strings.NewReplacer("__CALLER__", "'alice'", "__BODY__", `'{"caller_id":"alice","action":"resume","reason":"operator-reviewed"}'`).Replace(string(template))
	cli, httpSteps, found := strings.Cut(instructions, "# HTTP equivalent\n")
	if !found {
		t.Fatal("release instructions must contain CLI and HTTP alternatives")
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ROUTER_URL", admin.URL)
	t.Setenv("ROUTER_ADMIN_URL", admin.URL)
	t.Setenv("ROUTER_ADMIN_KEY", key.Token)
	t.Setenv("ROUTER_ADMIN_TOKEN", key.Token)
	t.Setenv("ROUTER_ADMIN_KEY_FILE", "")
	for _, steps := range []struct{ name, script string }{{"copied CLI", cli}, {"copied HTTP", httpSteps}} {
		t.Run(steps.name, func(t *testing.T) {
			suspend(t)
			command := exec.Command("bash", "-e", "-o", "pipefail", "-c", steps.script)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("copied release instructions: %v: %s", err, output)
			}
			infer(t, http.StatusOK)
		})
	}
	var resumes int64
	if err := runner.db.Model(&configControlPlaneChangeEventRow{}).Where("action = ? AND actor = ?", "caller_containment_resume", "adminkey:console-release").Count(&resumes).Error; err != nil || resumes != 2 {
		t.Fatalf("resume audit rows=%d err=%v", resumes, err)
	}
	// An explicit stale revision must reach the server unchanged and fail closed.
	command := exec.Command(binary, "contain", "--caller-id", "alice", "--reason", "fixture", "--if-match", `"rev-0"`)
	if output, err := command.CombinedOutput(); err == nil || !strings.Contains(string(output), "412") {
		t.Fatalf("explicit stale revision error=%v: %s", err, output)
	}
	infer(t, http.StatusOK)
}

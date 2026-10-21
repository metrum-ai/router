// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Exercise the real loopback HTTP boundary with disposable config and usage DBs.
func TestRevokedKeyScopedSecurityLocalhostE2E(t *testing.T) {
	t.Setenv("MOCK_API_KEY", "synthetic-provider-key")
	t.Setenv("REVOKED_TEST_ADMIN_HASH", mustBcryptHash(t, "synthetic-admin-password"))
	dbPath := serveAuthE2EDB(t)
	runner, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	applyConfigControlPlaneMigrationsForTest(t, runner)
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "synthetic-provider-key", dir)
	cfg.Server.ConfigSource = ConfigSourceConfig{Mode: "database", Driver: "sqlite", Path: dbPath, RuntimeScope: "staging"}
	enabled := true
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"), &enabled)
	cfg.Server.AdminReports = AdminReportsConfig{Enabled: true, DefaultSince: "24h", MaxRange: "31d", MaxRows: 100, Security: AdminSecurityReportsConfig{Enabled: true, RetentionDays: 30}}
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{Enabled: true, AllowInsecureHTTP: true, Users: []AdminBasicAuthUser{
		{Username: "scoped", PasswordHashEnv: "REVOKED_TEST_ADMIN_HASH", Subject: "basic:scoped", Domain: "search/dev"},
		{Username: "other", PasswordHashEnv: "REVOKED_TEST_ADMIN_HASH", Subject: "basic:other", Domain: "other/dev"},
	}}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{
		"p, basic:scoped, search/dev, admin:reports, read|drilldown", "p, basic:scoped, search/dev, admin:security_reports, read",
		"p, basic:other, other/dev, admin:reports, read|drilldown", "p, basic:other, other/dev, admin:security_reports, read",
	}}
	canonical, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	id, err := ImportCanonicalYAML(runner.db, "staging", "security", canonical, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := ActivateConfigSet(runner.db, "staging", id, "test"); err != nil {
		t.Fatal(err)
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	mux := http.NewServeMux()
	RegisterCallerDirectoryAdmin(mux, CallerAdminDeps{DB: runner.db, RuntimeScope: "staging", Authorize: func(r *http.Request, object, action string) bool { return r.Header.Get("X-Test-Admin") == "yes" }, AfterMutate: func(_ *http.Request, _ string) error { return svc.reloadActiveConfigFromDB() }})
	mux.Handle("/", svc.Handler())
	var listener net.Listener
	for port := 18100; port < 19000; port++ {
		listener, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()
	base := "http://" + listener.Addr().String()
	request := func(method, path string, payload any, token, admin string) (int, []byte, string) {
		t.Helper()
		var body io.Reader
		if payload != nil {
			raw, _ := json.Marshal(payload)
			body = bytes.NewReader(raw)
		}
		req, _ := http.NewRequest(method, base+path, body)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if admin == "issue" {
			req.Header.Set("X-Test-Admin", "yes")
		} else if admin != "" {
			req.SetBasicAuth(admin, "synthetic-admin-password")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, raw, resp.Header.Get("X-Request-Id")
	}
	status, raw, _ := request("POST", "/admin/callers/issue", map[string]any{"owner_user": "synthetic", "project": "search", "environment": "dev", "allow": []string{"default"}}, "", "issue")
	if status != 201 {
		t.Fatalf("issue status=%d", status)
	}
	var issued callerAdminIssueResponse
	if err := json.Unmarshal(raw, &issued); err != nil {
		t.Fatal(err)
	}
	status, _, _ = request("GET", "/v1/models", nil, issued.Token, "")
	if status != 200 {
		t.Fatalf("active status=%d", status)
	}
	status, _, _ = request("POST", "/admin/callers/"+issued.Caller.ID+"/revoke", nil, "", "issue")
	if status != 200 {
		t.Fatalf("revoke=%d", status)
	}
	for _, path := range []string{"/v1/models", "/v1/chat/completions"} {
		method := "GET"
		var payload any
		if strings.Contains(path, "chat") {
			method = "POST"
			payload = map[string]any{"model": "default"}
		}
		status, raw, rid := request(method, path, payload, issued.Token, "")
		if status != 403 || !strings.Contains(string(raw), "key-disabled") {
			t.Fatalf("revoked status=%d", status)
		}
		flushUsageForTest(t, svc)
		var usage usageRecord
		if err := svc.usage.db.Where("request_id = ?", rid).First(&usage).Error; err != nil {
			t.Fatal(err)
		}
		if usage.TokenID != issued.TokenID || usage.CallerID != issued.Caller.ID || usage.CallerProject != "search" || usage.CallerEnvironment != "dev" || usage.CallerUser != "synthetic" {
			t.Fatalf("revoked usage attribution mismatch: caller=%s project=%s env=%s token=%s", usage.CallerID, usage.CallerProject, usage.CallerEnvironment, usage.TokenID)
		}
		var event securityAccessEvent
		if err := svc.usage.db.Table("security_access_events").Select("token_id, caller_id, caller_project, caller_environment").Where("request_id = ?", rid).First(&event).Error; err != nil {
			t.Fatal(err)
		}
		if event.TokenID != usage.TokenID || event.CallerID != usage.CallerID || event.CallerProject != usage.CallerProject || event.CallerEnvironment != usage.CallerEnvironment {
			t.Fatal("security and usage identity differ")
		}
		for _, admin := range []string{"scoped", "other"} {
			status, report, _ := request("GET", "/admin/reports/api/security/events?since=24h", nil, "", admin)
			if status != 200 {
				t.Fatalf("report %s status=%d body=%s", admin, status, report)
			}
			found := strings.Contains(string(report), rid)
			if found != (admin == "scoped") {
				t.Fatalf("report visibility %s found=%v", admin, found)
			}
		}
	}
	status, _, rid := request("GET", "/v1/models", nil, "garbage-token", "")
	if status != 401 {
		t.Fatalf("unknown=%d", status)
	}
	flushUsageForTest(t, svc)
	var usage usageRecord
	if err := svc.usage.db.Where("request_id = ?", rid).First(&usage).Error; err != nil {
		t.Fatal(err)
	}
	if usage.TokenID != "invalid-token" || usage.CallerID != "" || usage.CallerUser != "" || usage.CallerProject != "" || usage.CallerEnvironment != "" {
		t.Fatal("unknown token acquired identity")
	}
	for _, admin := range []string{"scoped", "other"} {
		status, report, _ := request("GET", "/admin/reports/api/security/events?since=24h", nil, "", admin)
		if status != 200 || strings.Contains(string(report), rid) {
			t.Fatalf("unknown crossed domain %s", admin)
		}
	}
	t.Log("localhost issue -> models 200 -> revoke -> models/chat 403; scoped report sees denials, other domain and unknown token isolated; usage public token identity preserved")
}

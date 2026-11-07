// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// ACP-009: the legacy machine admin routes stay on the serving port behind a
// flag, advertise Deprecation/Sunset, and disappear without touching the
// browser reports UI or the /admin/auth/* session routes.

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func containsInt(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func newLegacyAdminFixture(t *testing.T, legacy *bool, planeEnabled bool) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Server.AdminReports.Enabled = true
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	// The reports UI keeps its own browser login (Basic here). It is not part of
	// the control plane and is unaffected by the legacy-route flag.
	const basicHashEnv = "ROUTER_ADMIN_BASIC_HASH_LEGACY_FLAG_TEST"
	t.Setenv(basicHashEnv, mustBcryptHash(t, "browser-password"))
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{
		Enabled: true,
		Source:  "static",
		Policy: []string{
			"p, basic:browser-admin, local/test, admin:reports, read",
			"p, adminkey:legacy-flag, local/test, admin:reports, read",
		},
	}
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{
		Enabled:           true,
		Realm:             "Legacy Flag Test",
		AllowInsecureHTTP: true,
		Users: []AdminBasicAuthUser{{
			Username:        "browser-admin",
			PasswordHashEnv: basicHashEnv,
			Subject:         "basic:browser-admin",
			Domain:          "local/test",
			Permissions:     []string{"admin:reports:read"},
		}},
	}
	generated := generateTestAdminKey(t, "legacy-flag")
	t.Setenv(adminKeyTestEnvName("legacy-flag"), generated.TokenHash)
	cfg.Server.AdminAuth.APIKeys = []AdminAPIKeyConfig{{
		KeyID:       "legacy-flag",
		KeyHashEnv:  adminKeyTestEnvName("legacy-flag"),
		Domain:      "local/test",
		Permissions: []string{"config:read", "admin:reports:read"},
	}}
	cfg.Admin.Enabled = planeEnabled
	if planeEnabled {
		cfg.Admin.Listen = "127.0.0.1:0"
	}
	if legacy != nil {
		cfg.Admin.LegacyRoutesOnServingPort = legacy
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc, generated.Token
}

func servingRequest(t *testing.T, svc *Service, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	var body *strings.Reader = strings.NewReader("")
	req := httptest.NewRequest(method, path, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	svc.Handler().ServeHTTP(recorder, req)
	return recorder
}

func TestLegacyAdminRoutesCarryDeprecationHeaders(t *testing.T) {
	svc, token := newLegacyAdminFixture(t, nil, true)
	recorder := servingRequest(t, svc, http.MethodGet, "/admin/config/status", token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /admin/config/status = %d, want 200 (body %q)", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Deprecation"); got != "true" {
		t.Fatalf("Deprecation = %q, want true", got)
	}
	sunset := recorder.Header().Get("Sunset")
	parsed, err := http.ParseTime(sunset)
	if err != nil {
		t.Fatalf("Sunset %q is not an HTTP date: %v", sunset, err)
	}
	if parsed.Before(time.Date(2027, 2, 18, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("Sunset %s is before the 90-day floor", parsed)
	}
	if link := recorder.Header().Get("Link"); !strings.Contains(link, "</admin/v1/config/status>") || !strings.Contains(link, `rel="deprecation"`) {
		t.Fatalf("Link = %q, want the /admin/v1 replacement", link)
	}
	// The browser reports UI and the session routes are not control-plane
	// routes and never carry the legacy deprecation headers.
	reports := servingRequest(t, svc, http.MethodGet, "/admin/reports/", token)
	if reports.Code != http.StatusOK {
		t.Fatalf("GET /admin/reports/ = %d, want 200 (body %q)", reports.Code, reports.Body.String())
	}
	if got := reports.Header().Get("Deprecation"); got != "" {
		t.Fatalf("reports UI Deprecation = %q, want none", got)
	}
	me := servingRequest(t, svc, http.MethodGet, "/admin/auth/me", token)
	if me.Code != http.StatusOK {
		t.Fatalf("GET /admin/auth/me = %d, want 200", me.Code)
	}
}

func TestLegacyAdminFlagOffRemovesMachineRoutes(t *testing.T) {
	off := false
	svc, token := newLegacyAdminFixture(t, &off, true)
	for _, request := range []struct {
		method string
		path   string
		// The Go mux answers 405 when a path matches a GET-only pattern (the docs
		// handler) and the method differs. Both mean "not routed".
		allowed []int
	}{
		{http.MethodGet, "/admin/config/status", []int{http.StatusNotFound}},
		{http.MethodGet, "/admin/config/groups", []int{http.StatusNotFound}},
		{http.MethodGet, "/admin/callers", []int{http.StatusNotFound}},
		{http.MethodPost, "/admin/training/export", []int{http.StatusNotFound, http.StatusMethodNotAllowed}},
	} {
		recorder := servingRequest(t, svc, request.method, request.path, token)
		if !containsInt(request.allowed, recorder.Code) {
			t.Fatalf("%s %s = %d, want one of %v with the flag off (body %q)", request.method, request.path, recorder.Code, request.allowed, recorder.Body.String())
		}
		if got := recorder.Header().Get("Deprecation"); got != "" {
			t.Fatalf("%s %s Deprecation = %q, want none", request.method, request.path, got)
		}
	}
	// The browser reports UI and its login keep working: the UI is not part of
	// the control plane and stays on the serving port.
	reports := servingRequest(t, svc, http.MethodGet, "/admin/reports/", token)
	if reports.Code != http.StatusOK {
		t.Fatalf("GET /admin/reports/ with the flag off = %d, want 200", reports.Code)
	}
	me := servingRequest(t, svc, http.MethodGet, "/admin/auth/me", token)
	if me.Code != http.StatusOK {
		t.Fatalf("GET /admin/auth/me with the flag off = %d, want 200", me.Code)
	}
}

// ACP-005: a disabled plane with the legacy flag off leaves no admin handler on
// the serving port, and silently enables none.
func TestLegacyAdminFlagOffWithPlaneDisabled(t *testing.T) {
	off := false
	svc, token := newLegacyAdminFixture(t, &off, false)
	if svc.AdminHandler() != nil {
		t.Fatal("disabled plane built an admin handler")
	}
	if svc.AdminListenAddr() != "" {
		t.Fatalf("disabled plane listen address = %q, want empty", svc.AdminListenAddr())
	}
	for _, path := range []string{"/admin/config/status", "/admin/callers", "/admin/training/export"} {
		recorder := servingRequest(t, svc, http.MethodGet, path, token)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404", path, recorder.Code)
		}
	}
}

// The default flag value keeps the v4.7 legacy surface when the plane is off.
func TestLegacyAdminFlagDefaultsOn(t *testing.T) {
	svc, token := newLegacyAdminFixture(t, nil, false)
	recorder := servingRequest(t, svc, http.MethodGet, "/admin/config/status", token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /admin/config/status = %d, want 200 with the default flag (body %q)", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Deprecation"); got != "true" {
		t.Fatalf("Deprecation = %q, want true", got)
	}
}

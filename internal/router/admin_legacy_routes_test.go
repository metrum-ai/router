// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestLegacyMachineAdminRoutesRemovedButReportsRemain(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Server.AdminReports.Enabled = true
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	const passwordHashEnv = "ROUTER_ADMIN_BASIC_V5_ROUTE_TEST"
	t.Setenv(passwordHashEnv, mustBcryptHash(t, "test-password"))
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{
		Enabled:           true,
		AllowInsecureHTTP: true,
		Users: []AdminBasicAuthUser{{
			Username:        "browser-admin",
			PasswordHashEnv: passwordHashEnv,
			Subject:         "basic:browser-admin",
			Domain:          "local/test",
		}},
	}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{
		Enabled: true,
		Source:  "static",
		Policy:  []string{"p, basic:browser-admin, local/test, admin:reports, read"},
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(svc.Close)

	for _, path := range []string{"/admin/config/status", "/admin/config/groups", "/admin/callers", "/admin/training/export"} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions, "TRACE", "CONNECT"} {
			recorder := httptest.NewRecorder()
			svc.Handler().ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
			if recorder.Code != http.StatusNotFound {
				t.Errorf("%s %s = %d, want 404", method, path, recorder.Code)
			}
		}
	}
	reports := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/reports/", nil)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("browser-admin:test-password")))
	svc.Handler().ServeHTTP(reports, req)
	if reports.Code != http.StatusOK {
		t.Fatalf("reports UI status = %d, want 200 after legacy route removal", reports.Code)
	}
}

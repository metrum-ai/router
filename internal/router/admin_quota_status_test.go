// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminQuotaStatusAuthzAndFilters(t *testing.T) {
	hash := mustBcryptHash(t, "yell-yell-yum")
	t.Setenv("SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST", hash)
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Callers = append(cfg.Callers, CallerConfig{
		ID:          "bob",
		OwnerUser:   "bob",
		Project:     "other-project",
		Environment: "prod",
		TokenSHA256: strings.Repeat("ab", 32),
		TokenID:     "rtr_bob_prod",
		Allow:       []string{"default"},
		Rate:        RateConfig{RPM: 10, TPM: 1000, Concurrent: 1},
		Quota:       QuotaConfig{Day: BudgetConfig{Tokens: 5000}, SoftPct: 80},
		Key:         KeyConfig{LifetimeTokens: 9000, SoftPct: 90},
	})
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{
		Enabled:           true,
		Realm:             "Unit Test Admin",
		AllowInsecureHTTP: true,
		Users: []AdminBasicAuthUser{{
			Username:        "admin",
			PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST",
			Subject:         "basic:admin",
			Domain:          "metrum-insights/test",
		}, {
			Username:        "global",
			PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST",
			Subject:         "basic:global",
			Domain:          "metrum-insights/test",
		}},
	}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{
		Enabled: true,
		Policy: []string{
			"p, basic:admin, metrum-insights/test, admin:reports, read",
			"p, basic:global, *, admin:reports, read",
		},
	}
	cfg.Server.AdminReports = AdminReportsConfig{Enabled: true, DefaultSince: "24h", MaxRange: "31d", MaxRows: 10}
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	unauth := httptest.NewRequest(http.MethodGet, "/admin/reports/api/quota-status", nil)
	unauthRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(unauthRR, unauth)
	if unauthRR.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status=%d body=%s", unauthRR.Code, unauthRR.Body.String())
	}

	ordinary := httptest.NewRequest(http.MethodGet, "/admin/reports/api/quota-status", nil)
	ordinary.Header.Set("Authorization", "Bearer "+testToken)
	ordinaryRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(ordinaryRR, ordinary)
	if ordinaryRR.Code != http.StatusForbidden || !strings.Contains(ordinaryRR.Body.String(), "reports-forbidden") {
		t.Fatalf("ordinary status=%d body=%s", ordinaryRR.Code, ordinaryRR.Body.String())
	}

	scoped := httptest.NewRequest(http.MethodGet, "/admin/reports/api/quota-status", nil)
	scoped.SetBasicAuth("admin", "yell-yell-yum")
	scopedRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(scopedRR, scoped)
	if scopedRR.Code != http.StatusOK {
		t.Fatalf("scoped status=%d body=%s", scopedRR.Code, scopedRR.Body.String())
	}
	var scopedBody struct {
		Callers []map[string]any `json:"callers"`
		Count   int              `json:"count"`
	}
	if err := json.Unmarshal(scopedRR.Body.Bytes(), &scopedBody); err != nil {
		t.Fatal(err)
	}
	if scopedBody.Count != 1 || scopedBody.Callers[0]["caller_id"] != "alice" {
		t.Fatalf("scoped callers=%v", scopedBody.Callers)
	}
	if strings.Contains(scopedRR.Body.String(), "token_sha256") {
		t.Fatal("hash leaked in quota-status")
	}

	owner := httptest.NewRequest(http.MethodGet, "/admin/reports/api/quota-status?owner_user=alice", nil)
	owner.SetBasicAuth("global", "yell-yell-yum")
	ownerRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(ownerRR, owner)
	if ownerRR.Code != http.StatusOK {
		t.Fatalf("owner status=%d body=%s", ownerRR.Code, ownerRR.Body.String())
	}
	var ownerBody struct {
		Callers []map[string]any `json:"callers"`
		Count   int              `json:"count"`
	}
	if err := json.Unmarshal(ownerRR.Body.Bytes(), &ownerBody); err != nil {
		t.Fatal(err)
	}
	if ownerBody.Count != 1 || ownerBody.Callers[0]["owner_user"] != "alice" {
		t.Fatalf("owner filter=%v", ownerBody.Callers)
	}

	all := httptest.NewRequest(http.MethodGet, "/admin/reports/api/quota-status", nil)
	all.SetBasicAuth("global", "yell-yell-yum")
	allRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(allRR, all)
	if allRR.Code != http.StatusOK {
		t.Fatalf("global status=%d body=%s", allRR.Code, allRR.Body.String())
	}
	var allBody struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(allRR.Body.Bytes(), &allBody); err != nil {
		t.Fatal(err)
	}
	if allBody.Count != 2 {
		t.Fatalf("global count=%d", allBody.Count)
	}
}

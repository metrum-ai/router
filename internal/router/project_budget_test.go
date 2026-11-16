// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const projectBudgetQuotaToken = "rtr_project_budget_quota_e2e_token"

func TestProjectBudgetManagerThresholdTripAndWindowReset(t *testing.T) {
	m := newProjectBudgetManager()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	cfg := ProjectBudgetConfig{DailyUSD: 1, MonthlyUSD: 2, AlertThresholds: []int{50, 80}}
	m.add("p1", cfg, .6)
	if !m.allow("p1", cfg) {
		t.Fatal("budget tripped below ceiling")
	}
	m.add("p1", cfg, .5)
	if m.allow("p1", cfg) {
		t.Fatal("daily ceiling did not block the next request")
	}
	now = now.Add(24 * time.Hour)
	if !m.allow("p1", cfg) {
		t.Fatal("daily window did not reset")
	}
}

func TestProjectBudgetManagerRecoversWhenCeilingChanges(t *testing.T) {
	m := newProjectBudgetManager()
	m.now = func() time.Time { return time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC) }
	tripped := ProjectBudgetConfig{DailyUSD: 1}
	m.add("p1", tripped, 1.25)
	if m.allow("p1", tripped) {
		t.Fatal("budget should trip at its original ceiling")
	}
	if !m.allow("p1", ProjectBudgetConfig{DailyUSD: 2}) {
		t.Fatal("raising the ceiling should clear the trip immediately")
	}
	if !m.allow("p1", ProjectBudgetConfig{}) {
		t.Fatal("removing the ceiling should clear the trip immediately")
	}
}

func TestProjectBudgetHTTPEnforcementE2E(t *testing.T) {
	var logOutput bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&logOutput)
	t.Cleanup(func() { log.SetOutput(oldOutput) })
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "budget", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 1000, "completion_tokens": 0, "total_tokens": 1000}})
	}))
	defer provider.Close()
	dir := t.TempDir()
	cfg := testConfig(t, provider.URL, "budget-test-key", dir)
	cfg.Callers[0].Project = "local"
	cfg.Callers[0].Environment = "prod"
	cfg.Callers[0].OwnerUser = "alice"
	cfg.Callers = append(cfg.Callers, CallerConfig{ID: "bob", OwnerUser: "bob", Project: "local", Environment: "prod", Status: "active", TokenSHA256: legacyCallerTokenSHA256Hex(projectBudgetQuotaToken), TokenID: "rtr_project_budget_quota_e2e", Allow: []string{"default"}})
	cfg.Users = []UserConfig{{ID: "alice", Status: "active", Type: "human"}}
	cfg.Users = append(cfg.Users, UserConfig{ID: "bob", Status: "active", Type: "human"})
	cfg.Projects = []ProjectConfig{{ID: "local", Status: "active", Environments: []ProjectEnvironmentConfig{{ID: "prod", Status: "active"}}}}
	cfg.ProjectMemberships = []ProjectMembershipConfig{{UserID: "alice", Project: "local", Environment: "prod", Role: "member", Status: "active"}}
	cfg.ProjectMemberships = append(cfg.ProjectMemberships, ProjectMembershipConfig{UserID: "bob", Project: "local", Environment: "prod", Role: "member", Status: "active"})
	group := cfg.Models["default"]
	group.Targets[0].InputPricePerMillionUSD = 1000
	cfg.Models["default"] = group
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	configDBPath := filepath.Join(dir, "config.sqlite")
	runner, closeConfigDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: configDBPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeConfigDB() })
	applyConfigControlPlaneMigrationsForTest(t, runner)
	cfg.Server.ConfigSource = ConfigSourceConfig{Mode: "database", RuntimeScope: "prod", Driver: "sqlite", Path: configDBPath}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Source: "static", Policy: []string{
		"p, adminkey:budget-test, *, admin:projects, write",
		"p, adminkey:budget-test, *, admin:budgets, write",
		"p, adminkey:budget-test, *, admin:quotas, read",
		"p, adminkey:budget-test, *, admin:quotas, write",
		"p, adminkey:budget-test, local/prod, admin:memberships, write",
	}}
	canonical, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	setID, err := ImportCanonicalYAML(runner.db, "prod", "budget-e2e", canonical, "test-seed")
	if err != nil {
		t.Fatal(err)
	}
	if err := ActivateConfigSet(runner.db, "prod", setID, "test-seed"); err != nil {
		t.Fatal(err)
	}
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	adminMux := http.NewServeMux()
	fakeAdminAuth := &fakeAuthenticator{principal: Principal{Subject: "adminkey:budget-test", Kind: "adminkey", CredentialID: "budget-test"}}
	allowAdmin := func(context.Context, Principal, string, string, string) error { return nil }
	if err := svc.MountAdminV1(adminMux, fakeAdminAuth, allowAdmin); err != nil {
		t.Fatal(err)
	}
	revision, err := currentRevisionForScope(svc.configControlPlane, "prod")
	if err != nil {
		t.Fatal(err)
	}
	budgetBody := `{"name":"Local","status":"active","environments":[{"id":"prod","status":"active"}],"budget":{"daily_usd":0.5,"alert_thresholds":[50]}}`
	adminRequest := httptest.NewRequest(http.MethodPut, "/admin/v1/projects/local", strings.NewReader(budgetBody))
	adminRequest.Header.Set("If-Match", AdminETagForRevision(revision))
	adminRequest.Header.Set("Content-Type", "application/json")
	adminResponse := httptest.NewRecorder()
	adminMux.ServeHTTP(adminResponse, adminRequest)
	if adminResponse.Code != http.StatusOK {
		t.Fatalf("admin project budget write status=%d body=%s", adminResponse.Code, adminResponse.Body.String())
	}
	revision, err = currentRevisionForScope(svc.configControlPlane, "prod")
	if err != nil {
		t.Fatal(err)
	}
	membershipWrite := httptest.NewRequest(http.MethodPost, "/admin/v1/projects/LOCAL/environments/PROD/memberships", strings.NewReader(`{"user_id":"bob","role":"developer"}`))
	membershipWrite.Header.Set("If-Match", AdminETagForRevision(revision))
	membershipWrite.Header.Set("Content-Type", "application/json")
	membershipResponse := httptest.NewRecorder()
	adminMux.ServeHTTP(membershipResponse, membershipWrite)
	if membershipResponse.Code != http.StatusCreated {
		t.Fatalf("mixed-case membership write status=%d body=%s", membershipResponse.Code, membershipResponse.Body.String())
	}
	router := httptest.NewServer(svc.Handler())
	defer router.Close()
	post := func(token string) int {
		req, _ := http.NewRequest(http.MethodPost, router.URL+"/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hello"}]}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	if status := post(testToken); status != http.StatusOK {
		t.Fatalf("first request status=%d", status)
	}
	if status := post(testToken); status != http.StatusServiceUnavailable {
		t.Fatalf("request after ceiling status=%d, want 503", status)
	}
	if !strings.Contains(logOutput.String(), "event=project_budget action=threshold project=\"local\"") || !strings.Contains(logOutput.String(), "action=trip project=\"local\"") {
		t.Fatalf("missing budget threshold/trip events: %s", logOutput.String())
	}
	if strings.Contains(logOutput.String(), "budget-test-key") || strings.Contains(logOutput.String(), "hello") {
		t.Fatal("budget events included provider credentials or prompt text")
	}
	// Remove the budget through the same API and prove the serving config recovers.
	revision, err = currentRevisionForScope(svc.configControlPlane, "prod")
	if err != nil {
		t.Fatal(err)
	}
	removeBudget := httptest.NewRequest(http.MethodPut, "/admin/v1/projects/local", strings.NewReader(`{"name":"Local","status":"active","environments":[{"id":"prod","status":"active"}],"budget":{}}`))
	removeBudget.Header.Set("If-Match", AdminETagForRevision(revision))
	removeBudget.Header.Set("Content-Type", "application/json")
	removed := httptest.NewRecorder()
	adminMux.ServeHTTP(removed, removeBudget)
	if removed.Code != http.StatusOK {
		t.Fatalf("remove budget status=%d body=%s", removed.Code, removed.Body.String())
	}
	revision, err = currentRevisionForScope(svc.configControlPlane, "prod")
	if err != nil {
		t.Fatal(err)
	}
	quotaWrite := httptest.NewRequest(http.MethodPost, "/admin/v1/callers/bob/quota", strings.NewReader(`{"quota":{"day":{"requests":1}}}`))
	quotaWrite.Header.Set("If-Match", AdminETagForRevision(revision))
	quotaWrite.Header.Set("Content-Type", "application/json")
	quotaResponse := httptest.NewRecorder()
	adminMux.ServeHTTP(quotaResponse, quotaWrite)
	if quotaResponse.Code != http.StatusOK {
		t.Fatalf("admin quota write status=%d body=%s", quotaResponse.Code, quotaResponse.Body.String())
	}
	quotaRead := httptest.NewRequest(http.MethodGet, "/admin/v1/callers/bob/quota", nil)
	quotaReadResponse := httptest.NewRecorder()
	adminMux.ServeHTTP(quotaReadResponse, quotaRead)
	if quotaReadResponse.Code != http.StatusOK || !strings.Contains(quotaReadResponse.Body.String(), `"requests":1`) {
		t.Fatalf("admin quota read status=%d body=%s", quotaReadResponse.Code, quotaReadResponse.Body.String())
	}
	if status := post(projectBudgetQuotaToken); status != http.StatusOK {
		t.Fatalf("first request after admin quota write status=%d", status)
	}
	if status := post(projectBudgetQuotaToken); status != http.StatusTooManyRequests {
		t.Fatalf("request over API-written quota status=%d, want 429", status)
	}
}

func TestProjectBudgetValidation(t *testing.T) {
	for _, cfg := range []ProjectBudgetConfig{{DailyUSD: -1}, {MonthlyUSD: 1, AlertThresholds: []int{101}}, {DailyUSD: 1, AlertThresholds: []int{50, 50}}} {
		base := minimalConfig(t)
		base.Projects = []ProjectConfig{{ID: "p1", Budget: cfg}}
		if err := base.validate(false); err == nil {
			t.Fatalf("accepted invalid budget: %+v", cfg)
		}
	}
}

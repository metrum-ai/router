// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestAdminResourceProviderListRedactsCredential(t *testing.T) {
	activeConfig := &Config{}
	if err := yamlUnmarshalForTest(configAdminTestYAML, activeConfig); err != nil {
		t.Fatal(err)
	}
	activeConfig.setDefaults()
	provider := activeConfig.Provider["alpha"]
	provider.APIKey = "must-not-return"
	provider.APIKeyEnv = "PROVIDER_KEY"
	activeConfig.Provider["alpha"] = provider
	deps := ConfigAdminDeps{
		Authenticate: func(http.ResponseWriter, *http.Request) (adminAuthSubject, bool) {
			return adminAuthSubject{subject: "admin"}, true
		},
		Authorize: func(adminAuthSubject, string) bool { return true },
		CurrentConfig: func() *Config {
			return activeConfig
		},
	}
	handler := newConfigAdminHandler(deps)
	response := httptest.NewRecorder()
	handler.handleListProviders(response, httptest.NewRequest("GET", "/admin/v1/providers", nil))
	if response.Code != 200 || strings.Contains(response.Body.String(), "must-not-return") || strings.Contains(response.Body.String(), "APIKey") || strings.Contains(response.Body.String(), "PROVIDER_KEY") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	catalog := httptest.NewRecorder()
	handler.handleListModelCatalog(catalog, httptest.NewRequest("GET", "/admin/v1/models", nil))
	if catalog.Code != 200 || strings.Contains(catalog.Body.String(), "must-not-return") || strings.Contains(catalog.Body.String(), "APIKey") || !strings.Contains(catalog.Body.String(), `"dialect":"openai"`) {
		t.Fatalf("catalog status=%d body=%s", catalog.Code, catalog.Body.String())
	}
	validBody := map[string]any{"base_url": "https://provider.example/v1", "dialect": "openai", "api_key_env": "PROVIDER_KEY", "models": map[string]any{"model-1": map[string]any{"model": "actual-model"}}}
	validation := invokeAdminResource(t, handler, "POST", validBody, map[string]string{"X-Test-Admin-Subject": "admin", "X-Test-Admin-Perms": adminPermissionProvidersWrite}, func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("provider", "gamma")
		handler.handleValidateProvider(w, r)
	})
	if validation.Code != http.StatusOK || !strings.Contains(validation.Body.String(), `"valid":true`) {
		t.Fatalf("validation status=%d body=%s", validation.Code, validation.Body.String())
	}
	invalidBody := map[string]any{"base_url": "https://provider.example/v1", "dialect": "not-a-dialect", "models": map[string]any{"model-1": map[string]any{"model": "actual-model", "dialect": "invalid"}}}
	validation = invokeAdminResource(t, handler, "POST", invalidBody, map[string]string{"X-Test-Admin-Subject": "admin", "X-Test-Admin-Perms": adminPermissionProvidersWrite}, func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("provider", "gamma")
		handler.handleValidateProvider(w, r)
	})
	if validation.Code != http.StatusUnprocessableEntity || activeConfig.Provider["alpha"].Dialect != "openai" || activeConfig.Provider["gamma"].Dialect != "" {
		t.Fatalf("invalid validation status=%d active=%+v body=%s", validation.Code, activeConfig.Provider["alpha"], validation.Body.String())
	}
}

func TestConfigAdminProviderWriteUsesRevisionAndNeverReturnsCredentials(t *testing.T) {
	db := configAdminTestDB(t)
	handler := adminResourceTestHandler(db)
	admin := adminHeaders("adminkey:ops", adminPermissionProvidersRead, adminPermissionProvidersWrite)
	revision, err := currentRevisionForScope(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	admin["If-Match"] = strconv.FormatInt(revision, 10)
	body := map[string]any{
		"base_url": "https://gamma.example/v1", "dialect": "openai", "api_key_env": "GAMMA_API_KEY",
		"models": map[string]any{"gamma-model": map[string]any{"model": "gamma-model"}},
	}
	response := invokeAdminResource(t, handler, "PUT", body, admin, func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("provider", "gamma")
		handler.handlePutProvider(w, r)
	})
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "GAMMA_API_KEY") || strings.Contains(response.Body.String(), "api_key_env") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	raw, err := LoadActiveCanonicalYAML(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "api_key_env: GAMMA_API_KEY") || !strings.Contains(string(raw), "api_key_env: ALPHA_API_KEY") {
		t.Fatalf("config update lost credential references: %s", raw)
	}
	list := invokeAdminResource(t, handler, "GET", nil, admin, handler.handleListProviders)
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), "APIKey") || strings.Contains(list.Body.String(), "api_key_env") || strings.Contains(list.Body.String(), "GAMMA_API_KEY") {
		t.Fatalf("status=%d body=%s", list.Code, list.Body.String())
	}
}

func TestConfigAdminCallerQuotaWriteActivatesRevision(t *testing.T) {
	db := configAdminTestDB(t)
	reloadCalls := 0
	handler := adminResourceTestHandlerWithReload(db, func() error { reloadCalls++; return nil })
	admin := adminHeaders("adminkey:ops", adminPermissionQuotasWrite)
	revision, err := currentRevisionForScope(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	admin["If-Match"] = strconv.FormatInt(revision, 10)
	body := map[string]any{"quota": map[string]any{"day": map[string]any{"requests": 2, "tokens": 100}, "soft_pct": 75}}
	response := invokeAdminResource(t, handler, "POST", body, admin, func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("id", "alice")
		handler.handleUpdateCallerQuota(w, r)
	})
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	cfg, err := LoadActiveCanonicalConfig(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Callers[0].Quota.Day.Requests != 2 || cfg.Callers[0].Quota.Day.Tokens != 100 || cfg.Callers[0].Quota.SoftPct != 75 {
		t.Fatalf("quota not activated: %+v", cfg.Callers[0].Quota)
	}
	if reloadCalls != 1 {
		t.Fatalf("reload callback calls=%d, want 1", reloadCalls)
	}
}

func TestConfigAdminQuotaWriteReportsReloadFailureAfterCommit(t *testing.T) {
	db := configAdminTestDB(t)
	handler := adminResourceTestHandlerWithReload(db, func() error { return errors.New("reload timeout") })
	revision, err := currentRevisionForScope(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	headers := adminHeaders("adminkey:ops", adminPermissionQuotasWrite)
	headers["If-Match"] = strconv.FormatInt(revision, 10)
	body := map[string]any{"quota": map[string]any{"day": map[string]any{"requests": 2, "tokens": 100}}}
	response := invokeAdminResource(t, handler, "POST", body, headers, func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("id", "alice")
		handler.handleUpdateCallerQuota(w, r)
	})
	if response.Code != http.StatusServiceUnavailable || decodeErrorType(t, response) != "config-reload-failed" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	updatedRevision, err := currentRevisionForScope(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if updatedRevision != revision+1 {
		t.Fatalf("revision=%d, want committed revision %d after reload failure", updatedRevision, revision+1)
	}
}

func TestConfigAdminProviderWriteRejectsInvalidDialect(t *testing.T) {
	db := configAdminTestDB(t)
	handler := adminResourceTestHandler(db)
	revision, err := currentRevisionForScope(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	headers := adminHeaders("adminkey:ops", adminPermissionProvidersWrite)
	headers["If-Match"] = strconv.FormatInt(revision, 10)
	body := map[string]any{"base_url": "https://gamma.example/v1", "dialect": "unknown"}
	response := invokeAdminResource(t, handler, "PUT", body, headers, func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("provider", "gamma")
		handler.handlePutProvider(w, r)
	})
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	updatedRevision, err := currentRevisionForScope(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if updatedRevision != revision {
		t.Fatalf("invalid provider write changed revision %d to %d", revision, updatedRevision)
	}
}

func TestConfigAdminProviderUpdatePreservesSpendBreaker(t *testing.T) {
	db := configAdminTestDB(t)
	revision, err := currentRevisionForScope(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeAdminConfigRevision(db, "prod", "test-admin", revision, testAdminAuditMeta(), "test-provider-breaker", time.Now().UTC(), func(cfg *Config) error {
		provider := cfg.Provider["alpha"]
		provider.SpendBreaker = ProviderSpendBreakerConfig{HourlyUSD: 12, DailyUSD: 40, OnTrip: "stop"}
		provider.TrafficShape = TrafficShapeConfig{Enabled: boolPtr(true), RequestStartPerSec: 1, RequestBurst: 7}
		cfg.Provider["alpha"] = provider
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	revision, err = currentRevisionForScope(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	headers := adminHeaders("adminkey:ops", adminPermissionProvidersWrite)
	headers["If-Match"] = strconv.FormatInt(revision, 10)
	active, err := LoadActiveCanonicalConfig(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	currentProvider := active.Provider["alpha"]
	body := adminProviderWriteRequest{BaseURL: currentProvider.BaseURL, Dialect: currentProvider.Dialect, UpstreamKind: currentProvider.UpstreamKind, APIKeyEnv: currentProvider.APIKeyEnv, KeyID: currentProvider.KeyID, AuthScheme: currentProvider.AuthScheme, Headers: currentProvider.Headers, Models: currentProvider.Models}
	handler := adminResourceTestHandler(db)
	response := invokeAdminResource(t, handler, "PUT", body, headers, func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("provider", "alpha")
		handler.handlePutProvider(w, r)
	})
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	cfg, err := LoadActiveCanonicalConfig(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	provider := cfg.Provider["alpha"]
	if provider.SpendBreaker.HourlyUSD != 12 || provider.SpendBreaker.DailyUSD != 40 || provider.SpendBreaker.OnTrip != "stop" {
		t.Fatalf("provider update dropped spend breaker: %+v", provider.SpendBreaker)
	}
	if provider.TrafficShape.Enabled == nil || !*provider.TrafficShape.Enabled || provider.TrafficShape.RequestBurst != 7 {
		t.Fatalf("provider update dropped traffic shaping: %+v", provider.TrafficShape)
	}
}

func TestConfigAdminProviderDisableRejectsReferencedProvider(t *testing.T) {
	db := configAdminTestDB(t)
	handler := adminResourceTestHandler(db)
	revision, err := currentRevisionForScope(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	headers := adminHeaders("adminkey:ops", adminPermissionProvidersWrite)
	headers["If-Match"] = strconv.FormatInt(revision, 10)
	response := invokeAdminResource(t, handler, "DELETE", nil, headers, func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("provider", "alpha")
		handler.handleDisableProvider(w, r)
	})
	if response.Code != http.StatusConflict || decodeErrorType(t, response) != "provider-in-use" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	updatedRevision, err := currentRevisionForScope(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if updatedRevision != revision {
		t.Fatalf("rejected disable changed revision %d to %d", revision, updatedRevision)
	}
	response = invokeAdminResource(t, handler, "DELETE", nil, headers, func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("provider", "beta")
		handler.handleDisableProvider(w, r)
	})
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"disabled"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	cfg, err := LoadActiveCanonicalConfig(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if _, found := cfg.Provider["beta"]; found {
		t.Fatal("disabled provider remains active")
	}
}

func TestConfigAdminRetentionShorteningNeedsDestructivePermission(t *testing.T) {
	db := configAdminTestDB(t)
	revision, err := currentRevisionForScope(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	retention := RetentionConfig{Enabled: true, DefaultBatchSize: 10, Classes: []RetentionClassConfig{{DataClass: retentionDataClassUsageDiagnostics, RetentionDays: 90, BatchSize: 10}}}
	if _, err := writeAdminConfigRevision(db, "prod", "test-admin", revision, testAdminAuditMeta(), "test-retention", time.Now().UTC(), func(cfg *Config) error {
		cfg.Server.Retention = retention
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	revision, err = currentRevisionForScope(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	handler := adminResourceTestHandler(db)
	headers := adminHeaders("adminkey:ops", adminPermissionRetentionWrite)
	headers["If-Match"] = strconv.FormatInt(revision, 10)
	shorter := RetentionConfig{Enabled: true, DefaultBatchSize: 10, Classes: []RetentionClassConfig{{DataClass: retentionDataClassUsageDiagnostics, RetentionDays: 30, BatchSize: 10}}}
	response := invokeAdminResource(t, handler, "PUT", map[string]any{"retention": shorter}, headers, handler.handlePutRetention)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	updatedRevision, err := currentRevisionForScope(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if updatedRevision != revision {
		t.Fatalf("rejected retention write changed revision %d to %d", revision, updatedRevision)
	}
	shortHeaders := adminHeaders("adminkey:ops", adminPermissionRetentionWrite, adminPermissionRetentionForce)
	shortHeaders["If-Match"] = strconv.FormatInt(revision, 10)
	response = invokeAdminResource(t, handler, "PUT", map[string]any{"retention": shorter, "force": true}, shortHeaders, handler.handlePutRetention)
	if response.Code != http.StatusOK {
		t.Fatalf("authorized shortening status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRetentionPolicyShortenedDetectsNewDeletionAuthority(t *testing.T) {
	dryRun := false
	classOff := false
	classOn := true
	base := RetentionConfig{Enabled: true, DryRun: boolPtr(true), Classes: []RetentionClassConfig{{DataClass: retentionDataClassUsageDiagnostics, RetentionDays: 90}}}
	cases := []struct {
		name   string
		before RetentionConfig
		after  RetentionConfig
	}{
		{"global enable", RetentionConfig{DryRun: boolPtr(false)}, RetentionConfig{Enabled: true, DryRun: boolPtr(false), Classes: base.Classes}},
		{"dry run disabled", base, RetentionConfig{Enabled: true, DryRun: &dryRun, Classes: base.Classes}},
		{"class enabled", RetentionConfig{Enabled: true, DryRun: &dryRun, Classes: []RetentionClassConfig{{DataClass: retentionDataClassUsageDiagnostics, Enabled: &classOff, RetentionDays: 90}}}, RetentionConfig{Enabled: true, DryRun: &dryRun, Classes: []RetentionClassConfig{{DataClass: retentionDataClassUsageDiagnostics, Enabled: &classOn, RetentionDays: 90}}}},
		{"first class added", RetentionConfig{Enabled: true, DryRun: &dryRun}, RetentionConfig{Enabled: true, DryRun: &dryRun, Classes: base.Classes}},
		{"class removed", base, RetentionConfig{Enabled: true, DryRun: &dryRun}},
		{"existing class shortened", base, RetentionConfig{Enabled: true, DryRun: boolPtr(true), Classes: []RetentionClassConfig{{DataClass: retentionDataClassUsageDiagnostics, RetentionDays: 30}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !retentionPolicyShortened(tc.before, tc.after) {
				t.Fatal("deletion-increasing change was not classified as shortening")
			}
		})
	}
	if retentionPolicyShortened(base, RetentionConfig{Enabled: true, DryRun: boolPtr(true), Classes: []RetentionClassConfig{{DataClass: retentionDataClassUsageDiagnostics, RetentionDays: 120}}}) {
		t.Fatal("increasing retention period should not require destructive permission")
	}
}

func TestConfigAdminProjectEnvironmentAndMembershipLifecycle(t *testing.T) {
	db := configAdminTestDB(t)
	handler := adminResourceTestHandler(db)
	admin := adminHeaders("adminkey:ops", adminPermissionProjectsWrite, adminPermissionMembershipsWrite)
	revision, err := currentRevisionForScope(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	admin["If-Match"] = strconv.FormatInt(revision, 10)
	projectBody := map[string]any{"name": "Search", "status": "active", "environments": []any{
		map[string]any{"id": "dev", "status": "active"}, map[string]any{"id": "prod", "status": "active"},
	}}
	response := invokeAdminResource(t, handler, "PUT", projectBody, admin, func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("project", "search")
		handler.handlePutProject(w, r)
	})
	if response.Code != http.StatusOK {
		t.Fatalf("project write status=%d body=%s", response.Code, response.Body.String())
	}
	domainRequest := httptest.NewRequest("POST", "/admin/v1/projects/SEARCH/environments/PROD/memberships", nil)
	domainRequest.SetPathValue("project", "SEARCH")
	domainRequest.SetPathValue("environment", "PROD")
	domain, err := AdminV1CallerResourceDomain(db, "prod", domainRequest)
	if err != nil || domain != "search/prod" {
		t.Fatalf("created project environment domain=%q err=%v", domain, err)
	}
	revision, err = currentRevisionForScope(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	admin["If-Match"] = strconv.FormatInt(revision, 10)
	membershipBody := map[string]any{"user_id": "alice", "role": "operator", "status": "active"}
	response = invokeAdminResource(t, handler, "POST", membershipBody, admin, func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("project", "SEARCH")
		r.SetPathValue("environment", "PROD")
		handler.handlePutMembership(w, r)
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("membership write status=%d body=%s", response.Code, response.Body.String())
	}
	cfg, err := LoadActiveCanonicalConfig(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if !projectHasEnvironment(cfg.Projects[0], "prod") || len(cfg.ProjectMemberships) != 2 {
		t.Fatalf("project environments or membership missing: project=%+v memberships=%+v", cfg.Projects[0], cfg.ProjectMemberships)
	}
	var joinedAt string
	for _, membership := range cfg.ProjectMemberships {
		if membership.UserID == "alice" && membership.Project == "search" && membership.Environment == "prod" {
			joinedAt = membership.JoinedAt
		}
	}
	if joinedAt == "" {
		t.Fatal("membership write did not retain joined_at")
	}
	revision, err = currentRevisionForScope(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	admin["If-Match"] = strconv.FormatInt(revision, 10)
	membershipBody["role"] = "developer"
	response = invokeAdminResource(t, handler, "POST", membershipBody, admin, func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("project", "SEARCH")
		r.SetPathValue("environment", "PROD")
		handler.handlePutMembership(w, r)
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("membership upsert status=%d body=%s", response.Code, response.Body.String())
	}
	cfg, err = LoadActiveCanonicalConfig(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	for _, membership := range cfg.ProjectMemberships {
		if membership.UserID == "alice" && membership.Project == "search" && membership.Environment == "prod" && membership.JoinedAt != joinedAt {
			t.Fatalf("membership upsert changed joined_at from %q to %q", joinedAt, membership.JoinedAt)
		}
	}
}

func TestProjectUpdatePreservesLongLivedKeyRestriction(t *testing.T) {
	canonicalYAML := configAdminTestYAML + `
projects:
  - id: search
    name: Search
    status: active
    forbid_long_lived_keys: true
    environments:
      - id: dev
        status: active
users:
  - id: alice
    name: Alice
    type: human
    status: active
project_memberships:
  - user_id: alice
    project: search
    role: member
    status: active
`
	db := configAdminTestDBWithYAML(t, canonicalYAML)
	service := adminOperationsTestService(t, db, "project-key-restriction")
	if err := service.reloadActiveConfigFromDB(); err != nil {
		t.Fatalf("load restricted project: %v", err)
	}
	handler := adminResourceTestHandlerWithReload(db, service.reloadActiveConfigFromDB)
	revision, err := currentRevisionForScope(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	requestHeaders := adminHeaders("adminkey:ops", adminPermissionProjectsWrite)
	requestHeaders["If-Match"] = strconv.FormatInt(revision, 10)
	response := invokeAdminResource(t, handler, http.MethodPut, map[string]any{
		"name": "Search updated", "status": "active",
		"environments": []any{map[string]any{"id": "dev", "status": "active"}},
	}, requestHeaders, func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("project", "search")
		handler.handlePutProject(w, r)
	})
	if response.Code != http.StatusOK {
		t.Fatalf("project update status=%d body=%s", response.Code, response.Body.String())
	}
	if !projectForbidsLongLivedKeys(service.cfg, "search") {
		t.Fatal("project update removed the long-lived-key restriction from the reloaded config")
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"agent-default","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer "+configAdminTestToken)
	served := httptest.NewRecorder()
	service.Handler().ServeHTTP(served, request)
	if served.Code != http.StatusForbidden || !strings.Contains(served.Body.String(), "long-lived-key-forbidden") {
		t.Fatalf("static caller after project update status=%d body=%s, want forbidden", served.Code, served.Body.String())
	}
}

func adminResourceTestHandler(db *gorm.DB) *configAdminHandler {
	return adminResourceTestHandlerWithReload(db, nil)
}

func adminResourceTestHandlerWithReload(db *gorm.DB, reload func() error) *configAdminHandler {
	return newConfigAdminHandler(ConfigAdminDeps{
		DB: db, RuntimeScope: "prod", Driver: "sqlite", Now: func() time.Time { return time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC) },
		Reload: reload,
		Authenticate: func(_ http.ResponseWriter, r *http.Request) (adminAuthSubject, bool) {
			permissions := map[string]bool{}
			for _, permission := range strings.Split(r.Header.Get("X-Test-Admin-Perms"), ",") {
				permissions[strings.TrimSpace(permission)] = true
			}
			return adminAuthSubject{subject: "adminkey:ops", domain: AdminGlobalDomain, source: "admin_key", permissions: permissions}, true
		},
		Authorize: func(subject adminAuthSubject, permission string) bool { return subject.permissions[permission] },
	})
}

func invokeAdminResource(t *testing.T, handler *configAdminHandler, method string, body any, headers map[string]string, call func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, "/admin/v1/test", reader)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	call(response, request)
	return response
}

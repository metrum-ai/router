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

// adminKeyTestToken is the raw admin key material for this test file only.
// It is generated per run; the config carries the PBKDF2 verifier via env.
func generateTestAdminKey(t *testing.T, keyID string) GeneratedAdminAPIKey {
	t.Helper()
	generated, err := GenerateAdminAPIKey(keyID, nil, callerTokenHashMinIterations)
	if err != nil {
		t.Fatal(err)
	}
	return generated
}

func adminKeyTestEnvName(keyID string) string {
	return "ROUTER_ADMIN_KEY_" + strings.ToUpper(strings.ReplaceAll(keyID, "-", "_")) + "_HASH"
}

// newAdminKeyService builds a service with one admin API key configured and
// no browser-admin mechanism, proving admin keys stand alone (#350).
func newAdminKeyService(t *testing.T, keys []AdminAPIKeyConfig) (*Service, map[string]GeneratedAdminAPIKey) {
	t.Helper()
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	generated := map[string]GeneratedAdminAPIKey{}
	for i := range keys {
		gen := generateTestAdminKey(t, keys[i].KeyID)
		t.Setenv(adminKeyTestEnvName(keys[i].KeyID), gen.TokenHash)
		keys[i].KeyHashEnv = adminKeyTestEnvName(keys[i].KeyID)
		generated[keys[i].KeyID] = gen
	}
	cfg.Server.AdminAuth.APIKeys = keys
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc, generated
}

func adminKeyMeRequest(t *testing.T, svc *Service, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin/auth/me", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	return rr
}

func TestGenerateAdminAPIKeyRoundTrip(t *testing.T) {
	gen := generateTestAdminKey(t, "ops-primary")
	if !strings.HasPrefix(gen.Token, "rtr_admin_ops-primary_") {
		t.Fatalf("token shape: %q", gen.Token[:20])
	}
	keyID, ok := parseAdminAPIKeyToken(gen.Token)
	if !ok || keyID != "ops-primary" {
		t.Fatalf("parse keyID=%q ok=%v", keyID, ok)
	}
	matched, err := VerifyCallerTokenHash(gen.TokenHash, gen.Token)
	if err != nil || !matched {
		t.Fatalf("verify matched=%v err=%v", matched, err)
	}
	wrong := gen.Token[:len(gen.Token)-2] + "zz"
	matched, err = VerifyCallerTokenHash(gen.TokenHash, wrong)
	if err == nil && matched {
		t.Fatal("wrong secret verified")
	}
}

func TestParseAdminAPIKeyTokenRejectsOtherCredentialClasses(t *testing.T) {
	cases := []string{
		"",
		"rtr_metrum_alice_search_dev_k20260101_secretsecret", // caller token
		"sk-liveproviderkey0123456789",                       // provider key shape
		"rtr_admin_",                                         // no key id or secret
		"rtr_admin_ops",                                      // no secret
		"rtr_admin_ops_",                                     // empty secret
		"rtr_admin_OPS_secretsecret",                         // uppercase key id
		"rtr_admin_op_s_key_secret",                          // underscore in key id would mis-split
		"rtr_admin_ops_short",                                // secret too short
		"basic dXNlcjpwYXNz",                                 // basic material
	}
	for _, token := range cases {
		if keyID, ok := parseAdminAPIKeyToken(token); ok {
			t.Fatalf("token %q parsed as admin key %q", token, keyID)
		}
	}
}

func TestAdminKeyValidatesConfig(t *testing.T) {
	gen := generateTestAdminKey(t, "ops-primary")
	t.Setenv(adminKeyTestEnvName("ops-primary"), gen.TokenHash)
	base := func() *Config {
		dir := t.TempDir()
		cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
		return cfg
	}
	valid := AdminAPIKeyConfig{KeyID: "ops-primary", KeyHashEnv: adminKeyTestEnvName("ops-primary"), Domain: "local/test", Permissions: []string{"config:read"}}

	cfg := base()
	cfg.Server.AdminAuth.APIKeys = []AdminAPIKeyConfig{valid}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	cfg = base()
	dup := valid
	dup.Subject = "adminkey:second"
	cfg.Server.AdminAuth.APIKeys = []AdminAPIKeyConfig{valid, dup}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate key_id") {
		t.Fatalf("duplicate key_id: %v", err)
	}

	cfg = base()
	bad := valid
	bad.Permissions = []string{"config:everything"}
	cfg.Server.AdminAuth.APIKeys = []AdminAPIKeyConfig{bad}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "not a recognized admin permission") {
		t.Fatalf("unknown permission: %v", err)
	}

	cfg = base()
	noEnv := valid
	noEnv.KeyHashEnv = "ROUTER_ADMIN_KEY_UNSET_HASH"
	cfg.Server.AdminAuth.APIKeys = []AdminAPIKeyConfig{noEnv}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "is not set") {
		t.Fatalf("unset hash env: %v", err)
	}

	cfg = base()
	disabled := valid
	disabled.Status = "disabled"
	disabled.KeyHashEnv = "ROUTER_ADMIN_KEY_UNSET_HASH"
	cfg.Server.AdminAuth.APIKeys = []AdminAPIKeyConfig{disabled}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("disabled key without env should pass validation: %v", err)
	}

	cfg = base()
	emptyPerms := valid
	emptyPerms.Permissions = nil
	cfg.Server.AdminAuth.APIKeys = []AdminAPIKeyConfig{emptyPerms}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "at least one permission") {
		t.Fatalf("empty permissions: %v", err)
	}
}

func TestAdminKeyAuthenticatesAdminRoutes(t *testing.T) {
	svc, generated := newAdminKeyService(t, []AdminAPIKeyConfig{
		{KeyID: "ops-primary", Domain: "local/test", Permissions: []string{"config:read", "admin:auth:read"}},
	})
	token := generated["ops-primary"].Token

	// No credentials: 401.
	if rr := adminKeyMeRequest(t, svc, ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("no token status=%d body=%s", rr.Code, rr.Body.String())
	}
	// Caller key: rejected, never confused with an admin credential.
	if rr := adminKeyMeRequest(t, svc, "Bearer "+testToken); rr.Code != http.StatusUnauthorized {
		t.Fatalf("caller token status=%d body=%s", rr.Code, rr.Body.String())
	}
	// Provider-key-shaped bearer: rejected.
	if rr := adminKeyMeRequest(t, svc, "Bearer sk-providerkey0123456789abcdef"); rr.Code != http.StatusUnauthorized {
		t.Fatalf("provider token status=%d body=%s", rr.Code, rr.Body.String())
	}
	// Admin-key-shaped but wrong secret: rejected.
	wrong := token[:len(token)-2] + "zz"
	if rr := adminKeyMeRequest(t, svc, "Bearer "+wrong); rr.Code != http.StatusUnauthorized {
		t.Fatalf("wrong secret status=%d body=%s", rr.Code, rr.Body.String())
	}
	// Unknown key id: rejected.
	unknown := strings.Replace(token, "ops-primary", "ops-unknown", 1)
	if rr := adminKeyMeRequest(t, svc, "Bearer "+unknown); rr.Code != http.StatusUnauthorized {
		t.Fatalf("unknown key id status=%d body=%s", rr.Code, rr.Body.String())
	}
	// Valid admin key: 200 with safe subject metadata only.
	rr := adminKeyMeRequest(t, svc, "Bearer "+token)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin key status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["source"] != "admin_key" || body["subject"] != "adminkey:ops-primary" {
		t.Fatalf("subject body=%v", body)
	}
	if strings.Contains(rr.Body.String(), token) || strings.Contains(rr.Body.String(), generated["ops-primary"].TokenHash) {
		t.Fatal("raw key or verifier leaked in response")
	}
	// Wrong-secret failure responses carry no key material either.
	if rr := adminKeyMeRequest(t, svc, "Bearer "+wrong); strings.Contains(rr.Body.String(), wrong) {
		t.Fatal("raw key echoed in failure response")
	}
}

func TestAdminKeyPermissionsEnforced(t *testing.T) {
	svc, generated := newAdminKeyService(t, []AdminAPIKeyConfig{
		{KeyID: "ops-full", Domain: "local/test", Permissions: []string{"config:read", "config:write", "admin:auth:read"}},
		{KeyID: "ops-readonly", Domain: "local/test", Permissions: []string{"config:read"}},
	})
	full := generated["ops-full"].Token
	readonly := generated["ops-readonly"].Token

	// auth/check requires admin:auth:read: the full key passes, the read-only
	// key is forbidden.
	req := httptest.NewRequest(http.MethodGet, "/admin/auth/check", nil)
	req.Header.Set("Authorization", "Bearer "+full)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("full key /admin/auth/check status=%d body=%s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/auth/check", nil)
	req.Header.Set("Authorization", "Bearer "+readonly)
	rr = httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "admin-forbidden") {
		t.Fatalf("readonly key /admin/auth/check status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAdminKeyOIDCPermissionParity(t *testing.T) {
	// The fine-grained permissions map to stable Casbin object/action pairs,
	// so an OIDC subject granted the same policy gets the same decision.
	mapping := map[string][2]string{
		"config:read":     {"admin:config", "read"},
		"config:write":    {"admin:config", "write"},
		"config:activate": {"admin:config", "activate"},
		"config:force":    {"admin:config", "force"},
		"shadow:write":    {"admin:shadow", "write"},
	}
	for permission, want := range mapping {
		object, action, ok := adminPermissionObjectAction(permission)
		if !ok || object != want[0] || action != want[1] {
			t.Fatalf("permission %q -> %q/%q ok=%v, want %q/%q", permission, object, action, ok, want[0], want[1])
		}
	}

	svc, _ := newAdminKeyService(t, []AdminAPIKeyConfig{
		{KeyID: "ops-primary", Domain: "local/test", Permissions: []string{"config:read"}},
	})
	oidcSubject := adminAuthSubject{subject: "user:admin@example.com", domain: "local/test", source: "oidc_session"}
	if svc.authorizeAdminPermission(oidcSubject, "config:read") {
		t.Fatal("OIDC subject without policy must be denied")
	}
	// Grant via the same authorizer path the config policy would populate.
	if _, err := svc.authorizer.enforcer.AddPolicy("user:admin@example.com", "local/test", "admin:config", "read"); err != nil {
		t.Fatal(err)
	}
	if !svc.authorizeAdminPermission(oidcSubject, "config:read") {
		t.Fatal("OIDC subject with admin:config read policy must be allowed")
	}
	if svc.authorizeAdminPermission(oidcSubject, "config:write") {
		t.Fatal("OIDC subject without write policy must be denied")
	}
	// Admin key subjects enforce their exact permission set without any
	// Casbin grant: the read-only key cannot write even if policy exists for
	// other subjects.
	keySubject := adminAuthSubject{subject: "adminkey:ops-primary", domain: "local/test", source: "admin_key", permissions: map[string]bool{"config:read": true}}
	if !svc.authorizeAdminPermission(keySubject, "config:read") {
		t.Fatal("admin key config:read grant must be honored")
	}
	if svc.authorizeAdminPermission(keySubject, "config:write") {
		t.Fatal("admin key config:write must be denied for a read-only key")
	}
	// Admin key permissions are never projected into Casbin policy.
	if svc.authorizeAdmin(keySubject, "admin:config", "read") {
		t.Fatal("admin key permissions must not become Casbin policy lines")
	}
}

func TestAdminKeyRotation(t *testing.T) {
	// Two simultaneously active keys allow zero-downtime rotation; disabling
	// the old key cuts access without a config reload of other credentials.
	svc, generated := newAdminKeyService(t, []AdminAPIKeyConfig{
		{KeyID: "ops-old", Domain: "local/test", Permissions: []string{"config:read", "admin:auth:read"}},
		{KeyID: "ops-new", Domain: "local/test", Permissions: []string{"config:read", "admin:auth:read"}},
	})
	for _, keyID := range []string{"ops-old", "ops-new"} {
		req := httptest.NewRequest(http.MethodGet, "/admin/auth/check", nil)
		req.Header.Set("Authorization", "Bearer "+generated[keyID].Token)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("active key %s status=%d", keyID, rr.Code)
		}
	}

	disabledSvc, disabledKeys := newAdminKeyService(t, []AdminAPIKeyConfig{
		{KeyID: "ops-old", Domain: "local/test", Permissions: []string{"config:read", "admin:auth:read"}, Status: "disabled"},
		{KeyID: "ops-new", Domain: "local/test", Permissions: []string{"config:read", "admin:auth:read"}},
	})
	_ = disabledKeys
	req := httptest.NewRequest(http.MethodGet, "/admin/auth/check", nil)
	req.Header.Set("Authorization", "Bearer "+generated["ops-old"].Token)
	rr := httptest.NewRecorder()
	disabledSvc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("disabled key status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAdminKeyNeverAuthenticatesAsCaller(t *testing.T) {
	// An admin key must not open the caller-facing model APIs even when the
	// bearer is a valid admin credential.
	svc, generated := newAdminKeyService(t, []AdminAPIKeyConfig{
		{KeyID: "ops-primary", Domain: "local/test", Permissions: []string{"config:read", "config:write"}},
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+generated["ops-primary"].Token)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code == http.StatusOK {
		t.Fatalf("admin key authenticated /v1/models: %s", rr.Body.String())
	}
}

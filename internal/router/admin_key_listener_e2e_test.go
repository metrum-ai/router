// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// End-to-end lifecycle of an API-issued admin key through the REAL admin listener
// and the production mount: a bootstrap configured key issues a key over HTTP, the
// issued key is refused until a policy grants it, then rotated with an overlap
// window, then revoked - every step decided by the sole Authorizer and visible on
// the next request.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type adminKeyE2E struct {
	service *Service
	server  *httptest.Server
	store   *adminKeyStore
	keyID   string // bootstrap configured key id
	token   string // bootstrap token
	now     time.Time
}

func newAdminKeyE2E(t *testing.T) *adminKeyE2E {
	t.Helper()
	db := callerAdminTestDB(t) // production config-control-plane migrations + active set
	if err := applyAdminKeySchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO router_config_runtime (runtime_scope, config_revision, active_set_id) VALUES ('staging', 7, 'set-1')`).Error; err != nil {
		t.Fatal(err)
	}
	usage, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = usage.Close() })
	// The deployment's authoritative policy set: the bootstrap administrator can
	// administer keys and policy, which also keeps the reverse guard satisfiable.
	if err := usage.CreateAuthzPolicySet(authzPolicySetInput{ID: "policy-set", Name: "main", Version: 1, Policy: []string{
		"p, adminkey:bootstrap-admin, *, admin:keys, issue|list|get|rotate|revoke",
		"p, adminkey:bootstrap-admin, *, admin:policy, write|activate",
		"g, adminkey:bootstrap-admin, " + securityAdministratorRole + ", *",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := usage.ActivateAuthzPolicySet("policy-set", "tester", "req-e2e", "admin key e2e", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	bootstrap := generateTestAdminKey(t, "bootstrap-admin")
	t.Setenv("TEST_BOOTSTRAP_ADMIN_HASH", bootstrap.TokenHash)
	cfg := &Config{
		Server: ServerConfig{
			Identifiers:       IdentifierConfig{Mode: "passthrough"},
			Listen:            ":0",
			DefaultModelGroup: "default",
			AdminAuth: AdminAuthConfig{
				APIKeys: []AdminAPIKeyConfig{{
					KeyID: bootstrap.KeyID, KeyHashEnv: "TEST_BOOTSTRAP_ADMIN_HASH",
					Subject: "adminkey:bootstrap-admin", Domain: "*",
					Permissions: []string{"config:read"}, Status: "active",
				}},
				// Policy authority so the key store's reverse guard applies.
				Authorization: AdminAuthorizationConfig{Enabled: true, Source: "db"},
			},
			ConfigSource: ConfigSourceConfig{Driver: "sqlite", Path: db.Config.Dialector.(*sqlite.Dialector).DSN, RuntimeScope: "staging"},
		},
		Admin: AdminPlaneConfig{Enabled: true, Listen: "127.0.0.1:0"},
	}
	svc := &Service{
		cfg:                cfg,
		configControlPlane: db,
		usage:              usage,
		quota:              &quotaStore{callers: map[string]*callerRuntime{}},
		policyFreshness:    newPolicyFreshnessState(time.Minute),
		callerAuth:         &callerAuthState{},
	}
	svc.configSnapshot = NewConfigSnapshot()
	svc.configSnapshot.Store(cfg)
	bootstrapAuthz, err := newAuthorizer(cfg.Server.AdminAuth.Authorization, nil, usage, cfg.Server.AdminAuth)
	if err != nil {
		t.Fatal(err)
	}
	svc.authorizer = bootstrapAuthz
	svc.adminAPIKeys = buildAdminAPIKeyRuntimes(cfg, os.LookupEnv)
	// Production init order: key store, machine runtime, admin plane, publication,
	// then the listener's route registration hook mounts the contract.
	if err := svc.initAdminKeys(); err != nil {
		t.Fatal(err)
	}
	if err := svc.initAdminMachineRuntime(); err != nil {
		t.Fatal(err)
	}
	svc.initAdminPlane()
	if err := svc.startAdminMachineGeneration(); err != nil {
		t.Fatal(err)
	}
	svc.RegisterAdminRoutes(func(m *AdminMux) {
		if err := svc.MountAdminV1(m, svc.AdminAuthenticator(), svc.Authorize); err != nil {
			t.Errorf("mount admin v1: %v", err)
		}
	})
	server := httptest.NewServer(svc.AdminHandler())
	t.Cleanup(server.Close)
	// A controllable clock for the rotation overlap window.
	e2e := &adminKeyE2E{service: svc, server: server, store: svc.adminKeys, keyID: bootstrap.KeyID, token: bootstrap.Token, now: time.Now().UTC()}
	svc.adminKeys.now = func() time.Time { return e2e.now }
	t.Cleanup(func() { svc.Close() })
	return e2e
}

func (e *adminKeyE2E) do(t *testing.T, method, path, token string, body any, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	request, err := http.NewRequestWithContext(context.Background(), method, e.server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := e.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var out bytes.Buffer
	if _, err := out.ReadFrom(response.Body); err != nil {
		t.Fatal(err)
	}
	return response, out.Bytes()
}

// grant administers policy and the key lifecycle to the given subjects through
// the machine generation's single publication, as a policy/configuration change
// would.
func (e *adminKeyE2E) grant(t *testing.T, subjects ...string) {
	t.Helper()
	var lines []string
	for _, subject := range subjects {
		// The lifecycle object every administrator of this API needs.
		lines = append(lines, "p, "+subject+", *, admin:keys, issue|list|get|rotate|revoke")
		// Policy administration keeps the reverse guard satisfiable.
		lines = append(lines, "p, "+subject+", *, admin:policy, write|activate")
		lines = append(lines, "g, "+subject+", "+securityAdministratorRole+", *")
	}
	cfg := e.service.cfg
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: lines}
	authz, err := newAuthorizer(cfg.Server.AdminAuth.Authorization, e.service.quota.callers, e.service.usage, cfg.Server.AdminAuth)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.service.publishMachinePlane(func(machinePlaneSnapshot) (*machinePlanePublication, error) {
		generation, err := e.service.prepareAdminMachineConfigGeneration(cfg, authz)
		if err != nil {
			return nil, err
		}
		return &machinePlanePublication{Generation: generation, Authorizer: authz}, nil
	}); err != nil {
		t.Fatal(err)
	}
	// The revoke guard builds its candidate from the published configuration.
	e.service.configSnapshot.Store(cfg)
}

func TestAdminKeyLifecycleThroughProductionListener(t *testing.T) {
	e2e := newAdminKeyE2E(t)
	e2e.grant(t, "adminkey:bootstrap-admin")
	ifMatch := AdminETagForRevision(7)

	// 1. Issue over the real listener, authenticated by the configured bootstrap key.
	response, body := e2e.do(t, http.MethodPost, "/admin/v1/admin-keys", e2e.token,
		map[string]any{"expires_at": e2e.now.Add(2 * time.Hour).Format(time.RFC3339)},
		map[string]string{"If-Match": ifMatch, "Idempotency-Key": "issue-e2e-1"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("issue = %d body=%s", response.StatusCode, body)
	}
	var issued struct {
		ID        string `json:"id"`
		Subject   string `json:"subject"`
		Secret    string `json:"secret"`
		Published *bool  `json:"published"`
	}
	if err := json.Unmarshal(body, &issued); err != nil {
		t.Fatal(err)
	}
	if issued.ID == "" || issued.Secret == "" || issued.Published == nil || !*issued.Published {
		t.Fatalf("issue response incomplete or unpublished: %+v", issued)
	}

	// 2. The new key authenticates but is refused without a grant.
	response, body = e2e.do(t, http.MethodGet, "/admin/v1/admin-keys", issued.Secret, nil, nil)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("unbound issued key = %d body=%s, want 403", response.StatusCode, body)
	}
	// 3. After the policy binds its server-generated subject it is allowed.
	e2e.grant(t, "adminkey:bootstrap-admin", issued.Subject)
	response, body = e2e.do(t, http.MethodGet, "/admin/v1/admin-keys", issued.Secret, nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("bound issued key = %d body=%s, want 200", response.StatusCode, body)
	}

	// 4. Rotate: the previous secret works inside the overlap and not after it.
	response, body = e2e.do(t, http.MethodPost, "/admin/v1/admin-keys/"+issued.ID+"/rotate", e2e.token,
		map[string]any{"overlap": "10m"},
		map[string]string{"If-Match": ifMatch, "Idempotency-Key": "rotate-e2e-1"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("rotate = %d body=%s", response.StatusCode, body)
	}
	var rotated struct {
		Secret    string `json:"secret"`
		Published *bool  `json:"published"`
	}
	if err := json.Unmarshal(body, &rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.Secret == "" || rotated.Secret == issued.Secret || rotated.Published == nil || !*rotated.Published {
		t.Fatalf("rotate response invalid: %+v", rotated)
	}
	if response, body = e2e.do(t, http.MethodGet, "/admin/v1/admin-keys", issued.Secret, nil, nil); response.StatusCode != http.StatusOK {
		t.Fatalf("previous generation inside overlap = %d body=%s, want 200", response.StatusCode, body)
	}
	e2e.now = e2e.now.Add(11 * time.Minute)
	if response, body = e2e.do(t, http.MethodGet, "/admin/v1/admin-keys", issued.Secret, nil, nil); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("previous generation after overlap = %d body=%s, want 401", response.StatusCode, body)
	}
	if response, body = e2e.do(t, http.MethodGet, "/admin/v1/admin-keys", rotated.Secret, nil, nil); response.StatusCode != http.StatusOK {
		t.Fatalf("current generation after overlap = %d body=%s, want 200", response.StatusCode, body)
	}

	// 5. Revoke: the current generation is refused on the very next request.
	response, body = e2e.do(t, http.MethodPost, "/admin/v1/admin-keys/"+issued.ID+"/revoke", e2e.token, nil,
		map[string]string{"If-Match": ifMatch, "Idempotency-Key": "revoke-e2e-1"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("revoke = %d body=%s", response.StatusCode, body)
	}
	if response, body = e2e.do(t, http.MethodGet, "/admin/v1/admin-keys", rotated.Secret, nil, nil); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked key = %d body=%s, want 401", response.StatusCode, body)
	}
	// The bootstrap administrator is unaffected.
	if response, body = e2e.do(t, http.MethodGet, "/admin/v1/admin-keys", e2e.token, nil, nil); response.StatusCode != http.StatusOK {
		t.Fatalf("bootstrap key after revoke = %d body=%s, want 200", response.StatusCode, body)
	}
	_ = gorm.ErrRecordNotFound
}

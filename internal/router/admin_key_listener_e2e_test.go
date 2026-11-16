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
	service     *Service
	server      *httptest.Server
	store       *adminKeyStore
	keyID       string // bootstrap configured key id
	token       string // bootstrap token
	backupToken string
	now         time.Time
}

func mountAdminV1ForTest(t testing.TB, svc *Service) {
	t.Helper()
	var mountErr error
	svc.RegisterAdminRoutes(func(m *AdminMux) {
		mountErr = svc.MountAdminV1(m, svc.AdminAuthenticator(), svc.Authorize)
	})
	if mountErr != nil {
		t.Fatalf("mount admin v1: %v", mountErr)
	}
}

func newAdminKeyE2E(t *testing.T, includeBackupAdmin ...bool) *adminKeyE2E {
	t.Helper()
	withBackup := len(includeBackupAdmin) == 0 || includeBackupAdmin[0]
	narrowBootstrap := len(includeBackupAdmin) > 1 && includeBackupAdmin[1]
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
	policy := []string{
		"p, adminkey:bootstrap-admin, *, admin:clients, list|get|review|create|update|disable|enable|delete|register|revoke",
	}
	if !narrowBootstrap {
		policy = append(policy,
			"p, adminkey:bootstrap-admin, *, admin:keys, issue|list|get|rotate|revoke",
			"p, adminkey:bootstrap-admin, *, admin:policy, write|activate",
			"g, adminkey:bootstrap-admin, "+securityAdministratorRole+", *")
	}
	if withBackup {
		policy = append(policy,
			"p, adminkey:backup-admin, *, admin:clients, list|get|review|create|update|disable|enable|delete|register|revoke")
		if narrowBootstrap {
			policy = append(policy, "p, adminkey:backup-admin, *, admin:projects, read|write")
		} else {
			policy = append(policy,
				"p, adminkey:backup-admin, *, admin:keys, issue|list|get|rotate|revoke",
				"p, adminkey:backup-admin, *, admin:policy, write|activate",
				"g, adminkey:backup-admin, "+securityAdministratorRole+", *")
		}
	}
	if err := usage.CreateAuthzPolicySet(authzPolicySetInput{ID: "policy-set", Name: "main", Version: 1, Policy: policy}); err != nil {
		t.Fatal(err)
	}
	if err := usage.ActivateAuthzPolicySet("policy-set", "tester", "req-e2e", "admin key e2e", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	bootstrap := generateTestAdminKey(t, "bootstrap-admin")
	var backup GeneratedAdminAPIKey
	if withBackup {
		backup = generateTestAdminKey(t, "backup-admin")
	}
	t.Setenv("TEST_BOOTSTRAP_ADMIN_HASH", bootstrap.TokenHash)
	if withBackup {
		t.Setenv("TEST_BACKUP_ADMIN_HASH", backup.TokenHash)
	}
	apiKeys := []AdminAPIKeyConfig{{KeyID: bootstrap.KeyID, KeyHashEnv: "TEST_BOOTSTRAP_ADMIN_HASH", Subject: "adminkey:bootstrap-admin", Domain: "*", Permissions: []string{"config:read"}, Status: "active"}}
	if withBackup {
		apiKeys = append(apiKeys, AdminAPIKeyConfig{KeyID: backup.KeyID, KeyHashEnv: "TEST_BACKUP_ADMIN_HASH", Subject: "adminkey:backup-admin", Domain: "*", Permissions: []string{"config:read"}, Status: "active"})
	}
	cfg := &Config{
		Server: ServerConfig{
			Identifiers:       IdentifierConfig{Mode: "passthrough"},
			Listen:            ":0",
			DefaultModelGroup: "default",
			AdminAuth: AdminAuthConfig{
				APIKeys: apiKeys,
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
	if err := applyAdminClientSchema(db); err != nil {
		t.Fatal(err)
	}
	svc.adminClients = &adminClientStore{db: db, now: time.Now}
	svc.adminClients.recorder = svc.adminClientLifecycleRecorder
	if err := svc.initAdminMachineRuntime(); err != nil {
		t.Fatal(err)
	}
	svc.initAdminPlane()
	if err := svc.startAdminMachineGeneration(); err != nil {
		t.Fatal(err)
	}
	mountAdminV1ForTest(t, svc)
	server := httptest.NewServer(svc.AdminHandler())
	t.Cleanup(server.Close)
	// A controllable clock for the rotation overlap window.
	e2e := &adminKeyE2E{service: svc, server: server, store: svc.adminKeys, keyID: bootstrap.KeyID, token: bootstrap.Token, backupToken: backup.Token, now: time.Now().UTC()}
	svc.adminKeys.now = func() time.Time { return e2e.now }
	t.Cleanup(func() { svc.Close() })
	return e2e
}

func TestAdminClientLifecycleCannotRemoveBroaderPrincipal(t *testing.T) {
	for _, action := range []string{"disable", "delete", "revoke"} {
		t.Run(action, func(t *testing.T) {
			e2e := newAdminKeyE2E(t, true, true)
			create, body := e2e.do(t, http.MethodPost, "/admin/v1/clients", e2e.token, map[string]any{"id": "broader-client", "name": "Broader client"}, nil)
			if create.StatusCode != http.StatusCreated {
				t.Fatalf("create client status=%d body=%s", create.StatusCode, body)
			}
			register, body := e2e.do(t, http.MethodPost, "/admin/v1/clients/broader-client/credentials", e2e.token, map[string]any{
				"id": e2e.service.adminAPIKeys["backup-admin"].keyID, "kind": "adminkey", "identity": e2e.service.adminAPIKeys["backup-admin"].keyID,
				"subject": "adminkey:backup-admin", "expires_at": e2e.now.Add(90 * 24 * time.Hour).Format(time.RFC3339),
			}, nil)
			if register.StatusCode != http.StatusCreated {
				t.Fatalf("register broader principal status=%d body=%s", register.StatusCode, body)
			}
			method, path := http.MethodPost, "/admin/v1/clients/broader-client/disable"
			switch action {
			case "delete":
				method, path = http.MethodDelete, "/admin/v1/clients/broader-client"
			case "revoke":
				method, path = http.MethodDelete, "/admin/v1/clients/broader-client/credentials/"+e2e.service.adminAPIKeys["backup-admin"].keyID
			}
			response, body := e2e.do(t, method, path, e2e.token, nil, nil)
			if response.StatusCode != http.StatusForbidden {
				t.Fatalf("narrow client role %s broader principal: status=%d body=%s", action, response.StatusCode, body)
			}
		})
	}
}

func TestAdminClientPolicyInventoryExcludesNonWorkingBindings(t *testing.T) {
	for _, state := range []string{"revoked", "expired", "disabled"} {
		t.Run(state, func(t *testing.T) {
			e2e := newAdminKeyE2E(t, false)
			create, body := e2e.do(t, http.MethodPost, "/admin/v1/clients", e2e.token, map[string]any{"id": "managed-bootstrap", "name": "Managed bootstrap"}, nil)
			if create.StatusCode != http.StatusCreated {
				t.Fatalf("create client status=%d body=%s", create.StatusCode, body)
			}
			register, body := e2e.do(t, http.MethodPost, "/admin/v1/clients/managed-bootstrap/credentials", e2e.token, map[string]any{
				"id": e2e.keyID, "kind": "adminkey", "identity": e2e.keyID, "subject": "adminkey:bootstrap-admin",
				"expires_at": e2e.now.Add(90 * 24 * time.Hour).Format(time.RFC3339),
			}, nil)
			if register.StatusCode != http.StatusCreated {
				t.Fatalf("register managed credential status=%d body=%s", register.StatusCode, body)
			}
			switch state {
			case "revoked":
				if err := e2e.service.adminClients.revokeCredential(context.Background(), "managed-bootstrap", e2e.keyID); err != nil {
					t.Fatal(err)
				}
			case "expired":
				if err := e2e.service.configControlPlane.Exec("UPDATE "+adminClientCredentialTable+" SET expires_at=? WHERE client_id=? AND id=?", e2e.now.Add(-time.Hour), "managed-bootstrap", e2e.keyID).Error; err != nil {
					t.Fatal(err)
				}
			case "disabled":
				if err := e2e.service.adminClients.setStatus(context.Background(), "managed-bootstrap", "disabled"); err != nil {
					t.Fatal(err)
				}
			}
			generation := e2e.service.machineGeneration.Load()
			admins, err := (adminClientPolicyInventory{base: generationInventory{generation: generation}, store: e2e.service.adminClients, now: e2e.now}).enabledPolicyAdmins(e2e.service.configSnapshot.Load())
			if err != nil {
				t.Fatal(err)
			}
			for _, admin := range admins {
				if admin.Subject == "adminkey:bootstrap-admin" {
					t.Fatalf("%s client binding counted as a working policy administrator: %+v", state, admin)
				}
			}
		})
	}
}

func TestAdminClientDisableRevokesCredentialThroughListenerE2E(t *testing.T) {
	e2e := newAdminKeyE2E(t)
	response, body := e2e.do(t, http.MethodGet, "/admin/v1/clients", testToken, nil, nil)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("inference token authenticated to admin plane: status=%d body=%s", response.StatusCode, body)
	}
	response, body = e2e.do(t, http.MethodPost, "/admin/v1/clients", e2e.token, map[string]any{"id": "service-a", "name": "Service A"}, nil)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create client status=%d body=%s", response.StatusCode, body)
	}
	response, body = e2e.do(t, http.MethodPost, "/admin/v1/clients/service-a/credentials", e2e.token, map[string]any{"id": e2e.keyID, "kind": "adminkey", "identity": e2e.keyID, "subject": "adminkey:bootstrap-admin", "expires_at": e2e.now.Add(time.Hour).Format(time.RFC3339)}, nil)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("register credential status=%d body=%s", response.StatusCode, body)
	}
	response, body = e2e.do(t, http.MethodGet, "/admin/v1/clients/access-review", e2e.token, nil, nil)
	if response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("effectiveGrants")) {
		t.Fatalf("access review status=%d body=%s", response.StatusCode, body)
	}
	if bytes.Contains(body, []byte(e2e.token)) || bytes.Contains(body, []byte("secret")) {
		t.Fatal("access review exposed credential material")
	}
	response, body = e2e.do(t, http.MethodPost, "/admin/v1/clients/service-a/disable", e2e.token, nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("disable client status=%d body=%s", response.StatusCode, body)
	}
	response, body = e2e.do(t, http.MethodGet, "/admin/v1/clients", e2e.token, nil, nil)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("disabled client credential status=%d body=%s", response.StatusCode, body)
	}
	var lifecycle []struct {
		Kind         string `gorm:"column:kind"`
		Lifecycle    string `gorm:"column:lifecycle"`
		CredentialID string `gorm:"column:credential_id"`
	}
	if err := e2e.service.configControlPlane.Raw("SELECT kind,lifecycle,credential_id FROM router_admin_credential_events WHERE credential_id=? ORDER BY at", e2e.keyID).Scan(&lifecycle).Error; err != nil {
		t.Fatal(err)
	}
	if len(lifecycle) < 2 {
		t.Fatalf("client credential lifecycle events=%+v", lifecycle)
	}
}

func TestAdminClientLifecyclePreservesLastPolicyAdministrator(t *testing.T) {
	for _, action := range []string{"disable", "delete", "revoke", "short-expiry-register"} {
		t.Run(action, func(t *testing.T) {
			e2e := newAdminKeyE2E(t, false)
			create, body := e2e.do(t, http.MethodPost, "/admin/v1/clients", e2e.token, map[string]any{"id": "sole-admin", "name": "Sole admin"}, nil)
			if create.StatusCode != http.StatusCreated {
				t.Fatalf("create client status=%d body=%s", create.StatusCode, body)
			}
			expiry := e2e.now.Add(90 * 24 * time.Hour)
			if action == "short-expiry-register" {
				expiry = e2e.now.Add(time.Hour)
			}
			register, body := e2e.do(t, http.MethodPost, "/admin/v1/clients/sole-admin/credentials", e2e.token, map[string]any{
				"id": e2e.keyID, "kind": "adminkey", "identity": e2e.keyID, "subject": "adminkey:bootstrap-admin", "expires_at": expiry.Format(time.RFC3339),
			}, nil)
			if action == "short-expiry-register" {
				if register.StatusCode != http.StatusConflict || !bytes.Contains(body, []byte("last-policy-administrator")) {
					t.Fatalf("short-expiry registration status=%d body=%s", register.StatusCode, body)
				}
				return
			}
			if register.StatusCode != http.StatusCreated {
				t.Fatalf("register long-lived credential status=%d body=%s", register.StatusCode, body)
			}
			method, path := http.MethodPost, "/admin/v1/clients/sole-admin/disable"
			switch action {
			case "delete":
				method, path = http.MethodDelete, "/admin/v1/clients/sole-admin"
			case "revoke":
				method, path = http.MethodDelete, "/admin/v1/clients/sole-admin/credentials/"+e2e.keyID
			}
			response, body := e2e.do(t, method, path, e2e.token, nil, nil)
			if response.StatusCode != http.StatusConflict || !bytes.Contains(body, []byte("last-policy-administrator")) {
				t.Fatalf("%s status=%d body=%s", action, response.StatusCode, body)
			}
		})
	}
}

func TestAdminClientCredentialRegistrationRequiresOwnedIdentity(t *testing.T) {
	e2e := newAdminKeyE2E(t)
	for _, id := range []string{"client-one", "client-two"} {
		response, body := e2e.do(t, http.MethodPost, "/admin/v1/clients", e2e.token, map[string]any{"id": id, "name": id}, nil)
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("create %s status=%d body=%s", id, response.StatusCode, body)
		}
	}
	credential := map[string]any{"id": e2e.keyID, "kind": "adminkey", "identity": e2e.keyID, "subject": "adminkey:bootstrap-admin", "expires_at": e2e.now.Add(90 * 24 * time.Hour).Format(time.RFC3339)}
	response, body := e2e.do(t, http.MethodPost, "/admin/v1/clients/client-one/credentials", e2e.token, credential, nil)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("register real credential status=%d body=%s", response.StatusCode, body)
	}
	for _, invalid := range []map[string]any{
		{"id": "unknown-key", "kind": "adminkey", "identity": "unknown-key", "subject": "adminkey:bootstrap-admin", "expires_at": e2e.now.Add(90 * 24 * time.Hour).Format(time.RFC3339)},
		{"id": e2e.keyID, "kind": "adminkey", "identity": e2e.keyID, "subject": "adminkey:other", "expires_at": e2e.now.Add(90 * 24 * time.Hour).Format(time.RFC3339)},
	} {
		response, body = e2e.do(t, http.MethodPost, "/admin/v1/clients/client-two/credentials", e2e.token, invalid, nil)
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("unowned credential registration status=%d body=%s", response.StatusCode, body)
		}
	}
	response, body = e2e.do(t, http.MethodPost, "/admin/v1/clients/client-two/credentials", e2e.token, credential, nil)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("credential reassignment status=%d body=%s", response.StatusCode, body)
	}
}

func TestAdminClientAccessReviewExcludesRevokedAndExpiredCredentials(t *testing.T) {
	e2e := newAdminKeyE2E(t)
	created, body := e2e.do(t, http.MethodPost, "/admin/v1/clients", e2e.token, map[string]any{"id": "review-client", "name": "Review client"}, nil)
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create client status=%d body=%s", created.StatusCode, body)
	}
	response, body := e2e.do(t, http.MethodPost, "/admin/v1/clients/review-client/credentials", e2e.token, map[string]any{
		"id": e2e.keyID, "kind": "adminkey", "identity": e2e.keyID, "subject": "adminkey:bootstrap-admin", "expires_at": e2e.now.Add(90 * 24 * time.Hour).Format(time.RFC3339),
	}, nil)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("register credential status=%d body=%s", response.StatusCode, body)
	}
	response, body = e2e.do(t, http.MethodDelete, "/admin/v1/clients/review-client/credentials/"+e2e.keyID, e2e.token, nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("revoke credential status=%d body=%s", response.StatusCode, body)
	}
	if err := e2e.service.configControlPlane.Exec("INSERT INTO "+adminClientCredentialTable+" (id,client_id,kind,identity,subject,expires_at,status) VALUES (?,?,?,?,?,?,'active')", "expired-bootstrap", "review-client", "adminkey", "expired-bootstrap", "adminkey:bootstrap-admin", e2e.now.Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	response, body = e2e.do(t, http.MethodGet, "/admin/v1/clients/access-review", e2e.backupToken, nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("access review status=%d body=%s", response.StatusCode, body)
	}
	var review struct {
		Clients []struct {
			ID          string `json:"id"`
			Credentials []struct {
				ID              string     `json:"id"`
				Status          string     `json:"status"`
				ExpiresAt       time.Time  `json:"expiresAt"`
				EffectiveGrants [][]string `json:"effectiveGrants"`
			} `json:"credentials"`
		} `json:"clients"`
	}
	if err := json.Unmarshal(body, &review); err != nil {
		t.Fatal(err)
	}
	for _, client := range review.Clients {
		if client.ID != "review-client" {
			continue
		}
		for _, credential := range client.Credentials {
			if len(credential.EffectiveGrants) != 0 {
				t.Fatalf("credential %s has grants despite status=%s expiry=%s: %+v", credential.ID, credential.Status, credential.ExpiresAt, credential.EffectiveGrants)
			}
		}
		if len(client.Credentials) != 2 {
			t.Fatalf("review credentials=%+v", client.Credentials)
		}
		return
	}
	t.Fatalf("review omitted client: %s", body)
}

func assertAdminClientLifecycleAudit(t *testing.T) {
	t.Helper()
	e2e := newAdminKeyE2E(t)
	actorToken := e2e.backupToken
	type mutation struct {
		method, path, actor string
		body                map[string]any
	}
	mutations := []mutation{
		{http.MethodPost, "/admin/v1/clients", e2e.token, map[string]any{"id": "audit-client", "name": "Audit client"}},
		{http.MethodPut, "/admin/v1/clients/audit-client", e2e.token, map[string]any{"name": "Updated audit client"}},
		{http.MethodPost, "/admin/v1/clients/audit-client/credentials", e2e.token, map[string]any{"id": e2e.keyID, "kind": "adminkey", "identity": e2e.keyID, "subject": "adminkey:bootstrap-admin", "expires_at": e2e.now.Add(time.Hour).Format(time.RFC3339)}},
		{http.MethodDelete, "/admin/v1/clients/audit-client/credentials/" + e2e.keyID, e2e.token, nil},
		{http.MethodPost, "/admin/v1/clients/audit-client/disable", actorToken, nil},
		{http.MethodPost, "/admin/v1/clients/audit-client/enable", actorToken, nil},
		{http.MethodDelete, "/admin/v1/clients/audit-client", actorToken, nil},
	}
	for _, mutation := range mutations {
		var before int64
		if err := e2e.service.configControlPlane.Raw("SELECT COUNT(*) FROM router_admin_credential_events WHERE kind='client'").Scan(&before).Error; err != nil {
			t.Fatal(err)
		}
		response, body := e2e.do(t, mutation.method, mutation.path, mutation.actor, mutation.body, nil)
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			t.Fatalf("client mutation %s %s status=%d body=%s", mutation.method, mutation.path, response.StatusCode, body)
		}
		var events []struct {
			ClientID  string `gorm:"column:client_id"`
			Actor     string `gorm:"column:actor"`
			RequestID string `gorm:"column:request_id"`
			Lifecycle string `gorm:"column:lifecycle"`
		}
		if err := e2e.service.configControlPlane.Raw("SELECT client_id,actor,request_id,lifecycle FROM router_admin_credential_events WHERE kind='client' ORDER BY at DESC LIMIT 1").Scan(&events).Error; err != nil {
			t.Fatal(err)
		}
		var after int64
		if err := e2e.service.configControlPlane.Raw("SELECT COUNT(*) FROM router_admin_credential_events WHERE kind='client'").Scan(&after).Error; err != nil {
			t.Fatal(err)
		}
		if after != before+1 || len(events) != 1 || events[0].ClientID != "audit-client" || events[0].Actor == "" || events[0].RequestID == "" {
			t.Fatalf("client mutation %s %s audit rows=%d before=%d event=%+v", mutation.method, mutation.path, after-before, before, events)
		}
	}
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

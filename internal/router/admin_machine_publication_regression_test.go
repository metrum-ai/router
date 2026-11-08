// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Negative regressions for the machine-plane publication contract, driven
// through the PRODUCTION admin machine runtime factory (adminMachineRuntime.prepare,
// including its durable credential-event recorder) rather than a stub:
//
//   - a policy-store revocation republishes the generation, so the next admin
//     request is denied instead of authorizing against grants the store dropped;
//   - a concurrent CA reload can never resurrect a credential that a
//     configuration reload removed, because it rebuilds from the current
//     configuration and synchronized authorizer under the one lock;
//   - a failure after preparation publishes nothing at all.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sqliteDriver "github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const adminCredentialEventsTestDDL = `CREATE TABLE IF NOT EXISTS router_admin_credential_events (event_id TEXT PRIMARY KEY, at TEXT NOT NULL, kind TEXT NOT NULL, credential_id TEXT NOT NULL DEFAULT '', subject TEXT NOT NULL DEFAULT '', issuer TEXT NOT NULL DEFAULT '', outcome TEXT NOT NULL, reason_code TEXT NOT NULL DEFAULT '', request_id TEXT NOT NULL DEFAULT '', client_ip_hash TEXT NOT NULL DEFAULT '', lifecycle TEXT NOT NULL DEFAULT 'auth', count INTEGER NOT NULL DEFAULT 1)`

func machineCredentialEventDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqliteDriver.Open(filepath.Join(t.TempDir(), "control.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(adminCredentialEventsTestDDL).Error; err != nil {
		t.Fatal(err)
	}
	if err := applyAdminEmergencyActivationSchema(db); err != nil {
		t.Fatal(err)
	}
	return db
}

type machineCredentialEventRowCount struct {
	Kind         string
	CredentialID string
	Outcome      string
	Lifecycle    string
	Count        int
}

func machineCredentialEvents(t *testing.T, db *gorm.DB) []machineCredentialEventRowCount {
	t.Helper()
	var rows []machineCredentialEventRowCount
	if err := db.Raw(`SELECT kind, credential_id, outcome, lifecycle, count FROM router_admin_credential_events ORDER BY at`).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

// machineRegressionService builds a Service whose machine plane uses the real
// runtime factory (real key catalogs, trust store, chain and event recorder).
func machineRegressionService(t *testing.T, cfg *Config, usage *usageStore) (*Service, *gorm.DB) {
	t.Helper()
	events := machineCredentialEventDB(t)
	callers := map[string]*callerRuntime{}
	quota := &quotaStore{callers: callers}
	authz, err := newAuthorizer(cfg.Server.AdminAuth.Authorization, callers, usage, cfg.Server.AdminAuth)
	if err != nil {
		t.Fatal(err)
	}
	snap := NewConfigSnapshot()
	snap.Store(cfg)
	svc := &Service{
		cfg:                cfg,
		authorizer:         authz,
		configSnapshot:     snap,
		policyFreshness:    newPolicyFreshnessState(30 * time.Second),
		quota:              quota,
		usage:              usage,
		callerAuth:         &callerAuthState{},
		configControlPlane: events,
		adminAPIKeys:       buildAdminAPIKeyRuntimes(cfg, os.LookupEnv),
	}
	if err := svc.initAdminMachineRuntime(); err != nil {
		t.Fatal(err)
	}
	return svc, events
}

// machineKeyEnv is the environment variable that carries one test key verifier.
func machineKeyEnv(key GeneratedAdminAPIKey) string {
	return "TEST_HASH_" + strings.ToUpper(strings.ReplaceAll(key.KeyID, "-", "_"))
}

func machineKeyConfig(t *testing.T, key GeneratedAdminAPIKey, permission string) AdminAPIKeyConfig {
	t.Helper()
	return AdminAPIKeyConfig{KeyID: key.KeyID, KeyHashEnv: machineKeyEnv(key), Subject: "adminkey:" + key.KeyID,
		Domain: "default", Permissions: []string{permission}, Status: "active"}
}

// machineAuthorizeRequest runs one authenticated admin request through the
// published generation and its captured authorization snapshot.
func machineAuthorizeRequest(svc *Service, token string) int {
	handler := svc.adminMachineGenerationMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFrom(r.Context())
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if err := svc.Authorize(svc.PinAuthorization(r.Context()), principal, "default", "admin:config", "read"); err != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, configuredMachineKeyRequest(token))
	return recorder.Code
}

func machineDDLPolicyStore(t *testing.T) *usageStore {
	t.Helper()
	store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.db.AutoMigrate(&policySyncRevisionRow{}); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&policySyncRevisionRow{ID: 1, Revision: 1}).Error; err != nil {
		t.Fatal(err)
	}
	return store
}

// A revocation written to the authoritative policy store must reach machine
// decisions on the synchronization that observes it. Before the fix the machine
// generation kept the authorizer it was pinned to at startup, so a revoked grant
// still authorized the next admin request.
func TestRuntimeMachinePolicySyncRevocationReachesMachineGeneration(t *testing.T) {
	key := generateTestAdminKey(t, "ops")
	t.Setenv(machineKeyEnv(key), key.TokenHash)
	cfg := &Config{}
	cfg.Admin.Enabled = true
	// The key's CONFIGURED permission is a different one: the grant under test
	// exists only in the authoritative policy store, so a revocation there is
	// observable end to end.
	cfg.Server.AdminAuth.APIKeys = []AdminAPIKeyConfig{machineKeyConfig(t, key, "admin:callers:list")}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Source: "db"}
	store := machineDDLPolicyStore(t)
	if err := store.CreateAuthzPolicySet(authzPolicySetInput{ID: "granted", Name: "granted", Version: 1, Policy: []string{"p, adminkey:ops, *, admin:config, read"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateAuthzPolicySet("granted", "offline", "", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	svc, events := machineRegressionService(t, cfg, store)

	// Startup order: the authoritative synchronization first, then the initial
	// machine generation, exactly as New() sequences it.
	if err := svc.syncPolicyAuthorization(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := svc.startAdminMachineGeneration(); err != nil {
		t.Fatal(err)
	}
	published := svc.machineGeneration.Load()
	if published == nil || published.authorizer != svc.currentAuthorizer() {
		t.Fatal("startup published a generation pinned to unpublished authority")
	}
	if code := machineAuthorizeRequest(svc, key.Token); code != http.StatusNoContent {
		t.Fatalf("granted machine request = %d, want 204", code)
	}
	rows := machineCredentialEvents(t, events)
	if len(rows) != 1 || rows[0].Kind != "adminkey" || rows[0].Outcome != "success" || rows[0].Lifecycle != "authenticate" {
		t.Fatalf("production chain did not record the authentication event: %+v", rows)
	}

	// Revoke the grant in the authoritative store and bump the singleton.
	if err := store.CreateAuthzPolicySet(authzPolicySetInput{ID: "revoked", Name: "revoked", Version: 1, Policy: []string{"p, someone-else, *, admin:config, read"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateAuthzPolicySet("revoked", "offline", "", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Model(&policySyncRevisionRow{}).Where("id = ?", 1).Update("revision", 2).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.syncPolicyAuthorization(context.Background()); err != nil {
		t.Fatal(err)
	}
	republished := svc.machineGeneration.Load()
	if republished == published {
		t.Fatal("policy synchronization did not republish the machine generation")
	}
	if republished.authorizer != svc.currentAuthorizer() {
		t.Fatal("republished generation is not pinned to the synchronized authorizer")
	}
	if code := machineAuthorizeRequest(svc, key.Token); code != http.StatusForbidden {
		t.Fatalf("revoked machine grant = %d, want 403", code)
	}
	// The identity still authenticated; only the revoked grant is gone.
	rows = machineCredentialEvents(t, events)
	if len(rows) != 1 || rows[0].Count != 2 {
		t.Fatalf("expected two authenticated requests on the one collapsed event row: %+v", rows)
	}
}

// A CA reload rebuilds from the current configuration and synchronized
// authorizer under the single publication lock, so a key that a configuration
// reload removed can never be published again by a concurrent CA poll.
func TestRuntimeMachineCAReloadCannotResurrectRemovedKey(t *testing.T) {
	keyA := generateTestAdminKey(t, "alpha")
	keyB := generateTestAdminKey(t, "beta")
	t.Setenv(machineKeyEnv(keyA), keyA.TokenHash)
	t.Setenv(machineKeyEnv(keyB), keyB.TokenHash)
	ca, _, _ := certificateBindingCA(t)
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	trustStoreWriteCA(t, caPath, ca)

	base := &Config{}
	base.Admin.Enabled = true
	base.Admin.Authentication.MTLS = AdminMTLSTrustConfig{Issuer: "authority", CAFiles: []string{caPath}}
	base.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, adminkey:alpha, *, admin:config, read", "p, adminkey:beta, *, admin:config, read"}}
	base.Server.AdminAuth.APIKeys = []AdminAPIKeyConfig{machineKeyConfig(t, keyA, adminPermissionConfigRead), machineKeyConfig(t, keyB, adminPermissionConfigRead)}
	removed := &Config{}
	removed.Admin.Enabled = true
	removed.Admin.Authentication = base.Admin.Authentication
	removed.Server.AdminAuth.Authorization = base.Server.AdminAuth.Authorization
	removed.Server.AdminAuth.APIKeys = []AdminAPIKeyConfig{machineKeyConfig(t, keyA, adminPermissionConfigRead)}

	svc, _ := machineRegressionService(t, base, nil)
	if err := svc.startAdminMachineGeneration(); err != nil {
		t.Fatal(err)
	}
	if code := machineAuthorizeRequest(svc, keyB.Token); code != http.StatusNoContent {
		t.Fatalf("configured key B = %d, want 204 before removal", code)
	}
	// Widen the reproduction window: every preparation is slow enough that CA
	// polls are still in flight while the configuration reload commits. A CA
	// reload that captured the superseded configuration therefore has every
	// opportunity to publish it after the removal.
	build := svc.machineGenerationBuilder
	svc.machineGenerationBuilder = func(next *Config, compatible *authorizer) (*adminMachineGeneration, error) {
		time.Sleep(10 * time.Millisecond)
		return build(next, compatible)
	}

	// The configuration reload path: prepare the generation once, inside the
	// single publication, from the configuration being published.
	applyReload := func(next *Config) error {
		return svc.publishMachinePlane(func(machinePlaneSnapshot) (*machinePlanePublication, error) {
			compatible, err := newAuthorizer(next.Server.AdminAuth.Authorization, svc.quota.callers, svc.usage, next.Server.AdminAuth)
			if err != nil {
				return nil, err
			}
			generation, err := svc.prepareAdminMachineConfigGeneration(next, compatible)
			if err != nil {
				return nil, err
			}
			return &machinePlanePublication{Generation: generation, Authorizer: compatible, Config: next,
				Callers: svc.quota.callers, APIKeys: buildAdminAPIKeyRuntimes(next, os.LookupEnv)}, nil
		})
	}
	// CA polling starts BEFORE the configuration reload and keeps republishing
	// across it and after it.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if err := svc.machineClientCASource().Reload(); err != nil {
					t.Errorf("CA reload: %v", err)
					return
				}
			}
		}()
	}
	if err := applyReload(removed); err != nil {
		t.Fatal(err)
	}
	if code := machineAuthorizeRequest(svc, keyB.Token); code != http.StatusUnauthorized {
		t.Fatalf("removed key B = %d, want 401 immediately after the reload", code)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for i := 0; time.Now().Before(deadline); i++ {
		generation := svc.machineGeneration.Load()
		if generation == nil || generation.keys == nil {
			t.Fatal("machine plane lost its published generation")
		}
		if generation.keys.keys[keyB.KeyID] != nil {
			t.Fatal("a CA reload resurrected the removed key")
		}
		if len(generation.config.Server.AdminAuth.APIKeys) != 1 || generation.config.Server.AdminAuth.APIKeys[0].KeyID != keyA.KeyID {
			t.Fatal("a CA reload published a generation built from the superseded configuration")
		}
		if generation.authorizer != svc.currentAuthorizer() {
			t.Fatal("a CA reload published a generation against superseded authority")
		}
	}
	close(stop)
	wg.Wait()
	if code := machineAuthorizeRequest(svc, keyB.Token); code != http.StatusUnauthorized {
		t.Fatalf("removed key B = %d, want 401 after concurrent CA reloads", code)
	}
	if code := machineAuthorizeRequest(svc, keyA.Token); code != http.StatusNoContent {
		t.Fatalf("surviving key A = %d, want 204", code)
	}
}

// A failure after preparation must leave the authorizer, configuration, caller
// directory and credential generation exactly as they were: the reload prepared
// a complete bundle, so there is nothing half-applied to roll back.
func TestRuntimeMachineFailedPublicationChangesNothing(t *testing.T) {
	keyA := generateTestAdminKey(t, "kept")
	keyB := generateTestAdminKey(t, "candidate")
	t.Setenv(machineKeyEnv(keyA), keyA.TokenHash)
	t.Setenv(machineKeyEnv(keyB), keyB.TokenHash)
	cfg := &Config{}
	cfg.Admin.Enabled = true
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, adminkey:kept, *, admin:config, read"}}
	cfg.Server.AdminAuth.APIKeys = []AdminAPIKeyConfig{machineKeyConfig(t, keyA, adminPermissionConfigRead)}
	candidate := &Config{}
	candidate.Admin.Enabled = true
	candidate.Server.AdminAuth.Authorization = cfg.Server.AdminAuth.Authorization
	candidate.Server.AdminAuth.APIKeys = []AdminAPIKeyConfig{machineKeyConfig(t, keyB, adminPermissionConfigRead)}
	svc, events := machineRegressionService(t, cfg, nil)
	if err := svc.startAdminMachineGeneration(); err != nil {
		t.Fatal(err)
	}
	published := svc.machineGeneration.Load()
	if published == nil {
		t.Fatal("no initial machine generation")
	}
	publishedAuthorizer := svc.currentAuthorizer()
	svc.quota.mu.Lock()
	callers := len(svc.quota.callers)
	svc.quota.mu.Unlock()
	desiredBefore, appliedBefore, observedBefore, okBefore := svc.PolicyFreshness()

	// Preparation succeeds for the candidate configuration and then fails: the
	// production factory rejects it, which must abort the whole reload.
	svc.machineGenerationBuilder = func(*Config, *authorizer) (*adminMachineGeneration, error) {
		return nil, errors.New("injected complete-generation failure")
	}
	err := svc.publishMachinePlane(func(machinePlaneSnapshot) (*machinePlanePublication, error) {
		compatible, err := newAuthorizer(candidate.Server.AdminAuth.Authorization, svc.quota.callers, svc.usage, candidate.Server.AdminAuth)
		if err != nil {
			return nil, err
		}
		generation, err := svc.prepareAdminMachineConfigGeneration(candidate, compatible)
		if err != nil {
			return nil, err
		}
		return &machinePlanePublication{Generation: generation, Authorizer: compatible, Config: candidate,
			Callers: svc.quota.callers, APIKeys: buildAdminAPIKeyRuntimes(candidate, os.LookupEnv)}, nil
	})
	if err == nil {
		t.Fatal("publication accepted a failed preparation")
	}
	if svc.machineGeneration.Load() != published || svc.currentAuthorizer() != publishedAuthorizer || svc.cfg != cfg || svc.configSnapshot.Load() != cfg {
		t.Fatal("failed preparation published an authorizer, configuration or generation")
	}
	svc.quota.mu.Lock()
	after := len(svc.quota.callers)
	svc.quota.mu.Unlock()
	if after != callers {
		t.Fatal("failed preparation replaced the caller directory")
	}
	desired, applied, observed, ok := svc.PolicyFreshness()
	if desired != desiredBefore || applied != appliedBefore || !observed.Equal(observedBefore) || ok != okBefore {
		t.Fatal("failed preparation acknowledged a policy synchronization")
	}
	if len(machineCredentialEvents(t, events)) != 0 {
		t.Fatal("failed preparation wrote credential events")
	}
	// Published credentials are untouched: the old key still authenticates, the
	// candidate's key never does.
	if code := machineAuthorizeRequest(svc, keyA.Token); code != http.StatusNoContent {
		t.Fatalf("published key A = %d, want 204", code)
	}
	if code := machineAuthorizeRequest(svc, keyB.Token); code != http.StatusUnauthorized {
		t.Fatalf("unpublished candidate key B = %d, want 401", code)
	}
}

// The published inventory is read from the atomic publication pointer, so a
// policy-mutation guard and a CA publication can run concurrently. The owned
// race suite is what proves it: this test only has to exercise both sides.
func TestRuntimeMachineInventoryReadIsRaceFree(t *testing.T) {
	key := generateTestAdminKey(t, "race")
	t.Setenv(machineKeyEnv(key), key.TokenHash)
	ca, _, _ := certificateBindingCA(t)
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	trustStoreWriteCA(t, caPath, ca)
	cfg := &Config{}
	cfg.Admin.Enabled = true
	cfg.Admin.Authentication.MTLS = AdminMTLSTrustConfig{Issuer: "authority", CAFiles: []string{caPath}}
	cfg.Server.AdminAuth.APIKeys = []AdminAPIKeyConfig{machineKeyConfig(t, key, "admin:callers:list")}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, " + "adminkey:race" + ", *, admin:config, read"}}
	svc, _ := machineRegressionService(t, cfg, nil)
	if err := svc.startAdminMachineGeneration(); err != nil {
		t.Fatal(err)
	}
	view := svc.policyAuthorizerView(svc.currentAuthorizer())
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if err := svc.machineClientCASource().Reload(); err != nil {
					t.Errorf("CA reload: %v", err)
					return
				}
			}
		}()
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = svc.GuardPolicyAdministrationLockout(view, cfg)
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = svc.enabledPolicyAdmins(cfg)
		}
	}()
	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// A generation handed to the publisher was prepared outside its lock, so the
// authorizer it is pinned to may already be superseded - for example when a
// policy synchronization lands between startup's preparation and its
// publication. Publishing it would leave machine requests on revoked grants
// indefinitely, because every later synchronization would see "no change".
// The publisher therefore refuses it, and only accepts the authorizer it installs.
func TestRuntimeMachinePublicationRefusesSupersededAuthorizer(t *testing.T) {
	key := generateTestAdminKey(t, "sync")
	t.Setenv(machineKeyEnv(key), key.TokenHash)
	store := machineDDLPolicyStore(t)
	if err := store.CreateAuthzPolicySet(authzPolicySetInput{ID: "granted", Name: "granted", Version: 1, Policy: []string{"p, adminkey:sync, *, admin:config, read"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateAuthzPolicySet("granted", "offline", "", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{}
	cfg.Admin.Enabled = true
	cfg.Server.AdminAuth.APIKeys = []AdminAPIKeyConfig{machineKeyConfig(t, key, "admin:callers:list")}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Source: "db"}
	svc, _ := machineRegressionService(t, cfg, store)
	if err := svc.syncPolicyAuthorization(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := svc.startAdminMachineGeneration(); err != nil {
		t.Fatal(err)
	}
	published := svc.machineGeneration.Load()
	staleAuthorizer := published.authorizer
	// Prepared OUTSIDE the publication, exactly like the old startup path.
	stale, err := svc.prepareAdminMachineConfigGeneration(cfg, staleAuthorizer)
	if err != nil {
		t.Fatal(err)
	}
	// A synchronization lands between preparation and publication and revokes the
	// grant, republishing with the new authorizer.
	if err := store.CreateAuthzPolicySet(authzPolicySetInput{ID: "revoked", Name: "revoked", Version: 1, Policy: []string{"p, someone-else, *, admin:config, read"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateAuthzPolicySet("revoked", "offline", "", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Model(&policySyncRevisionRow{}).Where("id = ?", 1).Update("revision", 2).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.syncPolicyAuthorization(context.Background()); err != nil {
		t.Fatal(err)
	}
	republished := svc.machineGeneration.Load()
	if republished == published || republished.authorizer == staleAuthorizer {
		t.Fatal("synchronization did not republish against the new authorizer")
	}
	// The old startup shape: a generation handed over WITHOUT an authorizer, so
	// the publisher would keep the current one. Its pin is stale, so it must be
	// refused instead of silently installing superseded authority.
	if err := svc.publishMachinePlane(func(machinePlaneSnapshot) (*machinePlanePublication, error) {
		return &machinePlanePublication{Generation: stale}, nil
	}); err == nil {
		t.Fatal("publisher accepted a generation pinned to superseded authority")
	}
	if svc.machineGeneration.Load() != republished {
		t.Fatal("refused publication replaced the current generation")
	}
	if code := machineAuthorizeRequest(svc, key.Token); code != http.StatusForbidden {
		t.Fatalf("revoked grant reachable after refused stale publication = %d, want 403", code)
	}
}

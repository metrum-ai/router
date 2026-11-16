// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

type adminClientTestAuthenticator struct{ principal Principal }

func (a adminClientTestAuthenticator) Authenticate(context.Context, *http.Request) (Principal, error) {
	return a.principal, nil
}

func TestAdminClientCredentialRevocationAndExpiryGate(t *testing.T) {
	_, db := newAdminKeyStoreForTest(t)
	if err := applyAdminClientSchema(db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 16, 4, 0, 0, 0, time.UTC)
	clients := &adminClientStore{db: db, now: func() time.Time { return now }}
	if err := clients.create(context.Background(), adminClient{ID: "service-a", Name: "Service A"}); err != nil {
		t.Fatal(err)
	}
	p := Principal{Subject: "spiffe://example/service-a", Identity: "uri:spiffe://example/service-a", Identities: "uri:spiffe://example/service-a\x00dns:service-a", Kind: "mtls", CredentialID: "cert-a", Issuer: "test-ca"}
	if err := clients.registerCredential(context.Background(), "service-a", adminClientCredential{ID: p.CredentialID, Kind: p.Kind, Identity: "dns:service-a", Subject: p.Subject, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	p2 := Principal{Subject: p.Subject, Kind: "oauth", CredentialID: "oauth-a", Issuer: "test-issuer"}
	if err := clients.registerCredential(context.Background(), "service-a", adminClientCredential{ID: p2.CredentialID, Kind: p2.Kind, Identity: "oauth-client-a", Subject: p2.Subject, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"adminkey", "federated"} {
		id := kind + "-a"
		if err := clients.registerCredential(context.Background(), "service-a", adminClientCredential{ID: id, Kind: kind, Identity: id, Subject: p.Subject, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	wrapped := &adminClientBindingAuthenticator{inner: adminClientTestAuthenticator{p}, store: func() *adminClientStore { return clients }}
	wrapped2 := &adminClientBindingAuthenticator{inner: adminClientTestAuthenticator{p2}, store: func() *adminClientStore { return clients }}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	if got, err := wrapped.Authenticate(request.Context(), request); err != nil || got != p {
		t.Fatalf("active credential=(%+v,%v)", got, err)
	}
	// A renewed certificate has a new fingerprint, but the registered SAN and
	// subject remain stable and identify the same credential binding.
	renewed := p
	renewed.CredentialID = "new-cert-fingerprint"
	wrappedRenewed := &adminClientBindingAuthenticator{inner: adminClientTestAuthenticator{renewed}, store: func() *adminClientStore { return clients }}
	if _, err := wrappedRenewed.Authenticate(request.Context(), request); err != nil {
		t.Fatalf("renewed mTLS credential was not matched by SAN: %v", err)
	}
	wrongSubject := p
	wrongSubject.Subject = "spiffe://example/other"
	if _, err := (&adminClientBindingAuthenticator{inner: adminClientTestAuthenticator{wrongSubject}, store: func() *adminClientStore { return clients }}).Authenticate(request.Context(), request); err != ErrInvalidCredential {
		t.Fatalf("credential with mismatched subject error=%v", err)
	}
	if err := clients.setStatus(context.Background(), "service-a", "disabled"); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.Authenticate(request.Context(), request); err != ErrInvalidCredential {
		t.Fatalf("disabled credential error=%v", err)
	}
	if _, err := wrapped2.Authenticate(request.Context(), request); err != ErrInvalidCredential {
		t.Fatalf("second disabled credential error=%v", err)
	}
	for _, principal := range []Principal{
		p,
		p2,
		{Subject: "spiffe://example/service-a", Identity: "fed-a", Kind: "federated", CredentialID: "fed-renewed"},
		{Subject: "spiffe://example/service-a", Kind: "adminkey", CredentialID: "unregistered-key"},
	} {
		wrapped := &adminClientBindingAuthenticator{inner: adminClientTestAuthenticator{principal}, store: func() *adminClientStore { return clients }}
		if _, err := wrapped.Authenticate(request.Context(), request); err != ErrInvalidCredential {
			t.Fatalf("disabled client credential kind %q was allowed: %v", principal.Kind, err)
		}
	}
	unknownMTLS := Principal{Subject: "spiffe://example/service-a", Identity: "uri:spiffe://example/renewed", Kind: "mtls", CredentialID: "renewed-fingerprint"}
	if _, err := (&adminClientBindingAuthenticator{inner: adminClientTestAuthenticator{unknownMTLS}, store: func() *adminClientStore { return clients }}).Authenticate(request.Context(), request); err != ErrInvalidCredential {
		t.Fatalf("unregistered mTLS credential for disabled subject error=%v", err)
	}
	noCredentialID := Principal{Subject: "spiffe://example/service-a", Kind: "oauth"}
	if _, err := (&adminClientBindingAuthenticator{inner: adminClientTestAuthenticator{noCredentialID}, store: func() *adminClientStore { return clients }}).Authenticate(request.Context(), request); err != ErrInvalidCredential {
		t.Fatalf("credential without ID for disabled subject error=%v", err)
	}
}

func TestAdminClientStoreLimitIgnoresDeletedClients(t *testing.T) {
	_, db := newAdminKeyStoreForTest(t)
	if err := applyAdminClientSchema(db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 16, 4, 0, 0, 0, time.UTC)
	for i := 0; i < adminClientMaxClients; i++ {
		id := fmt.Sprintf("deleted-%04d", i)
		if err := db.Exec("INSERT INTO "+adminClientTable+" (id,name,status,created_at,updated_at) VALUES (?,?, 'deleted',?,?)", id, id, now, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	store := &adminClientStore{db: db, now: func() time.Time { return now }}
	if err := store.create(context.Background(), adminClient{ID: "active-client", Name: "Active client"}); err != nil {
		t.Fatalf("deleted clients consumed the active client limit: %v", err)
	}
}

func TestAdminClientListAndDirectGetExcludeDeletedWithoutHidingLaterActive(t *testing.T) {
	_, db := newAdminKeyStoreForTest(t)
	if err := applyAdminClientSchema(db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i := 0; i < adminClientMaxClients+1; i++ {
		id := fmt.Sprintf("deleted-%04d", i)
		if err := db.Exec("INSERT INTO "+adminClientTable+" (id,name,status,created_at,updated_at) VALUES (?,?, 'deleted',?,?)", id, id, now, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec("INSERT INTO "+adminClientTable+" (id,name,status,created_at,updated_at) VALUES (?,?, 'active',?,?)", "visible-client", "Visible", now, now).Error; err != nil {
		t.Fatal(err)
	}
	store := &adminClientStore{db: db, now: time.Now}
	rows, err := store.list(context.Background())
	if err != nil || len(rows) != 1 || rows[0].ID != "visible-client" {
		t.Fatalf("list rows=%d err=%v rows=%+v", len(rows), err, rows)
	}
	got, err := store.get(context.Background(), "deleted-1000")
	if err != nil || got.Status != "deleted" {
		t.Fatalf("direct get=%+v err=%v", got, err)
	}
}

func TestAdminClientRecoveryReRegistersFixedIdentity(t *testing.T) {
	_, db := newAdminKeyStoreForTest(t)
	if err := applyAdminClientSchema(db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	store := &adminClientStore{db: db, now: func() time.Time { return now }}
	if err := store.create(context.Background(), adminClient{ID: "recover", Name: "Recover"}); err != nil {
		t.Fatal(err)
	}
	cred := adminClientCredential{ID: "client_id:svc", Kind: "oauth", Identity: "client_id:svc", Subject: "svc", ExpiresAt: now.Add(time.Hour)}
	if err := store.registerCredential(context.Background(), "recover", cred); err != nil {
		t.Fatal(err)
	}
	if err := store.setStatus(context.Background(), "recover", "disabled"); err != nil {
		t.Fatal(err)
	}
	if err := store.setStatus(context.Background(), "recover", "active"); err != nil {
		t.Fatal(err)
	}
	if err := store.registerCredential(context.Background(), "recover", cred); err != nil {
		t.Fatalf("re-register fixed identity: %v", err)
	}
	ok, err := store.authenticate(context.Background(), Principal{Kind: "oauth", CredentialID: cred.ID, Subject: cred.Subject})
	if err != nil || !ok {
		t.Fatalf("recovered credential allowed=%v err=%v", ok, err)
	}
}

func TestAdminClientUnusedCredentialWarningAndGauge(t *testing.T) {
	_, db := newAdminKeyStoreForTest(t)
	if err := applyAdminClientSchema(db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	events := 0
	store := &adminClientStore{db: db, now: func() time.Time { return now }, recorder: func(_ *gorm.DB, ev adminClientLifecycleEvent) error {
		if ev.Action == "unused_warning" {
			events++
			if ev.ClientID != "unused" || ev.Actor != "system" || ev.RequestID == "" {
				t.Errorf("incomplete unused event: %+v", ev)
			}
		}
		return nil
	}}
	if err := store.create(context.Background(), adminClient{ID: "unused", Name: "Unused"}); err != nil {
		t.Fatal(err)
	}
	if err := store.registerCredential(context.Background(), "unused", adminClientCredential{ID: "never-used", Kind: "oauth", Identity: "client", Subject: "subject", ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := store.emitUnusedWarnings(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.emitUnusedWarnings(context.Background()); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("unused warnings=%d want one", events)
	}
	count, err := store.unusedCount(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("unused count=%d err=%v", count, err)
	}
	metrics := newAdminMetricsStore()
	metrics.SetAdminClientCredentialsUnused(count)
	if !strings.Contains(metrics.Prometheus(nil), "metrum_ai_router_admin_client_credentials_unused 1") {
		t.Fatal("unused gauge missing")
	}
}

func TestAdminClientAuthenticatorFailsClosedWhenStoreFails(t *testing.T) {
	_, db := newAdminKeyStoreForTest(t)
	if err := applyAdminClientSchema(db); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	principal := Principal{Subject: "client:unavailable", Kind: "oauth", CredentialID: "oauth-x"}
	wrapped := &adminClientBindingAuthenticator{inner: adminClientTestAuthenticator{principal}, store: func() *adminClientStore { return &adminClientStore{db: db, now: time.Now} }}
	if _, err := wrapped.Authenticate(context.Background(), httptest.NewRequest(http.MethodGet, "/", nil)); err != ErrInvalidCredential {
		t.Fatalf("store failure authentication error=%v", err)
	}
}

func TestAdminClientCredentialExpiryGateAndReviewFields(t *testing.T) {
	_, db := newAdminKeyStoreForTest(t)
	if err := applyAdminClientSchema(db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	clients := &adminClientStore{db: db, now: func() time.Time { return now }}
	if err := clients.create(context.Background(), adminClient{ID: "service-b", Name: "Service B"}); err != nil {
		t.Fatal(err)
	}
	p := Principal{Subject: "client:service-b", Kind: "oauth", CredentialID: "oauth-client-b"}
	if err := clients.registerCredential(context.Background(), "service-b", adminClientCredential{ID: p.CredentialID, Kind: p.Kind, Identity: "oauth-client-b", Subject: p.Subject, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	wrapped := &adminClientBindingAuthenticator{inner: adminClientTestAuthenticator{p}, store: func() *adminClientStore { return clients }}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, err := wrapped.Authenticate(request.Context(), request); err != nil {
		t.Fatal(err)
	}
	rows, err := clients.list(context.Background())
	if err != nil || len(rows) != 1 || len(rows[0].Credentials) != 1 || rows[0].Credentials[0].LastUsedAt == nil {
		t.Fatalf("usage metadata=%+v err=%v", rows, err)
	}
	lastUsed := *rows[0].Credentials[0].LastUsedAt
	now = now.Add(2 * time.Hour)
	if _, err := wrapped.Authenticate(request.Context(), request); err != ErrInvalidCredential {
		t.Fatalf("expired credential error=%v", err)
	}
	rows, err = clients.list(context.Background())
	if err != nil || len(rows) != 1 || len(rows[0].Credentials) != 1 || rows[0].Credentials[0].LastUsedAt == nil || !rows[0].Credentials[0].LastUsedAt.Equal(lastUsed) {
		t.Fatalf("rejected expired credential changed last-used metadata: %+v err=%v", rows, err)
	}
}

func TestAdminClientCredentialRejectsInferenceTokenKinds(t *testing.T) {
	now := time.Now().UTC()
	for _, kind := range []string{"caller", "inference", "bearer"} {
		if validAdminClientCredential(adminClientCredential{ID: "c", Kind: kind, Subject: "client:x", ExpiresAt: now.Add(time.Hour)}, now) {
			t.Fatalf("accepted inference token kind %q", kind)
		}
	}
}

func TestAdminClientExpiryWarningIsDurableAndExportedAsMetric(t *testing.T) {
	_, db := newAdminKeyStoreForTest(t)
	if err := applyAdminClientSchema(db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var events int
	store := &adminClientStore{db: db, now: func() time.Time { return now }, recorder: func(_ *gorm.DB, event adminClientLifecycleEvent) error {
		if event.Action == "expiry_warning" {
			events++
			if event.ClientID != "service-c" || event.Actor != "system" || event.RequestID == "" {
				t.Errorf("incomplete warning lifecycle event: %+v", event)
			}
		}
		return nil
	}}
	if err := store.create(context.Background(), adminClient{ID: "service-c", Name: "Service C"}); err != nil {
		t.Fatal(err)
	}
	if err := store.registerCredential(context.Background(), "service-c", adminClientCredential{ID: "credential-c", Kind: "oauth", Identity: "client-c", Subject: "client:service-c", ExpiresAt: now.Add(24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if rows, err := store.warningCredentials(context.Background(), 30*24*time.Hour); err != nil || len(rows) != 1 {
		t.Fatalf("warning fixture rows=%+v err=%v", rows, err)
	}
	if err := store.emitDueExpiryWarnings(context.Background(), 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := store.emitDueExpiryWarnings(context.Background(), 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("expiry events=%d want exactly one", events)
	}
	count, err := store.expiringCount(context.Background(), 30*24*time.Hour)
	if err != nil || count != 1 {
		t.Fatalf("expiring count=%d err=%v", count, err)
	}
	metrics := newAdminMetricsStore()
	metrics.SetAdminCredentialsExpiring(count)
	if !strings.Contains(metrics.Prometheus(nil), "metrum_ai_router_admin_credentials_expiring_30d 1") {
		t.Fatal("expiry gauge missing from Prometheus")
	}
}

func TestAdminClientOperationRegistryIncludesLifecycleAndReview(t *testing.T) {
	registry, err := NewAdminV1Registry(adminClientOperations())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"listAdminClients": true, "getAdminClient": true, "createAdminClient": true, "getAdminClientAccessReview": true, "updateAdminClient": true, "disableAdminClient": true, "enableAdminClient": true, "deleteAdminClient": true, "registerAdminClientCredential": true, "revokeAdminClientCredential": true}
	for _, op := range registry.Operations() {
		if !want[op.ID] {
			continue
		}
		if op.Object != adminClientsObject || op.Domain != AdminDomainGlobal {
			t.Fatalf("bad client authorization tuple: %+v", op)
		}
		delete(want, op.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing client operations: %v", want)
	}
}

func TestAdminClientIDReservesAccessReviewRoute(t *testing.T) {
	if validAdminClientID("access-review") {
		t.Fatal("access-review route name was accepted as a client ID")
	}
	if !validAdminClientID("review-worker") {
		t.Fatal("ordinary client ID was rejected")
	}
}

func TestAdminClientCannotResurrectDeleteAndRevokedRowsDoNotConsumeCap(t *testing.T) {
	_, db := newAdminKeyStoreForTest(t)
	if err := applyAdminClientSchema(db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	store := &adminClientStore{db: db, now: func() time.Time { return now }}
	if err := store.create(context.Background(), adminClient{ID: "cap-client", Name: "Cap client"}); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < adminClientMaxCredentials; n++ {
		id := fmt.Sprintf("cred-%02d", n)
		if err := store.registerCredential(context.Background(), "cap-client", adminClientCredential{ID: id, Kind: "oauth", Identity: "identity-" + id, Subject: "subject-" + id, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	if err := store.revokeCredential(context.Background(), "cap-client", "cred-00"); err != nil {
		t.Fatal(err)
	}
	if err := store.registerCredential(context.Background(), "cap-client", adminClientCredential{ID: "cred-new", Kind: "oauth", Identity: "identity-new", Subject: "subject-new", ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatalf("revoked row consumed the credential cap: %v", err)
	}
	if err := store.setStatus(context.Background(), "cap-client", "deleted"); err != nil {
		t.Fatal(err)
	}
	if err := store.setStatus(context.Background(), "cap-client", "active"); !errors.Is(err, errAdminClientConflict) {
		t.Fatalf("deleted client re-enable error=%v", err)
	}
}

func TestAdminClientListKeepsActiveCredentialsAheadOfRevokedHistory(t *testing.T) {
	_, db := newAdminKeyStoreForTest(t)
	if err := applyAdminClientSchema(db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	store := &adminClientStore{db: db, now: func() time.Time { return now }}
	if err := store.create(context.Background(), adminClient{ID: "review-cap", Name: "Review cap"}); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < adminClientMaxCredentials+10; n++ {
		id := fmt.Sprintf("a-revoked-%02d", n)
		if err := db.Exec("INSERT INTO "+adminClientCredentialTable+" (id,client_id,kind,identity,subject,expires_at,status) VALUES (?,?,?,?,?,?,'revoked')", id, "review-cap", "oauth", "identity-"+id, "subject-"+id, now.Add(time.Hour)).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"z-active-one", "z-active-two"} {
		if err := store.registerCredential(context.Background(), "review-cap", adminClientCredential{ID: id, Kind: "oauth", Identity: "identity-" + id, Subject: "subject-" + id, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	clients, err := store.list(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 1 {
		t.Fatalf("client count=%d, want one", len(clients))
	}
	seen := map[string]bool{}
	for _, credential := range clients[0].Credentials {
		if credential.Status == "active" {
			seen[credential.ID] = true
		}
	}
	for _, id := range []string{"z-active-one", "z-active-two"} {
		if !seen[id] {
			t.Fatalf("active credential %s was hidden by revoked history: %+v", id, clients[0].Credentials)
		}
	}
}

func TestAdminClientPolicyInventoryExcludesUnregisteredCredentialForManagedSubject(t *testing.T) {
	_, db := newAdminKeyStoreForTest(t)
	if err := applyAdminClientSchema(db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	store := &adminClientStore{db: db, now: func() time.Time { return now }}
	if err := store.create(context.Background(), adminClient{ID: "managed-subject", Name: "Managed subject"}); err != nil {
		t.Fatal(err)
	}
	if err := store.registerCredential(context.Background(), "managed-subject", adminClientCredential{
		ID: "registered-key", Kind: "adminkey", Identity: "registered-key", Subject: "adminkey:shared",
		ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.revokeCredential(context.Background(), "managed-subject", "registered-key"); err != nil {
		t.Fatal(err)
	}
	generation := &adminMachineGeneration{keys: &adminConfiguredKeyCatalog{keys: map[string]*adminAPIKeyRuntime{
		"registered-key":   {keyID: "registered-key", subject: "adminkey:shared"},
		"unregistered-key": {keyID: "unregistered-key", subject: "adminkey:shared"},
	}}}
	admins, err := (adminClientPolicyInventory{base: generationInventory{generation: generation}, store: store, now: now}).enabledPolicyAdmins(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(admins) != 0 {
		t.Fatalf("managed subject with no working registration was counted as an administrator: %+v", admins)
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

func TestAdminImplicitPermissionsAcrossDomainsIncludesRoleOnlyAccess(t *testing.T) {
	a, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: []string{
		"p, role:reader, project/prod, admin:memberships, read",
		"g, service:worker, role:reader, project/prod",
	}}, nil, nil, AdminAuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	withoutDomain, err := a.enforcer.GetImplicitPermissionsForUser("service:worker")
	if err != nil {
		t.Fatal(err)
	}
	if len(withoutDomain) != 0 {
		t.Fatalf("fixture unexpectedly resolves role grant without domain: %+v", withoutDomain)
	}
	got, err := adminImplicitPermissionsAcrossDomains(a.enforcer, "service:worker")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, grant := range got {
		if len(grant) >= 4 && grant[1] == "project/prod" && grant[2] == "admin:memberships" && grant[3] == "read" {
			found = true
		}
	}
	if !found {
		t.Fatalf("role-derived grant missing from access-review permissions: %+v", got)
	}
}

func TestAdminClientLeastPrivilegeGuardChecksRoleOnlyDomain(t *testing.T) {
	_, db := newAdminKeyStoreForTest(t)
	if err := applyAdminClientSchema(db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	store := &adminClientStore{db: db, now: func() time.Time { return now }}
	if err := store.create(context.Background(), adminClient{ID: "role-client", Name: "Role client"}); err != nil {
		t.Fatal(err)
	}
	if err := store.registerCredential(context.Background(), "role-client", adminClientCredential{ID: "sub:worker", Kind: "oauth", Identity: "worker", Subject: "service:worker", ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	a, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: []string{
		"p, role:reader, project/prod, admin:memberships, read",
		"g, service:worker, role:reader, project/prod",
	}}, nil, nil, AdminAuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	jwt := &adminMachineJWTAuthenticator{cfg: adminMachineJWTConfig{Issuer: "https://issuer.example", Kind: "oauth", Subjects: []adminMachineJWTSubject{{Claim: "sub", Value: "worker", Subject: "service:worker"}}}}
	generation := &adminMachineGeneration{authorizer: a, jwt: &adminMachineJWTDispatcher{issuers: map[string]adminMachineJWTBinding{"https://issuer.example": {Authenticator: jwt}}}}
	svc := &Service{adminClients: store, configSnapshot: NewConfigSnapshot()}
	svc.configSnapshot.Store(&Config{})
	svc.authorizerSnapshot.Store(a)
	svc.machineGeneration.Store(generation)
	if err := svc.adminClientActorCoversClientPermissions(context.Background(), "narrow-actor", "role-client", nil); !errors.Is(err, ErrAdminForbidden) {
		t.Fatalf("role-only project/prod grant escaped least-privilege guard: %v", err)
	}
}

func TestAdminClientDisableRejectsRealJWTAndMTLSAtListener(t *testing.T) {
	e2e := newAdminKeyE2E(t)
	signing := machineJWTKey(t)
	var issuer string
	issuerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuer, "jwks_uri": issuer + "/keys"})
		case "/keys":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &signing.PublicKey, KeyID: "client-key", Algorithm: "ES256", Use: "sig"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuerServer.Close()
	issuer = issuerServer.URL
	jwtAuth, err := newAdminMachineJWTAuthenticator(context.Background(), adminMachineJWTConfig{allowInsecureLoopback: true, AllowPrivateNetwork: true, Issuer: issuer, Audience: "router-admin", Kind: "oauth", Subjects: []adminMachineJWTSubject{{Claim: "client_id", Value: "jwt-client", Subject: "client:jwt"}, {Claim: "client_id", Value: "unregistered", Subject: "client:mtls"}}}, issuerServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := newAdminMachineJWTDispatcher([]adminMachineJWTBinding{{Authenticator: jwtAuth}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ca, caKey, roots := certificateBindingCA(t)
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(77), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"client.example"}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatal(err)
	}
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	clientCert, err := tls.X509KeyPair(leafPEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	mtlsAuth, err := newAdminMTLSAuthenticator("test-ca", runtimeMTLSRootSource(func() *x509.CertPool { return roots }), map[string]string{"dns:client.example": "client:mtls"})
	if err != nil {
		t.Fatal(err)
	}
	g := e2e.service.machineGeneration.Load()
	policy, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: []string{
		"p, client:jwt, *, admin:clients, list", "p, client:mtls, *, admin:clients, list",
		"p, adminkey:bootstrap-admin, *, admin:clients, list|get|review|create|update|disable|enable|delete|register|revoke",
		"p, adminkey:bootstrap-admin, *, admin:keys, issue|list|get|rotate|revoke", "p, adminkey:bootstrap-admin, *, admin:policy, write|activate",
		"g, adminkey:bootstrap-admin, " + securityAdministratorRole + ", *",
	}}, nil, nil, e2e.service.cfg.Server.AdminAuth)
	if err != nil {
		t.Fatal(err)
	}
	g.authorizer = policy
	e2e.service.authorizerSnapshot.Store(policy)
	g.jwt = dispatcher.(*adminMachineJWTDispatcher)
	g.auth = &adminClientBindingAuthenticator{inner: NewAuthenticatorChain(dispatcher, mtlsAuth), store: func() *adminClientStore { return e2e.service.adminClients }}
	now := time.Now().UTC()
	for _, c := range []adminClient{{ID: "jwt-client", Name: "JWT"}, {ID: "mtls-client", Name: "mTLS"}} {
		if err := e2e.service.adminClients.create(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	if err := e2e.service.adminClients.registerCredential(context.Background(), "jwt-client", adminClientCredential{ID: "client_id:jwt-client", Kind: "oauth", Identity: "jwt-client", Subject: "client:jwt", ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := e2e.service.adminClients.registerCredential(context.Background(), "mtls-client", adminClientCredential{ID: "client-cert", Kind: "mtls", Identity: "dns:client.example", Subject: "client:mtls", ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{"iss": issuer, "aud": "router-admin", "client_id": "jwt-client", "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix()}
	jwt := machineSignedJWT(t, signing, "client-key", claims)
	listenerDebug := httptest.NewRequest(http.MethodGet, "https://admin.example/admin/v1/clients", nil)
	listenerDebug.Header.Set("Authorization", "Bearer "+jwt)
	if p, e := g.auth.Authenticate(context.Background(), listenerDebug); e != nil {
		t.Fatalf("generation authenticator failed JWT: %+v %v", p, e)
	}
	debugRequest := httptest.NewRequest(http.MethodGet, "https://admin.example/admin/v1/clients", nil)
	debugRequest.Header.Set("Authorization", "Bearer "+jwt)
	verified, err := dispatcher.Authenticate(context.Background(), debugRequest)
	if err != nil {
		t.Fatalf("real signed JWT did not authenticate: %+v %v", verified, err)
	}
	if verified.Subject != "client:jwt" || verified.CredentialID != "client_id:jwt-client" {
		t.Fatalf("JWT principal=%+v", verified)
	}
	if ok, e := e2e.service.adminClients.authenticate(context.Background(), verified); e != nil || !ok {
		t.Fatalf("binding store rejects JWT principal %+v: %v %v", verified, ok, e)
	}
	requestJWT := func(client *http.Client, baseURL, raw string) int {
		req, _ := http.NewRequest(http.MethodGet, baseURL+"/admin/v1/clients", nil)
		if raw != "" {
			req.Header.Set("Authorization", "Bearer "+raw)
		}
		resp, er := client.Do(req)
		if er != nil {
			t.Fatal(er)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if got := requestJWT(e2e.server.Client(), e2e.server.URL, jwt); got != http.StatusOK {
		t.Fatalf("real JWT listener status=%d", got)
	}
	mtlsServer := httptest.NewUnstartedServer(e2e.service.AdminHandler())
	mtlsServer.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}
	mtlsServer.StartTLS()
	defer mtlsServer.Close()
	mtlsClient := mtlsServer.Client()
	mtlsClient.Transport.(*http.Transport).TLSClientConfig.Certificates = []tls.Certificate{clientCert}
	if got := requestJWT(mtlsClient, mtlsServer.URL, ""); got != http.StatusOK {
		t.Fatalf("real certificate listener status=%d", got)
	}
	if err := e2e.service.adminClients.setStatus(context.Background(), "jwt-client", "disabled"); err != nil {
		t.Fatal(err)
	}
	if err := e2e.service.adminClients.setStatus(context.Background(), "mtls-client", "disabled"); err != nil {
		t.Fatal(err)
	}
	if got := requestJWT(e2e.server.Client(), e2e.server.URL, jwt); got != http.StatusUnauthorized {
		t.Fatalf("disabled real JWT status=%d want401", got)
	}
	if got := requestJWT(e2e.server.Client(), e2e.server.URL, machineSignedJWT(t, signing, "client-key", map[string]any{"iss": issuer, "aud": "router-admin", "client_id": "unregistered", "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix()})); got != http.StatusUnauthorized {
		t.Fatalf("unregistered JWT of managed mTLS subject status=%d want401", got)
	}
	if got := requestJWT(mtlsClient, mtlsServer.URL, ""); got != http.StatusUnauthorized {
		t.Fatalf("disabled real certificate status=%d want401", got)
	}
	_ = leaf
}

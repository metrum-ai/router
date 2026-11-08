// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestRuntimeGroupedTrustBoundTokenWithoutIdentityMap(t *testing.T) {
	ca, key, _ := certificateBindingCA(t)
	leaf := certificateBindingLeaf(t, ca, key, "valid")
	path := filepath.Join(t.TempDir(), "ca.pem")
	trustStoreWriteCA(t, path, ca)
	store, err := NewAdminMTLSTrustStore(AdminMTLSTrustConfig{TrustDomains: []AdminMTLSTrustDomainConfig{{Issuer: "trusted-ca", CAFiles: []string{path}}}})
	if err != nil {
		t.Fatal(err)
	}
	mapper, err := newAdminMTLSTrustAuthenticator(store)
	if err != nil {
		t.Fatal(err)
	}
	issuer, signingKey := runtimeJWTIssuerFixture(t, "token-owner", "federated")
	tokens, err := newAdminMachineJWTDispatcher([]adminMachineJWTBinding{{Authenticator: issuer}}, nil, store.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	claims := runtimeJWTClaims(issuer.cfg.Issuer)
	claims["cnf"] = map[string]string{"x5t#S256": certificateBindingThumb(leaf)}
	r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
	r.Header.Set("Authorization", "Bearer "+machineSignedJWT(t, signingKey, "known", claims))
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf, ca}}}
	chain := NewAuthenticatorChain(tokens, mapper)
	if p, err := chain.Authenticate(r.Context(), r); err != nil || p.Subject != "token-owner" || p.Kind != "federated" {
		t.Errorf("union-bound token required unrelated SAN identity: %v", err)
	}
	r.Header.Del("Authorization")
	if p, err := chain.Authenticate(r.Context(), r); !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) {
		t.Fatal("possession-only group became certificate identity")
	}
}

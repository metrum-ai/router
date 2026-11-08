// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRuntimeCertificateBoundTokenWithRealMTLSMapping(t *testing.T) {
	for _, kind := range []string{"oauth", "federated"} {
		for _, multi := range []bool{false, true} {
			name := kind + "/single"
			if multi {
				name = kind + "/issuer-aware"
			}
			t.Run(name, func(t *testing.T) {
				a, key := runtimeJWTIssuerFixture(t, "token-owner", kind)
				ca, caKey, roots := certificateBindingCA(t)
				leaf := certificateBindingLeaf(t, ca, caKey, "valid")
				source := runtimeMTLSRootSource(func() *x509.CertPool { return roots })
				subjects := map[string]string{"dns:unrelated.example": "certificate-only-identity"}
				var certificateIdentity Authenticator
				var err error
				if multi {
					certificateIdentity, err = newAdminMTLSIssuerDispatcher([]adminMTLSTrustDomain{{Issuer: "trusted-ca", Roots: source, Subjects: subjects}})
				} else {
					certificateIdentity, err = newAdminMTLSAuthenticator("trusted-ca", source, subjects)
				}
				if err != nil {
					t.Fatal(err)
				}
				tokens, err := newAdminMachineJWTDispatcher([]adminMachineJWTBinding{{Authenticator: a}}, nil, source.Snapshot)
				if err != nil {
					t.Fatal(err)
				}
				claims := runtimeJWTClaims(a.cfg.Issuer)
				claims["cnf"] = map[string]string{"x5t#S256": certificateBindingThumb(leaf)}
				raw := machineSignedJWT(t, key, "known", claims)
				r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
				r.Header.Set("Authorization", "Bearer "+raw)
				r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf, ca}}}
				fallbackCalls := 0
				fallback := testMachineAuthenticator(func(context.Context, *http.Request) (Principal, error) {
					fallbackCalls++
					return Principal{}, ErrNoCredential
				})
				chain := NewAuthenticatorChain(tokens, certificateIdentity, fallback)
				if p, err := chain.Authenticate(r.Context(), r); err != nil || p.Subject != "token-owner" || p.Kind != kind {
					t.Errorf("real SAN mapper rejected verified token possession: %v", err)
				}
				certificateOnly := r.Clone(r.Context())
				certificateOnly.Header.Del("Authorization")
				fallbackCalls = 0
				if p, err := chain.Authenticate(certificateOnly.Context(), certificateOnly); !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) || fallbackCalls != 0 {
					t.Error("unmapped certificate-only credential accepted or fell through")
				}
				// Only pure certificate identity mapping may be redundant. A credential
				// requiring an emergency activation/Use acknowledgement must never skip.
				emergencyCalls := 0
				emergency := WithEmergencyCredentialType(testMachineAuthenticator(func(context.Context, *http.Request) (Principal, error) {
					emergencyCalls++
					return Principal{}, ErrInvalidCredential
				}), EmergencyCertificate)
				if p, err := NewAuthenticatorChain(tokens, certificateIdentity, emergency).Authenticate(r.Context(), r); !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) || emergencyCalls != 1 {
					t.Error("certificate possession bypassed emergency credential validation")
				}
			})
		}
	}
}

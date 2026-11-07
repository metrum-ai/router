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

// These are chain/possession integration tests, not JWT signature E2E tests:
// the token mechanism models an already verified token and mapped Principal.
// Actual token validation is added with the issuer dispatcher/listener wiring.
func TestMachineChainCertificateBoundTokenNeedsNoMappedSAN(t *testing.T) {
	ca, key, roots := certificateBindingCA(t)
	leaf := certificateBindingLeaf(t, ca, key, "valid")
	if len(leaf.URIs) != 0 || len(leaf.DNSNames) != 0 {
		t.Fatal("fixture unexpectedly has mapped identity SANs")
	}
	want := Principal{Subject: "workload:token-owner", Kind: "oauth", CredentialID: "issuer:workload", Issuer: "https://issuer.example"}
	for _, bad := range []bool{false, true} {
		name := "valid-unmapped-certificate"
		if bad {
			name = "invalid-possession-no-fallback"
		}
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
			r.Header.Set("Authorization", "Bearer verified-access-token")
			r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf, ca}}}
			thumb := certificateBindingThumb(leaf)
			if bad {
				thumb = certificateBindingThumb(ca)
			}
			token := testMachineAuthenticator(func(ctx context.Context, r *http.Request) (Principal, error) {
				if !validateAdminCertificateBoundToken(ctx, r, func() *x509.CertPool { return roots }, thumb) {
					return Principal{}, ErrInvalidCredential
				}
				return want, nil
			})
			fallbackCalls := 0
			fallback := testMachineAuthenticator(func(context.Context, *http.Request) (Principal, error) {
				fallbackCalls++
				return Principal{}, ErrNoCredential
			})
			got, err := NewAuthenticatorChain(token, fallback).Authenticate(r.Context(), r)
			if bad {
				if !errors.Is(err, ErrInvalidCredential) || got != (Principal{}) || fallbackCalls != 0 {
					t.Fatal("invalid possession fell through")
				}
				return
			}
			if err != nil || got != want {
				t.Fatalf("verified token principal rejected without SAN mapping: %+v %v", got, err)
			}
		})
	}
}

func TestMachineChainCertificateProofCannotCrossMechanisms(t *testing.T) {
	ca, key, roots := certificateBindingCA(t)
	leaf := certificateBindingLeaf(t, ca, key, "valid")
	r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
	r.Header.Set("Authorization", "Bearer verified-access-token")
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf, ca}}}
	discarded := testMachineAuthenticator(func(ctx context.Context, r *http.Request) (Principal, error) {
		if !validateAdminCertificateBoundToken(ctx, r, func() *x509.CertPool { return roots }, certificateBindingThumb(leaf)) {
			t.Fatal("valid possession fixture rejected")
		}
		return Principal{}, ErrNoCredential
	})
	withoutProof := testMachineAuthenticator(func(context.Context, *http.Request) (Principal, error) {
		return Principal{Subject: "other-token", Kind: "oauth"}, nil
	})
	if p, err := NewAuthenticatorChain(discarded, withoutProof).Authenticate(r.Context(), r); !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) {
		t.Fatal("proof escaped unsuccessful mechanism")
	}
}

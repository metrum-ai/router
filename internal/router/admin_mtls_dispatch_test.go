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
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type runtimeMTLSRootSource func() *x509.CertPool

func (f runtimeMTLSRootSource) Snapshot() *x509.CertPool { return f() }
func runtimeMTLSLeaf(t *testing.T, ca *x509.Certificate, key *ecdsa.PrivateKey) *x509.Certificate {
	t.Helper()
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(9), DNSNames: []string{"robot.example"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &leafKey.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}
func TestRuntimeMTLSIssuerDispatchFromVerifiedChain(t *testing.T) {
	caA, keyA, rootsA := certificateBindingCA(t)
	caB, keyB, rootsB := certificateBindingCA(t)
	caC, keyC, _ := certificateBindingCA(t)
	leafA := runtimeMTLSLeaf(t, caA, keyA)
	leafB := runtimeMTLSLeaf(t, caB, keyB)
	leafC := runtimeMTLSLeaf(t, caC, keyC)
	// All roots have the same empty DN, and all leaves have the same SAN. Only
	// actual chain verification may select the configured issuer-qualified map.
	domains := []adminMTLSTrustDomain{{Issuer: "issuer-a", Roots: runtimeMTLSRootSource(func() *x509.CertPool { return rootsA }), Subjects: map[string]string{"dns:robot.example": "subject-a"}}, {Issuer: "issuer-b", Roots: runtimeMTLSRootSource(func() *x509.CertPool { return rootsB }), Subjects: map[string]string{"dns:robot.example": "subject-b"}}}
	for _, tc := range []struct {
		name     string
		leaf, ca *x509.Certificate
		want     string
	}{{"issuer-a", leafA, caA, "subject-a"}, {"issuer-b-not-first", leafB, caB, "subject-b"}, {"untrusted-issuer", leafC, caC, ""}} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := newAdminMTLSIssuerDispatcher(domains)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
			r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{tc.leaf}, VerifiedChains: [][]*x509.Certificate{{tc.leaf, tc.ca}}}
			fallbackCalls := 0
			fallback := testMachineAuthenticator(func(context.Context, *http.Request) (Principal, error) {
				fallbackCalls++
				return Principal{}, ErrNoCredential
			})
			p, err := NewAuthenticatorChain(a, fallback).Authenticate(r.Context(), r)
			if tc.want != "" {
				if err != nil || p.Subject != tc.want {
					t.Fatal("wrong trust-domain selection", p, err)
				}
			} else if !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) || fallbackCalls != 0 {
				t.Fatal("untrusted certificate fell through")
			}
		})
	}
	domains[1].Subjects = map[string]string{"dns:other.example": "unrelated"}
	a, err := newAdminMTLSIssuerDispatcher(domains)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leafB}, VerifiedChains: [][]*x509.Certificate{{leafB, caB}}}
	if p, err := a.Authenticate(r.Context(), r); !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) {
		t.Fatal("issuer B certificate borrowed issuer A SAN mapping")
	}
	domains[1].Roots = domains[0].Roots
	a, err = newAdminMTLSIssuerDispatcher(domains)
	if err != nil {
		t.Fatal(err)
	}
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leafA}, VerifiedChains: [][]*x509.Certificate{{leafA, caA}}}
	if p, err := a.Authenticate(r.Context(), r); !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) {
		t.Fatal("ambiguous trust domain silently preferred one issuer")
	}
}

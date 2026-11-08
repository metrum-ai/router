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

func TestMachineCertificateCoverageIsRequestAndMechanismBound(t *testing.T) {
	ca, key, roots := certificateBindingCA(t)
	leaf := certificateBindingLeaf(t, ca, key, "valid")
	other := certificateBindingLeaf(t, ca, key, "valid")
	for _, mode := range []string{"wrong-kind", "failed-after-valid", "changed-authorization", "changed-certificate", "new-request"} {
		t.Run(mode, func(t *testing.T) {
			makeRequest := func() *http.Request {
				r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
				r.Header.Set("Authorization", "Bearer verified-access-token")
				r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf, ca}}}
				return r
			}
			want := Principal{Subject: "workload:token-owner", Kind: "oauth"}
			calls := 0
			mechanism := testMachineAuthenticator(func(ctx context.Context, r *http.Request) (Principal, error) {
				calls++
				if mode == "new-request" && calls == 2 {
					return want, nil
				}
				if !validateAdminCertificateBoundToken(ctx, r, func() *x509.CertPool { return roots }, certificateBindingThumb(leaf)) {
					t.Fatal("valid possession fixture rejected")
				}
				switch mode {
				case "wrong-kind":
					return Principal{Subject: "key-owner", Kind: "adminkey"}, nil
				case "failed-after-valid":
					if validateAdminCertificateBoundToken(ctx, r, func() *x509.CertPool { return roots }, certificateBindingThumb(ca)) {
						t.Fatal("bad thumb accepted")
					}
				case "changed-authorization":
					r.Header.Set("Authorization", "Bearer different-token")
				case "changed-certificate":
					r.TLS.PeerCertificates = []*x509.Certificate{other}
				}
				return want, nil
			})
			chain := NewAuthenticatorChain(mechanism)
			if mode == "new-request" {
				if p, err := chain.Authenticate(context.Background(), makeRequest()); err != nil || p != want {
					t.Fatal("initial valid binding failed", err)
				}
			}
			if p, err := chain.Authenticate(context.Background(), makeRequest()); !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) {
				t.Fatal("binding evidence escaped scope")
			}
		})
	}
}

func TestMachineCertificateBindingIntermediateChain(t *testing.T) {
	root, rootKey, roots := certificateBindingCA(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(3), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, root, &key.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	intermediate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	leaf := certificateBindingLeaf(t, intermediate, key, "valid")
	r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
	r.Header.Set("Authorization", "Bearer verified-access-token")
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf, intermediate}, VerifiedChains: [][]*x509.Certificate{{leaf, intermediate, root}}}
	want := Principal{Subject: "workload:federated", Kind: "federated", Issuer: "https://issuer.example"}
	token := testMachineAuthenticator(func(ctx context.Context, r *http.Request) (Principal, error) {
		if !validateAdminCertificateBoundToken(ctx, r, func() *x509.CertPool { return roots }, certificateBindingThumb(leaf)) {
			return Principal{}, ErrInvalidCredential
		}
		return want, nil
	})
	if p, err := NewAuthenticatorChain(token).Authenticate(r.Context(), r); err != nil || p != want {
		t.Fatal("leaf-intermediate-root token possession rejected", err)
	}
}

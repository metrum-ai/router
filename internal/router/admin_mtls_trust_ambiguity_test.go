// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeMTLSTrustStoreCrossSignedAmbiguity(t *testing.T) {
	caA, keyA, _ := certificateBindingCA(t)
	caB, keyB, _ := certificateBindingCA(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var intermediates []*x509.Certificate
	for i, parent := range []*x509.Certificate{caA, caB} {
		signer := keyA
		if i == 1 {
			signer = keyB
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(int64(100 + i)), Subject: pkix.Name{CommonName: "shared-intermediate"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
		der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, signer)
		if err != nil {
			t.Fatal(err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		intermediates = append(intermediates, cert)
	}
	leaf := runtimeMTLSLeaf(t, intermediates[0], key)
	dir := t.TempDir()
	pathA, pathB := filepath.Join(dir, "a.pem"), filepath.Join(dir, "b.pem")
	trustStoreWriteCA(t, pathA, caA)
	trustStoreWriteCA(t, pathB, caB)
	store, err := NewAdminMTLSTrustStore(AdminMTLSTrustConfig{TrustDomains: []AdminMTLSTrustDomainConfig{{Issuer: "a", CAFiles: []string{pathA}, Subjects: []AdminMTLSSubjectConfig{{SAN: "dns:robot.example", Subject: "subject-a"}}}, {Issuer: "b", CAFiles: []string{pathB}, Subjects: []AdminMTLSSubjectConfig{{SAN: "dns:robot.example", Subject: "subject-b"}}}}})
	if err != nil {
		t.Fatal("distinct configured root keys unexpectedly rejected", err)
	}
	identity, err := newAdminMTLSTrustAuthenticator(store)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf, intermediates[0], intermediates[1]}, VerifiedChains: [][]*x509.Certificate{{leaf, intermediates[0], caA}}}
	if p, err := identity.Authenticate(r.Context(), r); !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) {
		t.Fatal("runtime cross-signed ambiguity selected an authority")
	}
	r.Header.Set("Authorization", "Bearer verified-token")
	if !validateAdminCertificateBoundToken(r.Context(), r, store.Snapshot, certificateBindingThumb(leaf)) {
		t.Fatal("union possession incorrectly required certificate identity authority")
	}
}

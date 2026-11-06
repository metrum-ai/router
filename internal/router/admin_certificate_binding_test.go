// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"math/big"
	"net/http/httptest"
	"testing"
	"time"
)

func certificateBindingCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return cert, key, pool
}
func certificateBindingLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, mode string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Minute), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	switch mode {
	case "expired":
		template.NotBefore = time.Now().Add(-time.Hour)
		template.NotAfter = time.Now().Add(-time.Minute)
	case "future":
		template.NotBefore = time.Now().Add(30 * time.Second)
	case "server":
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
func certificateBindingThumb(cert *x509.Certificate) string {
	digest := sha256.Sum256(cert.Raw)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func TestAdminCertificateBoundToken(t *testing.T) {
	ca, key, roots := certificateBindingCA(t)
	_, _, wrongRoots := certificateBindingCA(t)
	for _, mode := range []string{"valid", "missing", "unverified", "wrong-ca", "wrong-thumb", "padded-thumb", "expired", "future", "server", "nil-roots", "forwarded-only"} {
		t.Run(mode, func(t *testing.T) {
			cert := certificateBindingLeaf(t, ca, key, mode)
			thumb := certificateBindingThumb(cert)
			r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
			r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert, ca}}}
			current := roots
			switch mode {
			case "missing":
				r.TLS.PeerCertificates = nil
			case "unverified":
				r.TLS.VerifiedChains = nil
			case "wrong-ca":
				current = wrongRoots
			case "wrong-thumb":
				thumb = certificateBindingThumb(ca)
			case "padded-thumb":
				thumb += "="
			case "nil-roots":
				current = nil
			case "forwarded-only":
				r.TLS = nil
				r.Header.Set("X-Forwarded-Client-Cert", thumb)
			}
			got := validateAdminCertificateBoundToken(r.Context(), r, func() *x509.CertPool { return current }, thumb)
			if got != (mode == "valid") {
				t.Fatalf("certificate binding accepted=%v", got)
			}
		})
	}
	cert := certificateBindingLeaf(t, ca, key, "valid")
	r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert, ca}}}
	current := roots
	source := func() *x509.CertPool { return current }
	if !validateAdminCertificateBoundToken(r.Context(), r, source, certificateBindingThumb(cert)) {
		t.Fatal("valid initial binding rejected")
	}
	current = wrongRoots
	if validateAdminCertificateBoundToken(r.Context(), r, source, certificateBindingThumb(cert)) {
		t.Fatal("existing connection retained removed CA")
	}
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	if validateAdminCertificateBoundToken(ctx, r, source, certificateBindingThumb(cert)) {
		t.Fatal("cancelled request accepted")
	}
}

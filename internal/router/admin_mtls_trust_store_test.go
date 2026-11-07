// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func trustStoreWriteCA(t *testing.T, path string, cert *x509.Certificate) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestRuntimeMTLSTrustStoreStartupAndReloadRules(t *testing.T) {
	caA, keyA, _ := certificateBindingCA(t)
	caB, _, _ := certificateBindingCA(t)
	dir := t.TempDir()
	pathA, pathB, pathReissue := filepath.Join(dir, "a.pem"), filepath.Join(dir, "b.pem"), filepath.Join(dir, "a-reissue.pem")
	trustStoreWriteCA(t, pathA, caA)
	trustStoreWriteCA(t, pathB, caB)
	template := *caA
	template.SerialNumber = big.NewInt(98765)
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &keyA.PublicKey, keyA)
	if err != nil {
		t.Fatal(err)
	}
	reissue, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	if string(reissue.Raw) == string(caA.Raw) || string(reissue.RawSubjectPublicKeyInfo) != string(caA.RawSubjectPublicKeyInfo) {
		t.Fatal("invalid distinct-certificate/same-SPKI fixture")
	}
	trustStoreWriteCA(t, pathReissue, reissue)
	domains := func() []AdminMTLSTrustDomainConfig {
		return []AdminMTLSTrustDomainConfig{{Issuer: "a", CAFiles: []string{pathA}, Subjects: []AdminMTLSSubjectConfig{{SAN: "dns:robot.example", Subject: "subject-a"}}}, {Issuer: "b", CAFiles: []string{pathB}, Subjects: []AdminMTLSSubjectConfig{{SAN: "dns:robot.example", Subject: "subject-b"}}}}
	}
	cfg := AdminMTLSTrustConfig{TrustDomains: domains()}
	store, err := NewAdminMTLSTrustStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	good := store.Snapshot()
	hooks := 0
	store.OnReload(func(*x509.CertPool) { hooks++ })
	for _, mode := range []string{"duplicate-issuer", "exact-root", "shared-spki", "mixed-flat"} {
		t.Run(mode, func(t *testing.T) {
			bad := AdminMTLSTrustConfig{TrustDomains: domains()}
			switch mode {
			case "duplicate-issuer":
				bad.TrustDomains[1].Issuer = "a"
			case "exact-root":
				bad.TrustDomains[1].CAFiles = []string{pathA}
			case "shared-spki":
				bad.TrustDomains[1].CAFiles = []string{pathReissue}
			case "mixed-flat":
				bad.CAFiles = []string{pathA}
				bad.Issuer = "flat"
			}
			if _, err := NewAdminMTLSTrustStore(bad); err == nil {
				t.Error("invalid startup accepted")
			}
			if err := store.ReloadConfig(bad); err == nil {
				t.Error("invalid config reload accepted")
			}
			if store.Snapshot() != good || hooks != 1 {
				t.Fatal("failed reload changed last-good union or emitted success")
			}
		})
	}
	trustStoreWriteCA(t, pathB, reissue)
	if err := store.Reload(); err == nil {
		t.Fatal("file reload accepted shared-SPKI root")
	}
	if store.Snapshot() != good || hooks != 1 {
		t.Fatal("file reload lost last good")
	}
	trustStoreWriteCA(t, pathB, caB)
	if err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	if store.Snapshot() == good || hooks != 2 {
		t.Fatal("valid replacement not published")
	}
}

func TestRuntimeMTLSTrustStoreDomainMapsFlatAndUnion(t *testing.T) {
	caA, keyA, _ := certificateBindingCA(t)
	caB, keyB, _ := certificateBindingCA(t)
	caC, keyC, _ := certificateBindingCA(t)
	dir := t.TempDir()
	pathA, pathB := filepath.Join(dir, "a.pem"), filepath.Join(dir, "b.pem")
	trustStoreWriteCA(t, pathA, caA)
	trustStoreWriteCA(t, pathB, caB)
	cfg := AdminMTLSTrustConfig{TrustDomains: []AdminMTLSTrustDomainConfig{{Issuer: "configured-a", CAFiles: []string{pathA}, Subjects: []AdminMTLSSubjectConfig{{SAN: "dns:robot.example", Subject: "subject-a"}}}, {Issuer: "configured-b", CAFiles: []string{pathB}, Subjects: []AdminMTLSSubjectConfig{{SAN: "dns:robot.example", Subject: "subject-b"}}}}}
	store, err := NewAdminMTLSTrustStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := newAdminMTLSTrustAuthenticator(store)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		leaf, ca        *x509.Certificate
		issuer, subject string
	}{{runtimeMTLSLeaf(t, caA, keyA), caA, "configured-a", "subject-a"}, {runtimeMTLSLeaf(t, caB, keyB), caB, "configured-b", "subject-b"}} {
		r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{tc.leaf}, VerifiedChains: [][]*x509.Certificate{{tc.leaf, tc.ca}}}
		if p, err := identity.Authenticate(r.Context(), r); err != nil || p.Issuer != tc.issuer || p.Subject != tc.subject {
			t.Fatal("authority selected from DN/SAN/order instead of verified domain", err)
		}
		r.Header.Set("Authorization", "Bearer verified-token")
		if !validateAdminCertificateBoundToken(r.Context(), r, store.Snapshot, certificateBindingThumb(tc.leaf)) {
			t.Fatal("union rejected configured domain")
		}
	}
	// No SAN identity mapping is required for bound-token possession.
	for _, tc := range []struct {
		leaf, ca *x509.Certificate
		valid    bool
	}{{certificateBindingLeaf(t, caA, keyA, "valid"), caA, true}, {certificateBindingLeaf(t, caB, keyB, "valid"), caB, true}, {certificateBindingLeaf(t, caA, keyA, "no-eku"), caA, false}, {certificateBindingLeaf(t, caB, keyB, "any-eku"), caB, false}, {certificateBindingLeaf(t, caA, keyA, "client-and-any"), caA, false}, {certificateBindingLeaf(t, caC, keyC, "valid"), caC, false}} {
		r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
		r.Header.Set("Authorization", "Bearer verified-token")
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{tc.leaf}, VerifiedChains: [][]*x509.Certificate{{tc.leaf, tc.ca}}}
		if got := validateAdminCertificateBoundToken(r.Context(), r, store.Snapshot, certificateBindingThumb(tc.leaf)); got != tc.valid {
			t.Fatal("union possession bypassed EKU/trust or required SAN mapping")
		}
	}
	flat, err := NewAdminMTLSTrustStore(AdminMTLSTrustConfig{Issuer: "configured-flat", CAFiles: []string{pathA}, Subjects: []AdminMTLSSubjectConfig{{SAN: "dns:robot.example", Subject: "flat-subject"}}})
	if err != nil {
		t.Fatal(err)
	}
	flatIdentity, err := newAdminMTLSTrustAuthenticator(flat)
	if err != nil {
		t.Fatal(err)
	}
	leaf := runtimeMTLSLeaf(t, caA, keyA)
	r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf, caA}}}
	if p, err := flatIdentity.Authenticate(r.Context(), r); err != nil || p.Issuer != "configured-flat" || p.Subject != "flat-subject" {
		t.Fatal("flat config not one explicitly named domain")
	}
}

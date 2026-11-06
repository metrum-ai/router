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
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type machineTestCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newMachineTestCA(t *testing.T) machineTestCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return machineTestCA{cert, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}
func (ca machineTestCA) client(t *testing.T, mode string) *x509.Certificate {
	t.Helper()
	return ca.clientKeyPair(t, mode).Leaf
}
func (ca machineTestCA) clientKeyPair(t *testing.T, mode string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	uri, _ := url.Parse("spiffe://test.example/workload")
	template := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "ignored"}, URIs: []*url.URL{uri}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(10 * time.Minute), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	switch mode {
	case "expired":
		template.NotBefore = time.Now().Add(-time.Hour)
		template.NotAfter = time.Now().Add(-time.Minute)
	case "future":
		template.NotBefore = time.Now().Add(time.Minute)
	case "server":
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	case "no-eku":
		template.ExtKeyUsage = nil
	case "any-eku":
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageAny}
	case "client-and-any":
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageAny}
	case "client-and-server":
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}
	case "extra-sans":
		extra, _ := url.Parse("spiffe://test.example/unmapped")
		template.URIs = append(template.URIs, extra)
		template.DNSNames = []string{"unmapped.example"}
	case "raw-uri-case":
		rawSAN, err := asn1.Marshal([]asn1.RawValue{{Class: 2, Tag: 6, Bytes: []byte("SPIFFE://test.example/workload")}})
		if err != nil {
			t.Fatal(err)
		}
		template.ExtraExtensions = []pkix.Extension{{Id: asn1.ObjectIdentifier{2, 5, 29, 17}, Value: rawSAN}}
	case "dns-mixed":
		template.URIs = nil
		template.DNSNames = []string{"WoRkLoAd.ExAmPlE"}
	case "unmapped":
		template.URIs[0], _ = url.Parse("spiffe://test.example/other")
	case "cn":
		template.URIs = nil
		template.Subject.CommonName = "spiffe://test.example/workload"
	case "email":
		template.URIs = nil
		template.EmailAddresses = []string{"workload@test.example"}
	case "dns":
		template.URIs = nil
		template.DNSNames = []string{"workload.example"}
	case "conflict":
		template.DNSNames = []string{"conflict.example"}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert}
}

func TestAdminMachineMTLSNativeSocketReload(t *testing.T) {
	oldCA := newMachineTestCA(t)
	newCA := newMachineTestCA(t)
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, oldCA.pem, 0600); err != nil {
		t.Fatal(err)
	}
	roots, err := NewAdminClientCAStore([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := newAdminMTLSAuthenticator("trust-domain", roots, map[string]string{"uri:spiffe://test.example/workload": "workload"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := auth.Authenticate(r.Context(), r); err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: roots.Snapshot()}
	server.TLS.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		cfg := server.TLS.Clone()
		cfg.GetConfigForClient = nil
		cfg.ClientCAs = roots.Snapshot()
		return cfg, nil
	}
	server.StartTLS()
	defer server.Close()
	clientFor := func(cert tls.Certificate) *http.Client {
		tr := server.Client().Transport.(*http.Transport).Clone()
		tr.TLSClientConfig = tr.TLSClientConfig.Clone()
		tr.TLSClientConfig.Certificates = []tls.Certificate{cert}
		tr.ForceAttemptHTTP2 = false
		t.Cleanup(tr.CloseIdleConnections)
		return &http.Client{Transport: tr, Timeout: 5 * time.Second}
	}
	oldClient := clientFor(oldCA.clientKeyPair(t, "valid"))
	check := func(client *http.Client, want int) {
		t.Helper()
		resp, err := client.Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("status %d want %d", resp.StatusCode, want)
		}
	}
	check(oldClient, http.StatusNoContent)
	if err := os.WriteFile(path, newCA.pem, 0600); err != nil {
		t.Fatal(err)
	}
	if err := roots.Reload(); err != nil {
		t.Fatal(err)
	}
	// The existing connection is still open; request-time verification revokes it.
	check(oldClient, http.StatusUnauthorized)
	check(clientFor(newCA.clientKeyPair(t, "valid")), http.StatusNoContent)
	rejected := clientFor(oldCA.clientKeyPair(t, "valid"))
	if resp, err := rejected.Get(server.URL); err == nil {
		resp.Body.Close()
		t.Fatal("old CA accepted on new TLS handshake")
	}
}

func TestAdminMachineMTLSMappingsAndForwardedHeaders(t *testing.T) {
	ca := newMachineTestCA(t)
	path := filepath.Join(t.TempDir(), "root.pem")
	if err := os.WriteFile(path, ca.pem, 0600); err != nil {
		t.Fatal(err)
	}
	roots, err := NewAdminClientCAStore([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	for _, san := range []string{"email:workload@example.com", "cn:workload", "dns:*.example.com", "uri:not-absolute", "uri:spiffe://test.example/workload?query=1"} {
		if _, err := newAdminMTLSAuthenticator("trust-domain", roots, map[string]string{san: "subject"}); err == nil {
			t.Fatalf("invalid mapping accepted: %s", san)
		}
	}
	mappings := map[string]string{"uri:spiffe://test.example/workload": "original-subject"}
	auth, err := newAdminMTLSAuthenticator("trust-domain", roots, mappings)
	if err != nil {
		t.Fatal(err)
	}
	mappings["uri:spiffe://test.example/workload"] = "mutated"
	r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
	r.Header.Set("X-Forwarded-Client-Cert", "spiffe://test.example/workload")
	if p, err := auth.Authenticate(r.Context(), r); !errors.Is(err, ErrNoCredential) || p != (Principal{}) {
		t.Fatalf("forwarded identity accepted: %+v %v", p, err)
	}
	cert := ca.client(t, "valid")
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert, ca.cert}}}
	if p, err := auth.Authenticate(r.Context(), r); err != nil || p.Subject != "original-subject" {
		t.Fatalf("mapping not copied: %+v %v", p, err)
	}
}

func TestAdminMachineMTLSValidation(t *testing.T) {
	ca := newMachineTestCA(t)
	foreign := newMachineTestCA(t)
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, ca.pem, 0600); err != nil {
		t.Fatal(err)
	}
	roots, err := NewAdminClientCAStore([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := newAdminMTLSAuthenticator("configured-trust-domain", roots, map[string]string{"uri:spiffe://test.example/workload": "workload", "dns:conflict.example": "different-subject", "dns:workload.example": "workload"})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"valid", "dns", "dns-mixed", "extra-sans", "client-and-server", "no-eku", "any-eku", "client-and-any", "raw-uri-case", "expired", "future", "server", "unmapped", "cn", "email", "conflict", "wrong-ca", "unverified"} {
		t.Run(mode, func(t *testing.T) {
			cert := ca.client(t, mode)
			if mode == "wrong-ca" {
				cert = foreign.client(t, "valid")
			}
			r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
			r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert, ca.cert}}}
			if mode == "unverified" {
				r.TLS.VerifiedChains = nil
			}
			p, err := auth.Authenticate(context.Background(), r)
			if mode == "valid" || mode == "dns" || mode == "dns-mixed" || mode == "extra-sans" || mode == "client-and-server" {
				if err != nil || p.Subject != "workload" || p.Kind != "mtls" || p.Issuer != "configured-trust-domain" || p.CredentialID == "" {
					t.Fatalf("valid client: %+v %v", p, err)
				}
			} else if !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) {
				t.Fatalf("invalid client accepted: %+v %v", p, err)
			}
		})
	}
	// Revalidation must reject an old connection immediately after CA replacement.
	cert := ca.client(t, "valid")
	r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert, ca.cert}}}
	if err := os.WriteFile(path, foreign.pem, 0600); err != nil {
		t.Fatal(err)
	}
	if err := roots.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Authenticate(r.Context(), r); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("removed root did not return terminal invalid credential: %v", err)
	}
}

func TestAdminMachineMTLSRawURIAndDNSCaseMappings(t *testing.T) {
	ca := newMachineTestCA(t)
	path := filepath.Join(t.TempDir(), "root.pem")
	if err := os.WriteFile(path, ca.pem, 0600); err != nil {
		t.Fatal(err)
	}
	roots, err := NewAdminClientCAStore([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newAdminMTLSAuthenticator("domain", roots, map[string]string{"dns:WORKLOAD.example": "one", "dns:workload.example": "two"}); err == nil {
		t.Fatal("conflicting case-folded DNS mappings accepted")
	}
	for _, tc := range []struct{ mapping, mode string }{
		{"dns:WORKLOAD.EXAMPLE", "dns"},
		{"uri:SPIFFE://test.example/workload", "raw-uri-case"},
	} {
		a, err := newAdminMTLSAuthenticator("domain", roots, map[string]string{tc.mapping: "workload"})
		if err != nil {
			t.Fatal(err)
		}
		cert := ca.client(t, tc.mode)
		r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert, ca.cert}}}
		if p, err := a.Authenticate(r.Context(), r); err != nil || p.Subject != "workload" {
			t.Fatalf("explicit mapping rejected: %v", err)
		}
	}
}

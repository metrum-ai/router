// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package tlsacme

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/metrum-ai/router/internal/tlsacme/dns"
)

// testCertificate is a self-signed leaf for tests.
type testCertificate struct {
	CertificatePEM []byte
	PrivateKeyPEM  []byte
	NotBefore      time.Time
	NotAfter       time.Time
}

// makeTestCertificate builds a self-signed certificate for domains.
func makeTestCertificate(t *testing.T, domains []string, notBefore, notAfter time.Time) testCertificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: domains[0]},
		DNSNames:              domains,
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		SubjectKeyId:          []byte{0x69, 0x88, 0x76, 0x55},
		AuthorityKeyId:        []byte{0x69, 0x88, 0x76, 0x55},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return testCertificate{
		CertificatePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		PrivateKeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		NotBefore:      notBefore,
		NotAfter:       notAfter,
	}
}

// stubCertifier returns a fixed certificate or a fixed error.
type stubCertifier struct {
	cert  testCertificate
	err   error
	calls int
}

func (s *stubCertifier) Issue(context.Context, []string) (issuedCertificate, error) {
	s.calls++
	if s.err != nil {
		return issuedCertificate{}, s.err
	}
	return issuedCertificate{
		CertificatePEM: s.cert.CertificatePEM,
		PrivateKeyPEM:  s.cert.PrivateKeyPEM,
		NotBefore:      s.cert.NotBefore,
		NotAfter:       s.cert.NotAfter,
		OrderURL:       "https://acme.test/order/1",
	}, nil
}

// fakeProvider is a DNS provider that reports one zone.
type fakeProvider struct{ zones []string }

func (f fakeProvider) Name() string                            { return "fake" }
func (f fakeProvider) Zones(context.Context) ([]string, error) { return f.zones, nil }
func (f fakeProvider) Records(context.Context, string) ([]dns.Record, error) {
	return nil, nil
}
func (f fakeProvider) Create(context.Context, string, dns.Record) (string, error) { return "id", nil }
func (f fakeProvider) Update(context.Context, string, string, dns.Record) error   { return nil }
func (f fakeProvider) Delete(context.Context, string, string) error               { return nil }

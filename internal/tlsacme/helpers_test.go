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
	"fmt"
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

// fakeProvider is an in-memory DNS provider for the manager tests. It records
// the calls it receives so the tests can assert what the manager asked for.
type fakeProvider struct {
	zones   []string
	records []dns.Record
	creates int
	updates int
	deletes int
	nextID  int
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Zones(context.Context) ([]string, error) { return f.zones, nil }

func (f *fakeProvider) Records(context.Context, string) ([]dns.Record, error) {
	out := make([]dns.Record, len(f.records))
	copy(out, f.records)
	return out, nil
}

func (f *fakeProvider) Create(_ context.Context, _ string, record dns.Record) (string, error) {
	f.creates++
	f.nextID++
	record.ID = fmt.Sprintf("id-%d", f.nextID)
	f.records = append(f.records, record)
	return record.ID, nil
}

func (f *fakeProvider) Update(_ context.Context, _ string, id string, record dns.Record) error {
	f.updates++
	for i := range f.records {
		if f.records[i].ID == id {
			record.ID = id
			f.records[i] = record
			return nil
		}
	}
	return fmt.Errorf("record %s not found", id)
}

func (f *fakeProvider) Delete(_ context.Context, _ string, id string) error {
	f.deletes++
	for i := range f.records {
		if f.records[i].ID == id {
			f.records = append(f.records[:i], f.records[i+1:]...)
			return nil
		}
	}
	return nil
}

// value returns the value of the first record with the given name and type.
func (f *fakeProvider) value(recordType, name string) string {
	for _, record := range f.records {
		if record.Type == recordType && record.Name == name {
			return record.Value
		}
	}
	return ""
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testAdminCA(t *testing.T, name string) ([]byte, *x509.Certificate) {
	t.Helper()
	return testAdminCACertificate(t, name, true)
}

func testAdminCACertificate(t *testing.T, name string, isCA bool) ([]byte, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: isCA, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), cert
}

func TestAdminClientCAReload(t *testing.T) {
	oldPEM, oldCA := testAdminCA(t, "old")
	newPEM, newCA := testAdminCA(t, "new")
	path := filepath.Join(t.TempDir(), "client-ca.pem")
	if err := os.WriteFile(path, oldPEM, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := NewAdminClientCAStore([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	oldSnapshot := source.Snapshot()
	count := 0
	source.OnReload(func(pool *x509.CertPool) {
		count++
		if pool != source.Snapshot() {
			t.Error("hook and request snapshot differ")
		}
	})
	if err := os.WriteFile(path, newPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := source.Reload(); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("initial plus reload callback count: %d", count)
	}
	if _, err := newCA.Verify(x509.VerifyOptions{Roots: source.Snapshot()}); err != nil {
		t.Fatal("new root rejected", err)
	}
	if _, err := oldCA.Verify(x509.VerifyOptions{Roots: source.Snapshot()}); err == nil {
		t.Fatal("old root retained")
	}
	if _, err := oldCA.Verify(x509.VerifyOptions{Roots: oldSnapshot}); err != nil {
		t.Fatal("old immutable snapshot mutated", err)
	}
	good := source.Snapshot()
	for _, bad := range [][]byte{nil, []byte("bad PEM"), append(newPEM, []byte("trailing garbage")...), append([]byte("-----BEGIN CERTIFICATE-----\n!!!!\n-----END CERTIFICATE-----\n"), newPEM...), append([]byte("-----BEGIN CERTIFICATE-----\n!!!!\n"), newPEM...)} {
		if err := os.WriteFile(path, bad, 0600); err != nil {
			t.Fatal(err)
		}
		if err := source.Reload(); err == nil {
			t.Fatal("malformed reload accepted")
		}
		if source.Snapshot() != good || count != 2 {
			t.Fatal("failed reload changed trust or called hook")
		}
	}
}

func TestAdminClientCAConcurrentSnapshotsAndReload(t *testing.T) {
	caPEM, cert := testAdminCA(t, "concurrent")
	path := filepath.Join(t.TempDir(), "root.pem")
	if err := os.WriteFile(path, caPEM, 0600); err != nil {
		t.Fatal(err)
	}
	files := []string{path}
	source, err := NewAdminClientCAStore(files)
	if err != nil {
		t.Fatal(err)
	}
	files[0] = "caller-mutated"
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				if err := source.Reload(); err != nil {
					t.Error(err)
				}
				if _, err := cert.Verify(x509.VerifyOptions{Roots: source.Snapshot()}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
}

func TestAdminClientCAStartupRejectsMissingEmptyAndNonCA(t *testing.T) {
	if _, err := NewAdminClientCAStore(nil); err == nil {
		t.Fatal("empty roots accepted")
	}
	if _, err := NewAdminClientCAStore([]string{filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("missing file accepted")
	}
	path := filepath.Join(t.TempDir(), "not-ca.pem")
	leaf, _ := testAdminCACertificate(t, "leaf", false)
	for _, data := range [][]byte{leaf, []byte(""), []byte("-----BEGIN PRIVATE KEY-----\ninvalid\n-----END PRIVATE KEY-----"), make([]byte, maxAdminCABundleBytes+1)} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewAdminClientCAStore([]string{path}); err == nil {
			t.Fatal("invalid CA source accepted")
		}
	}
}

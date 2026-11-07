// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package tlsacme

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRecord(domains []string) CertificateRecord {
	return CertificateRecord{
		Domains:        domains,
		CertificatePEM: []byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"),
		PrivateKeyPEM:  []byte("-----BEGIN PRIVATE KEY-----\nMIIB\n-----END PRIVATE KEY-----\n"),
		Issuer:         "https://acme.test/directory",
		NotBefore:      time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:       time.Date(2027, 1, 30, 0, 0, 0, 0, time.UTC),
		IssuedAt:       time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		KeyType:        KeyTypeECDSAP256,
	}
}

func TestStoreRoundTripAndPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls")
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("cache dir mode = %04o, want 0700", perm)
	}
	record := testRecord([]string{"router.example.com"})
	if err := store.SaveCertificate(record); err != nil {
		t.Fatalf("save: %v", err)
	}
	path := store.CertificatePath(record.Domains)
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat record: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("record mode = %04o, want 0600", perm)
	}
	loaded, err := store.LoadCertificate(record.Domains)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded == nil || !loaded.NotAfter.Equal(record.NotAfter) || loaded.Issuer != record.Issuer {
		t.Fatalf("loaded = %+v", loaded)
	}
	// Domain order must not matter for the store key.
	loaded, err = store.LoadCertificate([]string{"router.example.com"})
	if err != nil || loaded == nil {
		t.Fatalf("load by same set: %v %v", loaded, err)
	}
	// No partial temporary files remain.
	entries, err := os.ReadDir(filepath.Join(dir, "certs"))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
}

func TestStoreRefusesGroupReadableFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls")
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	path := store.CertificatePath([]string{"router.example.com"})
	raw, _ := json.Marshal(testRecord([]string{"router.example.com"}))
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := store.LoadCertificate([]string{"router.example.com"}); err == nil || !strings.Contains(err.Error(), "group- or world-accessible") {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenStoreTightensInsecureDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := OpenStore(dir); err != nil {
		t.Fatalf("open store: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("cache dir mode = %04o, want 0700", perm)
	}
}

func TestStoreMissingRecordIsNotAnError(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	record, err := store.LoadCertificate([]string{"router.example.com"})
	if err != nil || record != nil {
		t.Fatalf("record = %+v err = %v, want nil, nil", record, err)
	}
}

func TestStoreCorruptRecordFailsClosed(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	path := store.CertificatePath([]string{"router.example.com"})
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := store.LoadCertificate([]string{"router.example.com"}); err == nil {
		t.Fatal("expected a parse error")
	}
}

func TestStoreAccountRoundTrip(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	account := AccountRecord{
		DirectoryURL:  "https://acme.test/directory",
		PrivateKeyPEM: []byte("pem"),
		URL:           "https://acme.test/account/1",
		Contact:       []string{"mailto:ops@example.com"},
	}
	if err := store.SaveAccount(account); err != nil {
		t.Fatalf("save account: %v", err)
	}
	loaded, err := store.LoadAccount(account.DirectoryURL)
	if err != nil || loaded == nil || loaded.URL != account.URL {
		t.Fatalf("loaded = %+v err = %v", loaded, err)
	}
}

func TestStoreIssuerLockIsExclusive(t *testing.T) {
	dir := t.TempDir()
	first, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	second, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	release, err := first.TryLock()
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	if _, err := second.TryLock(); err == nil {
		t.Fatal("second lock must fail while the first is held")
	} else if err != ErrLockBusy && !strings.Contains(err.Error(), "another replica") {
		t.Fatalf("second lock error = %v, want the busy error", err)
	}
	release()
	release() // idempotent
	release2, err := second.TryLock()
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	release2()
}

func TestStoreCertificatesModified(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if !store.CertificatesModified().IsZero() {
		t.Fatal("an empty store has no modification time")
	}
	if err := store.SaveCertificate(testRecord([]string{"router.example.com"})); err != nil {
		t.Fatalf("save: %v", err)
	}
	if store.CertificatesModified().IsZero() {
		t.Fatal("modification time not reported")
	}
}

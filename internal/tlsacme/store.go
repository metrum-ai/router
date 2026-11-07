// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package tlsacme

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrLockBusy means another replica holds the issuer lock.
var ErrLockBusy = errors.New("issuer lock is held by another replica")

// ErrLockUnsupported means the filesystem cannot provide a POSIX file lock, so
// this replica must not issue certificates.
var ErrLockUnsupported = errors.New("issuer lock is not supported on this filesystem")

// CertificateRecord is what the store keeps for one certificate. It holds the
// leaf chain and its private key and no credential of any kind.
type CertificateRecord struct {
	Domains        []string  `json:"domains"`
	CertificatePEM []byte    `json:"certificate_pem"`
	PrivateKeyPEM  []byte    `json:"private_key_pem"`
	Issuer         string    `json:"issuer"`
	OrderURL       string    `json:"order_url,omitempty"`
	NotBefore      time.Time `json:"not_before"`
	NotAfter       time.Time `json:"not_after"`
	IssuedAt       time.Time `json:"issued_at"`
	KeyType        string    `json:"key_type"`
}

// AccountRecord is the ACME account key and registration for one directory.
type AccountRecord struct {
	DirectoryURL  string   `json:"directory_url"`
	PrivateKeyPEM []byte   `json:"private_key_pem"`
	URL           string   `json:"url"`
	Contact       []string `json:"contact,omitempty"`
}

// Store keeps certificates and the ACME account on the local filesystem with
// owner-only permissions, writes atomically and serialises issuance with a
// file lock shared by every replica that mounts the same directory.
type Store struct {
	dir string
}

// OpenStore creates (or opens) cacheDir with mode 0700 and rejects a directory
// that is group- or world-accessible.
func OpenStore(cacheDir string) (*Store, error) {
	dir := strings.TrimSpace(cacheDir)
	if dir == "" {
		return nil, fmt.Errorf("tls: cache_dir is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("tls: create cache_dir %s: %w", dir, err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("tls: stat cache_dir %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("tls: cache_dir %s is not a directory", dir)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		// A mounted volume may arrive group-readable; tighten it when we are
		// allowed to and refuse when we are not, so certificate keys are never
		// readable by another account on the host.
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, fmt.Errorf("tls: cache_dir %s is group- or world-accessible (%04o) and cannot be tightened to 0700: %w", dir, perm, err)
		}
		info, err = os.Stat(dir)
		if err != nil {
			return nil, fmt.Errorf("tls: stat cache_dir %s: %w", dir, err)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			return nil, fmt.Errorf("tls: cache_dir %s is group- or world-accessible (%04o); require 0700", dir, perm)
		}
	}
	for _, sub := range []string{"certs", "account"} {
		path := filepath.Join(dir, sub)
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, fmt.Errorf("tls: create %s: %w", path, err)
		}
	}
	return &Store{dir: dir}, nil
}

// Dir returns the store directory.
func (s *Store) Dir() string { return s.dir }

// CertificatePath returns the file that holds the certificate for domains.
func (s *Store) CertificatePath(domains []string) string {
	return filepath.Join(s.dir, "certs", storeKey(domains)+".json")
}

func (s *Store) accountPath(directoryURL string) string {
	sum := sha256.Sum256([]byte(directoryURL))
	return filepath.Join(s.dir, "account", hex.EncodeToString(sum[:8])+".json")
}

// storeKey derives a stable file name from a sorted domain set.
func storeKey(domains []string) string {
	sorted := append([]string(nil), domains...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, ",")))
	return hex.EncodeToString(sum[:8])
}

// LoadCertificate reads the record for domains. A missing record returns
// (nil, nil) so callers can distinguish "nothing cached" from a read error.
func (s *Store) LoadCertificate(domains []string) (*CertificateRecord, error) {
	path := s.CertificatePath(domains)
	raw, err := readPrivateFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var rec CertificateRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("tls: parse %s: %w", path, err)
	}
	return &rec, nil
}

// SaveCertificate writes the record atomically with mode 0600.
func (s *Store) SaveCertificate(rec CertificateRecord) error {
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("tls: encode certificate record: %w", err)
	}
	return writeFileAtomic(s.CertificatePath(rec.Domains), raw)
}

// LoadAccount reads the ACME account for directoryURL. A missing record
// returns (nil, nil).
func (s *Store) LoadAccount(directoryURL string) (*AccountRecord, error) {
	raw, err := readPrivateFile(s.accountPath(directoryURL))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var rec AccountRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("tls: parse account record: %w", err)
	}
	return &rec, nil
}

// SaveAccount writes the ACME account atomically with mode 0600.
func (s *Store) SaveAccount(rec AccountRecord) error {
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("tls: encode account record: %w", err)
	}
	return writeFileAtomic(s.accountPath(rec.DirectoryURL), raw)
}

// TryLock takes the issuer lock without blocking. The returned function
// releases it and is safe to call once. ErrLockBusy and ErrLockUnsupported are
// returned for the two cases the manager treats differently.
func (s *Store) TryLock() (func(), error) {
	return lockFile(filepath.Join(s.dir, "issuer.lock"))
}

// CertificatesModified returns the newest modification time of the store's
// certificate files, used by replicas to notice a renewal by the issuer.
func (s *Store) CertificatesModified() time.Time {
	var newest time.Time
	entries, err := os.ReadDir(filepath.Join(s.dir, "certs"))
	if err != nil {
		return newest
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest
}

// readPrivateFile reads a file and refuses one that is group- or
// world-accessible.
func readPrivateFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("tls: %s is group- or world-accessible (%04o); require 0600", path, perm)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// writeFileAtomic writes data to path through a temporary file in the same
// directory, so a crash leaves either the old or the new file, never a partial
// one.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("tls: create temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("tls: set mode on %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("tls: write %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("tls: sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("tls: close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("tls: rename %s to %s: %w", tmpName, path, err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

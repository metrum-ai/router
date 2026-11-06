// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
)

// AdminClientCAStore supplies the listener's AdminClientCASource seam. Reload
// builds a complete replacement before publishing it; errors retain the last
// known good snapshot. Callers must never mutate a pool returned by Snapshot.
// Runtime configuration owns reload scheduling and failure event reporting.
type AdminClientCAStore struct {
	files   []string
	current atomic.Pointer[x509.CertPool]
	mu      sync.Mutex
	hooks   []func(*x509.CertPool)
}

const maxAdminCABundleBytes = 4 << 20
const maxAdminCACertificates = 1024

// NewAdminClientCAStore loads explicit trust anchors; system roots are never
// added. Empty or malformed bundles are errors, not an implicit trust fallback.
func NewAdminClientCAStore(files []string) (*AdminClientCAStore, error) {
	s := &AdminClientCAStore{files: append([]string(nil), files...)}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// Snapshot returns the current immutable client trust pool. Both native TLS
// handshakes and per-request certificate revalidation consume this snapshot.
func (s *AdminClientCAStore) Snapshot() *x509.CertPool { return s.current.Load() }

// OnReload registers a fast callback and immediately delivers the current
// snapshot. Callbacks are serialized with reloads and must not call OnReload or
// Reload recursively. Snapshot is safe to call from a callback. Install hooks
// before starting the listener; a callback must not mutate its supplied pool.
func (s *AdminClientCAStore) OnReload(hook func(*x509.CertPool)) {
	if hook == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks = append(s.hooks, hook)
	hook(s.current.Load())
}

// Reload atomically replaces all configured CA bundles. Callbacks run only
// after successful publication. A malformed or missing file changes no trust.
func (s *AdminClientCAStore) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.files) == 0 || len(s.files) > maxAdminCACertificates {
		return errors.New("admin client CA files required or limit exceeded")
	}
	pool := x509.NewCertPool()
	count := 0
	totalBytes := 0
	for _, path := range s.files {
		f, err := os.Open(path)
		if err != nil {
			return errors.New("admin client CA file unavailable")
		}
		data, readErr := io.ReadAll(io.LimitReader(f, maxAdminCABundleBytes+1))
		closeErr := f.Close()
		totalBytes += len(data)
		if readErr != nil || closeErr != nil || totalBytes > maxAdminCABundleBytes {
			return errors.New("admin client CA bundle unreadable or oversized")
		}
		data = bytes.TrimSpace(data)
		if len(data) == 0 {
			return errors.New("admin client CA bundle empty")
		}
		for len(data) > 0 {
			if !bytes.HasPrefix(data, []byte("-----BEGIN CERTIFICATE-----")) {
				return errors.New("admin client CA bundle malformed")
			}
			block, rest := pem.Decode(data)
			if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
				return errors.New("admin client CA PEM malformed")
			}
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil || !cert.BasicConstraintsValid || !cert.IsCA || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
				return errors.New("admin client CA certificate invalid")
			}
			count++
			if count > maxAdminCACertificates {
				return errors.New("admin client CA certificate limit exceeded")
			}
			pool.AddCert(cert)
			data = bytes.TrimSpace(rest)
		}
	}
	s.current.Store(pool)
	for _, hook := range s.hooks {
		hook(pool)
	}
	return nil
}

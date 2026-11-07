// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type adminFixedClientRoots struct{ pool *x509.CertPool }

func (r adminFixedClientRoots) Snapshot() *x509.CertPool { return r.pool }

type adminMTLSTrustPublication struct {
	union    *x509.CertPool
	identity *adminMTLSIssuerDispatcher
}

// AdminMTLSTrustStore publishes identity domains and possession/handshake union
// as ONE immutable state. It supplies the approved Snapshot/OnReload listener
// seam; actual listener/bootstrap installation remains a separate integration.
// Returned pools are stable until valid publication and must NEVER be mutated.
type AdminMTLSTrustStore struct {
	current atomic.Pointer[adminMTLSTrustPublication]
	mu      sync.Mutex
	config  AdminMTLSTrustConfig
	hooks   []func(*x509.CertPool)
}

func NewAdminMTLSTrustStore(cfg AdminMTLSTrustConfig) (*AdminMTLSTrustStore, error) {
	store := &AdminMTLSTrustStore{}
	if err := store.ReloadConfig(cfg); err != nil {
		return nil, err
	}
	return store, nil
}
func (s *AdminMTLSTrustStore) Snapshot() *x509.CertPool {
	if state := s.current.Load(); state != nil {
		return state.union
	}
	return nil
}

// Hooks are fast/serialized, delivered immediately and after valid replacement.
// A hook must not mutate a pool or recursively register/reload this store.
func (s *AdminMTLSTrustStore) OnReload(hook func(*x509.CertPool)) {
	if hook == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks = append(s.hooks, hook)
	hook(s.Snapshot())
}
func (s *AdminMTLSTrustStore) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publish(s.config)
}
func (s *AdminMTLSTrustStore) ReloadConfig(cfg AdminMTLSTrustConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publish(cfg)
}
func (s *AdminMTLSTrustStore) publish(cfg AdminMTLSTrustConfig) error {
	cfg = cloneAdminMTLSTrustConfig(cfg)
	state, err := buildAdminMTLSTrustPublication(cfg)
	if err != nil {
		return err
	}
	s.config = cfg
	s.current.Store(state)
	for _, hook := range s.hooks {
		hook(state.union)
	}
	return nil
}
func cloneAdminMTLSTrustConfig(cfg AdminMTLSTrustConfig) AdminMTLSTrustConfig {
	cfg.CAFiles = append([]string(nil), cfg.CAFiles...)
	cfg.Subjects = append([]AdminMTLSSubjectConfig(nil), cfg.Subjects...)
	cfg.TrustDomains = append([]AdminMTLSTrustDomainConfig(nil), cfg.TrustDomains...)
	for i := range cfg.TrustDomains {
		cfg.TrustDomains[i].CAFiles = append([]string(nil), cfg.TrustDomains[i].CAFiles...)
		cfg.TrustDomains[i].Subjects = append([]AdminMTLSSubjectConfig(nil), cfg.TrustDomains[i].Subjects...)
	}
	return cfg
}
func buildAdminMTLSTrustPublication(cfg AdminMTLSTrustConfig) (*adminMTLSTrustPublication, error) {
	invalid := func() (*adminMTLSTrustPublication, error) {
		return nil, errors.New("admin mTLS trust configuration invalid")
	}
	if cfg.ReloadInterval != "" {
		duration, err := time.ParseDuration(cfg.ReloadInterval)
		if err != nil || duration <= 0 {
			return invalid()
		}
	}
	domains := cfg.TrustDomains
	if len(domains) > 0 {
		if len(cfg.CAFiles) > 0 || cfg.Issuer != "" || len(cfg.Subjects) > 0 {
			return invalid()
		}
	} else {
		domains = []AdminMTLSTrustDomainConfig{{Issuer: cfg.Issuer, CAFiles: cfg.CAFiles, Subjects: cfg.Subjects}}
	}
	if len(domains) == 0 || len(domains) > 32 {
		return invalid()
	}
	state := &adminMTLSTrustPublication{union: x509.NewCertPool(), identity: &adminMTLSIssuerDispatcher{}}
	issuers := map[string]bool{}
	exactOwners, spkiOwners := map[[32]byte]string{}, map[[32]byte]string{}
	totalBytes, totalCertificates, totalFiles, totalMappings := 0, 0, 0, 0
	for _, domain := range domains {
		issuer := strings.TrimSpace(domain.Issuer)
		if issuer == "" || len(issuer) > 2048 || strings.ContainsAny(issuer, "\x00\r\n") || issuers[issuer] || len(domain.CAFiles) == 0 {
			return invalid()
		}
		issuers[issuer] = true
		pool := x509.NewCertPool()
		for _, path := range domain.CAFiles {
			totalFiles++
			if totalFiles > maxAdminCACertificates {
				return invalid()
			}
			file, err := os.Open(path)
			if err != nil {
				return nil, errors.New("admin client CA file unavailable")
			}
			// Limit allocation to the REMAINING global budget, not 4MiB per file.
			remaining := maxAdminCABundleBytes - totalBytes
			data, readErr := io.ReadAll(io.LimitReader(file, int64(remaining)+1))
			closeErr := file.Close()
			totalBytes += len(data)
			if readErr != nil || closeErr != nil || totalBytes > maxAdminCABundleBytes {
				return nil, errors.New("admin client CA bundle unreadable or oversized")
			}
			certs, err := decodeAdminClientCAs(data)
			if err != nil {
				return nil, err
			}
			for _, cert := range certs {
				totalCertificates++
				if totalCertificates > maxAdminCACertificates {
					return invalid()
				}
				exact, spki := sha256.Sum256(cert.Raw), sha256.Sum256(cert.RawSubjectPublicKeyInfo)
				if owner, exists := exactOwners[exact]; exists && owner != issuer {
					return nil, errors.New("admin mTLS root shared across trust domains")
				}
				if owner, exists := spkiOwners[spki]; exists && owner != issuer {
					return nil, errors.New("admin mTLS root key shared across trust domains")
				}
				exactOwners[exact], spkiOwners[spki] = issuer, issuer
				pool.AddCert(cert)
				state.union.AddCert(cert)
			}
		}
		mappings := map[string]string{}
		for _, mapping := range domain.Subjects {
			totalMappings++
			if totalMappings > maxAdminCACertificates {
				return invalid()
			}
			if previous, exists := mappings[mapping.SAN]; exists && previous != mapping.Subject {
				return invalid()
			}
			mappings[mapping.SAN] = mapping.Subject
		}
		roots := adminFixedClientRoots{pool}
		var identity *adminMTLSAuthenticator
		if len(mappings) == 0 {
			// Possession-only trust domain: verifies authority but can never produce a
			// certificate-only identity. No dummy SAN enrollment is required for JWTs.
			identity = &adminMTLSAuthenticator{issuer: issuer, roots: roots, subjects: map[string]string{}}
		} else {
			validated, err := newAdminMTLSAuthenticator(issuer, roots, mappings)
			if err != nil {
				return nil, err
			}
			identity = validated.(*adminMTLSAuthenticator)
		}
		state.identity.domains = append(state.identity.domains, identity)
	}
	return state, nil
}

// Pure certificate identity verification/mapping with a publication checkpoint.
// No independently required audit/activation/acknowledgement may be added here.
type adminMTLSTrustAuthenticator struct{ store *AdminMTLSTrustStore }

func newAdminMTLSTrustAuthenticator(store *AdminMTLSTrustStore) (Authenticator, error) {
	if store == nil || store.current.Load() == nil {
		return nil, errors.New("admin mTLS trust source required")
	}
	return &adminMTLSTrustAuthenticator{store}, nil
}
func (a *adminMTLSTrustAuthenticator) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	state := a.store.current.Load()
	if state == nil {
		return Principal{}, ErrInvalidCredential
	}
	p, err := state.identity.Authenticate(ctx, r)
	if err != nil {
		return Principal{}, err
	}
	if a.store.current.Load() != state || ctx.Err() != nil {
		return Principal{}, ErrInvalidCredential
	}
	return p, nil
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Trusted OFFLINE enrollment only, never certificate-derived lifetime/identity
// or an HTTP DTO. Issuer is the configured enrollment authority, not a DN.
type adminEmergencyCertificateConfig struct {
	id, subject, issuer, thumbprint string
	issuedAt, expiresAt             time.Time
}
type adminEmergencyCertificateEnrollment struct {
	credential adminEmergencyCredential
	issuedAt   time.Time
}
type adminEmergencyCertificateCatalog struct {
	byPin map[string]*adminEmergencyCertificateEnrollment
	byID  map[string]*adminEmergencyCertificateEnrollment
	now   func() time.Time
}

func newAdminEmergencyCertificateCatalog(configs []adminEmergencyCertificateConfig, reservedIDs map[string]bool, now func() time.Time) (*adminEmergencyCertificateCatalog, error) {
	if len(configs) > 32 {
		return nil, errors.New("emergency certificate enrollment limit exceeded")
	}
	if now == nil {
		now = time.Now
	}
	catalog := &adminEmergencyCertificateCatalog{byPin: map[string]*adminEmergencyCertificateEnrollment{}, byID: map[string]*adminEmergencyCertificateEnrollment{}, now: now}
	for _, cfg := range configs {
		_, reserved := reservedIDs[cfg.id]
		pin, err := base64.RawURLEncoding.DecodeString(cfg.thumbprint)
		if reserved || catalog.byID[cfg.id] != nil || catalog.byPin[cfg.thumbprint] != nil || !validAdminKeyID(cfg.id) || err != nil || len(pin) != sha256.Size || base64.RawURLEncoding.EncodeToString(pin) != cfg.thumbprint || cfg.issuedAt.IsZero() || cfg.expiresAt.IsZero() || !cfg.expiresAt.After(cfg.issuedAt) || cfg.expiresAt.Sub(cfg.issuedAt) > time.Hour || strings.TrimSpace(cfg.subject) == "" || strings.TrimSpace(cfg.issuer) == "" || len(cfg.subject) > 1024 || len(cfg.issuer) > 2048 || strings.TrimSpace(cfg.subject) != cfg.subject || strings.TrimSpace(cfg.issuer) != cfg.issuer {
			return nil, errors.New("emergency certificate enrollment invalid")
		}
		p := Principal{Subject: cfg.subject, Kind: "emergency", CredentialID: cfg.id, Issuer: cfg.issuer}
		if !validMachinePrincipal(p) {
			return nil, errors.New("emergency certificate identity invalid")
		}
		cfg.issuedAt, cfg.expiresAt = cfg.issuedAt.UTC(), cfg.expiresAt.UTC()
		encoded, err := json.Marshal(struct {
			Kind, ID, Subject, Issuer, Pin string
			IssuedAt, ExpiresAt            time.Time
		}{"emergency-certificate", cfg.id, cfg.subject, cfg.issuer, cfg.thumbprint, cfg.issuedAt, cfg.expiresAt})
		if err != nil {
			return nil, errors.New("emergency certificate binding invalid")
		}
		digest := sha256.Sum256(encoded)
		clear(encoded)
		entry := &adminEmergencyCertificateEnrollment{credential: adminEmergencyCredential{ID: cfg.id, Subject: cfg.subject, Issuer: cfg.issuer, Fingerprint: hex.EncodeToString(digest[:]), ExpiresAt: cfg.expiresAt}, issuedAt: cfg.issuedAt}
		catalog.byPin[cfg.thumbprint], catalog.byID[cfg.id] = entry, entry
	}
	return catalog, nil
}
func (c *adminEmergencyCertificateCatalog) credential(id string) (adminEmergencyCredential, bool) {
	if c == nil || c.byID[id] == nil {
		return adminEmergencyCredential{}, false
	}
	return c.byID[id].credential, true
}

type adminEmergencyCertificateMachineAuthenticator struct {
	current func() *adminEmergencyCertificateCatalog
	roots   func() *x509.CertPool
	store   *adminEmergencyActivationStore
	record  func(*gorm.DB, Principal, adminEmergencyActivation) error
}

func newAdminEmergencyCertificateMachineAuthenticator(current func() *adminEmergencyCertificateCatalog, roots func() *x509.CertPool, store *adminEmergencyActivationStore, record func(*gorm.DB, Principal, adminEmergencyActivation) error) (Authenticator, error) {
	if current == nil || current() == nil || roots == nil || roots() == nil || store == nil || record == nil {
		return nil, errors.New("emergency certificate state/trust/event adapters required")
	}
	return WithEmergencyCredentialType(&adminEmergencyCertificateMachineAuthenticator{current, roots, store, record}, EmergencyCertificate), nil
}
func (a *adminEmergencyCertificateMachineAuthenticator) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	if ctx == nil || ctx.Err() != nil || r == nil {
		return Principal{}, ErrInvalidCredential
	}
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return Principal{}, ErrNoCredential
	}
	if r.TLS.PeerCertificates[0] == nil || len(r.TLS.PeerCertificates) > 16 {
		return Principal{}, ErrInvalidCredential
	}
	catalog := a.current()
	if catalog == nil {
		return Principal{}, ErrInvalidCredential
	}
	leaf := r.TLS.PeerCertificates[0]
	digest := sha256.Sum256(leaf.Raw)
	entry := catalog.byPin[base64.RawURLEncoding.EncodeToString(digest[:])]
	if entry == nil {
		return Principal{}, ErrNoCredential
	}
	validAt := func(now time.Time) bool { return !now.Before(entry.issuedAt) && now.Before(entry.credential.ExpiresAt) }
	if !validAt(catalog.now()) {
		return Principal{}, ErrInvalidCredential
	}
	observedRoots, ok := verifyAdminNativeClientCertificate(ctx, r, a.roots)
	if !ok || a.current() != catalog || ctx.Err() != nil || !validAt(catalog.now()) {
		return Principal{}, ErrInvalidCredential
	}
	credential := entry.credential
	p := Principal{Subject: credential.Subject, Kind: "emergency", CredentialID: credential.ID, Issuer: credential.Issuer}
	if _, err := a.store.Use(ctx, credential, func(tx *gorm.DB, activation adminEmergencyActivation) error { return a.record(tx, p, activation) }); err != nil {
		return Principal{}, ErrInvalidCredential
	}
	// A committed attempt acknowledgement is not authority after an observed
	// publication/cancellation/expiry. This is not a dispatch-generation lock.
	actualNow := time.Now()
	if a.current() != catalog || a.roots() != observedRoots || ctx.Err() != nil || !validAt(catalog.now()) || actualNow.Before(leaf.NotBefore) || !actualNow.Before(leaf.NotAfter) {
		return Principal{}, ErrInvalidCredential
	}
	return p, nil
}

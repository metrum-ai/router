// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"
)

type adminEmergencyKeyCatalog struct {
	keys map[string]*adminEmergencyKeyVerifier
}

// Publish a NEW immutable catalog for every enrollment change. The caller owns
// a single node-wide slot for the lifetime of the service and must reuse it
// across catalogs/reloads; old in-flight derivations share the same CPU budget.
// Never close the semaphore while a verifier can use it.
// ordinaryIDs reserves by map presence, not value/status: disabled IDs overlap too.
func newAdminEmergencyKeyCatalog(configs []adminEmergencyKeyVerifierConfig, ordinaryIDs map[string]bool, slots chan struct{}, now func() time.Time) (*adminEmergencyKeyCatalog, error) {
	if len(configs) > 32 || slots == nil || cap(slots) != 1 {
		return nil, errors.New("emergency enrollment or verification budget invalid")
	}
	catalog := &adminEmergencyKeyCatalog{keys: make(map[string]*adminEmergencyKeyVerifier, len(configs))}
	for _, cfg := range configs {
		_, ordinary := ordinaryIDs[cfg.id]
		if _, exists := catalog.keys[cfg.id]; exists || ordinary {
			return nil, errors.New("emergency credential ID overlaps")
		}
		verifier, err := newAdminEmergencyKeyVerifier(cfg, now)
		if err != nil {
			return nil, err
		}
		if !validMachinePrincipal(Principal{Subject: verifier.cfg.subject, Kind: "emergency", CredentialID: verifier.cfg.id, Issuer: verifier.cfg.issuer}) {
			return nil, errors.New("emergency identity invalid")
		}
		// Still private/unpublished: no existing verifier's immutable state changes.
		verifier.slots = slots
		catalog.keys[cfg.id] = verifier
	}
	return catalog, nil
}

// Trusted descriptor for the OFFLINE controller, never an HTTP enrollment or
// activation operation. Protected binding includes verifier/identity/lifetime.
func (c *adminEmergencyKeyCatalog) credential(id string) (adminEmergencyCredential, bool) {
	if c == nil {
		return adminEmergencyCredential{}, false
	}
	verifier := c.keys[id]
	if verifier == nil {
		return adminEmergencyCredential{}, false
	}
	return adminEmergencyCredential{ID: verifier.cfg.id, Subject: verifier.cfg.subject, Issuer: verifier.cfg.issuer, Fingerprint: verifier.bindingDigest, ExpiresAt: verifier.cfg.expiresAt}, true
}

type adminEmergencyKeyMachineAuthenticator struct {
	current func() *adminEmergencyKeyCatalog
	store   *adminEmergencyActivationStore
	record  func(*gorm.DB, Principal, adminEmergencyActivation) error
}

func newAdminEmergencyKeyMachineAuthenticator(current func() *adminEmergencyKeyCatalog, store *adminEmergencyActivationStore, record func(*gorm.DB, Principal, adminEmergencyActivation) error) (Authenticator, error) {
	if current == nil || current() == nil || store == nil || record == nil {
		return nil, errors.New("emergency activation and event adapters required")
	}
	return WithEmergencyCredentialType(&adminEmergencyKeyMachineAuthenticator{current: current, store: store, record: record}, EmergencyBearerKey), nil
}
func (a *adminEmergencyKeyMachineAuthenticator) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	if ctx == nil || ctx.Err() != nil || r == nil {
		return Principal{}, ErrInvalidCredential
	}
	raw := bearerTokenFromRequest(r)
	if raw == "" || !strings.HasPrefix(raw, adminAPIKeyPrefix+"_") {
		return Principal{}, ErrNoCredential
	}
	if len(raw) > maxCallerTokenBytes {
		return Principal{}, ErrInvalidCredential
	}
	catalog := a.current()
	if catalog == nil {
		return Principal{}, ErrInvalidCredential
	}
	// Public ID is ONLY a class-selection hint. Even malformed secret syntax for
	// a known emergency ID belongs here and is terminal, never ordinary fallback.
	rest := strings.TrimPrefix(raw, adminAPIKeyPrefix+"_")
	id, _, _ := strings.Cut(rest, "_")
	verifier := catalog.keys[id]
	if verifier == nil {
		return Principal{}, ErrNoCredential
	}
	proof, err := verifier.Verify(ctx, raw)
	if err != nil || ctx.Err() != nil || a.current() != catalog {
		return Principal{}, ErrInvalidCredential
	}
	credential, ok := catalog.credential(proof.credentialID)
	if !ok || proof.bindingDigest != credential.Fingerprint || proof.subject != credential.Subject || proof.issuer != credential.Issuer || !proof.expiresAt.Equal(credential.ExpiresAt) {
		return Principal{}, ErrInvalidCredential
	}
	p := Principal{Subject: proof.subject, Kind: "emergency", CredentialID: proof.credentialID, Issuer: proof.issuer}
	// No Principal escapes before authoritative state read and transaction-bound
	// Use acknowledgement succeed. This is identity, not a grant: Authorize is
	// still mandatory downstream, including its own audit acknowledgement.
	if _, err := a.store.Use(ctx, credential, func(tx *gorm.DB, activation adminEmergencyActivation) error { return a.record(tx, p, activation) }); err != nil {
		return Principal{}, ErrInvalidCredential
	}
	// Reload/cancellation after a committed Use may leave an attempt event, but
	// must never produce an obsolete identity or dispatch an operation.
	if a.current() != catalog || ctx.Err() != nil {
		return Principal{}, ErrInvalidCredential
	}
	return p, nil
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// Ordinary and emergency catalogs must originate from the same immutable
// runtime publication. Full ordinary ID reservation (including disabled IDs)
// is validated when building the emergency catalog. This adapter additionally
// fails closed on active overlap and on either observed publication change.
// These checkpoints do not replace joint authn/authz generation fencing.
type adminOrdinaryKeyMachineAuthenticator struct {
	normal    func() *adminConfiguredKeyCatalog
	emergency func() *adminEmergencyKeyCatalog
	store     func() *adminStoreKeyCatalog
	verifier  Authenticator
	// storeVerifier claims API-issued identifiers; it runs before the configured
	// catalog so an issued key is never mistaken for an unknown configured one.
	storeVerifier Authenticator
}

// newAdminOrdinaryKeyMachineAuthenticator composes the two sources of ordinary
// admin keys: the configured catalog and, when the control-plane key store
// exists, the API-issued keys of this exact generation. Both are immutable
// per-generation snapshots; neither mutates another's namespace.
func newAdminOrdinaryKeyMachineAuthenticator(normal func() *adminConfiguredKeyCatalog, emergency func() *adminEmergencyKeyCatalog, store func() *adminStoreKeyCatalog) (Authenticator, error) {
	if normal == nil || emergency == nil || !adminMachineKeyCatalogsDisjoint(normal(), emergency()) {
		return nil, errors.New("machine key catalogs unavailable or overlapping")
	}
	if store == nil {
		return nil, errors.New("admin store key source required")
	}
	verifier, err := newConfiguredAdminKeyAuthenticator(normal)
	if err != nil {
		return nil, err
	}
	storeVerifier, err := newAdminStoreKeyAuthenticator(store)
	if err != nil {
		return nil, err
	}
	return &adminOrdinaryKeyMachineAuthenticator{normal: normal, emergency: emergency, store: store, verifier: verifier, storeVerifier: storeVerifier}, nil
}
func adminMachineKeyCatalogsDisjoint(normal *adminConfiguredKeyCatalog, emergency *adminEmergencyKeyCatalog) bool {
	if normal == nil || emergency == nil {
		return false
	}
	for id := range emergency.keys {
		if _, exists := normal.keys[id]; exists {
			return false
		}
	}
	return true
}
func (a *adminOrdinaryKeyMachineAuthenticator) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	if ctx == nil || ctx.Err() != nil || r == nil {
		return Principal{}, ErrInvalidCredential
	}
	raw := bearerTokenFromRequest(r)
	if !strings.HasPrefix(raw, adminAPIKeyPrefix) {
		return Principal{}, ErrNoCredential
	}
	if len(raw) > maxCallerTokenBytes {
		return Principal{}, ErrInvalidCredential
	}
	normal, emergency := a.normal(), a.emergency()
	if !adminMachineKeyCatalogsDisjoint(normal, emergency) {
		return Principal{}, ErrInvalidCredential
	}
	// Explicit namespace ownership, NOT success/failure fallback: even expired,
	// inactive or malformed known emergency credentials never become ordinary.
	// The emergency validator must independently acknowledge its proof and Use.
	if strings.HasPrefix(raw, adminAPIKeyPrefix+"_") {
		id, _, _ := strings.Cut(strings.TrimPrefix(raw, adminAPIKeyPrefix+"_"), "_")
		if _, enrolled := emergency.keys[id]; enrolled {
			return Principal{}, ErrNoCredential
		}
	}
	if a.store() != nil {
		principal, err := a.storeVerifier.Authenticate(ctx, r)
		if !errors.Is(err, ErrNoCredential) {
			// Owned by the store snapshot: the verdict is terminal.
			if err != nil {
				return Principal{}, err
			}
			if a.normal() != normal || a.emergency() != emergency || ctx.Err() != nil {
				return Principal{}, ErrInvalidCredential
			}
			return principal, nil
		}
	}
	p, err := a.verifier.Authenticate(ctx, r)
	if err != nil {
		return Principal{}, err
	}
	if a.normal() != normal || a.emergency() != emergency || ctx.Err() != nil {
		return Principal{}, ErrInvalidCredential
	}
	return p, nil
}

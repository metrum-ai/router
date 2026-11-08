// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// API-issued admin keys authenticate through the same immutable machine
// generation as every other credential: the generation carries a per-generation
// SNAPSHOT of the authoritative store rows (active, unrevoked, unexpired, inside
// their rotation overlap), and the store is still asked for the authoritative
// verdict on every presented key. A revoked or expired key therefore stops
// authenticating without depending on a cache, and a key family that the
// snapshot does not contain is never accepted.

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// adminStoreKeyCatalog is the immutable per-generation view of API-issued keys.
// Only the verified digest of each runtime is mutable (atomic), exactly as for
// configured keys; the map itself is never mutated after publication.
type adminStoreKeyCatalog struct {
	// keys is indexed by the identifier carried in the token, which is the key
	// ID without the `key_` prefix.
	keys  map[string][]adminStoreKeyEntry
	auth  *callerAuthState
	store *adminKeyStore
}

type adminStoreKeyEntry struct {
	keyID      string
	generation int
	runtime    *adminAPIKeyRuntime
}

// newAdminStoreKeyCatalog reads one consistent snapshot of the authoritative
// rows. reserved carries the configured key IDs: a store key may never shadow a
// configured one, so an ambiguous identifier fails the whole preparation.
func newAdminStoreKeyCatalog(ctx context.Context, store *adminKeyStore, reserved map[string]bool) (*adminStoreKeyCatalog, error) {
	if store == nil {
		return nil, nil
	}
	rows, err := store.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	catalog := &adminStoreKeyCatalog{keys: map[string][]adminStoreKeyEntry{}, auth: newCallerAuthState(), store: store}
	for _, row := range rows {
		if !validIssuedAdminKeyID(row.KeyID) || row.Subject == "" || reserved[row.KeyID] {
			return nil, errors.New("admin store key catalog invalid")
		}
		tokenID := strings.TrimPrefix(row.KeyID, "key_")
		catalog.keys[tokenID] = append(catalog.keys[tokenID], adminStoreKeyEntry{
			keyID:      row.KeyID,
			generation: row.Generation,
			runtime:    &adminAPIKeyRuntime{keyID: row.KeyID, subject: row.Subject, verifier: row.verifier},
		})
	}
	return catalog, nil
}

// validIssuedAdminKeyID accepts exactly the identifiers the store generates:
// `key_` followed by 16 lowercase hex characters. The store namespace is
// disjoint from the configured one by construction, and a configured key can
// never be shadowed by an issued key.
func validIssuedAdminKeyID(id string) bool {
	rest, ok := strings.CutPrefix(id, "key_")
	if !ok || len(rest) != 16 {
		return false
	}
	for _, r := range rest {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			continue
		}
		return false
	}
	return true
}

// adminStoreKeyAuthenticator claims API-issued key identifiers and verifies them
// against the generation snapshot plus the authoritative store verdict.
type adminStoreKeyAuthenticator struct {
	current func() *adminStoreKeyCatalog
}

func newAdminStoreKeyAuthenticator(current func() *adminStoreKeyCatalog) (Authenticator, error) {
	if current == nil {
		return nil, errors.New("admin store key source required")
	}
	return &adminStoreKeyAuthenticator{current: current}, nil
}

// Authenticate reports whether this class owns the presented identifier. An
// identifier present in the snapshot is owned even when the secret is wrong
// (terminal invalid_credential, never a fall-through). An identifier absent from
// the snapshot is not owned, so a revoked key ends as an unknown credential.
func (a *adminStoreKeyAuthenticator) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	if ctx == nil || ctx.Err() != nil || r == nil {
		return Principal{}, ErrInvalidCredential
	}
	token := bearerTokenFromRequest(r)
	if !strings.HasPrefix(token, adminAPIKeyPrefix) || len(token) > maxCallerTokenBytes {
		return Principal{}, ErrNoCredential
	}
	tokenID, ok := parseAdminAPIKeyToken(token)
	if !ok {
		return Principal{}, ErrInvalidCredential
	}
	catalog := a.current()
	if catalog == nil {
		return Principal{}, ErrNoCredential
	}
	entries, owned := catalog.keys[tokenID]
	if !owned || len(entries) == 0 {
		return Principal{}, ErrNoCredential
	}
	for _, entry := range entries {
		if !verifyAdminAPIKeyRuntime(ctx, entry.runtime, catalog.auth, token) {
			continue
		}
		// The snapshot decided which generations exist; the store decides whether
		// this one is still live. Both must agree before an identity is returned.
		accepted, err := catalog.store.accepts(ctx, entry.keyID, entry.generation, entry.runtime.verifier)
		if err != nil || !accepted {
			return Principal{}, ErrInvalidCredential
		}
		if a.current() != catalog || ctx.Err() != nil {
			return Principal{}, ErrInvalidCredential
		}
		return Principal{Subject: entry.runtime.subject, Kind: "adminkey", CredentialID: entry.keyID, Issuer: "local"}, nil
	}
	return Principal{}, ErrInvalidCredential
}

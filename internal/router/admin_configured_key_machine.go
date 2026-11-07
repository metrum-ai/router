// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// Immutable publication unit. Only each runtime's verified digest is mutable
// (atomic). Runtime integration must publish a fresh catalog on every reload,
// including disable/remove/hash/env changes, never mutate an existing map.
// This deliberately does not read Service.adminAPIKeys, whose legacy reload
// publication is not an atomic snapshot.
type adminConfiguredKeyCatalog struct {
	keys map[string]*adminAPIKeyRuntime
	auth *callerAuthState
}

func newConfiguredAdminKeyCatalog(cfg *Config, lookup func(string) (string, bool)) (*adminConfiguredKeyCatalog, error) {
	if cfg == nil || lookup == nil || validateAdminAPIKeys(cfg.Server.AdminAuth.APIKeys, false) != nil {
		return nil, errors.New("configured admin key catalog invalid")
	}
	catalog := &adminConfiguredKeyCatalog{keys: map[string]*adminAPIKeyRuntime{}, auth: newCallerAuthState()}
	for _, key := range cfg.Server.AdminAuth.APIKeys {
		// Config validation treats status case-insensitively. Normalize a COPY for
		// machine auth; do not change legacy config or serving-port semantics.
		key.Status = strings.ToLower(strings.TrimSpace(key.Status))
		if key.Status == "disabled" {
			continue
		}
		runtime := buildAdminAPIKeyRuntime(key, lookup)
		if runtime == nil {
			return nil, errors.New("configured admin key verifier unavailable")
		}
		catalog.keys[runtime.keyID] = runtime
	}
	return catalog, nil
}

type adminConfiguredKeyAuthenticator struct {
	current func() *adminConfiguredKeyCatalog
}

func newConfiguredAdminKeyAuthenticator(current func() *adminConfiguredKeyCatalog) (Authenticator, error) {
	if current == nil {
		return nil, errors.New("configured admin key source required")
	}
	return &adminConfiguredKeyAuthenticator{current: current}, nil
}
func (a *adminConfiguredKeyAuthenticator) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	if ctx == nil || ctx.Err() != nil || r == nil {
		return Principal{}, ErrInvalidCredential
	}
	token := bearerTokenFromRequest(r)
	if !strings.HasPrefix(token, adminAPIKeyPrefix) {
		return Principal{}, ErrNoCredential
	}
	id, ok := parseAdminAPIKeyToken(token)
	if !ok {
		return Principal{}, ErrInvalidCredential
	}
	catalog := a.current()
	if catalog == nil {
		return Principal{}, ErrInvalidCredential
	}
	runtime := catalog.keys[id]
	if !verifyAdminAPIKeyRuntime(ctx, runtime, catalog.auth, token) {
		return Principal{}, ErrInvalidCredential
	}
	// Recheck authoritative publication even on digest-cache hits and after KDF.
	// An old credential cannot cross disable/removal/replacement publication.
	if a.current() != catalog || ctx.Err() != nil {
		return Principal{}, ErrInvalidCredential
	}
	return Principal{Subject: runtime.subject, Kind: "adminkey", CredentialID: runtime.keyID, Issuer: "router-config"}, nil
}

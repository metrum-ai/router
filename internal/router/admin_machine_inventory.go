// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// #866's policy administrator inventory, answered from the PREPARED machine
// generation: the same immutable bundle the single publisher installs. The
// reverse guard therefore evaluates a candidate against the inventory that
// candidate will actually have, never against an older published generation.

import (
	"sort"
)

// generationInventory answers the inventory for one prepared generation. It is
// installed on the Service together with the generation, so reload evaluates a
// candidate built from the same catalogs it is about to publish.
type generationInventory struct {
	generation *adminMachineGeneration
	// excludeCredential removes one credential from the inventory. A revocation
	// evaluates the administrators the store would leave, not the ones still
	// published.
	excludeCredential string
}

func (g generationInventory) enabledPolicyAdmins(_ *Config) ([]Principal, error) {
	if g.generation == nil {
		return nil, ErrAuthorizationUnavailable
	}
	seen := map[string]Principal{}
	add := func(p Principal) {
		if g.excludeCredential != "" && p.CredentialID == g.excludeCredential {
			return
		}
		key := p.Kind + "\x00" + p.Subject + "\x00" + p.CredentialID
		seen[key] = p
	}
	// Configured admin keys that resolved a verifier in this generation.
	if g.generation.keys != nil {
		for _, keyID := range sortedAdminKeyIDs(g.generation.keys.keys) {
			runtime := g.generation.keys.keys[keyID]
			if runtime == nil {
				continue
			}
			subject := runtime.subject
			if subject == "" {
				subject = "adminkey:" + keyID
			}
			add(Principal{Subject: subject, Kind: "adminkey", CredentialID: keyID, Issuer: "local"})
		}
	}
	// mTLS identities: the configured trust domains' exact SAN mappings.
	if g.generation.trust != nil {
		if published := g.generation.trust.current.Load(); published != nil && published.identity != nil {
			for _, domain := range published.identity.domains {
				for san, subject := range domain.subjects {
					add(Principal{Subject: subject, Kind: "mtls", CredentialID: san, Issuer: domain.issuer})
				}
			}
		}
	}
	// OAuth/federated identities: the issuer-qualified immutable subject maps.
	if g.generation.jwt != nil {
		for issuer, binding := range g.generation.jwt.issuers {
			kind := binding.Authenticator.cfg.Kind
			if kind == "" {
				kind = "oauth"
			}
			for _, mapping := range binding.Authenticator.cfg.Subjects {
				add(Principal{Subject: mapping.Subject, Kind: kind, CredentialID: mapping.Value, Issuer: issuer})
			}
		}
	}
	// API-issued keys are administrators too: they are counted from the same
	// immutable snapshot this generation will authenticate them from.
	if g.generation.storeKeys != nil {
		for _, entries := range g.generation.storeKeys.keys {
			for _, entry := range entries {
				add(Principal{Subject: entry.runtime.subject, Kind: "adminkey", CredentialID: entry.keyID, Issuer: "local"})
			}
		}
	}
	out := make([]Principal, 0, len(seen))
	for _, p := range seen {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Subject < out[j].Subject
	})
	return out, nil
}

func sortedAdminKeyIDs(keys map[string]*adminAPIKeyRuntime) []string {
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

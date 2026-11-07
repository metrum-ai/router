// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var errAdminEmergencyKeyConfig = errors.New("emergency key configuration invalid")
var errAdminEmergencyKeyProof = errors.New("emergency key proof invalid")

// Resolved offline enrollment metadata, not an HTTP or serialization DTO.
// The loader must keep these IDs disjoint from ordinary admin-key IDs.
type adminEmergencyKeyVerifierConfig struct {
	id, subject, issuer, tokenHash string
	issuedAt, expiresAt            time.Time
}

// Cryptographic proof only. This cannot activate a credential, create a
// Principal, grant authority, or replace the durable activation/use check.
// All fields are private, including the protected activation-binding digest.
type adminEmergencyKeyProof struct {
	credentialID, subject, issuer, bindingDigest string
	issuedAt, expiresAt                          time.Time
}

type adminEmergencyKeyVerifier struct {
	cfg           adminEmergencyKeyVerifierConfig
	verifier      callerTokenVerifier
	bindingDigest string
	now           func() time.Time
	slots         chan struct{}
}

func newAdminEmergencyKeyVerifier(c adminEmergencyKeyVerifierConfig, now func() time.Time) (*adminEmergencyKeyVerifier, error) {
	if !validAdminKeyID(c.id) || len(c.subject) > 1024 || strings.TrimSpace(c.subject) == "" || strings.TrimSpace(c.subject) != c.subject || len(c.issuer) > 2048 || strings.TrimSpace(c.issuer) == "" || strings.TrimSpace(c.issuer) != c.issuer || len(c.tokenHash) > 1024 || c.issuedAt.IsZero() || c.expiresAt.IsZero() || !c.expiresAt.After(c.issuedAt) || c.expiresAt.Sub(c.issuedAt) > time.Hour {
		return nil, errAdminEmergencyKeyConfig
	}
	verifier, err := parseCallerTokenHash(c.tokenHash)
	if err != nil {
		return nil, errAdminEmergencyKeyConfig
	}
	c.issuedAt = c.issuedAt.UTC()
	c.expiresAt = c.expiresAt.UTC()
	// Canonical, domain-separated binding includes the actual verifier and all
	// immutable identity/lifetime fields, never the raw token. No hash is exposed.
	encoded, err := json.Marshal(struct {
		Kind, ID, Subject, Issuer, Verifier string
		IssuedAt, ExpiresAt                 time.Time
	}{"emergency-adminkey", c.id, c.subject, c.issuer, verifier.String(), c.issuedAt, c.expiresAt})
	if err != nil {
		return nil, errAdminEmergencyKeyConfig
	}
	digest := sha256.Sum256(encoded)
	clear(encoded)
	c.tokenHash = ""
	if now == nil {
		now = time.Now
	}
	return &adminEmergencyKeyVerifier{cfg: c, verifier: verifier, bindingDigest: hex.EncodeToString(digest[:]), now: now, slots: make(chan struct{}, 1)}, nil
}

// Verify reuses the existing full-token PBKDF2-HMAC-SHA3-512 verifier and admin
// token namespace. Selection of a separately enrolled emergency ID is done by
// the caller; ordinary configured admin keys never become emergency credentials
// implicitly. Successful proof MUST be followed by durable activation/use event
// acknowledgement and the sole Authorize decision before request dispatch.
func (v *adminEmergencyKeyVerifier) Verify(ctx context.Context, token string) (adminEmergencyKeyProof, error) {
	var zero adminEmergencyKeyProof
	if v == nil || ctx == nil || ctx.Err() != nil || len(token) > maxCallerTokenBytes || strings.TrimSpace(token) != token {
		return zero, errAdminEmergencyKeyProof
	}
	id, ok := parseAdminAPIKeyToken(token)
	if !ok || id != v.cfg.id {
		return zero, errAdminEmergencyKeyProof
	}
	// Bound expensive work to one derivation per configured emergency verifier.
	// Waiters respect their request context; no raw-token or success cache exists.
	select {
	case v.slots <- struct{}{}:
	case <-ctx.Done():
		return zero, errAdminEmergencyKeyProof
	}
	defer func() { <-v.slots }()
	validAt := func(now time.Time) bool { return !now.Before(v.cfg.issuedAt) && now.Before(v.cfg.expiresAt) }
	if ctx.Err() != nil || !validAt(v.now()) {
		return zero, errAdminEmergencyKeyProof
	}
	if !v.verifier.matches(token) || ctx.Err() != nil || !validAt(v.now()) {
		return zero, errAdminEmergencyKeyProof
	}
	return adminEmergencyKeyProof{credentialID: v.cfg.id, subject: v.cfg.subject, issuer: v.cfg.issuer, bindingDigest: v.bindingDigest, issuedAt: v.cfg.issuedAt, expiresAt: v.cfg.expiresAt}, nil
}

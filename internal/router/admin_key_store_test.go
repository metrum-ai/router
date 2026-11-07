// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Tests-first for #815 admin-key lifecycle. The durable store is authoritative
// on every validation; metadata projections never carry secrets or verifiers.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqliteDriver "github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func mustJSONForTest(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func adminKeyVerifierForSecret(t *testing.T, secret string) callerTokenVerifier {
	t.Helper()
	encoded, err := HashCallerToken(secret, rand.Reader, 0)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := parseCallerTokenHash(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

func newAdminKeyStoreForTest(t *testing.T) (*adminKeyStore, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqliteDriver.Open(filepath.Join(t.TempDir(), "keys.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyAdminKeyPhase13(db); err != nil {
		t.Fatal(err)
	}
	return newAdminKeyStore(db, func() time.Time { return time.Now().UTC() }), db
}

func TestAdminKeyIssueReturnsSecretOnce(t *testing.T) {
	store, _ := newAdminKeyStoreForTest(t)
	issued, err := store.issue(context.Background(), adminKeyIssueRequest{Subject: "", ExpiresAt: time.Now().UTC().Add(24 * time.Hour), MaxLifetime: 720 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if issued.Secret == "" || !strings.HasPrefix(issued.Secret, "rtr_admin_") {
		t.Fatalf("issue did not return a one-time secret: %q", issued.Secret)
	}
	if !strings.HasPrefix(issued.Subject, "adminkey:") || strings.Contains(issued.Subject, issued.Secret) {
		t.Fatalf("server-generated subject invalid: %q", issued.Subject)
	}
	// No read surface may return the secret or a verifier.
	list, err := store.list(context.Background(), adminKeyListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Keys) != 1 {
		t.Fatalf("list returned %d keys", len(list.Keys))
	}
	encoded := mustJSONForTest(t, list)
	if strings.Contains(encoded, issued.Secret) || strings.Contains(encoded, "verifier") || strings.Contains(encoded, "pbkdf2") {
		t.Fatalf("metadata projection leaked secret or verifier: %s", encoded)
	}
	single, err := store.get(context.Background(), issued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(mustJSONForTest(t, single), issued.Secret) {
		t.Fatal("get leaked the one-time secret")
	}
	// Rotation also shows the new secret exactly once.
	rotation, err := store.rotate(context.Background(), issued.ID, adminKeyRotateRequest{Overlap: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if rotation.Secret == "" || rotation.Secret == issued.Secret || rotation.Generation != 2 {
		t.Fatalf("rotation did not issue a new one-time generation secret: %+v", rotation)
	}
}

func TestAdminKeyRotationOverlapBoundAndRevoke(t *testing.T) {
	store, _ := newAdminKeyStoreForTest(t)
	issued, err := store.issue(context.Background(), adminKeyIssueRequest{ExpiresAt: time.Now().UTC().Add(24 * time.Hour), MaxLifetime: 720 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.rotate(context.Background(), issued.ID, adminKeyRotateRequest{Overlap: 5 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	// A third live generation is refused while the overlap remains active.
	if _, err := store.rotate(context.Background(), issued.ID, adminKeyRotateRequest{Overlap: 5 * time.Minute}); err == nil {
		t.Fatal("rotation created a third live generation during an active overlap")
	}
	// Both overlapping generations authenticate until revoke cuts them off.
	issueVerifier := adminKeyVerifierForSecret(t, issued.Secret)
	if ok, err := store.accepts(context.Background(), issued.ID, 1, issueVerifier); err != nil || !ok {
		t.Fatalf("previous generation not accepted during overlap: ok=%v err=%v", ok, err)
	}
	if err := store.revoke(context.Background(), issued.ID); err != nil {
		t.Fatal(err)
	}
	for generation := 1; generation <= 2; generation++ {
		if ok, err := store.accepts(context.Background(), issued.ID, generation, issueVerifier); err != nil || ok {
			t.Fatalf("revoked key generation %d still accepted: ok=%v err=%v", generation, ok, err)
		}
	}
	if _, err := store.rotate(context.Background(), issued.ID, adminKeyRotateRequest{Overlap: time.Minute}); err == nil {
		t.Fatal("revoked key could rotate")
	}
}

func TestAdminKeyLifetimeAndRotationOverlapBounds(t *testing.T) {
	store, _ := newAdminKeyStoreForTest(t)
	now := time.Now().UTC()
	for _, tc := range []struct {
		name string
		req  adminKeyIssueRequest
		bad  bool
	}{
		{"missing expiry", adminKeyIssueRequest{MaxLifetime: 720 * time.Hour}, true},
		{"expired expiry", adminKeyIssueRequest{ExpiresAt: now.Add(-time.Minute), MaxLifetime: 720 * time.Hour}, true},
		{"beyond maximum", adminKeyIssueRequest{ExpiresAt: now.Add(900 * time.Hour), MaxLifetime: 720 * time.Hour}, true},
	} {
		if _, err := store.issue(context.Background(), tc.req); (err != nil) != tc.bad {
			t.Fatalf("%s: err=%v want bad=%v", tc.name, err, tc.bad)
		}
	}
	issued, err := store.issue(context.Background(), adminKeyIssueRequest{ExpiresAt: now.Add(time.Hour), MaxLifetime: 720 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.rotate(context.Background(), issued.ID, adminKeyRotateRequest{Overlap: 2 * time.Hour}); err == nil {
		t.Fatal("rotation overlap beyond the configured maximum accepted")
	}
	// Rotation must not silently extend the absolute expiry.
	rotation, err := store.rotate(context.Background(), issued.ID, adminKeyRotateRequest{Overlap: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if rotation.ExpiresAt.After(issued.ExpiresAt) {
		t.Fatal("rotation extended the absolute key expiry")
	}
}

func TestAdminKeyMetadataAuthorityRejectsStaleCache(t *testing.T) {
	store, db := newAdminKeyStoreForTest(t)
	issued, err := store.issue(context.Background(), adminKeyIssueRequest{ExpiresAt: time.Now().UTC().Add(time.Hour), MaxLifetime: 720 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	verifier := adminKeyVerifierForSecret(t, issued.Secret)
	if ok, err := store.accepts(context.Background(), issued.ID, 1, verifier); err != nil || !ok {
		t.Fatalf("active key rejected: ok=%v err=%v", ok, err)
	}
	// Revocation must be observed from the authoritative row, not a cache.
	if err := db.Exec("UPDATE "+adminKeyMetadataTable+" SET revoked_at = ? WHERE id = ?", time.Now().UTC(), issued.ID).Error; err != nil {
		t.Fatal(err)
	}
	if ok, _ := store.accepts(context.Background(), issued.ID, 1, verifier); ok {
		t.Fatal("revocation not rechecked against the authoritative store")
	}
	// Unknown IDs and mismatched generations never authenticate.
	if ok, _ := store.accepts(context.Background(), "absent", 1, verifier); ok {
		t.Fatal("unknown key accepted")
	}
}

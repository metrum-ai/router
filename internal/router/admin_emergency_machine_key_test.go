// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
)

// Real PBKDF2 proof -> durable state/event acknowledgement -> machine identity.
// Not actual CLI binary, production outbox or sole Authorizer/dispatch E2E.
func TestRuntimeEmergencyKeyMachineDurableLifecycle(t *testing.T) {
	generated, err := GenerateAdminAPIKey("rescue", nil, CallerTokenHashDefaultIterations)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	cfg := adminEmergencyKeyVerifierConfig{id: generated.KeyID, subject: "emergency:rescue", issuer: "offline-enrollment", tokenHash: generated.TokenHash, issuedAt: now.Add(-time.Minute), expiresAt: now.Add(30 * time.Minute)}
	slots := make(chan struct{}, 1)
	catalog, err := newAdminEmergencyKeyCatalog([]adminEmergencyKeyVerifierConfig{cfg}, nil, slots, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newAdminEmergencyKeyCatalog([]adminEmergencyKeyVerifierConfig{cfg}, map[string]bool{generated.KeyID: true}, slots, func() time.Time { return now }); err == nil {
		t.Fatal("emergency and ordinary key IDs overlap")
	}
	store, db := emergencyActivationFixture(t, filepath.Join(t.TempDir(), "config.sqlite"), func() time.Time { return now })
	failUse := false
	record := func(tx *gorm.DB, p Principal, a adminEmergencyActivation) error {
		if p.Kind != "emergency" || p.Subject != cfg.subject || p.CredentialID != generated.KeyID || p.Issuer != cfg.issuer {
			t.Error("wrong identity at durable Use")
		}
		if failUse {
			return errors.New("audit unavailable")
		}
		return emergencyTestRecorder(tx, a)
	}
	auth, err := newAdminEmergencyKeyMachineAuthenticator(func() *adminEmergencyKeyCatalog { return catalog }, store, record)
	if err != nil {
		t.Fatal(err)
	}
	fallbackCalls := 0
	fallback := testMachineAuthenticator(func(context.Context, *http.Request) (Principal, error) {
		fallbackCalls++
		return Principal{}, ErrNoCredential
	})
	chain := NewAuthenticatorChain(auth, fallback)
	authenticate := func(token string, want bool) {
		t.Helper()
		fallbackCalls = 0
		r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		p, err := chain.Authenticate(r.Context(), r)
		if want {
			if err != nil || p.Kind != "emergency" || p.Subject != cfg.subject {
				t.Fatal("active proof rejected", err)
			}
		} else if !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) || fallbackCalls != 0 {
			t.Fatal("invalid/inactive proof accepted or fell through")
		}
	}
	command, err := newAdminEmergencyOfflineCommand(store, catalog.credential, emergencyTestRecorder, func(tx *gorm.DB, d adminEmergencyDeactivation) error { return emergencyTestRecorder(tx, d.Activation) })
	if err != nil {
		t.Fatal(err)
	}
	activate := func() {
		t.Helper()
		var out bytes.Buffer
		if err := command.Run(context.Background(), []string{"activate", "--credential-id", generated.KeyID, "--reason", "incident", "--until", now.Add(10 * time.Minute).Format(time.RFC3339Nano)}, &out); err != nil {
			t.Fatal(err)
		}
	}
	authenticate(generated.Token, false)
	activate()
	authenticate(generated.Token+"x", false)
	authenticate(generated.Token, true)
	var out bytes.Buffer
	if err := command.Run(context.Background(), []string{"deactivate", "--credential-id", generated.KeyID, "--reason", "resolved"}, &out); err != nil {
		t.Fatal(err)
	}
	authenticate(generated.Token, false)
	activate()
	failUse = true
	authenticate(generated.Token, false)
	var events int64
	if err := db.Table("emergency_test_events").Count(&events).Error; err != nil || events != 4 {
		t.Fatal("failed Use acknowledgement committed or lifecycle event missing")
	}
}

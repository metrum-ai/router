// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestRuntimeEmergencyKeyPublicationIsolation(t *testing.T) {
	generated, err := GenerateAdminAPIKey("rescue", nil, CallerTokenHashDefaultIterations)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	cfg := adminEmergencyKeyVerifierConfig{id: generated.KeyID, subject: "emergency:rescue", issuer: "local", tokenHash: generated.TokenHash, issuedAt: now.Add(-time.Minute), expiresAt: now.Add(30 * time.Minute)}
	slots := make(chan struct{}, 1)
	// False can represent a disabled key. Presence, not status, must reserve ID.
	if _, err := newAdminEmergencyKeyCatalog([]adminEmergencyKeyVerifierConfig{cfg}, map[string]bool{generated.KeyID: false}, slots, nil); err == nil {
		t.Error("disabled ordinary key ID overlapped emergency enrollment")
	}
	for _, during := range []string{"derivation", "use-ack"} {
		t.Run(during, func(t *testing.T) {
			var current *adminEmergencyKeyCatalog
			empty, err := newAdminEmergencyKeyCatalog(nil, nil, slots, nil)
			if err != nil {
				t.Fatal(err)
			}
			timeReads := 0
			clock := func() time.Time {
				timeReads++
				if during == "derivation" && timeReads == 2 {
					current = empty
				}
				return now
			}
			current, err = newAdminEmergencyKeyCatalog([]adminEmergencyKeyVerifierConfig{cfg}, nil, slots, clock)
			if err != nil {
				t.Fatal(err)
			}
			original := current
			if current.keys[generated.KeyID].slots != slots {
				t.Fatal("catalog replaced node-wide verification budget")
			}
			store, db := emergencyActivationFixture(t, filepath.Join(t.TempDir(), "config.sqlite"), func() time.Time { return now })
			credential, ok := current.credential(generated.KeyID)
			if !ok {
				t.Fatal("trusted enrollment missing")
			}
			if _, err := store.Activate(context.Background(), credential, "incident", now.Add(10*time.Minute), emergencyTestRecorder); err != nil {
				t.Fatal(err)
			}
			useCalls := 0
			record := func(tx *gorm.DB, _ Principal, a adminEmergencyActivation) error {
				useCalls++
				if during == "use-ack" {
					current = empty
				}
				return emergencyTestRecorder(tx, a)
			}
			auth, err := newAdminEmergencyKeyMachineAuthenticator(func() *adminEmergencyKeyCatalog { return current }, store, record)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
			r.Header.Set("Authorization", "Bearer "+generated.Token)
			if p, err := NewAuthenticatorChain(auth).Authenticate(r.Context(), r); !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) {
				t.Fatal("retired enrollment produced identity")
			}
			wantCalls := 0
			if during == "use-ack" {
				wantCalls = 1
			}
			if useCalls != wantCalls {
				t.Fatal("unexpected durable Use acknowledgement")
			}
			// A committed attempt event after publication is not dispatch authority.
			var events int64
			if err := db.Table("emergency_test_events").Count(&events).Error; err != nil || events != int64(1+wantCalls) {
				t.Fatal("incorrect transaction event count")
			}
			if original.keys[generated.KeyID].slots != slots {
				t.Fatal("retirement changed verifier budget")
			}
		})
	}
}

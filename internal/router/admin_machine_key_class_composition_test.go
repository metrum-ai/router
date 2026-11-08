// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestRuntimeEmergencyAndConfiguredKeyComposition(t *testing.T) {
	normalCatalog, normalToken := configuredMachineKeyFixture(t, "active", "adminkey:routine")
	generated, err := GenerateAdminAPIKey("rescue", nil, CallerTokenHashDefaultIterations)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	enrollment, err := newAdminEmergencyKeyCatalog([]adminEmergencyKeyVerifierConfig{{id: generated.KeyID, subject: "emergency:rescue", issuer: "local", tokenHash: generated.TokenHash, issuedAt: now.Add(-time.Minute), expiresAt: now.Add(30 * time.Minute)}}, map[string]bool{"machine-test": true}, make(chan struct{}, 1), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	store, _ := emergencyActivationFixture(t, filepath.Join(t.TempDir(), "config.sqlite"), func() time.Time { return now })
	credential, ok := enrollment.credential(generated.KeyID)
	if !ok {
		t.Fatal("missing enrollment")
	}
	if _, err := store.Activate(context.Background(), credential, "incident", now.Add(10*time.Minute), emergencyTestRecorder); err != nil {
		t.Fatal(err)
	}
	emergency, err := newAdminEmergencyKeyMachineAuthenticator(func() *adminEmergencyKeyCatalog { return enrollment }, store, func(tx *gorm.DB, _ Principal, a adminEmergencyActivation) error { return emergencyTestRecorder(tx, a) })
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := newAdminOrdinaryKeyMachineAuthenticator(func() *adminConfiguredKeyCatalog { return normalCatalog },
		func() *adminEmergencyKeyCatalog { return enrollment }, func() *adminStoreKeyCatalog { return nil })
	if err != nil {
		t.Fatal(err)
	}
	ordinaryCalls := 0
	countedOrdinary := testMachineAuthenticator(func(ctx context.Context, r *http.Request) (Principal, error) {
		ordinaryCalls++
		return ordinary.Authenticate(ctx, r)
	})
	fallbackCalls := 0
	fallback := testMachineAuthenticator(func(context.Context, *http.Request) (Principal, error) {
		fallbackCalls++
		return Principal{}, ErrNoCredential
	})
	chain := NewAuthenticatorChain(emergency, countedOrdinary, fallback)
	authenticate := func(token, kind string) {
		t.Helper()
		ordinaryCalls = 0
		fallbackCalls = 0
		p, err := chain.Authenticate(context.Background(), configuredMachineKeyRequest(token))
		if kind != "" {
			if err != nil || p.Kind != kind {
				t.Errorf("real ordinary/emergency composition rejected %s: %v", kind, err)
			}
		} else if !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) || fallbackCalls != 0 {
			t.Error("invalid credential accepted or fell through")
		}
	}
	authenticate(normalToken, "adminkey")
	authenticate(generated.Token, "emergency")
	for _, token := range []string{generated.Token + "x", adminAPIKeyPrefix + "_" + generated.KeyID + "_x", adminAPIKeyPrefix + "_" + generated.KeyID} {
		authenticate(token, "")
		if ordinaryCalls != 0 {
			t.Error("known invalid emergency ID fell through to real ordinary validator")
		}
	}
	authenticate(normalToken+"x", "")
	if _, err := store.Deactivate(context.Background(), generated.KeyID, "resolved", func(tx *gorm.DB, d adminEmergencyDeactivation) error { return emergencyTestRecorder(tx, d.Activation) }); err != nil {
		t.Fatal(err)
	}
	authenticate(generated.Token, "")
	if ordinaryCalls != 0 {
		t.Error("inactive emergency ID fell through to real ordinary validator")
	}
}

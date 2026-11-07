// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func emergencyKeyBoundaryFixture(t *testing.T) (adminEmergencyKeyVerifierConfig, string) {
	t.Helper()
	generated, err := GenerateAdminAPIKey("emergency-boundary", nil, CallerTokenHashDefaultIterations)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return adminEmergencyKeyVerifierConfig{id: generated.KeyID, subject: "emergency:rescue", issuer: "local", tokenHash: generated.TokenHash, issuedAt: now, expiresAt: now.Add(time.Hour)}, generated.Token
}

func TestAdminEmergencyKeyExactBoundariesAndNil(t *testing.T) {
	cfg, token := emergencyKeyBoundaryFixture(t)
	v, err := newAdminEmergencyKeyVerifier(cfg, func() time.Time { return cfg.issuedAt })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatalf("exact issuance and one-hour lifetime rejected: %v", err)
	}
	var absent *adminEmergencyKeyVerifier
	for _, tc := range []struct {
		v   *adminEmergencyKeyVerifier
		ctx context.Context
	}{{absent, context.Background()}, {v, nil}} {
		if p, err := tc.v.Verify(tc.ctx, token); !errors.Is(err, errAdminEmergencyKeyProof) || p != (adminEmergencyKeyProof{}) {
			t.Fatal("nil input did not fail closed")
		}
	}
	replacement := cfg
	replacement.id = "emergency-replacement"
	changed, err := newAdminEmergencyKeyVerifier(replacement, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if changed.bindingDigest == v.bindingDigest {
		t.Fatal("credential ID absent from activation binding")
	}
}

type emergencyKeyWaitingContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *emergencyKeyWaitingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func TestAdminEmergencyKeyWaitingCancellationAndExpiry(t *testing.T) {
	for _, mode := range []string{"cancel", "expire"} {
		t.Run(mode, func(t *testing.T) {
			cfg, token := emergencyKeyBoundaryFixture(t)
			var expired atomic.Bool
			var clockCalls atomic.Int64
			v, err := newAdminEmergencyKeyVerifier(cfg, func() time.Time {
				clockCalls.Add(1)
				if expired.Load() {
					return cfg.expiresAt
				}
				return cfg.issuedAt
			})
			if err != nil {
				t.Fatal(err)
			}
			if cap(v.slots) != 1 {
				t.Fatal("KDF concurrency bound changed")
			}
			v.slots <- struct{}{} // occupy the sole derivation slot before starting waiter
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &emergencyKeyWaitingContext{Context: parent, entered: make(chan struct{})}
			done := make(chan error, 1)
			go func() {
				p, err := v.Verify(ctx, token)
				if p != (adminEmergencyKeyProof{}) {
					done <- errors.New("failed waiter returned proof")
					return
				}
				done <- err
			}()
			select {
			case <-ctx.entered:
			case <-time.After(time.Second):
				t.Fatal("waiter never reached slot selection")
			}
			if clockCalls.Load() != 0 {
				t.Fatal("validity checked before waiting for occupied slot")
			}
			if mode == "cancel" {
				cancel()
			} else {
				expired.Store(true)
				<-v.slots
			}
			select {
			case err := <-done:
				if !errors.Is(err, errAdminEmergencyKeyProof) {
					t.Fatalf("waiter did not fail closed: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("waiter failed to stop")
			}
			if mode == "cancel" {
				if len(v.slots) != 1 {
					t.Fatal("cancelled waiter released another caller's slot")
				}
				<-v.slots
			} else if clockCalls.Load() != 1 {
				t.Fatal("expired waiter reached derivation/post-KDF check")
			}
			expired.Store(false)
			if _, err := v.Verify(context.Background(), token+"x"); !errors.Is(err, errAdminEmergencyKeyProof) {
				t.Fatal("bad token accepted")
			}
			if _, err := v.Verify(context.Background(), token); err != nil {
				t.Fatal("slot leaked after failure", err)
			}
		})
	}
}

func TestAdminEmergencyKeyCancellationAfterInitialContextSample(t *testing.T) {
	cfg, token := emergencyKeyBoundaryFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The clock hook runs after the initial context sample, before the KDF.
	// Only the post-KDF context check can reject this otherwise valid proof.
	v, err := newAdminEmergencyKeyVerifier(cfg, func() time.Time { cancel(); return cfg.issuedAt })
	if err != nil {
		t.Fatal(err)
	}
	if p, err := v.Verify(ctx, token); !errors.Is(err, errAdminEmergencyKeyProof) || p != (adminEmergencyKeyProof{}) {
		t.Fatal("missing post-KDF cancellation check")
	}
}

func TestAdminEmergencyKeyConcurrentCallsReleaseSlot(t *testing.T) {
	cfg, token := emergencyKeyBoundaryFixture(t)
	v, err := newAdminEmergencyKeyVerifier(cfg, func() time.Time { return cfg.issuedAt })
	if err != nil {
		t.Fatal(err)
	}
	const count = 8
	done := make(chan error, count)
	for i := 0; i < count; i++ {
		go func() { _, err := v.Verify(context.Background(), token); done <- err }()
	}
	for i := 0; i < count; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent derivation deadlocked")
		}
	}
	if len(v.slots) != 0 {
		t.Fatal("concurrent calls leaked derivation slot")
	}
}

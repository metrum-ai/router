// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

func TestAdminMachineJWKSCancelledInitiatorCannotBurnRotation(t *testing.T) {
	for _, state := range []string{"fresh-cache", "expired-cache"} {
		t.Run(state, func(t *testing.T) {
			oldKey, newKey := machineJWTKey(t), machineJWTKey(t)
			var calls atomic.Int64
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			var issuer string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/.well-known/openid-configuration" {
					_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuer, "jwks_uri": issuer + "/keys"})
					return
				}
				n := calls.Add(1)
				if n == 2 {
					close(started)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				keys := []jose.JSONWebKey{{Key: &oldKey.PublicKey, KeyID: "old", Use: "sig", Algorithm: "ES256"}}
				if n >= 2 {
					keys = append(keys, jose.JSONWebKey{Key: &newKey.PublicKey, KeyID: "new", Use: "sig", Algorithm: "ES256"})
				}
				_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: keys})
			}))
			defer server.Close()
			defer unblock()
			issuer = server.URL
			cfg := adminMachineJWTConfig{allowInsecureLoopback: true, AllowPrivateNetwork: true, Issuer: issuer, Audience: "admin", Subjects: []adminMachineJWTSubject{{Claim: "sub", Value: "automation", Subject: "automation"}}}
			a, err := newAdminMachineJWTAuthenticator(context.Background(), cfg, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if state == "expired-cache" {
				a.expires = time.Now().Add(-time.Minute)
				a.lastAttempt = time.Now().Add(-time.Minute)
			}
			claims := map[string]any{"iss": issuer, "aud": "admin", "sub": "automation", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix()}
			unknown := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
			unknown.Header.Set("Authorization", "Bearer "+machineSignedJWT(t, oldKey, "random-unauthenticated-kid", claims))
			ctx, cancel := context.WithCancel(unknown.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := a.Authenticate(ctx, unknown.WithContext(ctx)); done <- err }()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("refresh never started")
			}
			// A second waiter may cancel without aborting the initiator/shared work.
			waiting := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
			waiting.Header.Set("Authorization", "Bearer "+machineSignedJWT(t, newKey, "new", claims))
			waitCtx, stopWait := context.WithTimeout(waiting.Context(), 100*time.Millisecond)
			before := time.Now()
			_, waitErr := a.Authenticate(waitCtx, waiting.WithContext(waitCtx))
			stopWait()
			if !errors.Is(waitErr, ErrInvalidCredential) || time.Since(before) > time.Second {
				t.Fatalf("waiting request did not cancel independently: %v", waitErr)
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, ErrInvalidCredential) {
					t.Fatalf("cancelled waiter error: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("initiating request cannot cancel independently")
			}
			unblock()
			rotated := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
			rotated.Header.Set("Authorization", "Bearer "+machineSignedJWT(t, newKey, "new", claims))
			validCtx, stop := context.WithTimeout(rotated.Context(), 2*time.Second)
			defer stop()
			if p, err := a.Authenticate(validCtx, rotated.WithContext(validCtx)); err != nil || p.Subject != "automation" {
				t.Fatalf("disconnect burned refresh slot and blocked new key: %v", err)
			}
			if calls.Load() != 2 {
				t.Fatalf("refresh did not coalesce: fetches=%d", calls.Load())
			}
		})
	}
}

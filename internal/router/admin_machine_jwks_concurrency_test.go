// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

func TestAdminMachineJWKSUnknownRefreshDoesNotBlockCachedKey(t *testing.T) {
	key := machineJWTKey(t)
	var calls atomic.Int64
	started, release := make(chan struct{}), make(chan struct{})
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuer, "jwks_uri": issuer + "/keys"})
			return
		}
		if calls.Add(1) == 2 {
			close(started)
			<-release
		}
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "known", Use: "sig", Algorithm: "ES256"}}})
	}))
	defer server.Close()
	defer close(release)
	issuer = server.URL
	cfg := adminMachineJWTConfig{allowInsecureLoopback: true, AllowPrivateNetwork: true, Issuer: issuer, Audience: "admin", Subjects: []adminMachineJWTSubject{{Claim: "sub", Value: "automation", Subject: "automation"}}}
	auth, err := newAdminMachineJWTAuthenticator(context.Background(), cfg, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{"iss": issuer, "aud": "admin", "sub": "automation", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix()}
	request := func(kid string) *http.Request {
		r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
		r.Header.Set("Authorization", "Bearer "+machineSignedJWT(t, key, kid, claims))
		return r
	}
	unknown, known := request("unknown"), request("known")
	unknownDone := make(chan error, 1)
	go func() { _, err := auth.Authenticate(unknown.Context(), unknown); unknownDone <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("unknown-kid refresh did not start")
	}
	knownDone := make(chan error, 1)
	go func() { _, err := auth.Authenticate(known.Context(), known); knownDone <- err }()
	select {
	case err := <-knownDone:
		if err != nil {
			t.Fatal("cached key failed during refresh", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("valid cached key blocked behind unknown-kid network I/O")
	}
}

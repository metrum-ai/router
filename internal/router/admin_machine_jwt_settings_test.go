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

func TestRuntimeMachineJWTConsumesCacheRefreshTimeoutAndZeroSkew(t *testing.T) {
	key := machineJWTKey(t)
	var issuer string
	var fetches atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "jwks_uri": issuer + "/keys"})
		case "/keys":
			fetches.Add(1)
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "known", Algorithm: "ES256", Use: "sig"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	issuer = server.URL
	cfg := adminMachineJWTConfig{allowInsecureLoopback: true, AllowPrivateNetwork: true, Issuer: issuer, Audience: "router-admin", Subjects: []adminMachineJWTSubject{{Claim: "sub", Value: "automation", Subject: "workload:automation"}}, JWKSCacheTTL: 2 * time.Second, JWKSRefreshMinInterval: time.Second, JWKSRequestTimeout: time.Second, clockSkewExplicit: true}
	auth, err := newAdminMachineJWTAuthenticator(context.Background(), cfg, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	remaining := time.Until(auth.expires)
	if remaining < time.Second || remaining > 3*time.Second {
		t.Errorf("configured cache TTL ignored: remaining=%v", remaining)
	}
	if auth.client.Timeout != cfg.JWKSRequestTimeout {
		t.Errorf("configured JWKS request timeout ignored: %v", auth.client.Timeout)
	}
	now := time.Now().Unix()
	expired := machineSignedJWT(t, key, "known", map[string]any{"iss": issuer, "aud": "router-admin", "sub": "automation", "iat": now - 20, "exp": now - 5})
	request := httptest.NewRequest(http.MethodGet, "https://admin.example/admin/v1/test", nil)
	request.Header.Set("Authorization", "Bearer "+expired)
	if _, err := auth.Authenticate(context.Background(), request); err == nil {
		t.Error("explicit 0s skew silently became default30s")
	}
	auth.mu.Lock()
	auth.lastUnknownRefresh = time.Now().Add(-2 * time.Second)
	auth.lastAttempt = auth.lastUnknownRefresh
	auth.mu.Unlock()
	_, _ = auth.key(context.Background(), "rotated", "ES256")
	if fetches.Load() != 2 {
		t.Errorf("configured refresh interval ignored: fetches=%d want2", fetches.Load())
	}
}

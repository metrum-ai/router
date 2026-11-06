// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

func TestAdminMachineJWKSRotationAndFederation(t *testing.T) {
	oldKey, newKey := machineJWTKey(t), machineJWTKey(t)
	var keys atomic.Value
	keys.Store(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &oldKey.PublicKey, KeyID: "old", Use: "sig", Algorithm: "ES256"}}})
	var fetches atomic.Int64
	var fail atomic.Bool
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuer, "jwks_uri": issuer + "/keys"})
			return
		}
		fetches.Add(1)
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(keys.Load())
	}))
	defer server.Close()
	issuer = server.URL
	cfg := adminMachineJWTConfig{allowInsecureLoopback: true, Issuer: issuer, Audience: "router-admin", Kind: "federated", Subjects: []adminMachineJWTSubject{{Claim: "sub", Value: "system:serviceaccount:ops:deploy", Subject: "deployer"}}}
	auth, err := newAdminMachineJWTAuthenticator(context.Background(), cfg, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{"iss": issuer, "aud": "router-admin", "sub": "system:serviceaccount:ops:deploy", "iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix()}
	authenticate := func(raw string) (Principal, error) {
		r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
		r.Header.Set("Authorization", "Bearer "+raw)
		return auth.Authenticate(r.Context(), r)
	}
	for i := 0; i < 5; i++ {
		if p, err := authenticate(machineSignedJWT(t, oldKey, "old", claims)); err != nil || p.Kind != "federated" || p.Subject != "deployer" {
			t.Fatalf("projected token: %+v %v", p, err)
		}
	}
	if fetches.Load() != 1 {
		t.Fatal("cached key refetched")
	}
	keys.Store(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &newKey.PublicKey, KeyID: "new", Algorithm: "ES256", Use: "sig"}}})
	if _, err := authenticate(machineSignedJWT(t, newKey, "new", claims)); err != nil {
		t.Fatal("rotated key not discovered", err)
	}
	if fetches.Load() != 2 {
		t.Fatalf("unknown kid refresh count: %d", fetches.Load())
	}
	for i := 0; i < 5; i++ {
		if _, err := authenticate(machineSignedJWT(t, newKey, "unknown", claims)); err == nil {
			t.Fatal("unknown kid accepted")
		}
	}
	if fetches.Load() != 2 {
		t.Fatal("unknown-kid refresh was not rate limited")
	}
	claims["sub"] = "system:serviceaccount:other:deploy"
	if _, err := authenticate(machineSignedJWT(t, newKey, "new", claims)); err == nil {
		t.Fatal("other namespace accepted")
	}
	claims["sub"] = "system:serviceaccount:ops:other"
	if _, err := authenticate(machineSignedJWT(t, newKey, "new", claims)); err == nil {
		t.Fatal("other service account accepted")
	}
	claims["sub"] = "system:serviceaccount:ops:deploy"
	fail.Store(true)
	auth.mu.Lock()
	auth.expires = time.Now().Add(-time.Minute)
	auth.lastAttempt = time.Time{}
	auth.mu.Unlock()
	if _, err := authenticate(machineSignedJWT(t, newKey, "new", claims)); err == nil {
		t.Fatal("expired JWKS accepted on failed refresh")
	}
	calls := fetches.Load()
	if _, err := authenticate(machineSignedJWT(t, newKey, "new", claims)); err == nil {
		t.Fatal("stale JWKS accepted")
	}
	if fetches.Load() != calls {
		t.Fatal("failed stale refresh was not rate limited")
	}
}

func TestAdminMachineJWTAlgorithmAndMapping(t *testing.T) {
	ec := machineJWTKey(t)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuer, "jwks_uri": issuer + "/keys"})
			return
		}
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &ec.PublicKey, KeyID: "ec", Algorithm: "ES256", Use: "sig"}, {Key: &rsaKey.PublicKey, KeyID: "rsa", Algorithm: "RS256", Use: "sig"}}})
	}))
	defer server.Close()
	issuer = server.URL
	cfg := adminMachineJWTConfig{allowInsecureLoopback: true, Issuer: issuer, Audience: "router-admin", Subjects: []adminMachineJWTSubject{{Claim: "client_id", Value: "ci-client", Subject: "ci"}}}
	auth, err := newAdminMachineJWTAuthenticator(context.Background(), cfg, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{"iss": issuer, "aud": []string{"other", "router-admin"}, "client_id": "ci-client", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix()}
	check := func(raw string, valid bool) {
		t.Helper()
		r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
		r.Header.Set("Authorization", "Bearer "+raw)
		p, err := auth.Authenticate(r.Context(), r)
		if valid {
			if err != nil || p.Subject != "ci" {
				t.Fatalf("valid token: %+v %v", p, err)
			}
		} else if err == nil || p != (Principal{}) {
			t.Fatalf("invalid algorithm accepted: %+v %v", p, err)
		}
	}
	check(machineSignedJWTAlgorithm(t, rsaKey, "rsa", jose.RS256, claims), true)
	check(machineSignedJWTAlgorithm(t, ec, "ec", jose.ES256, claims), true)
	check(machineSignedJWTAlgorithm(t, []byte("01234567890123456789012345678901"), "rsa", jose.HS256, claims), false)
	payload, _ := json.Marshal(claims)
	check(base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","kid":"rsa"}`))+"."+base64.RawURLEncoding.EncodeToString(payload)+".", false)
	cfg.Algorithms = []string{"HS256"}
	if _, err := newAdminMachineJWTAuthenticator(context.Background(), cfg, server.Client()); err == nil {
		t.Fatal("symmetric algorithm configuration accepted")
	}
	cfg.Algorithms = nil
	cfg.Subjects = []adminMachineJWTSubject{{Claim: "email", Value: "ci@example.com", Subject: "ci"}}
	if _, err := newAdminMachineJWTAuthenticator(context.Background(), cfg, server.Client()); err == nil {
		t.Fatal("email mapping accepted")
	}
}

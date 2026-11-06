// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

func machineJWTKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
func machineSignedJWT(t *testing.T, key *ecdsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	return machineSignedJWTAlgorithm(t, key, kid, jose.ES256, claims)
}

func machineSignedJWTAlgorithm(t *testing.T, key any, kid string, alg jose.SignatureAlgorithm, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", kid).WithType("at+jwt"))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAdminMachineJWTNegativeValidation(t *testing.T) {
	key := machineJWTKey(t)
	wrongKey := machineJWTKey(t)
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
	cfg := adminMachineJWTConfig{allowInsecureLoopback: true, Issuer: issuer, Audience: "router-admin", Subjects: []adminMachineJWTSubject{{Claim: "sub", Value: "automation", Subject: "mapped-client"}}}
	auth, err := newAdminMachineJWTAuthenticator(context.Background(), cfg, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		kid    string
		key    *ecdsa.PrivateKey
		valid  bool
	}{
		{"valid", nil, "known", key, true},
		{"wrong issuer", func(c map[string]any) { c["iss"] = "https://wrong.example" }, "known", key, false},
		{"wrong audience", func(c map[string]any) { c["aud"] = "inference" }, "known", key, false},
		{"expired", func(c map[string]any) { c["iat"] = now - 600; c["exp"] = now - 60 }, "known", key, false},
		{"future nbf", func(c map[string]any) { c["nbf"] = now + 120 }, "known", key, false},
		{"future iat", func(c map[string]any) { c["iat"] = now + 120 }, "known", key, false},
		{"missing exp", func(c map[string]any) { delete(c, "exp") }, "known", key, false},
		{"missing iat", func(c map[string]any) { delete(c, "iat") }, "known", key, false},
		{"overlong", func(c map[string]any) { c["exp"] = now + 7200 }, "known", key, false},
		{"reversed time", func(c map[string]any) { c["exp"] = now - 10; c["iat"] = now }, "known", key, false},
		{"unmapped", func(c map[string]any) { c["sub"] = "other-client" }, "known", key, false},
		{"bad signature", nil, "known", wrongKey, false},
		{"unknown kid", nil, "unknown", key, false},
		{"bound token no proof", func(c map[string]any) { c["cnf"] = map[string]any{"jkt": "missing-key"} }, "known", key, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := map[string]any{"iss": issuer, "aud": "router-admin", "sub": "automation", "iat": now - 1, "nbf": now - 1, "exp": now + 300}
			if tc.change != nil {
				tc.change(claims)
			}
			token := machineSignedJWT(t, tc.key, tc.kid, claims)
			r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
			r.Header.Set("Authorization", "Bearer "+token)
			p, err := auth.Authenticate(r.Context(), r)
			if tc.valid {
				if err != nil || p.Subject != "mapped-client" || p.Kind != "oauth" || p.Issuer != issuer {
					t.Fatalf("valid token: %+v %v", p, err)
				}
			} else if err == nil || p != (Principal{}) {
				t.Fatalf("invalid token accepted: %+v %v", p, err)
			}
		})
	}
	if fetches.Load() < 1 {
		t.Fatal("JWKS never fetched")
	}
}

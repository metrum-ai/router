// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

func TestAdminDPoPInvalidProofCannotPoisonReplayCache(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	key := dpopTestKey(t)
	token := "access-token"
	validator, err := newAdminDPoPValidator("https://admin.example", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	validator.now = func() time.Time { return now }
	r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
	r.Header.Set("Authorization", "DPoP "+token)
	claims := dpopTestClaims(token, "unique-proof-id-000001", now)
	claims["htm"] = "POST"
	r.Header.Set("DPoP", dpopTestProof(t, key, "dpop+jwt", claims))
	thumb := dpopTestThumbprint(t, key)
	if validator.Validate(r.Context(), r, token, thumb) == nil {
		t.Fatal("wrong method accepted")
	}
	claims["htm"] = "GET"
	r.Header.Set("DPoP", dpopTestProof(t, key, "dpop+jwt", claims))
	if err := validator.Validate(r.Context(), r, token, thumb); err != nil {
		t.Fatal("invalid proof poisoned replay state", err)
	}
	// The supervisor-approved five-second leeway extends the old expiry.
	now = now.Add(time.Minute + 6*time.Second)
	if validator.Validate(r.Context(), r, token, thumb) == nil {
		t.Fatal("expired proof accepted")
	}
	claims = dpopTestClaims(token, "unique-proof-id-000002", now)
	r.Header.Set("DPoP", dpopTestProof(t, key, "dpop+jwt", claims))
	if err := validator.Validate(r.Context(), r, token, thumb); err != nil {
		t.Fatal("expired replay entries not reclaimed", err)
	}
}

func TestAdminDPoPRejectsMalformedEnvelopesAndKeys(t *testing.T) {
	key := dpopTestKey(t)
	other := dpopTestKey(t)
	token := "access-token"
	now := time.Now()
	thumb := dpopTestThumbprint(t, key)
	for _, tc := range []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"private embedded key", func(r *http.Request) {
			r.Header.Set("DPoP", dpopTestProofHeader(t, key, jose.JSONWebKey{Key: key}, "dpop+jwt", dpopTestClaims(token, "unique-proof-id-000001", now)))
		}},
		{"invalid signature", func(r *http.Request) {
			r.Header.Set("DPoP", dpopTestProofHeader(t, other, jose.JSONWebKey{Key: &key.PublicKey}, "dpop+jwt", dpopTestClaims(token, "unique-proof-id-000001", now)))
		}},
		{"duplicate proof", func(r *http.Request) { r.Header["dpop"] = []string{r.Header.Get("DPoP")} }},
		{"duplicate authorization", func(r *http.Request) { r.Header["authorization"] = []string{r.Header.Get("Authorization")} }},
		{"oversized proof", func(r *http.Request) { r.Header.Set("DPoP", strings.Repeat("x", 16*1024+1)) }},
		{"oversized jti", func(r *http.Request) {
			r.Header.Set("DPoP", dpopTestProof(t, key, "dpop+jwt", dpopTestClaims(token, strings.Repeat("x", 257), now)))
		}},
		{"bearer scheme", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }},
		{"wrong access token", func(r *http.Request) { r.Header.Set("Authorization", "DPoP other-token") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			validator, err := newAdminDPoPValidator("https://admin.example", time.Minute, 10)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
			r.Header.Set("Authorization", "DPoP "+token)
			r.Header.Set("DPoP", dpopTestProof(t, key, "dpop+jwt", dpopTestClaims(token, "unique-proof-id-000001", now)))
			tc.mutate(r)
			if validator.Validate(r.Context(), r, token, thumb) == nil {
				t.Fatal("invalid proof/envelope accepted")
			}
		})
	}
}

func TestAdminDPoPRejectsNoneAndHMAC(t *testing.T) {
	key := dpopTestKey(t)
	accessToken := "access-token"
	claims := dpopTestClaims(accessToken, "unique-proof-id-000001", time.Now())
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	options := (&jose.SignerOptions{}).WithType("dpop+jwt").WithHeader("jwk", jose.JSONWebKey{Key: &key.PublicKey})
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte("01234567890123456789012345678901")}, options)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	hmacProof, err := signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	header, err := json.Marshal(map[string]any{"alg": "none", "typ": "dpop+jwt", "jwk": jose.JSONWebKey{Key: &key.PublicKey}})
	if err != nil {
		t.Fatal(err)
	}
	noneProof := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + "."
	validator, err := newAdminDPoPValidator("https://admin.example", time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, proof := range []string{noneProof, hmacProof} {
		r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
		r.Header.Set("Authorization", "DPoP "+accessToken)
		r.Header.Set("DPoP", proof)
		if validator.Validate(r.Context(), r, accessToken, dpopTestThumbprint(t, key)) == nil {
			t.Fatal("non-asymmetric proof accepted")
		}
	}
}

func TestAdminDPoPTrustedURINormalization(t *testing.T) {
	for _, tc := range []struct {
		proofURI, base string
		valid          bool
	}{
		{"https://ADMIN.EXAMPLE:443/admin/v1/%63onfig?ignored=x#ignored", "https://admin.example", true},
		{"https://admin.example/admin%2Fv1/config", "https://admin.example", false},
		{"http://admin.example/admin/v1/config", "https://admin.example", false},
		{"https://admin.example/proxy/admin/v1/config", "https://admin.example/proxy", true},
	} {
		t.Run(tc.proofURI, func(t *testing.T) {
			key := dpopTestKey(t)
			token := "access-token"
			now := time.Now()
			validator, err := newAdminDPoPValidator(tc.base, time.Minute, 10)
			if err != nil {
				t.Fatal(err)
			}
			claims := dpopTestClaims(token, "unique-proof-id-000001", now)
			claims["htu"] = tc.proofURI
			r := httptest.NewRequest("GET", "https://untrusted.example/admin/v1/config", nil)
			r.Host = "evil.example"
			r.Header.Set("Forwarded", "host=evil.example;proto=http")
			r.Header.Set("Authorization", "DPoP "+token)
			r.Header.Set("DPoP", dpopTestProof(t, key, "dpop+jwt", claims))
			if err := validator.Validate(r.Context(), r, token, dpopTestThumbprint(t, key)); (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

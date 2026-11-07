// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

func dpopTestKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
func dpopTestThumbprint(t *testing.T, key *ecdsa.PrivateKey) string {
	t.Helper()
	thumb, err := (&jose.JSONWebKey{Key: &key.PublicKey}).Thumbprint(crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(thumb)
}
func dpopTestProof(t *testing.T, key *ecdsa.PrivateKey, typ string, claims map[string]any) string {
	t.Helper()
	return dpopTestProofHeader(t, key, jose.JSONWebKey{Key: &key.PublicKey}, typ, claims)
}
func dpopTestProofHeader(t *testing.T, key *ecdsa.PrivateKey, jwk jose.JSONWebKey, typ string, claims map[string]any) string {
	t.Helper()
	options := (&jose.SignerOptions{}).WithType(jose.ContentType(typ)).WithHeader("jwk", jwk)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, options)
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
func dpopTestClaims(token, jti string, now time.Time) map[string]any {
	digest := sha256.Sum256([]byte(token))
	return map[string]any{"jti": jti, "iat": now.Unix(), "htm": "GET", "htu": "https://admin.example/admin/v1/config", "ath": base64.RawURLEncoding.EncodeToString(digest[:])}
}

func TestAdminDPoPProofValidation(t *testing.T) {
	key := dpopTestKey(t)
	other := dpopTestKey(t)
	token := "access-token"
	now := time.Now()
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		typ    string
		key    *ecdsa.PrivateKey
		valid  bool
	}{
		{"valid", nil, "dpop+jwt", key, true},
		{"wrong typ", nil, "JWT", key, false},
		{"wrong key", nil, "dpop+jwt", other, false},
		{"wrong method", func(c map[string]any) { c["htm"] = "POST" }, "dpop+jwt", key, false},
		{"wrong URI", func(c map[string]any) { c["htu"] = "https://evil.example/admin/v1/config" }, "dpop+jwt", key, false},
		{"wrong token hash", func(c map[string]any) { c["ath"] = "wrong" }, "dpop+jwt", key, false},
		{"expired", func(c map[string]any) { c["iat"] = now.Add(-2 * time.Minute).Unix() }, "dpop+jwt", key, false},
		{"future", func(c map[string]any) { c["iat"] = now.Add(time.Minute).Unix() }, "dpop+jwt", key, false},
		{"missing iat", func(c map[string]any) { delete(c, "iat") }, "dpop+jwt", key, false},
		{"missing jti", func(c map[string]any) { delete(c, "jti") }, "dpop+jwt", key, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			validator, err := newAdminDPoPValidator("https://admin.example", time.Minute, 10)
			if err != nil {
				t.Fatal(err)
			}
			claims := dpopTestClaims(token, "unique-proof-id-000001", now)
			if tc.change != nil {
				tc.change(claims)
			}
			r := httptest.NewRequest("GET", "https://untrusted-host.example/admin/v1/config?ignored=1", nil)
			r.Header.Set("Authorization", "DPoP "+token)
			r.Header.Set("DPoP", dpopTestProof(t, tc.key, tc.typ, claims))
			r.Header.Set("Forwarded", "host=evil.example;proto=http")
			err = validator.Validate(context.Background(), r, token, dpopTestThumbprint(t, key))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}

func TestAdminDPoPReplayAtomicAndCapacity(t *testing.T) {
	key := dpopTestKey(t)
	token := "access-token"
	now := time.Now()
	validator, err := newAdminDPoPValidator("https://admin.example", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
	r.Header.Set("Authorization", "DPoP "+token)
	r.Header.Set("DPoP", dpopTestProof(t, key, "dpop+jwt", dpopTestClaims(token, "unique-proof-id-000001", now)))
	thumb := dpopTestThumbprint(t, key)
	var successes atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if validator.Validate(r.Context(), r, token, thumb) == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("replay accepted %d times", successes.Load())
	}
	next := r.Clone(r.Context())
	next.Header.Set("DPoP", dpopTestProof(t, key, "dpop+jwt", dpopTestClaims(token, "unique-proof-id-000002", now)))
	if validator.Validate(next.Context(), next, token, thumb) == nil {
		t.Fatal("full replay cache admitted new proof")
	}
	if validator.Validate(r.Context(), r, token, thumb) == nil {
		t.Fatal("capacity pressure evicted live replay record")
	}
}

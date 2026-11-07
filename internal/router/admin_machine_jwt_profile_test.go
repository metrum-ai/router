// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

type adminJWTProfileRoundTripper func(*http.Request) (*http.Response, error)

func (f adminJWTProfileRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func adminJWTProfileConfig() adminMachineJWTConfig {
	return adminMachineJWTConfig{allowInsecureLoopback: true, Issuer: "https://issuer.example", Audience: "admin", Subjects: []adminMachineJWTSubject{{Claim: "sub", Value: "automation", Subject: "automation"}}}
}
func adminJWTProfileAuthenticator(t *testing.T, cfg adminMachineJWTConfig, keys []jose.JSONWebKey, jwksURL string) (*adminMachineJWTAuthenticator, int, error) {
	t.Helper()
	if jwksURL == "" {
		jwksURL = cfg.Issuer + "/keys"
	}
	fetched := 0
	client := &http.Client{Transport: adminJWTProfileRoundTripper(func(r *http.Request) (*http.Response, error) {
		var body any = jose.JSONWebKeySet{Keys: keys}
		if strings.Contains(r.URL.Path, "/.well-known/") {
			body = map[string]string{"issuer": cfg.Issuer, "jwks_uri": jwksURL}
		} else {
			fetched++
		}
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data)))}, nil
	})}
	a, err := newAdminMachineJWTAuthenticator(context.Background(), cfg, client)
	return a, fetched, err
}
func adminJWTProfileClaims(issuer string) map[string]any {
	return map[string]any{"iss": issuer, "aud": "admin", "sub": "automation", "iat": time.Now().Add(-time.Second).Unix(), "exp": time.Now().Add(time.Minute).Unix()}
}
func adminJWTProfileSigned(t *testing.T, key any, typ string, claims map[string]any) string {
	t.Helper()
	opts := (&jose.SignerOptions{}).WithHeader("kid", "test")
	if typ != "" {
		opts.WithType(jose.ContentType(typ))
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, opts)
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

func TestAdminMachineJWTAccessTokenTypeProfile(t *testing.T) {
	key := machineJWTKey(t)
	for _, tc := range []struct {
		typ           string
		legacy, valid bool
	}{
		{"at+jwt", false, true}, {"application/at+jwt", false, true}, {"JWT", false, false}, {"", false, false}, {"id+jwt", false, false},
		{"JWT", true, true}, {"", true, true}, {"id+jwt", true, false},
	} {
		t.Run(tc.typ+map[bool]string{true: "-legacy", false: "-strict"}[tc.legacy], func(t *testing.T) {
			cfg := adminJWTProfileConfig()
			cfg.AllowLegacyTokenType = tc.legacy
			a, _, err := adminJWTProfileAuthenticator(t, cfg, []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Algorithm: "ES256"}}, "")
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
			r.Header.Set("Authorization", "Bearer "+adminJWTProfileSigned(t, key, tc.typ, adminJWTProfileClaims(cfg.Issuer)))
			p, err := a.Authenticate(r.Context(), r)
			if tc.valid {
				if err != nil || p.Subject != "automation" {
					t.Fatalf("valid profile rejected: %v", err)
				}
			} else if !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) {
				t.Fatalf("wrong token type did not fail terminally: %v", err)
			}
		})
	}
}

func TestAdminMachineJWTJWKSOriginAndPrivateNetwork(t *testing.T) {
	key := machineJWTKey(t)
	keys := []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Algorithm: "ES256"}}
	for _, tc := range []struct {
		name, url      string
		hosts          []string
		private, valid bool
	}{
		{"same-origin", "https://issuer.example/keys", nil, false, true},
		{"cross-host-denied", "https://other.example/keys", nil, false, false},
		{"cross-host-explicit", "https://other.example/keys", []string{"other.example"}, false, true},
		{"loopback-jwks", "https://127.0.0.1/keys", []string{"127.0.0.1"}, false, false},
		{"private-jwks", "https://10.0.0.1/keys", []string{"10.0.0.1"}, false, false},
		{"link-local-jwks", "https://169.254.169.254/keys", []string{"169.254.169.254"}, false, false},
		{"unspecified-jwks", "https://[::]/keys", []string{"[::]"}, false, false},
		{"operator-private-opt-in", "https://127.0.0.1/keys", []string{"127.0.0.1"}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := adminJWTProfileConfig()
			cfg.AllowedJWKSHosts = tc.hosts
			cfg.AllowPrivateNetwork = tc.private
			_, fetches, err := adminJWTProfileAuthenticator(t, cfg, keys, tc.url)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t err=%v", tc.valid, err)
			}
			if !tc.valid && fetches != 0 {
				t.Fatal("unsafe JWKS endpoint was contacted")
			}
		})
	}
}

func TestAdminMachineJWTOnlyApprovedAlgorithms(t *testing.T) {
	key := machineJWTKey(t)
	for _, alg := range []string{"RS384", "RS512", "PS256", "PS384", "PS512", "ES384", "ES512", "EdDSA", "HS256", "none"} {
		t.Run(alg, func(t *testing.T) {
			cfg := adminJWTProfileConfig()
			cfg.Algorithms = []string{alg}
			if _, _, err := adminJWTProfileAuthenticator(t, cfg, []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test"}}, ""); err == nil {
				t.Fatal("unapproved algorithm configured")
			}
		})
	}
}

func TestAdminMachineJWTKeyAndIssuerConfusion(t *testing.T) {
	strong, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	ec := machineJWTKey(t)
	for _, tc := range []struct {
		name   string
		signer *rsa.PrivateKey
		jwk    jose.JSONWebKey
		issuer string
		valid  bool
	}{
		{"RSA2048", strong, jose.JSONWebKey{Key: &strong.PublicKey, KeyID: "test", Algorithm: "RS256"}, "https://issuer.example", true},
		{"RS256-names-EC-kid", strong, jose.JSONWebKey{Key: &ec.PublicKey, KeyID: "test"}, "https://issuer.example", false},
		{"RSA1024", weak, jose.JSONWebKey{Key: &weak.PublicKey, KeyID: "test", Algorithm: "RS256"}, "https://issuer.example", false},
		{"JWK-alg-mismatch", strong, jose.JSONWebKey{Key: &strong.PublicKey, KeyID: "test", Algorithm: "RS512"}, "https://issuer.example", false},
		{"issuer-B-to-A", strong, jose.JSONWebKey{Key: &strong.PublicKey, KeyID: "test", Algorithm: "RS256"}, "https://issuer-b.example", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := adminJWTProfileConfig()
			a, _, err := adminJWTProfileAuthenticator(t, cfg, []jose.JSONWebKey{tc.jwk}, "")
			if err != nil {
				t.Fatal(err)
			}
			if tc.name != "issuer-B-to-A" && machineJWTKeyMatches(tc.jwk, "RS256") != tc.valid {
				t.Fatal("isolated key policy mismatch")
			}
			header, _ := json.Marshal(map[string]any{"alg": "RS256", "kid": "test", "typ": "at+jwt"})
			payload, _ := json.Marshal(adminJWTProfileClaims(tc.issuer))
			input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
			digest := sha256.Sum256([]byte(input))
			signature, err := rsa.SignPKCS1v15(rand.Reader, tc.signer, crypto.SHA256, digest[:])
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
			r.Header.Set("Authorization", "Bearer "+input+"."+base64.RawURLEncoding.EncodeToString(signature))
			p, err := a.Authenticate(r.Context(), r)
			if tc.valid {
				if err != nil || p.Subject != "automation" {
					t.Fatalf("valid RSA token rejected: %v", err)
				}
			} else if !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) {
				t.Fatalf("invalid key/issuer did not fail terminally: %v", err)
			}
		})
	}
}

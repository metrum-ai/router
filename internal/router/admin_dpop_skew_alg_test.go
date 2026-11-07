// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

func TestAdminDPoPClockLeewayAndExtendedReplay(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Second)
	key := dpopTestKey(t)
	thumb := dpopTestThumbprint(t, key)
	const access = "access-token"
	for _, seconds := range []int{5, 6} {
		t.Run((time.Duration(seconds) * time.Second).String(), func(t *testing.T) {
			v, err := newAdminDPoPValidator("https://admin.example", time.Minute, 2)
			if err != nil {
				t.Fatal(err)
			}
			v.now = func() time.Time { return base }
			r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
			r.Header.Set("Authorization", "DPoP "+access)
			r.Header.Set("DPoP", dpopTestProof(t, key, "dpop+jwt", dpopTestClaims(access, "boundary", base.Add(time.Duration(seconds)*time.Second))))
			if err := v.Validate(r.Context(), r, access, thumb); (err == nil) != (seconds == 5) {
				t.Fatalf("iat +%ds: %v", seconds, err)
			}
		})
	}
	v, err := newAdminDPoPValidator("https://admin.example", time.Minute, 2)
	if err != nil {
		t.Fatal(err)
	}
	now := base
	v.now = func() time.Time { return now }
	issued := base.Add(5 * time.Second)
	r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
	r.Header.Set("Authorization", "DPoP "+access)
	proof := dpopTestProof(t, key, "dpop+jwt", dpopTestClaims(access, "extended-original", issued))
	r.Header.Set("DPoP", proof)
	if err := v.Validate(r.Context(), r, access, thumb); err != nil {
		t.Fatal(err)
	}
	// Past iat+maxAge but inside iat+maxAge+leeway. A different proof with
	// identical iat must succeed, isolating replay rejection from age rejection.
	now = base.Add(67 * time.Second)
	control := r.Clone(r.Context())
	control.Header.Set("DPoP", dpopTestProof(t, key, "dpop+jwt", dpopTestClaims(access, "extended-control", issued)))
	if err := v.Validate(control.Context(), control, access, thumb); err != nil {
		t.Fatalf("extended-window positive control: %v", err)
	}
	if err := v.Validate(r.Context(), r, access, thumb); err == nil {
		t.Fatal("replay accepted inside extended window")
	}
	for _, expiry := range v.seen {
		if !expiry.Equal(issued.Add(65 * time.Second)) {
			t.Fatalf("incorrect replay retention: %s", expiry)
		}
	}
	fresh := r.Clone(r.Context())
	fresh.Header.Set("DPoP", dpopTestProof(t, key, "dpop+jwt", dpopTestClaims(access, "capacity-control", now)))
	if err := v.Validate(fresh.Context(), fresh, access, thumb); err == nil {
		t.Fatal("live extended replay entries evicted under pressure")
	}
	now = issued.Add(65 * time.Second)
	if err := v.Validate(r.Context(), r, access, thumb); err == nil {
		t.Fatal("proof accepted at extended expiry")
	}
	fresh.Header.Set("DPoP", dpopTestProof(t, key, "dpop+jwt", dpopTestClaims(access, "reclaimed", now)))
	if err := v.Validate(fresh.Context(), fresh, access, thumb); err != nil {
		t.Fatal("expired extended replay records not reclaimed", err)
	}
}

// Sign directly so signer-library key-policy checks cannot prevent building
// deliberately weak/mismatched proofs for the validator's negative cases.
func dpopTestRawSigned(t *testing.T, key any, jwk jose.JSONWebKey, alg string, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"typ": "dpop+jwt", "alg": alg, "jwk": jwk})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(input))
	var signature []byte
	switch k := key.(type) {
	case *rsa.PrivateKey:
		signature, err = rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, digest[:])
	case *ecdsa.PrivateKey:
		r, s, e := ecdsa.Sign(rand.Reader, k, digest[:])
		err = e
		if e == nil {
			size := (k.Curve.Params().BitSize + 7) / 8
			signature = make([]byte, 2*size)
			r.FillBytes(signature[:size])
			s.FillBytes(signature[size:])
		}
	default:
		t.Fatal("unsupported test signing key")
	}
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func TestAdminDPoPRSAAndAlgorithmKeyPolicy(t *testing.T) {
	strong, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		key   any
		jwk   jose.JSONWebKey
		alg   string
		valid bool
	}{
		{"RSA2048", strong, jose.JSONWebKey{Key: &strong.PublicKey, Algorithm: "RS256"}, "RS256", true},
		{"RSA1024", weak, jose.JSONWebKey{Key: &weak.PublicKey, Algorithm: "RS256"}, "RS256", false},
		{"JWK-alg-mismatch", strong, jose.JSONWebKey{Key: &strong.PublicKey, Algorithm: "RS512"}, "RS256", false},
		{"P384-with-ES256", p384, jose.JSONWebKey{Key: &p384.PublicKey, Algorithm: "ES256"}, "ES256", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if dpopProofKeyValid(&tc.jwk, tc.alg) != tc.valid {
				t.Fatal("isolated key policy mismatch")
			}
			thumb, err := tc.jwk.Thumbprint(crypto.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			v, err := newAdminDPoPValidator("https://admin.example", time.Minute, 2)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
			r.Header.Set("Authorization", "DPoP access-token")
			r.Header.Set("DPoP", dpopTestRawSigned(t, tc.key, tc.jwk, tc.alg, dpopTestClaims("access-token", "algorithm-case", time.Now())))
			if err := v.Validate(r.Context(), r, "access-token", base64.RawURLEncoding.EncodeToString(thumb)); (err == nil) != tc.valid {
				t.Fatalf("valid=%t error=%v", tc.valid, err)
			}
		})
	}
}

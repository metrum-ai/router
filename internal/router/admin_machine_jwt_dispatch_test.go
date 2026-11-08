// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

func runtimeJWTIssuerFixture(t *testing.T, subject, kind string) (*adminMachineJWTAuthenticator, *ecdsa.PrivateKey) {
	t.Helper()
	key := machineJWTKey(t)
	issuer := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuer, "jwks_uri": issuer + "/keys"})
		case "/keys":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "known", Algorithm: "ES256", Use: "sig"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	issuer = server.URL
	a, err := newAdminMachineJWTAuthenticator(context.Background(), adminMachineJWTConfig{allowInsecureLoopback: true, AllowPrivateNetwork: true, Issuer: issuer, Audience: "router-admin", Kind: kind, Subjects: []adminMachineJWTSubject{{Claim: "sub", Value: "automation", Subject: subject}}}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return a, key
}
func runtimeJWTClaims(issuer string) map[string]any {
	now := time.Now().Unix()
	return map[string]any{"iss": issuer, "aud": "router-admin", "sub": "automation", "iat": now - 1, "exp": now + 120}
}

func TestRuntimeJWTExactIssuerDispatchAndNoFallback(t *testing.T) {
	a, keyA := runtimeJWTIssuerFixture(t, "issuer-a", "oauth")
	b, keyB := runtimeJWTIssuerFixture(t, "issuer-b", "federated")
	dispatcher, err := newAdminMachineJWTDispatcher([]adminMachineJWTBinding{{Authenticator: a}, {Authenticator: b}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, issuer string
		key          *ecdsa.PrivateKey
		want         string
	}{{"issuer-a", a.cfg.Issuer, keyA, "issuer-a"}, {"issuer-b", b.cfg.Issuer, keyB, "issuer-b"}, {"cross-issuer-key", a.cfg.Issuer, keyB, ""}, {"unknown-issuer", "https://unknown.example", keyA, ""}} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
			r.Header.Set("Authorization", "Bearer "+machineSignedJWT(t, tc.key, "known", runtimeJWTClaims(tc.issuer)))
			calls := 0
			fallback := testMachineAuthenticator(func(context.Context, *http.Request) (Principal, error) { calls++; return Principal{}, ErrNoCredential })
			p, err := NewAuthenticatorChain(dispatcher, fallback).Authenticate(r.Context(), r)
			if tc.want != "" {
				if err != nil || p.Subject != tc.want {
					t.Fatal("configured issuer rejected", err)
				}
			} else if !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) || calls != 0 {
				t.Fatal("invalid token fell through or confused issuers")
			}
		})
	}
}

func TestRuntimeJWTDPoPAndCertificateBinding(t *testing.T) {
	a, key := runtimeJWTIssuerFixture(t, "token-owner", "oauth")
	ca, caKey, roots := certificateBindingCA(t)
	leaf := certificateBindingLeaf(t, ca, caKey, "valid")
	proofKey := dpopTestKey(t)
	for _, mode := range []string{"mtls-valid-unmapped", "mtls-wrong-thumb", "dpop-valid", "dpop-bad-proof", "dpop-required-missing", "dpop-bearer-downgrade", "unknown-cnf", "both-cnf"} {
		t.Run(mode, func(t *testing.T) {
			validator, err := newAdminDPoPValidator("https://admin.example", time.Minute, 16)
			if err != nil {
				t.Fatal(err)
			}
			dispatcher, err := newAdminMachineJWTDispatcher([]adminMachineJWTBinding{{Authenticator: a}}, validator, func() *x509.CertPool { return roots })
			if err != nil {
				t.Fatal(err)
			}
			claims := runtimeJWTClaims(a.cfg.Issuer)
			scheme := "Bearer"
			switch mode {
			case "mtls-valid-unmapped":
				claims["cnf"] = map[string]string{"x5t#S256": certificateBindingThumb(leaf)}
			case "mtls-wrong-thumb":
				claims["cnf"] = map[string]string{"x5t#S256": certificateBindingThumb(ca)}
			case "unknown-cnf":
				claims["cnf"] = map[string]string{"unknown": "value"}
			case "both-cnf":
				claims["cnf"] = map[string]string{"x5t#S256": certificateBindingThumb(leaf), "jkt": dpopTestThumbprint(t, proofKey)}
			default:
				claims["cnf"] = map[string]string{"jkt": dpopTestThumbprint(t, proofKey)}
				if mode != "dpop-bearer-downgrade" {
					scheme = "DPoP"
				}
			}
			token := machineSignedJWT(t, key, "known", claims)
			r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
			r.Header.Set("Authorization", scheme+" "+token)
			if mode == "mtls-valid-unmapped" || mode == "mtls-wrong-thumb" {
				r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf, ca}}}
			}
			if scheme == "DPoP" && mode != "dpop-required-missing" {
				proofClaims := dpopTestClaims(token, "runtime-unique-proof-00001", time.Now())
				if mode == "dpop-bad-proof" {
					proofClaims["ath"] = "wrong"
				}
				r.Header.Set("DPoP", dpopTestProof(t, proofKey, "dpop+jwt", proofClaims))
			}
			calls := 0
			fallback := testMachineAuthenticator(func(context.Context, *http.Request) (Principal, error) { calls++; return Principal{}, ErrNoCredential })
			chain := NewAuthenticatorChain(dispatcher, fallback)
			response := httptest.NewRecorder()
			dispatched := false
			adminMachineAuthenticationMiddleware(chain)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				dispatched = true
				p, ok := PrincipalFrom(r.Context())
				if !ok || p.Subject != "token-owner" {
					t.Error("wrong token identity")
				}
				w.WriteHeader(http.StatusNoContent)
			})).ServeHTTP(response, r)
			valid := mode == "mtls-valid-unmapped" || mode == "dpop-valid"
			if valid {
				if !dispatched || response.Code != http.StatusNoContent {
					t.Fatalf("valid binding rejected: %d", response.Code)
				}
			} else {
				if dispatched || calls != 0 || response.Code != http.StatusUnauthorized {
					t.Fatal("sender failure dispatched or fell through")
				}
				var problem AdminProblem
				if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
					t.Fatal(err)
				}
				if problem.Code != "sender_constraint_failed" {
					t.Fatalf("wrong problem code %q", problem.Code)
				}
			}
		})
	}
}

// RFC 9449: a token bound to a DPoP key thumbprint must be presented with the
// DPoP scheme. The dispatcher refuses a Bearer presentation even when an
// otherwise valid proof accompanies it, and does so before the proof validator
// runs, so a downgraded presentation cannot touch replay state.
func TestRuntimeJWTCnfJKTRequiresDPoPScheme(t *testing.T) {
	a, key := runtimeJWTIssuerFixture(t, "token-owner", "oauth")
	validator, err := newAdminDPoPValidator("https://admin.example", time.Minute, 16)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := newAdminMachineJWTDispatcher([]adminMachineJWTBinding{{Authenticator: a}}, validator, nil)
	if err != nil {
		t.Fatal(err)
	}
	proofKey := dpopTestKey(t)
	claims := runtimeJWTClaims(a.cfg.Issuer)
	claims["cnf"] = map[string]string{"jkt": dpopTestThumbprint(t, proofKey)}
	token := machineSignedJWT(t, key, "known", claims)
	proof := dpopTestProof(t, proofKey, "dpop+jwt", dpopTestClaims(token, "runtime-scheme-proof-00001", time.Now()))
	request := func(scheme string) (Principal, error) {
		r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
		r.Header.Set("Authorization", scheme+" "+token)
		r.Header.Set("DPoP", proof)
		return dispatcher.Authenticate(r.Context(), r)
	}
	if _, err := request("Bearer"); !errors.Is(err, ErrSenderConstraintFailed) {
		t.Fatalf("Bearer presentation of a cnf.jkt token = %v, want sender_constraint_failed", err)
	}
	if validator == nil || len(validator.seen) != 0 {
		t.Fatal("Bearer presentation reached the DPoP proof validator")
	}
	if principal, err := request("DPoP"); err != nil || principal.Subject != "token-owner" {
		t.Fatalf("DPoP presentation of the same token = %v, want the configured identity", err)
	}
	if _, err := request("DPoP"); !errors.Is(err, ErrSenderConstraintFailed) {
		t.Fatal("replayed proof accepted")
	}
}

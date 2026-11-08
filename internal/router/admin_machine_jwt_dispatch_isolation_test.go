// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRuntimeJWTSignatureBeforeDPoPReplayClaim(t *testing.T) {
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
	wrongKey := machineJWTKey(t)
	calls := 0
	fallback := testMachineAuthenticator(func(context.Context, *http.Request) (Principal, error) { calls++; return Principal{}, ErrNoCredential })
	chain := NewAuthenticatorChain(dispatcher, fallback)
	for _, valid := range []bool{false, true} {
		signingKey := wrongKey
		if valid {
			signingKey = key
		}
		token := machineSignedJWT(t, signingKey, "known", claims)
		r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
		r.Header.Set("Authorization", "DPoP "+token)
		// Same proof key and jti, valid per-request ath. An invalid access token
		// must NOT consume the replay slot before access-token verification.
		r.Header.Set("DPoP", dpopTestProof(t, proofKey, "dpop+jwt", dpopTestClaims(token, "same-jti-after-invalid-token", time.Now())))
		p, err := chain.Authenticate(r.Context(), r)
		if valid {
			if err != nil || p.Subject != "token-owner" {
				t.Fatal("invalid signature burned replay slot", err)
			}
		} else if !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) || calls != 0 {
			t.Fatal("invalid signature fell through")
		}
	}
}

func TestRuntimeJWTConfirmationStrictness(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"jkt":null}`, `{"jkt":1}`, `{"jkt":""}`, `{"jkt":"a","jkt":"a"}`, `{"jkt":"a","x5t#S256":"b"}`, `{"unknown":"a"}`, `{"jkt":"a"} {}`, `[]`} {
		if _, _, ok := adminJWTConfirmation([]byte(raw)); ok {
			t.Fatalf("ambiguous confirmation accepted: %s", raw)
		}
	}
	for _, tc := range []struct{ raw, mode string }{{`{"jkt":"a"}`, "dpop"}, {`{"x5t#S256":"a"}`, "mtls"}} {
		if mode, value, ok := adminJWTConfirmation([]byte(tc.raw)); !ok || mode != tc.mode || value != "a" {
			t.Fatal("recognized binding rejected")
		}
	}
	a, key := runtimeJWTIssuerFixture(t, "token-owner", "oauth")
	validator, err := newAdminDPoPValidator("https://admin.example", time.Minute, 16)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := newAdminMachineJWTDispatcher([]adminMachineJWTBinding{{Authenticator: a, MinimumConstraint: "dpop"}}, validator, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
	r.Header.Set("Authorization", "Bearer "+machineSignedJWT(t, key, "known", runtimeJWTClaims(a.cfg.Issuer)))
	if p, err := dispatcher.Authenticate(r.Context(), r); !errors.Is(err, ErrSenderConstraintFailed) || p != (Principal{}) {
		t.Fatal("minimum constraint silently downgraded")
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"errors"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRuntimeJWTInvalidClaimsBeforeDPoPReplayClaim(t *testing.T) {
	a, key := runtimeJWTIssuerFixture(t, "token-owner", "oauth")
	proofKey := dpopTestKey(t)
	for _, name := range []string{"issuer", "audience", "expiry", "not-before", "subject-mapping"} {
		t.Run(name, func(t *testing.T) {
			validator, err := newAdminDPoPValidator("https://admin.example", time.Minute, 16)
			if err != nil {
				t.Fatal(err)
			}
			dispatcher, err := newAdminMachineJWTDispatcher([]adminMachineJWTBinding{{Authenticator: a}}, validator, nil)
			if err != nil {
				t.Fatal(err)
			}
			chain := NewAuthenticatorChain(dispatcher)
			for _, valid := range []bool{false, true} {
				claims := runtimeJWTClaims(a.cfg.Issuer)
				claims["cnf"] = map[string]string{"jkt": dpopTestThumbprint(t, proofKey)}
				if !valid {
					switch name {
					case "issuer":
						claims["iss"] = "https://unknown.example"
					case "audience":
						claims["aud"] = "serving-audience"
					case "expiry":
						claims["iat"] = time.Now().Add(-10 * time.Minute).Unix()
						claims["exp"] = time.Now().Add(-time.Minute).Unix()
					case "not-before":
						claims["nbf"] = time.Now().Add(2 * time.Minute).Unix()
					case "subject-mapping":
						claims["sub"] = "unmapped"
					}
				}
				token := machineSignedJWT(t, key, "known", claims)
				r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
				r.Header.Set("Authorization", "DPoP "+token)
				r.Header.Set("DPoP", dpopTestProof(t, proofKey, "dpop+jwt", dpopTestClaims(token, "same-jti-after-invalid-claims", time.Now())))
				p, err := chain.Authenticate(r.Context(), r)
				if valid {
					if err != nil || p.Subject != "token-owner" {
						t.Fatal("invalid claims burned replay slot", err)
					}
				} else if !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) {
					t.Fatal("invalid signed claims authenticated")
				}
			}
		})
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	jose "github.com/go-jose/go-jose/v4"
)

func TestAdminMachineJWTDiscoveryFailsClosed(t *testing.T) {
	for _, mode := range []string{"wrong-issuer", "malformed-discovery", "empty-jwks", "too-many-keys", "oversized-jwks", "redirect", "malformed-jwks"} {
		t.Run(mode, func(t *testing.T) {
			key := machineJWTKey(t)
			var issuer string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/.well-known/openid-configuration" {
					if mode == "malformed-discovery" {
						_, _ = w.Write([]byte("not json"))
						return
					}
					discovered := issuer
					if mode == "wrong-issuer" {
						discovered = "https://wrong.example"
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"issuer": discovered, "jwks_uri": issuer + "/keys"})
					return
				}
				switch mode {
				case "redirect":
					http.Redirect(w, r, issuer+"/elsewhere", http.StatusTemporaryRedirect)
				case "malformed-jwks":
					_, _ = w.Write([]byte("not json"))
				case "oversized-jwks":
					_, _ = w.Write([]byte(strings.Repeat("x", (1<<20)+1)))
				default:
					keys := jose.JSONWebKeySet{}
					if mode == "too-many-keys" {
						for i := 0; i < 129; i++ {
							keys.Keys = append(keys.Keys, jose.JSONWebKey{Key: &key.PublicKey, KeyID: "duplicated", Use: "sig", Algorithm: "ES256"})
						}
					}
					_ = json.NewEncoder(w).Encode(keys)
				}
			}))
			defer server.Close()
			issuer = server.URL
			cfg := adminMachineJWTConfig{allowInsecureLoopback: true, Issuer: issuer, Audience: "admin", Subjects: []adminMachineJWTSubject{{Claim: "sub", Value: "automation", Subject: "automation"}}}
			if _, err := newAdminMachineJWTAuthenticator(context.Background(), cfg, server.Client()); err == nil {
				t.Fatal("invalid discovery/JWKS accepted")
			}
		})
	}
}

func TestAdminMachineJWTProductionRejectsHTTP(t *testing.T) {
	cfg := adminMachineJWTConfig{Issuer: "http://127.0.0.1:1", Audience: "admin", Subjects: []adminMachineJWTSubject{{Claim: "sub", Value: "automation", Subject: "automation"}}}
	if _, err := newAdminMachineJWTAuthenticator(context.Background(), cfg, &http.Client{}); err == nil {
		t.Fatal("production loopback HTTP accepted")
	}
	cfg.allowInsecureLoopback = true
	cfg.Issuer = "http://192.0.2.1"
	if _, err := newAdminMachineJWTAuthenticator(context.Background(), cfg, &http.Client{}); err == nil {
		t.Fatal("test HTTP escaped loopback")
	}
}

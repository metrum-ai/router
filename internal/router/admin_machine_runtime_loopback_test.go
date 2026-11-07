// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func b64(v *big.Int) string {
	return base64.RawURLEncoding.EncodeToString(v.FillBytes(make([]byte, 32)))
}
func nowUnix() int64 { return time.Now().Unix() }

// A permitted loopback-HTTP admin.external_url must not break an issuer that
// does not require DPoP. Tokens presenting cnf must still fail closed (no
// insecure DPoP), so the optional validator stays absent.
func TestRuntimeMachineFactoryLoopbackHTTPExternalURLWithoutDPoP(t *testing.T) {
	signing := machineJWTKey(t)
	var issuer string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "jwks_uri": issuer + "/keys"})
		case "/keys":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "EC", "crv": "P-256", "kid": "known", "alg": "ES256", "use": "sig", "x": b64(signing.PublicKey.X), "y": b64(signing.PublicKey.Y)}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	issuer = server.URL
	cfg := &Config{}
	cfg.Admin.Enabled = true
	cfg.Admin.Listen = "127.0.0.1:0"
	cfg.Admin.ExternalURL = "http://localhost:9001"
	cfg.Admin.Authentication.Issuers = []AdminMachineIssuerConfig{{IssuerURL: issuer, Audience: "router-admin", Kind: "oauth", AllowPrivateNetwork: true, Subjects: []AdminMachineJWTSubjectConfig{{Claim: "sub", Value: "automation", Subject: "workload:worker"}}}}
	rt := &adminMachineRuntime{replay: newAdminDPoPReplayState(), emergencySlots: make(chan struct{}, 1), lookup: func(string) (string, bool) { return "", false }, httpClient: server.Client()}
	compatible, err := newAuthorizer(cfg.Server.AdminAuth.Authorization, nil, nil, cfg.Server.AdminAuth)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := rt.prepare(cfg, compatible)
	if err != nil {
		t.Fatalf("optional DPoP rejected supported loopback-HTTP configuration: %v", err)
	}
	if generation.jwt == nil || generation.jwt.dpop != nil {
		t.Fatal("loopback HTTP must omit the DPoP validator, not build an insecure one")
	}
	bound := machineSignedJWT(t, signing, "known", map[string]any{"iss": issuer, "aud": "router-admin", "sub": "automation", "iat": nowUnix() - 5, "exp": nowUnix() + 120, "cnf": map[string]any{"jkt": "anything"}})
	request := httptest.NewRequest("GET", "http://localhost:9001/admin/v1/x", nil)
	request.Header.Set("Authorization", "Bearer "+bound)
	if _, err := generation.jwt.Authenticate(request.Context(), request); err == nil {
		t.Fatal("cnf-bound token accepted without a DPoP validator")
	}
	plain := machineSignedJWT(t, signing, "known", map[string]any{"iss": issuer, "aud": "router-admin", "sub": "automation", "iat": nowUnix() - 5, "exp": nowUnix() + 120})
	request = httptest.NewRequest("GET", "http://localhost:9001/admin/v1/x", nil)
	request.Header.Set("Authorization", "Bearer "+plain)
	if _, err := generation.jwt.Authenticate(request.Context(), request); err != nil {
		t.Fatalf("unbound issuer token rejected under loopback HTTP: %v", err)
	}
	cfg.Admin.Authentication.Issuers[0].SenderConstraint = "dpop"
	if _, err := rt.prepare(cfg, compatible); err == nil {
		t.Fatal("explicit DPoP minimum accepted loopback HTTP external URL")
	}
}

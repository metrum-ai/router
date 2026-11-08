// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// Full factory composition with a privately injected TLS-root client, not
// default New/system trust or a production HTTP/insecure configuration escape.
func TestRuntimeMachineFactoryTLSIssuerDPoPAcrossGenerations(t *testing.T) {
	signing := machineJWTKey(t)
	var issuer string
	var fetches atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "jwks_uri": issuer + "/keys"})
		case "/keys":
			fetches.Add(1)
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &signing.PublicKey, KeyID: "known", Algorithm: "ES256", Use: "sig"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	issuer = server.URL
	cfg := &Config{}
	cfg.Admin.Enabled = true
	cfg.Admin.ExternalURL = "https://admin.example"
	cfg.Admin.Authentication.JWKS = AdminMachineJWKSConfig{CacheTTL: "2s", RefreshMinInterval: "1s", RequestTimeout: "1s"}
	cfg.Admin.Authentication.Issuers = []AdminMachineIssuerConfig{{IssuerURL: issuer, Audience: "router-admin", Kind: "federated", Algorithms: []string{"ES256"}, ClockSkew: "0s", SenderConstraint: "dpop", AllowPrivateNetwork: true, Subjects: []AdminMachineJWTSubjectConfig{{Claim: "sub", Value: "automation", Subject: "workload:worker"}}}}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, workload:worker, default, admin:callers, list"}}
	rt := &adminMachineRuntime{replay: newAdminDPoPReplayState(), emergencySlots: make(chan struct{}, 1), lookup: func(string) (string, bool) { return "", false }, httpClient: server.Client()}
	svc := &Service{}
	install := func() {
		t.Helper()
		compatible, err := newAuthorizer(cfg.Server.AdminAuth.Authorization, nil, nil, cfg.Server.AdminAuth)
		if err != nil {
			t.Fatal(err)
		}
		g, err := rt.prepare(cfg, compatible)
		if err != nil {
			t.Fatal("factory rejected correctly trusted private TLS issuer", err)
		}
		if err := svc.publishAdminMachineGeneration(g); err != nil {
			t.Fatal(err)
		}
		profile := g.jwt.issuers[issuer].Authenticator.cfg
		if profile.JWKSCacheTTL != 2*time.Second || profile.JWKSRefreshMinInterval != time.Second || profile.JWKSRequestTimeout != time.Second || profile.ClockSkew != 0 || !profile.clockSkewExplicit {
			t.Fatal("factory discarded validated JWT settings")
		}
	}
	install()
	proofKey := dpopTestKey(t)
	thumb := dpopTestThumbprint(t, proofKey)
	now := time.Now()
	token := machineSignedJWT(t, signing, "known", map[string]any{"iss": issuer, "aud": "router-admin", "sub": "automation", "iat": now.Unix() - 10, "exp": now.Unix() + 120, "cnf": map[string]any{"jkt": thumb}})
	proof := dpopTestProof(t, proofKey, "dpop+jwt", dpopTestClaims(token, "factory-cross-generation-0001", now))
	handler := svc.adminMachineGenerationMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFrom(r.Context())
		if !ok || p.Kind != "federated" || p.Issuer != issuer || svc.Authorize(svc.PinAuthorization(r.Context()), p, "default", "admin:callers", "list") != nil {
			w.WriteHeader(403)
			return
		}
		w.WriteHeader(204)
	}))
	request := func(rawProof string, want int) {
		t.Helper()
		r := httptest.NewRequest("GET", "http://untrusted.example/admin/v1/config", nil)
		r.Header.Set("Authorization", "DPoP "+token)
		r.Header.Set("DPoP", rawProof)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("factory JWT/DPoP status=%d want=%d", w.Code, want)
		}
	}
	request(proof, 204)
	install()
	request(proof, 401)
	fresh := dpopTestProof(t, proofKey, "dpop+jwt", dpopTestClaims(token, "factory-next-generation-00002", now))
	request(fresh, 204)
	if fetches.Load() != 2 {
		t.Fatalf("factory JWKS initial loads=%d want2", fetches.Load())
	}
}

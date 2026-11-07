// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func generationKeyFixture(t *testing.T, permission string) (*adminMachineGeneration, string) {
	t.Helper()
	key := generateTestAdminKey(t, "generation-key")
	cfg := &Config{}
	cfg.Server.AdminAuth.Authorization.Enabled = true
	cfg.Server.AdminAuth.APIKeys = []AdminAPIKeyConfig{{KeyID: key.KeyID, KeyHashEnv: "TEST_HASH", Subject: "adminkey:generation-key", Domain: "default", Permissions: []string{permission}, Status: "active"}}
	keys, err := newConfiguredAdminKeyCatalog(cfg, func(string) (string, bool) { return key.TokenHash, true })
	if err != nil {
		t.Fatal(err)
	}
	auth, err := newConfiguredAdminKeyAuthenticator(func() *adminConfiguredKeyCatalog { return keys })
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true}, nil, nil, cfg.Server.AdminAuth)
	if err != nil {
		t.Fatal(err)
	}
	return &adminMachineGeneration{auth: NewAuthenticatorChain(auth), authorizer: authorizer, keys: keys, config: cfg}, key.Token
}

func TestRuntimeMachineGenerationCapturedAuthorization(t *testing.T) {
	old, oldToken := generationKeyFixture(t, "admin:callers:list")
	replacement, newToken := generationKeyFixture(t, "admin:auth:read")
	svc := &Service{authorizer: old.authorizer}
	if err := svc.publishAdminMachineGeneration(old); err != nil {
		t.Fatal(err)
	}
	// A generic/serving snapshot may already be newer than the machine
	// bundle. A preexisting generic pin must not override its captured grants.
	svc.authorizerSnapshot.Store(replacement.authorizer)
	arrived, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseRequest := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseRequest()
	var first atomic.Bool
	handler := svc.adminMachineGenerationMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deterministic barrier AFTER cryptographic authentication and BEFORE any
		// authorization. No admission lock: publication must finish while paused.
		if first.CompareAndSwap(false, true) {
			close(arrived)
			<-release
		}
		p, ok := PrincipalFrom(r.Context())
		if !ok {
			t.Error("missing authenticated identity")
			w.WriteHeader(500)
			return
		}
		ctx := svc.PinAuthorization(r.Context())
		if bearerTokenFromRequest(r) == oldToken {
			if err := svc.Authorize(ctx, p, "default", "admin:auth", "read"); !errors.Is(err, ErrAdminForbidden) {
				t.Error("captured old request inherited new grants")
			}
		}
		object, action := "admin:callers", "list"
		if r.URL.Path == "/admin/v1/auth" {
			object, action = "admin:auth", "read"
		}
		if err := svc.Authorize(ctx, p, "default", object, action); err != nil {
			w.WriteHeader(403)
			return
		}
		w.WriteHeader(204)
	}))
	request := func(token, path string) int {
		r := configuredMachineKeyRequest(token)
		r.URL.Path = path
		r = r.WithContext(svc.PinAuthorization(r.Context()))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	result := make(chan int, 1)
	go func() { result <- request(oldToken, "/admin/v1/callers") }()
	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("request did not reach authenticated barrier")
	}
	published := make(chan error, 1)
	go func() { published <- svc.publishAdminMachineGeneration(replacement) }()
	select {
	case err := <-published:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("publication waited for captured request: no admission lock allowed")
	}
	// Simulate independent serving-port snapshot moving too. Machine pin must
	// ignore this global authorizer and retain its captured bundle.
	svc.authorizerSnapshot.Store(replacement.authorizer)
	releaseRequest()
	if status := <-result; status != 204 {
		t.Fatalf("captured old credentials mixed with replacement grants: %d", status)
	}
	if status := request(oldToken, "/admin/v1/callers"); status != 401 {
		t.Fatalf("removed key accepted after publication: %d", status)
	}
	if status := request(newToken, "/admin/v1/callers"); status != 403 {
		t.Fatalf("new key inherited old grants: %d", status)
	}
	if status := request(newToken, "/admin/v1/auth"); status != 204 {
		t.Fatalf("new generation did not receive compatible new grants: %d", status)
	}
}

func TestRuntimeMachineGenerationFailedPreparationRetainsBundle(t *testing.T) {
	old, token := generationKeyFixture(t, "admin:callers:list")
	svc := &Service{}
	if err := svc.publishAdminMachineGeneration(old); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("invalid replacement config/roots/policy")
	if err := svc.replaceAdminMachineGeneration(func() (*adminMachineGeneration, error) { return nil, failure }); !errors.Is(err, failure) {
		t.Fatal("preparation failure hidden")
	}
	if svc.machineGeneration.Load() != old {
		t.Fatal("failed preparation replaced existing bundle")
	}
	if err := svc.publishAdminMachineGeneration(&adminMachineGeneration{auth: old.auth}); err == nil || svc.machineGeneration.Load() != old {
		t.Fatal("incomplete bundle published")
	}
	handler := svc.adminMachineGenerationMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalFrom(r.Context())
		if err := svc.Authorize(svc.PinAuthorization(r.Context()), p, "default", "admin:callers", "list"); err != nil {
			t.Error(err)
		}
		w.WriteHeader(204)
	}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, configuredMachineKeyRequest(token))
	if w.Code != 204 {
		t.Fatal("old bundle lost after failed preparation")
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// Actual New->AdminHandler->AdminPlane.Start, with no manual generation or
// authenticator installation. This proves configured-key production bootstrap,
// not native TLS, emergency outbox or routerctl installation.
func TestRuntimeMachineFactoryConfiguredKeyActualNew(t *testing.T) {
	key := generateTestAdminKey(t, "factory-key")
	t.Setenv("TEST_FACTORY_ADMIN_HASH", key.TokenHash)
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
			cfg.Admin.Enabled = true
			cfg.Admin.Listen = "127.0.0.1:0"
			cfg.Admin.Authentication.APIKeys.Enabled = &enabled
			cfg.Server.AdminAuth.APIKeys = []AdminAPIKeyConfig{{KeyID: key.KeyID, KeyHashEnv: "TEST_FACTORY_ADMIN_HASH", Subject: "adminkey:factory-key", Domain: "default", Permissions: []string{"admin:callers:list"}, Status: "active"}}
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			var dispatched atomic.Int64
			svc.RegisterAdminRoutes(func(m *AdminMux) {
				m.HandleFunc("GET /admin/v1/factory", func(w http.ResponseWriter, r *http.Request) {
					p, ok := PrincipalFrom(r.Context())
					if !ok {
						w.WriteHeader(500)
						return
					}
					if err := svc.Authorize(svc.PinAuthorization(r.Context()), p, "default", "admin:callers", "list"); err != nil {
						w.WriteHeader(403)
						return
					}
					dispatched.Add(1)
					w.WriteHeader(204)
				})
			})
			plane, err := NewAdminPlane(&cfg.Admin, svc.AdminHandler())
			if err != nil {
				t.Fatal(err)
			}
			if source := svc.AdminClientCASource(); source != nil {
				plane.SetClientCASource(source)
			}
			if err := plane.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = plane.Shutdown(ctx)
			}()
			client := &http.Client{Timeout: 5 * time.Second}
			request := func(token string, want int) {
				t.Helper()
				r, err := http.NewRequest("GET", "http://"+plane.Addr()+"/admin/v1/factory", nil)
				if err != nil {
					t.Fatal(err)
				}
				r.Header.Set("Authorization", "Bearer "+token)
				response, err := client.Do(r)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				if response.StatusCode != want {
					t.Fatalf("constructor-configured real key actual listener status=%d want=%d", response.StatusCode, want)
				}
			}
			want := 401
			if enabled {
				want = 204
			}
			request(key.Token, want)
			if enabled {
				// The same built listener must use a complete subsequent factory
				// publication, not a fixed boot verifier or serving-port snapshot.
				rotated := generateTestAdminKey(t, "factory-key")
				t.Setenv("TEST_FACTORY_ADMIN_HASH", rotated.TokenHash)
				if err := svc.machineClientCASource().Reload(); err != nil {
					t.Fatal(err)
				}
				request(key.Token, 401)
				request(rotated.Token, 204)
			}
			if enabled && dispatched.Load() != 2 || !enabled && dispatched.Load() != 0 {
				t.Fatal("constructor key enable/disable dispatch mismatch")
			}
		})
	}
}

// Review regressions: a certificate enrollment must not collide with its own
// reserved ID, and its authenticator must see the generation's trust roots.
func TestRuntimeMachineFactoryEmergencyCertificateEnrollment(t *testing.T) {
	dir := t.TempDir()
	ca := newAdminTestCA(t)
	leafCert, _ := ca.issue(t, "emergency", nil, []string{"spiffe://ops/emergency"}, false)
	leaf := parseAdminTestCert(t, leafCert)
	digest := sha256.Sum256(leaf.Raw)
	pin := base64.RawURLEncoding.EncodeToString(digest[:])
	caPath := writeTestPEM(t, dir, "ca.pem", ca.certPEM)
	now := time.Now().UTC()
	cfg := &Config{}
	cfg.Admin.Enabled = true
	cfg.Admin.Authentication.MTLS = AdminMTLSTrustConfig{Issuer: "authority", CAFiles: []string{caPath}}
	cfg.Admin.Authentication.Emergency.Credentials = []AdminMachineEmergencyCredentialConfig{{
		ID: "emergency-cert", Subject: "emergency:ops", Issuer: "offline", Kind: "certificate",
		CertificateSHA256: pin,
		IssuedAt:          now.Add(-time.Minute).Format(time.RFC3339Nano),
		ExpiresAt:         now.Add(30 * time.Minute).Format(time.RFC3339Nano),
	}}
	rt := &adminMachineRuntime{replay: newAdminDPoPReplayState(), emergencySlots: make(chan struct{}, 1),
		lookup: func(string) (string, bool) { return "", false }, events: mustMachineEventSink(t), activation: mustActivationStore(t)}
	compatible, err := newAuthorizer(cfg.Server.AdminAuth.Authorization, nil, nil, cfg.Server.AdminAuth)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := rt.prepare(cfg, compatible)
	if err != nil {
		t.Fatalf("valid emergency certificate enrollment rejected: %v", err)
	}
	if generation.trust == nil || generation.emergencyCertificates.byID["emergency-cert"] == nil {
		t.Fatal("emergency certificate prepared without trust roots or enrollment")
	}
}

// Review regression: a successful ordinary authentication must acknowledge its
// durable event and refuse the identity when that acknowledgement fails.
func TestRuntimeMachineAuthenticationFailsClosedOnCredentialEvent(t *testing.T) {
	key := generateTestAdminKey(t, "event-key")
	cfg := &Config{}
	cfg.Admin.Enabled = true
	cfg.Server.AdminAuth.APIKeys = []AdminAPIKeyConfig{{KeyID: key.KeyID, KeyHashEnv: "TEST_EVENT_HASH", Subject: "adminkey:event-key", Domain: "default", Permissions: []string{"admin:callers:list"}, Status: "active"}}
	t.Setenv("TEST_EVENT_HASH", key.TokenHash)
	okEvents, err := newAdminMachineCredentialEventSink(adminCredentialEventSinkFunc(func(context.Context, AdminCredentialEvent) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	rt := &adminMachineRuntime{replay: newAdminDPoPReplayState(), emergencySlots: make(chan struct{}, 1),
		lookup: os.LookupEnv, events: okEvents}
	compatible, err := newAuthorizer(cfg.Server.AdminAuth.Authorization, nil, nil, cfg.Server.AdminAuth)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := rt.prepare(cfg, compatible)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "https://admin.example/admin/v1/x", nil)
	request.Header.Set("Authorization", "Bearer "+key.Token)
	if _, err := generation.auth.Authenticate(request.Context(), request); err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
	recorder := &failingCredentialEventSink{}
	generation.auth = &adminMachineEventRecordingAuthenticator{inner: generation.auth,
		events: &adminMachineCredentialEventSink{sink: recorder}}
	if _, err := generation.auth.Authenticate(request.Context(), request); err == nil {
		t.Fatal("authentication succeeded despite a failed durable credential event")
	}
}

// Production rt.prepare chain with events enabled: each mechanism must run
// exactly once (one PBKDF2/one use/one event), and an emergency certificate
// must not be rejected by the terminal mTLS mapper before its own check.
func TestRuntimeMachinePreparedChainRunsEachMechanismOnce(t *testing.T) {
	key := generateTestAdminKey(t, "once-key")
	t.Setenv("TEST_ONCE_HASH", key.TokenHash)
	var events atomic.Int64
	sink, err := newAdminMachineCredentialEventSink(adminCredentialEventSinkFunc(func(context.Context, AdminCredentialEvent) error {
		events.Add(1)
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{}
	cfg.Admin.Enabled = true
	cfg.Server.AdminAuth.APIKeys = []AdminAPIKeyConfig{{KeyID: key.KeyID, KeyHashEnv: "TEST_ONCE_HASH", Subject: "adminkey:once", Domain: "default", Permissions: []string{"admin:callers:list"}, Status: "active"}}
	rt := &adminMachineRuntime{replay: newAdminDPoPReplayState(), emergencySlots: make(chan struct{}, 1), lookup: os.LookupEnv, events: sink}
	compatible, err := newAuthorizer(cfg.Server.AdminAuth.Authorization, nil, nil, cfg.Server.AdminAuth)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := rt.prepare(cfg, compatible)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := generation.auth.(*adminMachineEventRecordingAuthenticator); !ok {
		t.Fatal("events are not wrapped around the final chain")
	}
	request := httptest.NewRequest("GET", "https://admin.example/admin/v1/x", nil)
	request.Header.Set("Authorization", "Bearer "+key.Token)
	if _, err := generation.auth.Authenticate(request.Context(), request); err != nil {
		t.Fatalf("configured key rejected: %v", err)
	}
	if events.Load() != 1 {
		t.Fatalf("credential events=%d want exactly1", events.Load())
	}
	// The mTLS mapper must be the last member so an emergency certificate is
	// evaluated before it, and the recorder wraps the whole chain once.
	inner := generation.auth.(*adminMachineEventRecordingAuthenticator).inner
	chain, ok := inner.(*authenticatorChain)
	if !ok || len(chain.mechanisms) == 0 {
		t.Fatal("wrapped chain missing")
	}
	for _, member := range chain.mechanisms {
		if _, doubled := member.(*adminMachineEventRecordingAuthenticator); doubled {
			t.Fatal("recording wrapper is a chain member: every mechanism would run twice")
		}
	}
}

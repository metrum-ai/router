// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"io"
	"net/http"
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

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

// Real merged #831 Service.AdminHandler and AdminPlane.Start, with manually
// prepared generations. Config-to-generation factory and production #816
// events are separate work, not established by this listener wiring test.
func TestRuntimeMachineGenerationActualListener(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Admin.Enabled = true
	cfg.Admin.Listen = "127.0.0.1:0"
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	old, oldToken := generationKeyFixture(t, "admin:callers:list")
	newer, newToken := generationKeyFixture(t, "admin:auth:read")
	var proofCalls atomic.Int64
	for _, generation := range []*adminMachineGeneration{old, newer} {
		proof := generation.auth
		generation.auth = testMachineAuthenticator(func(ctx context.Context, r *http.Request) (Principal, error) {
			proofCalls.Add(1)
			return proof.Authenticate(ctx, r)
		})
	}
	if err := svc.publishAdminMachineGeneration(old); err != nil {
		t.Fatal(err)
	}
	svc.RegisterAdminRoutes(func(m *AdminMux) {
		m.HandleFunc("GET /admin/v1/generation", func(w http.ResponseWriter, r *http.Request) {
			deadline, ok := r.Context().Deadline()
			if !ok || time.Until(deadline) > 60*time.Second {
				t.Error("actual listener missing finite60s handler context")
			}
			p, ok := PrincipalFrom(r.Context())
			if !ok {
				w.WriteHeader(500)
				return
			}
			for range 2 {
				checked, err := svc.AdminAuthenticator().Authenticate(r.Context(), r)
				if err != nil || checked != p {
					t.Error("contract recheck changed captured identity", err)
				}
			}
			object, action := "admin:callers", "list"
			if r.URL.Query().Get("target") == "auth" {
				object, action = "admin:auth", "read"
			}
			if err := svc.Authorize(svc.PinAuthorization(r.Context()), p, "default", object, action); err != nil {
				w.WriteHeader(403)
				return
			}
			w.WriteHeader(204)
		})
	})
	plane, err := NewAdminPlane(&cfg.Admin, svc.AdminHandler())
	if err != nil {
		t.Fatal(err)
	}
	if err := plane.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = plane.Shutdown(ctx)
	}()
	plane.mu.Lock()
	writeTimeout := plane.srv.WriteTimeout
	plane.mu.Unlock()
	if writeTimeout <= 0 || writeTimeout > 60*time.Second {
		t.Fatal("actual listener missing bounded write timeout")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	request := func(path, token string, want int) {
		t.Helper()
		r, err := http.NewRequest("GET", "http://"+plane.Addr()+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_, readErr := io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != want {
			t.Fatalf("actual generation listener status=%d want=%d", response.StatusCode, want)
		}
	}
	request("/healthz", "", 200)
	request("/admin/v1/generation", oldToken, 204)
	if err := svc.publishAdminMachineGeneration(newer); err != nil {
		t.Fatal(err)
	}
	request("/admin/v1/generation", oldToken, 401)
	request("/admin/v1/generation", newToken, 403)
	request("/admin/v1/generation?target=auth", newToken, 204)
	request("/readyz", "", 200)
	if proofCalls.Load() != 4 {
		t.Fatal("health/rechecks repeated authentication or recaptured a generation")
	}
	if _, err := svc.AdminAuthenticator().Authenticate(context.Background(), nil); err == nil {
		t.Fatal("captured adapter accepted identity without captured authenticated request")
	}
}

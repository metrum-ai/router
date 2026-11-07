// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func configuredMachineKeyFixture(t *testing.T, status, subject string) (*adminConfiguredKeyCatalog, string) {
	t.Helper()
	key := generateTestAdminKey(t, "machine-test")
	cfg := &Config{}
	cfg.Server.AdminAuth.APIKeys = []AdminAPIKeyConfig{{KeyID: key.KeyID, KeyHashEnv: "TEST_KEY_HASH", Subject: subject, Domain: "default", Permissions: []string{"admin:callers:list"}, Status: status}}
	catalog, err := newConfiguredAdminKeyCatalog(cfg, func(string) (string, bool) { return key.TokenHash, true })
	if err != nil {
		t.Fatal(err)
	}
	return catalog, key.Token
}
func configuredMachineKeyRequest(token string) *http.Request {
	r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func TestConfiguredAdminKeyMachineIdentityAndFailures(t *testing.T) {
	for _, tc := range []struct {
		status, subject string
		valid           bool
	}{{"active", "", true}, {"", "adminkey:custom", true}, {"disabled", "", false}, {"DISABLED", "", false}, {" Disabled ", "", false}} {
		t.Run(tc.status+tc.subject, func(t *testing.T) {
			catalog, token := configuredMachineKeyFixture(t, tc.status, tc.subject)
			a, err := newConfiguredAdminKeyAuthenticator(func() *adminConfiguredKeyCatalog { return catalog })
			if err != nil {
				t.Fatal(err)
			}
			p, err := a.Authenticate(context.Background(), configuredMachineKeyRequest(token))
			if tc.valid {
				subject := tc.subject
				if subject == "" {
					subject = "adminkey:machine-test"
				}
				if err != nil || p != (Principal{Subject: subject, Kind: "adminkey", CredentialID: "machine-test", Issuer: "router-config"}) {
					t.Fatalf("wrong identity: %+v %v", p, err)
				}
			} else if !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) {
				t.Fatal("disabled key authenticated")
			}
		})
	}
	catalog, token := configuredMachineKeyFixture(t, "active", "")
	a, _ := newConfiguredAdminKeyAuthenticator(func() *adminConfiguredKeyCatalog { return catalog })
	for _, bad := range []string{token + "x", "rtr_admin_broken", strings.Replace(token, "machine-test", "unknown", 1), strings.Repeat("rtr_admin_", 1000)} {
		if p, err := a.Authenticate(context.Background(), configuredMachineKeyRequest(bad)); !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) {
			t.Fatal("presented invalid key did not fail terminally")
		}
	}
	for _, absent := range []string{"", "provider-secret", "rtr_metrum_caller_secret", "some.jwt.token"} {
		if _, err := a.Authenticate(context.Background(), configuredMachineKeyRequest(absent)); !errors.Is(err, ErrNoCredential) {
			t.Fatal("other credential class consumed")
		}
	}
}

func TestConfiguredAdminKeyMachineCacheAndReload(t *testing.T) {
	catalog, token := configuredMachineKeyFixture(t, "active", "")
	var current atomic.Pointer[adminConfiguredKeyCatalog]
	current.Store(catalog)
	a, _ := newConfiguredAdminKeyAuthenticator(current.Load)
	if _, err := a.Authenticate(context.Background(), configuredMachineKeyRequest(token)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < cap(catalog.auth.kdfSlots); i++ {
		catalog.auth.kdfSlots <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := a.Authenticate(ctx, configuredMachineKeyRequest(token)); err != nil {
		t.Fatal("verified digest cache waited for KDF", err)
	}
	if _, err := a.Authenticate(ctx, configuredMachineKeyRequest(token+"x")); !errors.Is(err, ErrInvalidCredential) {
		t.Fatal("KDF waiter failed to cancel")
	}
	disabled, _ := configuredMachineKeyFixture(t, "disabled", "")
	current.Store(disabled)
	if _, err := a.Authenticate(context.Background(), configuredMachineKeyRequest(token)); !errors.Is(err, ErrInvalidCredential) {
		t.Fatal("digest bypassed disabled catalog")
	}
	// Even an already-cached proof cannot cross a catalog publication boundary.
	calls := 0
	crossing, _ := newConfiguredAdminKeyAuthenticator(func() *adminConfiguredKeyCatalog {
		calls++
		if calls == 1 {
			return catalog
		}
		return disabled
	})
	if _, err := crossing.Authenticate(context.Background(), configuredMachineKeyRequest(token)); !errors.Is(err, ErrInvalidCredential) {
		t.Fatal("reload during cached verification accepted")
	}
}

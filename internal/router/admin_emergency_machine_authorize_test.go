// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
)

// Real TLS HTTP + PBKDF + SQLite controller/Use + sole Service.Authorize.
// Test event callbacks are NOT the #816 production outbox/shared redactor;
// this is NOT actual AdminPlane listener/config/routerctl-binary integration.
func TestRuntimeEmergencyKeySoleAuthorizeBeforeDispatch(t *testing.T) {
	generated, err := GenerateAdminAPIKey("rescue", nil, CallerTokenHashDefaultIterations)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	catalog, err := newAdminEmergencyKeyCatalog([]adminEmergencyKeyVerifierConfig{{id: generated.KeyID, subject: "emergency:rescue", issuer: "local", tokenHash: generated.TokenHash, issuedAt: now.Add(-time.Minute), expiresAt: now.Add(30 * time.Minute)}}, nil, make(chan struct{}, 1), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	store, db := emergencyActivationFixture(t, filepath.Join(t.TempDir(), "config.sqlite"), func() time.Time { return now })
	if err := db.Exec("CREATE TABLE emergency_authorize_test_events (credential_id TEXT, domain TEXT, object TEXT, action TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	auth, err := newAdminEmergencyKeyMachineAuthenticator(func() *adminEmergencyKeyCatalog { return catalog }, store, func(tx *gorm.DB, _ Principal, a adminEmergencyActivation) error { return emergencyTestRecorder(tx, a) })
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true}, nil, nil, AdminAuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{authorizer: authorizer}
	// Channel-driven audit mode is synchronized by completed round trips below;
	// no concurrent mutation of Service hooks during the race-enabled test.
	auditMode := make(chan bool, 1)
	svc.authorizationAudit = func(ctx context.Context, p Principal, domain, object, action string, decision error) error {
		fail := false
		select {
		case fail = <-auditMode:
		default:
			t.Error("unauthenticated request reached authorization audit")
			return errors.New("unexpected authorization audit")
		}
		if p.Kind != "emergency" || p.CredentialID != generated.KeyID || domain != AdminGlobalDomain || object != "admin:auth" || action != "read" || decision != nil {
			t.Error("wrong sole Authorize identity/target/decision")
		}
		return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Table("emergency_authorize_test_events").Create(map[string]any{"credential_id": p.CredentialID, "domain": domain, "object": object, "action": action}).Error; err != nil {
				return err
			}
			if fail {
				return errors.New("authorization audit unavailable")
			}
			return nil
		})
	}
	dispatched := make(chan struct{}, 8)
	handler := adminMachineAuthenticationMiddleware(NewAuthenticatorChain(auth))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFrom(r.Context())
		if !ok {
			t.Error("missing trusted Principal")
			w.WriteHeader(500)
			return
		}
		ctx := svc.PinAuthorization(r.Context())
		if err := svc.Authorize(ctx, p, AdminGlobalDomain, "admin:auth", "read"); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		dispatched <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	command, err := newAdminEmergencyOfflineCommand(store, catalog.credential, emergencyTestRecorder, func(tx *gorm.DB, d adminEmergencyDeactivation) error { return emergencyTestRecorder(tx, d.Activation) })
	if err != nil {
		t.Fatal(err)
	}
	request := func(want int, wantDispatch bool) {
		t.Helper()
		r, err := http.NewRequest("GET", server.URL+"/admin/v1/config", nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+generated.Token)
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("request status=%d want=%d", response.StatusCode, want)
		}
		select {
		case <-dispatched:
			if !wantDispatch {
				t.Fatal("dispatched before successful sole Authorize acknowledgement")
			}
		default:
			if wantDispatch {
				t.Fatal("authorized operation not dispatched")
			}
		}
	}
	request(http.StatusUnauthorized, false)
	var output bytes.Buffer
	if err := command.Run(context.Background(), []string{"activate", "--credential-id", generated.KeyID, "--reason", "incident", "--until", now.Add(10 * time.Minute).Format(time.RFC3339Nano)}, &output); err != nil {
		t.Fatal(err)
	}
	auditMode <- false
	request(http.StatusNoContent, true)
	auditMode <- true
	request(http.StatusServiceUnavailable, false)
	output.Reset()
	if err := command.Run(context.Background(), []string{"deactivate", "--credential-id", generated.KeyID, "--reason", "resolved"}, &output); err != nil {
		t.Fatal(err)
	}
	request(http.StatusUnauthorized, false)
	var authzEvents, events int64
	if err := db.Table("emergency_authorize_test_events").Count(&authzEvents).Error; err != nil || authzEvents != 1 {
		t.Fatal("failed authorization audit committed")
	}
	if err := db.Table("emergency_test_events").Count(&events).Error; err != nil || events != 4 {
		t.Fatal("activate/Use/Use/deactivate acknowledgements incorrect")
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
)

// Native-peer/state/controller integration. Not actual listener/binary,
// production event/redactor or sole Authorize integration for certificates.
func TestRuntimeEmergencyCertificateMachineLifecycle(t *testing.T) {
	ca, key, roots := certificateBindingCA(t)
	leaf := certificateBindingLeaf(t, ca, key, "valid")
	now := time.Now().UTC().Truncate(time.Millisecond)
	cfg := adminEmergencyCertificateConfig{id: "rescue-cert", subject: "emergency:rescue", issuer: "offline-enrollment", thumbprint: certificateBindingThumb(leaf), issuedAt: now.Add(-time.Minute), expiresAt: now.Add(30 * time.Minute)}
	catalog, err := newAdminEmergencyCertificateCatalog([]adminEmergencyCertificateConfig{cfg}, nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newAdminEmergencyCertificateCatalog([]adminEmergencyCertificateConfig{cfg}, map[string]bool{cfg.id: false}, nil); err == nil {
		t.Fatal("disabled ordinary/emergency key ID collided with certificate enrollment")
	}
	store, db := emergencyActivationFixture(t, filepath.Join(t.TempDir(), "config.sqlite"), func() time.Time { return now })
	failUse := false
	auth, err := newAdminEmergencyCertificateMachineAuthenticator(func() *adminEmergencyCertificateCatalog { return catalog }, func() *x509.CertPool { return roots }, store, func(tx *gorm.DB, p Principal, a adminEmergencyActivation) error {
		if p.Kind != "emergency" || p.Subject != cfg.subject || p.CredentialID != cfg.id {
			t.Error("wrong enrolled certificate identity")
		}
		if failUse {
			return errors.New("audit unavailable")
		}
		return emergencyTestRecorder(tx, a)
	})
	if err != nil {
		t.Fatal(err)
	}
	mapper, err := newAdminMTLSAuthenticator("ordinary-ca", runtimeMTLSRootSource(func() *x509.CertPool { return roots }), map[string]string{"dns:unrelated.example": "unrelated"})
	if err != nil {
		t.Fatal(err)
	}
	mapperCalls := 0
	countedMapper := testMachineAuthenticator(func(ctx context.Context, r *http.Request) (Principal, error) {
		mapperCalls++
		return mapper.Authenticate(ctx, r)
	})
	// Counted wrapper is deliberately not skippable. Check invalid emergency
	// stops before it, then use exact pure mapper for the positive composition.
	failingChain := NewAuthenticatorChain(auth, countedMapper)
	chain := NewAuthenticatorChain(auth, mapper)
	request := func() *http.Request {
		r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf, ca}}}
		return r
	}
	denied := func() {
		t.Helper()
		mapperCalls = 0
		r := request()
		if p, err := failingChain.Authenticate(r.Context(), r); !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) || mapperCalls != 0 {
			t.Fatal("invalid/inactive enrolled certificate fell through")
		}
	}
	command, err := newAdminEmergencyOfflineCommand(store, catalog.credential, emergencyTestRecorder, func(tx *gorm.DB, d adminEmergencyDeactivation) error { return emergencyTestRecorder(tx, d.Activation) })
	if err != nil {
		t.Fatal(err)
	}
	credential, ok := catalog.credential(cfg.id)
	if !ok {
		t.Fatal("trusted descriptor missing")
	}
	denied()
	if _, err := store.Activate(context.Background(), credential, "incident", now.Add(10*time.Minute), emergencyTestRecorder); err != nil {
		t.Fatal(err)
	}
	r := request()
	if p, err := chain.Authenticate(r.Context(), r); err != nil || p.Kind != "emergency" || p.Subject != cfg.subject {
		t.Error("active emergency certificate required unrelated SAN identity", err)
	}
	mapperCalls = 0
	r = request()
	if p, err := failingChain.Authenticate(r.Context(), r); !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) || mapperCalls != 1 {
		t.Fatal("certificate acknowledgement skipped independent wrapper obligation")
	}
	// The committed Use attempt survives this later wrapper denial without
	// granting identity or dispatch.
	failUse = true
	denied()
	failUse = false
	// Use the actual private offline controller for cleanup; never HTTP.
	var output bytes.Buffer
	if err := command.Run(context.Background(), []string{"deactivate", "--credential-id", cfg.id, "--reason", "resolved"}, &output); err != nil {
		t.Fatal(err)
	}
	denied()
	var events int64
	if err := db.Table("emergency_test_events").Count(&events).Error; err != nil || events != 4 {
		t.Fatal("failed certificate Use ack committed or lifecycle event missing")
	}
}

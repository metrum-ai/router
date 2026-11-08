// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
)

// Every negative has an active, binding-matched durable enrollment: a missing
// Use record cannot be explained by an unrelated inactive state or wrong pin.
func TestRuntimeEmergencyCertificateNativeProof(t *testing.T) {
	ca, key, roots := certificateBindingCA(t)
	_, _, wrongRoots := certificateBindingCA(t)
	for _, mode := range []string{"valid", "client-and-server", "no-eku", "any-eku", "client-and-any", "server", "expired", "future", "unverified", "wrong-ca", "nil-roots", "missing", "forwarded-only"} {
		t.Run(mode, func(t *testing.T) {
			leaf := certificateBindingLeaf(t, ca, key, mode)
			now := time.Now().UTC().Truncate(time.Millisecond)
			cfg := adminEmergencyCertificateConfig{id: "rescue-cert", subject: "emergency:rescue", issuer: "local", thumbprint: certificateBindingThumb(leaf), issuedAt: now.Add(-time.Minute), expiresAt: now.Add(30 * time.Minute)}
			catalog, err := newAdminEmergencyCertificateCatalog([]adminEmergencyCertificateConfig{cfg}, nil, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			credential, ok := catalog.credential(cfg.id)
			if !ok {
				t.Fatal("no trusted binding")
			}
			store, db := emergencyActivationFixture(t, filepath.Join(t.TempDir(), "config.sqlite"), func() time.Time { return now })
			if _, err := store.Activate(context.Background(), credential, "incident", now.Add(10*time.Minute), emergencyTestRecorder); err != nil {
				t.Fatal(err)
			}
			currentRoots := roots
			uses := 0
			auth, err := newAdminEmergencyCertificateMachineAuthenticator(func() *adminEmergencyCertificateCatalog { return catalog }, func() *x509.CertPool { return currentRoots }, store, func(tx *gorm.DB, p Principal, a adminEmergencyActivation) error {
				uses++
				return emergencyTestRecorder(tx, a)
			})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
			r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf, ca}}}
			switch mode {
			case "unverified":
				r.TLS.VerifiedChains = nil
			case "wrong-ca":
				currentRoots = wrongRoots
			case "nil-roots":
				currentRoots = nil
			case "missing":
				r.TLS.PeerCertificates = nil
			case "forwarded-only":
				r.TLS = nil
				r.Header.Set("X-Forwarded-Client-Cert", cfg.thumbprint)
			}
			p, err := NewAuthenticatorChain(auth).Authenticate(r.Context(), r)
			success := mode == "valid" || mode == "client-and-server"
			if success {
				if err != nil || p.Kind != "emergency" || uses != 1 {
					t.Fatal("valid native certificate control failed", err)
				}
			} else if err == nil || p != (Principal{}) || uses != 0 {
				t.Fatal("invalid proof minted identity or reached durable Use")
			}
			want := int64(1)
			if success {
				want++
			}
			var events int64
			if err := db.Table("emergency_test_events").Count(&events).Error; err != nil || events != want {
				t.Fatal("unexpected durable proof acknowledgement")
			}
		})
	}
}

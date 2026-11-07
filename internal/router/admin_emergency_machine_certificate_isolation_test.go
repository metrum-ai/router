// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestRuntimeEmergencyCertificatePublicationAndAck(t *testing.T) {
	ca, key, roots := certificateBindingCA(t)
	_, _, replacement := certificateBindingCA(t)
	for _, mode := range []string{"catalog", "roots", "deadline", "enrollment-clock", "cancel", "audit-error"} {
		t.Run(mode, func(t *testing.T) {
			leaf := certificateBindingLeaf(t, ca, key, "valid")
			now := time.Now().UTC().Truncate(time.Millisecond)
			catalogNow := now
			cfg := adminEmergencyCertificateConfig{id: "rescue-cert", subject: "emergency:rescue", issuer: "local", thumbprint: certificateBindingThumb(leaf), issuedAt: now.Add(-time.Minute), expiresAt: now.Add(30 * time.Minute)}
			current, err := newAdminEmergencyCertificateCatalog([]adminEmergencyCertificateConfig{cfg}, nil, func() time.Time { return catalogNow })
			if err != nil {
				t.Fatal(err)
			}
			empty, err := newAdminEmergencyCertificateCatalog(nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			currentRoots := roots
			store, db := emergencyActivationFixture(t, filepath.Join(t.TempDir(), "config.sqlite"), func() time.Time { return now })
			credential, ok := current.credential(cfg.id)
			if !ok {
				t.Fatal("missing trusted descriptor")
			}
			if _, err := store.Activate(context.Background(), credential, "incident", now.Add(10*time.Minute), emergencyTestRecorder); err != nil {
				t.Fatal(err)
			}
			trigger := false
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			record := func(tx *gorm.DB, _ Principal, a adminEmergencyActivation) error {
				if err := emergencyTestRecorder(tx, a); err != nil {
					return err
				}
				if trigger {
					switch mode {
					case "catalog":
						current = empty
					case "roots":
						currentRoots = replacement
					case "deadline":
						// Store.Use itself rechecks its clock after the callback
						// and rolls back an attempt that crosses its deadline.
						now, catalogNow = cfg.expiresAt, cfg.expiresAt
					case "enrollment-clock":
						// Isolate the adapter's post-ack check: only its enrollment
						// clock advances, so the store commits the attempt.
						catalogNow = cfg.expiresAt
					case "cancel":
						cancel()
					case "audit-error":
						return errors.New("event commit unavailable")
					}
				}
				return nil
			}
			auth, err := newAdminEmergencyCertificateMachineAuthenticator(func() *adminEmergencyCertificateCatalog { return current }, func() *x509.CertPool { return currentRoots }, store, record)
			if err != nil {
				t.Fatal(err)
			}
			chain := NewAuthenticatorChain(auth)
			native := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil).WithContext(ctx)
			native.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf, ca}}}
			if p, err := chain.Authenticate(ctx, native); err != nil || p.Kind != "emergency" {
				t.Fatal("positive acknowledged certificate control rejected", err)
			}
			trigger = true
			if p, err := chain.Authenticate(ctx, native); !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) {
				t.Fatal("publication/expiry/cancel/audit failure produced identity")
			}
			want := int64(3)
			if mode == "cancel" || mode == "audit-error" || mode == "deadline" {
				want = 2
			}
			var events int64
			if err := db.Table("emergency_test_events").Count(&events).Error; err != nil || events != want {
				t.Fatalf("ack event count=%d want=%d err=%v", events, want, err)
			}
		})
	}
}

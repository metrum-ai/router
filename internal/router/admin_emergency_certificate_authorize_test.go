// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

// Actual client-private-key TLS -> certificate proof -> durable Use -> sole
// Service.Authorize -> dispatch. Test transactions, NOT production outbox,
// actual AdminPlane configuration/listener lifecycle or routerctl binary.
func TestRuntimeEmergencyCertificateNativeSoleAuthorize(t *testing.T) {
	ca, caKey, _ := certificateBindingCA(t)
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	trustStoreWriteCA(t, caPath, ca)
	trust, err := NewAdminMTLSTrustStore(AdminMTLSTrustConfig{TrustDomains: []AdminMTLSTrustDomainConfig{{Issuer: "configured-ca", CAFiles: []string{caPath}}}})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := newAdminMTLSTrustAuthenticator(trust)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	template := &x509.Certificate{SerialNumber: big.NewInt(517), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	cfg := adminEmergencyCertificateConfig{id: "rescue-cert", subject: "emergency:rescue", issuer: "offline", thumbprint: certificateBindingThumb(leaf), issuedAt: now.Add(-time.Minute), expiresAt: now.Add(30 * time.Minute)}
	catalog, err := newAdminEmergencyCertificateCatalog([]adminEmergencyCertificateConfig{cfg}, nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	store, db := emergencyActivationFixture(t, filepath.Join(t.TempDir(), "config.sqlite"), func() time.Time { return now })
	if err := db.Exec("CREATE TABLE emergency_certificate_authorize_events (credential_id TEXT, domain TEXT, object TEXT, action TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	auth, err := newAdminEmergencyCertificateMachineAuthenticator(func() *adminEmergencyCertificateCatalog { return catalog }, trust.Snapshot, store, func(tx *gorm.DB, _ Principal, a adminEmergencyActivation) error { return emergencyTestRecorder(tx, a) })
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true}, nil, nil, AdminAuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{authorizer: authorizer}
	auditMode := make(chan bool, 1)
	svc.authorizationAudit = func(ctx context.Context, p Principal, domain, object, action string, decision error) error {
		var fail bool
		select {
		case fail = <-auditMode:
		default:
			t.Error("unauthenticated certificate reached authorization audit")
			return errors.New("unexpected audit")
		}
		if p.Kind != "emergency" || p.CredentialID != cfg.id || p.Subject != cfg.subject || domain != AdminGlobalDomain || object != "admin:auth" || action != "read" || decision != nil {
			t.Error("wrong certificate sole Authorize identity/decision")
		}
		return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Table("emergency_certificate_authorize_events").Create(map[string]any{"credential_id": p.CredentialID, "domain": domain, "object": object, "action": action}).Error; err != nil {
				return err
			}
			if fail {
				return errors.New("authorization audit unavailable")
			}
			return nil
		})
	}
	dispatched := make(chan struct{}, 4)
	operation := adminMachineAuthenticationMiddleware(NewAuthenticatorChain(auth, identity))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFrom(r.Context())
		if !ok {
			t.Error("no trusted certificate principal")
			w.WriteHeader(500)
			return
		}
		if err := svc.Authorize(svc.PinAuthorization(r.Context()), p, AdminGlobalDomain, "admin:auth", "read"); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		dispatched <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	var mu sync.Mutex
	var addresses []string
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		addresses = append(addresses, r.RemoteAddr)
		mu.Unlock()
		operation.ServeHTTP(w, r)
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: trust.Snapshot()}
	server.StartTLS()
	defer server.Close()
	client := server.Client()
	transport := client.Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.Certificates = []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: leafKey}}
	transport.ForceAttemptHTTP2 = false
	transport.MaxConnsPerHost = 1
	client.Transport = transport
	client.Timeout = 5 * time.Second
	defer transport.CloseIdleConnections()
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
			t.Fatalf("native emergency cert status=%d want=%d", response.StatusCode, want)
		}
		select {
		case <-dispatched:
			if !wantDispatch {
				t.Fatal("dispatched without sole Authorize ack")
			}
		default:
			if wantDispatch {
				t.Fatal("authorized certificate did not dispatch")
			}
		}
	}
	request(http.StatusUnauthorized, false)
	var output bytes.Buffer
	if err := command.Run(context.Background(), []string{"activate", "--credential-id", cfg.id, "--reason", "incident", "--until", now.Add(10 * time.Minute).Format(time.RFC3339Nano)}, &output); err != nil {
		t.Fatal(err)
	}
	auditMode <- false
	request(http.StatusNoContent, true)
	auditMode <- true
	request(http.StatusServiceUnavailable, false)
	output.Reset()
	if err := command.Run(context.Background(), []string{"deactivate", "--credential-id", cfg.id, "--reason", "resolved"}, &output); err != nil {
		t.Fatal(err)
	}
	request(http.StatusUnauthorized, false)
	var events, authzEvents int64
	if err := db.Table("emergency_test_events").Count(&events).Error; err != nil || events != 4 {
		t.Fatal("incorrect certificate lifecycle/Use acknowledgements")
	}
	if err := db.Table("emergency_certificate_authorize_events").Count(&authzEvents).Error; err != nil || authzEvents != 1 {
		t.Fatal("failed authorization audit committed")
	}
	mu.Lock()
	same := len(addresses) == 4
	for _, address := range addresses {
		if address != addresses[0] {
			same = false
		}
	}
	mu.Unlock()
	if !same {
		t.Fatal("deactivation lifecycle did not reuse one real native TLS connection")
	}
}

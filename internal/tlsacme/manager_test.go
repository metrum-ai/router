// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package tlsacme

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func boolPtr(v bool) *bool { return &v }

// newTestManager builds an ACME-mode manager with a stub certifier and a fake
// DNS provider, rooted in a temporary store.
func newTestManager(t *testing.T, now *time.Time, mutate func(*Config)) (*Manager, *stubCertifier, *Store) {
	return newTestManagerWithProvider(t, now, mutate, nil)
}

func newTestManagerWithProvider(t *testing.T, now *time.Time, mutate func(*Config), provider *fakeProvider) (*Manager, *stubCertifier, *Store) {
	t.Helper()
	// The manager resolves the DNS credential for redaction even when the test
	// injects the provider, so the reference must resolve.
	t.Setenv("CF_DNS_API_TOKEN", "test-token")
	cfg := validACMEConfig()
	cfg.CacheDir = filepath.Join(t.TempDir(), "tls")
	cfg.DNS.Zone = "example.com"
	// ARI is exercised by the ARI tests; unit tests must not perform HTTP.
	cfg.Renewal.UseARI = boolPtr(false)
	if mutate != nil {
		mutate(&cfg)
	}
	store, err := OpenStore(cfg.CacheDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	leaf := makeTestCertificate(t, cfg.Domains, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	certifier := &stubCertifier{cert: leaf}
	if provider == nil {
		provider = &fakeProvider{zones: []string{"example.com"}}
	}
	manager, err := New(cfg,
		WithStore(store),
		WithProvider(provider),
		WithCertifier(certifier),
		WithNow(func() time.Time { return *now }),
		WithRandomInt63(func(int64) int64 { return 0 }),
	)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return manager, certifier, store
}

func TestManagerOffModeIsInert(t *testing.T) {
	manager, err := New(Config{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if manager.TLSConfig() != nil {
		t.Fatal("TLSConfig must be nil in off mode")
	}
	if manager.Prometheus() != "" {
		t.Fatalf("off mode must export no TLS metrics (v4.7 output is unchanged), got %q", manager.Prometheus())
	}
	if err := manager.EnsureCertificate(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
}

func TestManagerTLSConfigVersions(t *testing.T) {
	now := time.Now()
	manager, _, _ := newTestManager(t, &now, nil)
	cfg := manager.TLSConfig()
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("default min version = %x, want TLS 1.2", cfg.MinVersion)
	}
	if len(cfg.NextProtos) == 0 || cfg.NextProtos[0] != "h2" {
		t.Fatalf("ALPN = %v, want h2 first", cfg.NextProtos)
	}
	if cfg.ClientAuth != tls.NoClientCert && cfg.ClientAuth != 0 {
		t.Fatalf("ClientAuth = %v; mTLS must be composed by the caller", cfg.ClientAuth)
	}

	now2 := time.Now()
	strict, _, _ := newTestManager(t, &now2, func(c *Config) { c.MinVersion = "1.3" })
	if got := strict.TLSConfig().MinVersion; got != tls.VersionTLS13 {
		t.Fatalf("min version = %x, want TLS 1.3", got)
	}
}

func TestManagerIssuesAndReusesCertificate(t *testing.T) {
	now := time.Now()
	manager, certifier, store := newTestManager(t, &now, nil)
	if err := manager.EnsureCertificate(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if certifier.calls != 1 {
		t.Fatalf("certifier calls = %d, want 1", certifier.calls)
	}
	record, err := store.LoadCertificate(manager.cfg.Domains)
	if err != nil || record == nil {
		t.Fatalf("stored record = %+v err = %v", record, err)
	}
	status := manager.Status()
	if len(status) != 1 || !status[0].Valid || status[0].Source != "issued" {
		t.Fatalf("status = %+v", status)
	}
	wantDeadline := record.NotBefore.Add(time.Duration(float64(record.NotAfter.Sub(record.NotBefore)) * (2.0 / 3.0)))
	// The deadline carries the configured jitter (delta is +- the jitter).
	tolerance := manager.cfg.Renewal.Jitter + time.Second
	if delta := status[0].RenewalDue.Sub(wantDeadline); delta > tolerance || delta < -tolerance {
		t.Fatalf("renewal due = %v, want about %v (two thirds of lifetime)", status[0].RenewalDue, wantDeadline)
	}

	// A second manager (a restart, or another replica) that shares the store
	// loads the stored certificate and issues nothing.
	second, secondCertifier, _ := newTestManager(t, &now, nil)
	second.store = store
	secondCertifier.calls = 0
	if err := second.EnsureCertificate(context.Background()); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if secondCertifier.calls != 0 {
		t.Fatalf("second manager issued %d certificate(s) despite a valid cache", secondCertifier.calls)
	}
}

func TestManagerHandshakeServesCertificateAndRejectsUnknownName(t *testing.T) {
	now := time.Now()
	manager, _, _ := newTestManager(t, &now, nil)
	if err := manager.EnsureCertificate(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	cfg := manager.TLSConfig()

	pool := x509.NewCertPool()
	record := manager.record
	if record == nil {
		t.Fatal("no record installed")
	}
	if !pool.AppendCertsFromPEM(record.CertificatePEM) {
		t.Fatal("could not build a root pool")
	}

	handshake := func(serverName string) error {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		defer func() { _ = listener.Close() }()
		serverErr := make(chan error, 1)
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				serverErr <- err
				return
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			serverErr <- tls.Server(conn, cfg).Handshake()
		}()
		dialer := &net.Dialer{Timeout: 5 * time.Second}
		client, err := tls.DialWithDialer(dialer, "tcp", listener.Addr().String(), &tls.Config{RootCAs: pool, ServerName: serverName})
		if err == nil {
			_ = client.Close()
		}
		if clientErr := err; clientErr != nil {
			<-serverErr
			return fmt.Errorf("client: %w", clientErr)
		}
		return <-serverErr
	}
	if err := handshake("router.example.com"); err != nil {
		t.Fatalf("handshake for the configured name failed: %v", err)
	}
	if err := handshake("other.example.com"); err == nil {
		t.Fatal("handshake for an unknown name must fail")
	}
}

func TestManagerRenewalFailureRaisesMetricAndKeepsServing(t *testing.T) {
	now := time.Now()
	manager, certifier, _ := newTestManager(t, &now, nil)
	if err := manager.EnsureCertificate(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	// Move past the renewal deadline and fail the next order.
	now = now.Add(70 * 24 * time.Hour)
	certifier.err = errors.New("acme: order failed: rejectedIdentifier")

	manager.tick(context.Background())
	if got := manager.metrics.value(metricRenewalFailures, mergeLabels(manager.metrics.baseLabels(), [2]string{"reason", "acme"})); got != 1 {
		t.Fatalf("renewal failure metric = %v, want 1", got)
	}
	if manager.record == nil {
		t.Fatal("the previous certificate must keep being served")
	}
	if manager.Status()[0].Valid != true {
		t.Fatal("the previously issued certificate is still valid at this point")
	}
	if manager.nextAttempt.IsZero() {
		t.Fatal("a failed renewal must schedule a retry")
	}

	// A later success installs the new certificate.
	certifier.err = nil
	certifier.cert = makeTestCertificate(t, manager.cfg.Domains, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	before := manager.record.NotAfter
	now = manager.nextAttempt
	manager.tick(context.Background())
	if !manager.record.NotAfter.After(before) {
		t.Fatalf("renewal did not install the new certificate: %v <= %v", manager.record.NotAfter, before)
	}
	if got := manager.metrics.value(metricACMEOrders, mergeLabels(manager.metrics.baseLabels(), [2]string{"result", "success"})); got != 2 {
		t.Fatalf("successful orders = %v, want 2", got)
	}
}

func TestManagerDoesNotIssueWhileAnotherReplicaHoldsTheLock(t *testing.T) {
	now := time.Now()
	manager, certifier, store := newTestManager(t, &now, nil)
	release, err := store.TryLock()
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	defer release()
	err = manager.issue(context.Background())
	if !errors.Is(err, errIssuerBusy) {
		t.Fatalf("issue error = %v, want the busy error", err)
	}
	if certifier.calls != 0 {
		t.Fatalf("certifier was called %d times while the lock was held", certifier.calls)
	}
	// Contention is not a renewal failure.
	manager.tick(context.Background())
	if got := manager.metrics.value(metricRenewalFailures, mergeLabels(manager.metrics.baseLabels(), [2]string{"reason", "busy"})); got != 0 {
		t.Fatalf("busy contention counted as a renewal failure: %v", got)
	}
}

func TestManagerPicksUpCertificateRenewedByAnotherReplica(t *testing.T) {
	now := time.Now()
	manager, _, store := newTestManager(t, &now, nil)
	if err := manager.EnsureCertificate(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	old := manager.record.NotAfter
	renewed := makeTestCertificate(t, manager.cfg.Domains, now.Add(-time.Hour), now.Add(120*24*time.Hour))
	if err := store.SaveCertificate(CertificateRecord{
		Domains:        manager.cfg.Domains,
		CertificatePEM: renewed.CertificatePEM,
		PrivateKeyPEM:  renewed.PrivateKeyPEM,
		Issuer:         "https://acme.test/directory",
		NotBefore:      renewed.NotBefore,
		NotAfter:       renewed.NotAfter,
		IssuedAt:       now,
		KeyType:        KeyTypeECDSAP256,
	}); err != nil {
		t.Fatalf("save renewed record: %v", err)
	}
	manager.maybeReload(true)
	if !manager.record.NotAfter.After(old) {
		t.Fatalf("replica did not reload the renewed certificate: %v", manager.record.NotAfter)
	}
	if manager.Status()[0].Source != "store" {
		t.Fatalf("source = %q, want store", manager.Status()[0].Source)
	}
}

func TestManagerRejectsMismatchedStoredCertificate(t *testing.T) {
	now := time.Now()
	manager, _, store := newTestManager(t, &now, nil)
	other := makeTestCertificate(t, []string{"other.example.com"}, now.Add(-time.Hour), now.Add(24*time.Hour))
	if err := store.SaveCertificate(CertificateRecord{
		Domains:        manager.cfg.Domains,
		CertificatePEM: other.CertificatePEM,
		PrivateKeyPEM:  other.PrivateKeyPEM,
		NotBefore:      other.NotBefore,
		NotAfter:       other.NotAfter,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := manager.EnsureCertificate(context.Background()); err == nil || !strings.Contains(err.Error(), "does not cover") {
		t.Fatalf("error = %v, want a name coverage error", err)
	}
}

func TestManagerFilesMode(t *testing.T) {
	dir := t.TempDir()
	cert := makeTestCertificate(t, []string{"router.example.com"}, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certPath, cert.CertificatePEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, cert.PrivateKeyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	manager, err := New(Config{
		Mode:              ModeRouter,
		CertificateSource: SourceFiles,
		Domains:           []string{"router.example.com"},
		CertFile:          certPath,
		KeyFile:           keyPath,
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if manager.TLSConfig() == nil {
		t.Fatal("files mode must serve a TLS config")
	}
	if got := manager.Status(); len(got) != 1 || got[0].Source != "files" {
		t.Fatalf("status = %+v", got)
	}
	if manager.Prometheus() == "" {
		t.Fatal("files mode should export certificate metrics")
	}

	if _, err := New(Config{Mode: ModeRouter, CertificateSource: SourceFiles, Domains: []string{"router.example.com"}, CertFile: filepath.Join(dir, "missing"), KeyFile: keyPath}); err == nil {
		t.Fatal("a missing certificate file must fail")
	}
}

func TestManagerPrometheusExposesExpiryAndRenewal(t *testing.T) {
	now := time.Now()
	manager, _, _ := newTestManager(t, &now, nil)
	if err := manager.EnsureCertificate(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	output := manager.Prometheus()
	for _, want := range []string{
		"# TYPE " + metricCertExpiry + " gauge",
		"metrum_ai_router_tls_certificate_expiry_timestamp_seconds{listener=\"serving\",domains=\"router.example.com\"}",
		metricCertRenewalDue,
		metricCertValid + "{listener=\"serving\",domains=\"router.example.com\"} 1",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("metrics output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "PRIVATE KEY") {
		t.Fatal("metrics output contains key material")
	}
}

func TestManagerPotentialJitterStaysInsideLifetime(t *testing.T) {
	now := time.Now()
	manager, _, _ := newTestManager(t, &now, func(c *Config) {
		c.Renewal.Jitter = 48 * time.Hour // above the 10% cap for a 90 day cert
	})
	if err := manager.EnsureCertificate(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	status := manager.Status()[0]
	if status.RenewalDue.Before(status.NotBefore) || status.RenewalDue.After(status.NotAfter) {
		t.Fatalf("renewal due %v outside the certificate lifetime %v..%v", status.RenewalDue, status.NotBefore, status.NotAfter)
	}
}

func TestManagerReissuesExpiredCache(t *testing.T) {
	now := time.Now()
	manager, certifier, store := newTestManager(t, &now, nil)
	expired := makeTestCertificate(t, manager.cfg.Domains, now.Add(-48*time.Hour), now.Add(-time.Hour))
	if err := store.SaveCertificate(CertificateRecord{
		Domains:        manager.cfg.Domains,
		CertificatePEM: expired.CertificatePEM,
		PrivateKeyPEM:  expired.PrivateKeyPEM,
		NotBefore:      expired.NotBefore,
		NotAfter:       expired.NotAfter,
	}); err != nil {
		t.Fatalf("save expired record: %v", err)
	}
	certifier.calls = 0
	if err := manager.EnsureCertificate(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if certifier.calls != 1 {
		t.Fatalf("certifier calls = %d, want 1 (an expired cache must be reissued)", certifier.calls)
	}
	if got := manager.Status(); len(got) != 1 || !got[0].Valid {
		t.Fatalf("status after reissue = %+v", got)
	}
}

func TestManagerServesValidCacheWhenRenewalCannotRun(t *testing.T) {
	now := time.Now()
	manager, certifier, _ := newTestManager(t, &now, func(c *Config) {
		// Renew almost immediately so the cached certificate is already due.
		c.Renewal.WindowRatio = 0.1
		c.Renewal.Jitter = time.Nanosecond
	})
	if err := manager.EnsureCertificate(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	// A second manager with the same store and an unreachable CA must still
	// start: the valid cache is served and renewal retries in the background.
	certifier.calls = 0
	second, secondCertifier, _ := newTestManager(t, &now, func(c *Config) {
		c.Renewal.WindowRatio = 0.1
		c.Renewal.Jitter = time.Nanosecond
	})
	second.store = manager.store
	secondCertifier.err = errors.New("acme: CA unavailable")
	if err := second.EnsureCertificate(context.Background()); err != nil {
		t.Fatalf("a valid cache must let the router start while the CA is down: %v", err)
	}
	if second.record == nil {
		t.Fatal("the cached certificate was not installed")
	}
	// Move past the renewal deadline: the first tick retries and records the
	// failure without touching the certificate in service.
	now = second.record.NotBefore.Add(second.record.NotAfter.Sub(second.record.NotBefore) / 4)
	second.tick(context.Background())
	if got := second.metrics.value(metricRenewalFailures, mergeLabels(second.metrics.baseLabels(), [2]string{"reason", "acme"})); got != 1 {
		t.Fatalf("renewal failure metric = %v, want 1", got)
	}
	if second.record == nil {
		t.Fatal("a failed renewal must keep the certificate in service")
	}
}

func TestManagerAdoptsCertificateRenewedUnderTheLock(t *testing.T) {
	now := time.Now()
	// Replica A holds the store and has already renewed.
	a, aCertifier, store := newTestManager(t, &now, nil)
	if err := a.EnsureCertificate(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	// Replica B still believes the (older) certificate is due.
	b, bCertifier, _ := newTestManager(t, &now, func(c *Config) {
		c.Renewal.WindowRatio = 0.01
		c.Renewal.Jitter = time.Nanosecond
	})
	b.store = store
	bCertifier.calls = 0
	aCertifier.calls = 0
	if err := b.issue(context.Background()); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if bCertifier.calls != 0 {
		t.Fatalf("replica B ordered a duplicate renewal (%d calls); it must adopt the stored certificate", bCertifier.calls)
	}
	if b.record == nil {
		t.Fatal("replica B did not install the stored certificate")
	}
}

func TestManagerResolvesAndDecodesEABKey(t *testing.T) {
	now := time.Now()
	t.Setenv("TLS_TEST_EAB_KEY", "c2VjcmV0LWhtYWMta2V5") // base64url("secret-hmac-key")
	// The manager resolves the DNS credential for redaction even when the test
	// injects the provider, so the reference must resolve.
	t.Setenv("CF_DNS_API_TOKEN", "test-token")
	cfg := validACMEConfig()
	cfg.CacheDir = filepath.Join(t.TempDir(), "tls")
	cfg.DNS.Zone = "example.com"
	cfg.Renewal.UseARI = boolPtr(false)
	cfg.EAB = EABConfig{KID: "kid-1", HMACKey: SecretRef{Env: "TLS_TEST_EAB_KEY"}}
	store, err := OpenStore(cfg.CacheDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	manager, err := New(cfg, WithStore(store), WithProvider(&fakeProvider{zones: []string{"example.com"}}), WithNow(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	certifier, ok := manager.certifier.(*acmeCertifier)
	if !ok {
		t.Fatalf("certifier = %T", manager.certifier)
	}
	if certifier.eabKID != "kid-1" || certifier.eabHMACKey != "secret-hmac-key" {
		t.Fatalf("EAB not passed to the certifier: kid=%q key=%q", certifier.eabKID, certifier.eabHMACKey)
	}

	t.Setenv("TLS_TEST_EAB_KEY", "not base64!!")
	if _, err := New(cfg, WithStore(store), WithProvider(&fakeProvider{zones: []string{"example.com"}})); err == nil || !strings.Contains(err.Error(), "not valid base64url") {
		t.Fatalf("error = %v, want a base64url error", err)
	}
}

func TestManagerFilesModeRejectsUnsafeInput(t *testing.T) {
	dir := t.TempDir()
	expired := makeTestCertificate(t, []string{"router.example.com"}, time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour))
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certPath, expired.CertificatePEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, expired.PrivateKeyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	if _, err := New(Config{Mode: ModeRouter, CertificateSource: SourceFiles, Domains: []string{"router.example.com"}, CertFile: certPath, KeyFile: keyPath}); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("error = %v, want an expiry error", err)
	}

	valid := makeTestCertificate(t, []string{"router.example.com"}, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	if err := os.WriteFile(certPath, valid.CertificatePEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, valid.PrivateKeyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatalf("chmod key: %v", err)
	}
	if _, err := New(Config{Mode: ModeRouter, CertificateSource: SourceFiles, Domains: []string{"router.example.com"}, CertFile: certPath, KeyFile: keyPath}); err == nil || !strings.Contains(err.Error(), "group- or world-accessible") {
		t.Fatalf("error = %v, want a key permission error", err)
	}
}

func TestManagerReconcilesManagedRecordAtStartupAndOnChange(t *testing.T) {
	now := time.Now()
	provider := &fakeProvider{zones: []string{"example.com"}}
	manager, _, _ := newTestManagerWithProvider(t, &now, func(c *Config) {
		c.DNS.Record = RecordConfig{Manage: true, Type: "A", Value: "203.0.113.10", TTL: 300, Owner: "host-1"}
	}, provider)
	if err := manager.EnsureCertificate(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if got := provider.value("A", "router.example.com"); got != "203.0.113.10" {
		t.Fatalf("managed record = %q, want the configured address", got)
	}
	if marker := provider.value("TXT", "_metrum-router.router.example.com"); marker == "" {
		t.Fatal("no ownership marker was created")
	}

	// A changed address converges on the next reconciliation tick, without
	// waiting for the certificate to renew.
	for i := range provider.records {
		if provider.records[i].Type == "A" {
			provider.records[i].Value = "203.0.113.99"
		}
	}
	manager.cfg.DNS.Record.Value = "203.0.113.11"
	now = now.Add(reconcileInterval + time.Minute)
	manager.tick(context.Background())
	if got := provider.value("A", "router.example.com"); got != "203.0.113.11" {
		t.Fatalf("managed record = %q, want the new address", got)
	}
}

func TestManagerValidityGaugeTurnsInvalidAtExpiry(t *testing.T) {
	now := time.Now()
	manager, certifier, _ := newTestManager(t, &now, nil)
	if err := manager.EnsureCertificate(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	labels := manager.metrics.baseLabels()
	if got := manager.metrics.value(metricCertValid, labels); got != 1 {
		t.Fatalf("valid gauge = %v, want 1", got)
	}
	certifier.err = errors.New("acme: unavailable")
	now = manager.record.NotAfter.Add(time.Minute)
	manager.tick(context.Background())
	if got := manager.metrics.value(metricCertValid, labels); got != 0 {
		t.Fatalf("valid gauge = %v after expiry, want 0", got)
	}
	if !strings.Contains(manager.Prometheus(), metricCertValid) {
		t.Fatal("validity metric missing from /metrics")
	}
}

// TestManagerSeamComposesWithMTLS proves the listener seam the admin plane
// needs: one GetConfigForClient that asks this manager for the certificate and
// the SNI decision, then applies its own client-authentication policy.
func TestManagerSeamComposesWithMTLS(t *testing.T) {
	now := time.Now()
	manager, _, _ := newTestManager(t, &now, nil)
	if err := manager.EnsureCertificate(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	// A private client CA for the mTLS half.
	clientCA, clientCAKey, _ := makeTestCA(t, "router-client-ca")
	clientCAPool := x509.NewCertPool()
	clientCAPool.AddCert(clientCA)

	serverRoots := x509.NewCertPool()
	serverRoots.AppendCertsFromPEM(manager.record.CertificatePEM)

	listenerConfig := &tls.Config{
		MinVersion: manager.minVersion(),
		// The listener owns the single GetConfigForClient: it composes this
		// package's certificate selection with its own mTLS snapshot.
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			cert, err := manager.GetCertificate(hello)
			if err != nil {
				return nil, err
			}
			if err := manager.CheckSNI(hello); err != nil {
				return nil, err
			}
			return &tls.Config{
				MinVersion:   manager.minVersion(),
				Certificates: []tls.Certificate{*cert},
				ClientAuth:   tls.RequireAndVerifyClientCert,
				ClientCAs:    clientCAPool,
			}, nil
		},
	}

	handshake := func(clientCert tls.Certificate, withCert bool) error {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		defer func() { _ = listener.Close() }()
		serverErr := make(chan error, 1)
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				serverErr <- err
				return
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			serverErr <- tls.Server(conn, listenerConfig).Handshake()
		}()
		clientConfig := &tls.Config{RootCAs: serverRoots, ServerName: "router.example.com"}
		if withCert {
			clientConfig.Certificates = []tls.Certificate{clientCert}
		}
		dialer := &net.Dialer{Timeout: 5 * time.Second}
		conn, err := tls.DialWithDialer(dialer, "tcp", listener.Addr().String(), clientConfig)
		if err == nil {
			_ = conn.Close()
			return <-serverErr
		}
		<-serverErr
		return err
	}

	goodClient := issueTestClientCertificate(t, clientCA, clientCAKey, "client-1")
	if err := handshake(goodClient, true); err != nil {
		t.Fatalf("ACME certificate + mTLS handshake failed: %v", err)
	}
	if err := handshake(tls.Certificate{}, false); err == nil {
		t.Fatal("a client without a certificate must fail when the listener requires one")
	}
	otherCA, otherCAKey, _ := makeTestCA(t, "other-ca")
	wrongCA := issueTestClientCertificate(t, otherCA, otherCAKey, "client-2")
	if err := handshake(wrongCA, true); err == nil {
		t.Fatal("a client certificate from an unknown CA must fail the handshake")
	}
}

// makeTestCA returns a self-signed CA certificate, its signer and its PEM.
func makeTestCA(t *testing.T, name string) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA: %v", err)
	}
	return parsed, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// issueTestClientCertificate signs a client certificate with the test CA.
func issueTestClientCertificate(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, commonName string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create client certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// failingCertifier returns a fixed error, to exercise the startup and renewal
// error paths.
type failingCertifier struct {
	err   error
	calls int
}

func (f *failingCertifier) Issue(context.Context, []string) (issuedCertificate, error) {
	f.calls++
	return issuedCertificate{}, f.err
}

// TestManagerRedactsSecretsInStartupAndRenewalErrors is the regression test for
// the redaction bug: an error text that quotes a resolved credential must never
// reach the caller (main logs it with log.Fatalf) or the renewal log.
func TestManagerRedactsSecretsInStartupAndRenewalErrors(t *testing.T) {
	const (
		envName = "TLS_MANAGER_SENTINEL_TOKEN"
		secret  = "sentinel-provider-token-abcdef"
	)
	t.Setenv(envName, secret)
	now := time.Now()

	newManager := func(certifier Certifier, logs *bytes.Buffer) *Manager {
		cfg := validACMEConfig()
		cfg.CacheDir = filepath.Join(t.TempDir(), "tls")
		cfg.DNS.Zone = "example.com"
		cfg.DNS.Provider = ProviderDigitalOcean
		cfg.DNS.Credentials = Credentials{Token: SecretRef{Env: envName}}
		cfg.Renewal.UseARI = boolPtr(false)
		store, err := OpenStore(cfg.CacheDir)
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		manager, err := New(cfg,
			WithStore(store),
			WithProvider(&fakeProvider{zones: []string{"example.com"}}),
			WithCertifier(certifier),
			WithNow(func() time.Time { return now }),
			WithLogger(log.New(logs, "", 0)),
		)
		if err != nil {
			t.Fatalf("new manager: %v", err)
		}
		return manager
	}

	// Startup: the provider error quotes the credential; main turns this error
	// into log.Fatalf, so it must already be scrubbed.
	var startupLogs bytes.Buffer
	startup := newManager(&failingCertifier{err: fmt.Errorf("acme: provider rejected token %s", secret)}, &startupLogs)
	err := startup.EnsureCertificate(context.Background())
	if err == nil {
		t.Fatal("expected a startup error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("startup error leaks the credential: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "<redacted:") {
		t.Fatalf("startup error is not redacted: %q", err.Error())
	}

	// Renewal: install a valid certificate, then fail the next order with the
	// same credential in the message; the log must be scrubbed too.
	var renewalLogs bytes.Buffer
	certifier := &stubCertifier{cert: makeTestCertificate(t, []string{"router.example.com"}, now.Add(-time.Hour), now.Add(90*24*time.Hour))}
	renewing := newManager(certifier, &renewalLogs)
	if err := renewing.EnsureCertificate(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	renewalLogs.Reset()
	certifier.err = fmt.Errorf("acme: rejected token %s", secret)
	now = renewing.record.NotBefore.Add(renewing.record.NotAfter.Sub(renewing.record.NotBefore) * 3 / 4)
	renewing.tick(context.Background())
	output := renewalLogs.String()
	if output == "" {
		t.Fatal("the failed renewal was not logged")
	}
	if strings.Contains(output, secret) {
		t.Fatalf("renewal log leaks the credential: %q", output)
	}
	if !strings.Contains(output, "<redacted:") {
		t.Fatalf("renewal log is not redacted: %q", output)
	}
}

// TestManagerFilesModeValidityGaugeFollowsExpiry proves the validity gauge is
// re-evaluated for a files certificate, which has no renewal loop of its own.
func TestManagerFilesModeValidityGaugeFollowsExpiry(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	cert := makeTestCertificate(t, []string{"router.example.com"}, now.Add(-2*time.Hour), now.Add(time.Hour))
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certPath, cert.CertificatePEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, cert.PrivateKeyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	manager, err := New(Config{
		Mode:              ModeRouter,
		CertificateSource: SourceFiles,
		Domains:           []string{"router.example.com"},
		CertFile:          certPath,
		KeyFile:           keyPath,
	}, WithNow(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	labels := manager.metrics.baseLabels()
	if got := manager.metrics.value(metricCertValid, labels); got != 1 {
		t.Fatalf("valid gauge = %v, want 1", got)
	}
	now = now.Add(2 * time.Hour) // past expiry
	_ = manager.Prometheus()
	if got := manager.metrics.value(metricCertValid, labels); got != 0 {
		t.Fatalf("valid gauge = %v after expiry, want 0", got)
	}
	if got := manager.Status(); len(got) != 1 || got[0].Valid {
		t.Fatalf("status = %+v, want an invalid certificate", got)
	}
}

// TestManagerFilesModeWithoutDomainsUsesCertificateSANs proves a files
// certificate with no configured domain list serves the names in its SANs
// instead of rejecting every SNI.
func TestManagerFilesModeWithoutDomainsUsesCertificateSANs(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	cert := makeTestCertificate(t, []string{"files.example.com"}, now.Add(-time.Hour), now.Add(24*time.Hour))
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certPath, cert.CertificatePEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, cert.PrivateKeyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	manager, err := New(Config{
		Mode:              ModeRouter,
		CertificateSource: SourceFiles,
		CertFile:          certPath,
		KeyFile:           keyPath,
	}, WithNow(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if err := manager.CheckSNI(&tls.ClientHelloInfo{ServerName: "files.example.com"}); err != nil {
		t.Fatalf("the certificate's own SAN must be served: %v", err)
	}
	if _, err := manager.GetCertificate(&tls.ClientHelloInfo{ServerName: "files.example.com"}); err != nil {
		t.Fatalf("GetCertificate for the SAN: %v", err)
	}
	if err := manager.CheckSNI(&tls.ClientHelloInfo{ServerName: "other.example.com"}); err == nil {
		t.Fatal("a name outside the certificate SANs must be refused")
	}
}

// roundTripFunc lets a test observe HTTP calls without a network.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// TestManagerHandshakeNeverWaitsOnARI proves the handshake path does not fetch
// renewal information: only the renewal loop does.
func TestManagerHandshakeNeverWaitsOnARI(t *testing.T) {
	now := time.Now()
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return nil, fmt.Errorf("no network in this test")
	})}
	cfg := validACMEConfig()
	cfg.CacheDir = filepath.Join(t.TempDir(), "tls")
	cfg.DNS.Zone = "example.com"
	cfg.Renewal.UseARI = boolPtr(true) // ARI on, but the handshake must not use it
	t.Setenv("CF_DNS_API_TOKEN", "test-token")
	store, err := OpenStore(cfg.CacheDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cert := makeTestCertificate(t, cfg.Domains, now.Add(-time.Hour), now.Add(24*time.Hour))
	manager, err := New(cfg,
		WithStore(store),
		WithProvider(&fakeProvider{zones: []string{"example.com"}}),
		WithCertifier(&stubCertifier{cert: cert}),
		WithHTTPClient(client),
		WithNow(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if err := manager.EnsureCertificate(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	calls = 0
	if _, err := manager.GetCertificate(&tls.ClientHelloInfo{ServerName: cfg.Domains[0]}); err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if calls != 0 {
		t.Fatalf("the handshake path made %d HTTP call(s); the ARI lookup belongs to the renewal loop", calls)
	}
}

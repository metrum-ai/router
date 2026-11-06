// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package tlsacme

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
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
	t.Helper()
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
	manager, err := New(cfg,
		WithStore(store),
		WithProvider(fakeProvider{zones: []string{"example.com"}}),
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

	// A second manager (or a restart) loads the stored certificate instead of
	// issuing again.
	second, secondCertifier, _ := newTestManager(t, &now, nil)
	if err := second.EnsureCertificate(context.Background()); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if secondCertifier.calls != 1 {
		t.Fatalf("second manager issued a new certificate despite a valid cache")
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
	manager.maybeReload()
	if !manager.record.NotAfter.After(old) {
		t.Fatalf("replica did not reload the renewed certificate: %v", manager.record.NotAfter)
	}
	if manager.Status()[0].Source != "store" {
		t.Fatalf("source = %q, want store", manager.Status()[0].Source)
	}
}

func TestManagerRejectsExpiredOrMismatchedStoredCertificate(t *testing.T) {
	now := time.Now()
	manager, _, store := newTestManager(t, &now, nil)
	expired := makeTestCertificate(t, manager.cfg.Domains, now.Add(-48*time.Hour), now.Add(-time.Hour))
	if err := store.SaveCertificate(CertificateRecord{
		Domains:        manager.cfg.Domains,
		CertificatePEM: expired.CertificatePEM,
		PrivateKeyPEM:  expired.PrivateKeyPEM,
		NotBefore:      expired.NotBefore,
		NotAfter:       expired.NotAfter,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := manager.EnsureCertificate(context.Background()); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("error = %v, want an expiry error", err)
	}

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
		Mode:            ModeFiles,
		Domains:         []string{"router.example.com"},
		CertificateFile: certPath,
		KeyFile:         keyPath,
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

	if _, err := New(Config{Mode: ModeFiles, Domains: []string{"router.example.com"}, CertificateFile: filepath.Join(dir, "missing"), KeyFile: keyPath}); err == nil {
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

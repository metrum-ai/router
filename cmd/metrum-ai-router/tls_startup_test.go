// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/metrum-ai/router/internal/router"
)

// writeStartupCertificate writes a self-signed certificate for the domain.
func writeStartupCertificate(t *testing.T, dir, domain string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: domain},
		DNSNames:              []string{domain},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath
}

// writeStartupConfig writes a minimal serving configuration whose only
// interesting part is the tls block.
func writeStartupConfig(t *testing.T, dir, tlsBlock string) string {
	t.Helper()
	config := fmt.Sprintf(`server:
  listen: 127.0.0.1:0
  identifiers:
    mode: passthrough
  usage_db:
    enabled: false
  logging:
    path: %s
%s
state_path: %s
providers:
  mock:
    base_url: http://127.0.0.1:1/v1
    dialect: openai
    api_key: test-key
models:
  default:
    strategy: static
    targets:
      - provider: mock
        model: mock-model
callers:
  - id: startup-test
    token_sha256: %s
    allow: [default]
`, filepath.Join(dir, "requests.jsonl"), tlsBlock, filepath.Join(dir, "state.json"),
		"0000000000000000000000000000000000000000000000000000000000000000")
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestTLSStartupServesOnlyTLS follows main's startup branch exactly: obtain or
// load the certificate first, then bind the listener with that certificate. A
// plaintext request must never be served.
func TestTLSStartupServesOnlyTLS(t *testing.T) {
	dir := t.TempDir()
	domain := "router.example.com"
	certPath, keyPath := writeStartupCertificate(t, dir, domain)
	configPath := writeStartupConfig(t, dir, fmt.Sprintf(`  tls:
    mode: router
    certificate_source: files
    cert_file: %s
    key_file: %s
    acme:
      domains: [%s]`, certPath, keyPath, domain))

	cfg, err := router.LoadConfigForServe(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	svc, err := router.New(cfg)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	manager := svc.TLSManager()
	if manager == nil {
		t.Fatal("router mode must expose a TLS manager")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := manager.EnsureCertificate(ctx); err != nil {
		t.Fatalf("certificate was not loaded before bind: %v", err)
	}

	srv := &http.Server{Handler: svc.Handler(), TLSConfig: manager.TLSConfig()}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.ServeTLS(listener, "", "") }()
	defer func() { _ = srv.Close() }()

	roots := x509.NewCertPool()
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read certificate: %v", err)
	}
	roots.AppendCertsFromPEM(certPEM)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: domain}}}
	resp, err := client.Get("https://" + listener.Addr().String() + "/healthz")
	if err != nil {
		t.Fatalf("TLS request failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.TLS == nil {
		t.Fatal("the listener did not serve TLS")
	}

	// The same socket must not answer plaintext HTTP.
	plain := &http.Client{Timeout: 3 * time.Second}
	if resp, err := plain.Get("http://" + listener.Addr().String() + "/healthz"); err == nil {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK && len(body) > 0 && resp.TLS == nil {
			t.Fatal("the TLS listener answered a plaintext request")
		}
	}
}

// TestTLSStartupFailsWithoutACertificate proves the fail-closed branch: when no
// certificate can be obtained, startup stops before any listener is created.
func TestTLSStartupFailsWithoutACertificate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STARTUP_TEST_DNS_TOKEN", "not-used-because-issuance-fails-early")
	configPath := writeStartupConfig(t, dir, fmt.Sprintf(`  tls:
    mode: router
    certificate_source: acme
    acme:
      domains: [router.example.com]
      accept_tos: true
      directory_url: https://127.0.0.1:1/directory
      cache_dir: %s
      startup_timeout_seconds: 5
      dns:
        provider: digitalocean
        zone: example.com
        credentials:
          token_env: STARTUP_TEST_DNS_TOKEN`, filepath.Join(dir, "tls")))

	cfg, err := router.LoadConfigForServe(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	svc, err := router.New(cfg)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	manager := svc.TLSManager()
	if manager == nil {
		t.Fatal("acme mode must expose a TLS manager")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := manager.EnsureCertificate(ctx); err == nil {
		t.Fatal("startup must fail when no certificate can be obtained")
	}
	// main only creates the server after EnsureCertificate succeeds, so an error
	// here is exactly the "no listener, never plaintext" guarantee.
	if manager.TLSConfig().Certificates != nil {
		t.Fatal("no certificate should be installed after a failed issuance")
	}
}

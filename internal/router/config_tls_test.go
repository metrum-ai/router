// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// writeTestCertificate writes a self-signed certificate and key for the tests
// and returns their paths.
func writeTestCertificate(t *testing.T, dir string, domains []string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: domains[0]},
		DNSNames:              domains,
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

func TestTLSServerConfigDefaultsKeepV47Behaviour(t *testing.T) {
	var zero TLSServerConfig
	if zero.Enabled() {
		t.Fatal("an absent server.tls block must not enable TLS")
	}
	if err := validateTLSServerConfig(zero); err != nil {
		t.Fatalf("an absent block must validate: %v", err)
	}
	manager, err := newTLSManager(zero)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if manager != nil {
		t.Fatal("an absent server.tls block must not create a manager")
	}
	raw, err := yaml.Marshal(testConfig(t, "http://127.0.0.1:1", "key", t.TempDir()))
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatalf("unmarshal marshaled config: %v", err)
	}
	server, _ := document["server"].(map[string]any)
	if _, present := server["tls"]; present {
		t.Fatalf("a v4.7 configuration must serialize without server.tls (canonical document hash):\n%s", raw)
	}
	// Explicit mode: off behaves identically.
	manager, err = newTLSManager(TLSServerConfig{Mode: "off"})
	if err != nil || manager != nil {
		t.Fatalf("mode off: manager=%v err=%v", manager, err)
	}
}

func TestValidateTLSServerConfig(t *testing.T) {
	valid := func() TLSServerConfig {
		return TLSServerConfig{
			Mode:         "acme",
			Domains:      []string{"router.example.com"},
			AcceptTOS:    true,
			DirectoryURL: "https://acme.example.com/directory",
			CacheDir:     "/var/lib/metrum-ai-router/tls",
			DNS: TLSDNSConfig{
				Provider: "cloudflare",
				Credentials: TLSDNSCredentialsConfig{
					APITokenEnv: "CF_DNS_API_TOKEN",
				},
			},
		}
	}
	cases := []struct {
		name    string
		mutate  func(*TLSServerConfig)
		wantErr string
	}{
		{name: "valid"},
		{
			name:    "unknown mode",
			mutate:  func(c *TLSServerConfig) { c.Mode = "auto" },
			wantErr: "server.tls: mode must be one of off, files, acme",
		},
		{
			name:    "missing domains",
			mutate:  func(c *TLSServerConfig) { c.Domains = nil },
			wantErr: "server.tls: domains is required",
		},
		{
			name:    "tos not accepted",
			mutate:  func(c *TLSServerConfig) { c.AcceptTOS = false },
			wantErr: "server.tls: accept_tos must be true",
		},
		{
			name:    "http directory",
			mutate:  func(c *TLSServerConfig) { c.DirectoryURL = "http://acme.internal/directory" },
			wantErr: "server.tls: directory_url must be https",
		},
		{
			name:    "literal and file credentials together",
			mutate:  func(c *TLSServerConfig) { c.DNS.Credentials.APITokenFile = "/run/secrets/cf" },
			wantErr: "server.tls.dns.credentials.api_token: set exactly one of api_token_env or api_token_file",
		},
		{
			name:    "zone not a suffix",
			mutate:  func(c *TLSServerConfig) { c.DNS.Zone = "other.net" },
			wantErr: "is not a suffix of domain",
		},
		{
			name: "files mode needs both files",
			mutate: func(c *TLSServerConfig) {
				c.Mode = "files"
				c.Domains = nil
			},
			wantErr: "server.tls: certificate_file and key_file are required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid()
			if tc.mutate != nil {
				tc.mutate(&cfg)
			}
			err := validateTLSServerConfig(cfg)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestServerTLSFilesModeServesTLS(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeTestCertificate(t, dir, []string{"router.example.com"})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "key", dir)
	cfg.Callers[0].MetricsAdmin = true
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.TLS = TLSServerConfig{
		Mode:            "files",
		CertificateFile: certPath,
		KeyFile:         keyPath,
		Domains:         []string{"router.example.com"},
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	manager := svc.TLSManager()
	if manager == nil {
		t.Fatal("files mode must expose a TLS manager")
	}
	tlsConfig := manager.TLSConfig()
	if tlsConfig == nil {
		t.Fatal("files mode must expose a TLS config")
	}

	server := httptest.NewUnstartedServer(svc.Handler())
	server.TLS = tlsConfig
	server.StartTLS()
	defer server.Close()

	roots := x509.NewCertPool()
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read certificate: %v", err)
	}
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("could not build a root pool")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:    roots,
		ServerName: "router.example.com",
	}}}
	resp, err := client.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatalf("TLS request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d", resp.StatusCode)
	}
	if resp.TLS == nil {
		t.Fatal("the connection was not TLS")
	}

	metricsReq, err := http.NewRequest(http.MethodGet, server.URL+"/metrics", nil)
	if err != nil {
		t.Fatalf("metrics request: %v", err)
	}
	metricsReq.Header.Set("Authorization", "Bearer "+testToken)
	metricsResp, err := client.Do(metricsReq)
	if err != nil {
		t.Fatalf("metrics request: %v", err)
	}
	defer func() { _ = metricsResp.Body.Close() }()
	body, _ := io.ReadAll(metricsResp.Body)
	if !strings.Contains(string(body), "metrum_ai_router_tls_certificate_expiry_timestamp_seconds") {
		t.Fatal("the TLS certificate metric is not exported by /metrics")
	}
}

func TestServerWithoutTLSKeepsMetricsAndBehaviour(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "key", dir)
	cfg.Callers[0].MetricsAdmin = true
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	if svc.TLSManager() != nil {
		t.Fatal("a v4.7 configuration must not create a TLS manager")
	}
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "metrum_ai_router_tls_") {
		t.Fatal("a v4.7 configuration must not emit TLS metrics")
	}
}

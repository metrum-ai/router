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
// and returns their paths. The certificate covers the domain and 127.0.0.1.
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

func TestListenerTLSDefaultsKeepV47Behaviour(t *testing.T) {
	var zero ListenerTLSConfig
	if zero.Enabled() {
		t.Fatal("an absent tls block must not enable TLS")
	}
	if err := ValidateListenerTLS(zero, "serving", "server.tls"); err != nil {
		t.Fatalf("an absent block must validate: %v", err)
	}
	manager, err := NewListenerTLSManager(zero, "serving", "server.tls")
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if manager != nil {
		t.Fatal("an absent tls block must not create a manager")
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
	// Explicitly off behaves identically.
	manager, err = NewListenerTLSManager(ListenerTLSConfig{Mode: "off"}, "serving", "server.tls")
	if err != nil || manager != nil {
		t.Fatalf("mode off: manager=%v err=%v", manager, err)
	}
}

func TestValidateListenerTLS(t *testing.T) {
	validACME := func() ListenerTLSConfig {
		return ListenerTLSConfig{
			Mode:              "router",
			CertificateSource: "acme",
			ACME: TLSACMEServerConfig{
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
			},
		}
	}
	cases := []struct {
		name    string
		mutate  func(*ListenerTLSConfig)
		wantErr string
	}{
		{name: "valid acme"},
		{
			name: "valid files",
			mutate: func(c *ListenerTLSConfig) {
				*c = ListenerTLSConfig{Mode: "router", CertificateSource: "files", CertFile: "/etc/tls.crt", KeyFile: "/etc/tls.key"}
			},
		},
		{
			name:    "unknown mode",
			mutate:  func(c *ListenerTLSConfig) { c.Mode = "auto" },
			wantErr: "server.tls: mode must be off or router",
		},
		{
			name:    "proxy is admin-only",
			mutate:  func(c *ListenerTLSConfig) { c.Mode = "proxy" },
			wantErr: "server.tls: mode must be off or router",
		},
		{
			name:    "unknown certificate source",
			mutate:  func(c *ListenerTLSConfig) { c.CertificateSource = "kubernetes" },
			wantErr: "server.tls: certificate_source must be files or acme",
		},
		{
			name:    "files source needs both files",
			mutate:  func(c *ListenerTLSConfig) { c.CertificateSource = "files" },
			wantErr: "server.tls: cert_file and key_file are required",
		},
		{
			name:    "acme without domains",
			mutate:  func(c *ListenerTLSConfig) { c.ACME.Domains = nil },
			wantErr: "server.tls: domains is required",
		},
		{
			name:    "tos not accepted",
			mutate:  func(c *ListenerTLSConfig) { c.ACME.AcceptTOS = false },
			wantErr: "server.tls: accept_tos must be true",
		},
		{
			name:    "http directory",
			mutate:  func(c *ListenerTLSConfig) { c.ACME.DirectoryURL = "http://acme.internal/directory" },
			wantErr: "server.tls: directory_url must be https",
		},
		{
			name:    "both credential forms",
			mutate:  func(c *ListenerTLSConfig) { c.ACME.DNS.Credentials.APITokenFile = "/run/secrets/cf" },
			wantErr: "server.tls.dns.credentials.api_token: set exactly one of api_token_env or api_token_file",
		},
		{
			name:    "zone not a suffix",
			mutate:  func(c *ListenerTLSConfig) { c.ACME.DNS.Zone = "other.net" },
			wantErr: "is not a suffix of domain",
		},
		{
			name: "mode off with certificate keys",
			mutate: func(c *ListenerTLSConfig) {
				*c = ListenerTLSConfig{Mode: "off", CertFile: "/etc/tls.crt"}
			},
			wantErr: "server.tls: certificate_source and cert_file require mode router",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validACME()
			if tc.mutate != nil {
				tc.mutate(&cfg)
			}
			err := ValidateListenerTLS(cfg, "serving", "server.tls")
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

// TestAdminListenerTLSSeam covers the shared seam the admin listener (#809)
// uses: the same block, the admin listener label, proxy mode accepted and no
// manager built for it.
func TestAdminListenerTLSSeam(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeTestCertificate(t, dir, []string{"admin.example.com"})
	cfg := ListenerTLSConfig{
		Mode:              "router",
		CertificateSource: "files",
		CertFile:          certPath,
		KeyFile:           keyPath,
	}
	if err := ValidateAdminListenerTLS(cfg, "admin.tls"); err != nil {
		t.Fatalf("admin files config: %v", err)
	}
	manager, err := NewListenerTLSManager(cfg, "admin", "admin.tls")
	if err != nil {
		t.Fatalf("admin manager: %v", err)
	}
	if manager == nil || manager.TLSConfig() == nil {
		t.Fatal("admin router mode must expose a certificate manager")
	}
	// Proxy mode is the listener lane's: no router-held certificate, no manager.
	proxy := ListenerTLSConfig{Mode: "proxy"}
	if err := ValidateAdminListenerTLS(proxy, "admin.tls"); err != nil {
		t.Fatalf("proxy mode must be accepted for the admin listener: %v", err)
	}
	if manager, err := NewListenerTLSManager(proxy, "admin", "admin.tls"); err != nil || manager != nil {
		t.Fatalf("proxy mode: manager=%v err=%v", manager, err)
	}
	// A serving-port block may not select proxy mode.
	if err := ValidateListenerTLS(proxy, "serving", "server.tls"); err == nil {
		t.Fatal("proxy mode is not valid for the serving listener")
	}
	// An invalid admin block names admin.tls, not server.tls.
	broken := cfg
	broken.CertificateSource = "acme"
	broken.ACME.Domains = []string{"admin.example.com"}
	err = ValidateAdminListenerTLS(broken, "admin.tls")
	if err == nil || !strings.HasPrefix(err.Error(), "admin.tls:") {
		t.Fatalf("error = %v, want an admin.tls prefix", err)
	}
}

func TestServerTLSFilesSourceServesTLS(t *testing.T) {
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
	cfg.Server.TLS = ListenerTLSConfig{
		Mode:              "router",
		CertificateSource: "files",
		CertFile:          certPath,
		KeyFile:           keyPath,
		ACME:              TLSACMEServerConfig{Domains: []string{"router.example.com"}},
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	manager := svc.TLSManager()
	if manager == nil {
		t.Fatal("router mode must expose a TLS manager")
	}
	tlsConfig := manager.TLSConfig()
	if tlsConfig == nil {
		t.Fatal("router mode must expose a TLS config")
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

// TestAdminCertificateSourceAdapter proves the manager satisfies the admin
// listener's handshake seam (#809) through the adapter: yes/no SNI plus the
// certificate for the hello.
func TestAdminCertificateSourceAdapter(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeTestCertificate(t, dir, []string{"admin.example.com"})
	manager, err := NewListenerTLSManager(ListenerTLSConfig{
		Mode:              "router",
		CertificateSource: "files",
		CertFile:          certPath,
		KeyFile:           keyPath,
		ACME:              TLSACMEServerConfig{Domains: []string{"admin.example.com"}},
	}, "admin", "admin.tls")
	if err != nil || manager == nil {
		t.Fatalf("manager: %v", err)
	}
	source := AdminCertificateSource{Manager: manager}
	if !source.CheckSNI("admin.example.com") {
		t.Fatal("the configured name must be served")
	}
	if source.CheckSNI("other.example.com") {
		t.Fatal("an unknown name must not be served")
	}
	cert, err := source.GetCertificate(&tls.ClientHelloInfo{ServerName: "admin.example.com"})
	if err != nil || cert == nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if _, err := source.GetCertificate(&tls.ClientHelloInfo{ServerName: "other.example.com"}); err == nil {
		t.Fatal("GetCertificate must refuse an unknown name")
	}
}

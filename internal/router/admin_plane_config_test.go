// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"log"
	"net/http"
	"strings"
	"testing"
)

// ACP-002/ACP-005/ACP-007/ACP-N02: the admin block fails closed at config load.
func TestAdminPlaneConfigValidation(t *testing.T) {
	enabled := func() *AdminPlaneConfig {
		cfg := &AdminPlaneConfig{Enabled: true}
		defaultAdminPlaneConfig(cfg)
		return cfg
	}
	cases := []struct {
		name    string
		mutate  func(*AdminPlaneConfig)
		wantErr string
	}{
		{
			name:   "enabled defaults are loopback with TLS off",
			mutate: func(cfg *AdminPlaneConfig) {},
		},
		{
			name:    "plaintext non-loopback refused",
			mutate:  func(cfg *AdminPlaneConfig) { cfg.Listen = "0.0.0.0:8081" },
			wantErr: "not loopback",
		},
		{
			name: "plaintext non-loopback with explicit override",
			mutate: func(cfg *AdminPlaneConfig) {
				cfg.Listen = "0.0.0.0:8081"
				cfg.AllowPlaintextNonLoopback = true
			},
		},
		{
			name:    "listen without a port",
			mutate:  func(cfg *AdminPlaneConfig) { cfg.Listen = "127.0.0.1" },
			wantErr: "host:port",
		},
		{
			name: "acme without an acme block",
			mutate: func(cfg *AdminPlaneConfig) {
				cfg.TLS.Mode = adminTLSModeRouter
				cfg.TLS.CertificateSource = adminCertificateSourceACME
			},
			wantErr: "admin.tls.acme",
		},
		{
			name: "files source with an acme block beyond domains",
			mutate: func(cfg *AdminPlaneConfig) {
				cfg.TLS.Mode = adminTLSModeRouter
				cfg.TLS.CertFile = "server.crt"
				cfg.TLS.KeyFile = "server.key"
				cfg.TLS.ACME = TLSACMEServerConfig{DirectoryURL: "https://acme.test/directory"}
			},
			wantErr: "only acme.domains may be set",
		},
		{
			name: "unknown certificate source",
			mutate: func(cfg *AdminPlaneConfig) {
				cfg.TLS.Mode = adminTLSModeRouter
				cfg.TLS.CertFile = "server.crt"
				cfg.TLS.KeyFile = "server.key"
				cfg.TLS.CertificateSource = "vault"
			},
			wantErr: "must be files or acme",
		},
		{
			name: "acme without router TLS mode",
			mutate: func(cfg *AdminPlaneConfig) {
				cfg.TLS.CertificateSource = adminCertificateSourceACME
				cfg.TLS.ACME = TLSACMEServerConfig{DirectoryURL: "https://acme.test/directory"}
			},
			wantErr: "certificate_source",
		},
		{
			name:    "router TLS without certificate",
			mutate:  func(cfg *AdminPlaneConfig) { cfg.TLS.Mode = adminTLSModeRouter },
			wantErr: "cert_file and key_file are required",
		},
		{
			name: "router TLS without a client CA",
			mutate: func(cfg *AdminPlaneConfig) {
				cfg.TLS.Mode = adminTLSModeRouter
				cfg.TLS.CertFile = "server.crt"
				cfg.TLS.KeyFile = "server.key"
				cfg.TLS.ClientAuth = adminClientAuthRequire
			},
			wantErr: "requires a client CA",
		},
		{
			name: "router TLS with a client CA",
			mutate: func(cfg *AdminPlaneConfig) {
				cfg.TLS.Mode = adminTLSModeRouter
				cfg.TLS.CertFile = "server.crt"
				cfg.TLS.KeyFile = "server.key"
				cfg.TLS.ClientAuth = adminClientAuthOptional
				cfg.Authentication.MTLS.CAFiles = []string{"client-ca.crt"}
				cfg.Authentication.MTLS.Issuer = "configured-client-ca"
			},
		},
		{
			name: "flat authentication CA requires configured issuer",
			mutate: func(cfg *AdminPlaneConfig) {
				cfg.TLS.ClientAuth = adminClientAuthOptional
				cfg.Authentication.MTLS.CAFiles = []string{"client-ca.crt"}
			},
			wantErr: "explicit issuer",
		},
		{
			name:    "client CA without client auth",
			mutate:  func(cfg *AdminPlaneConfig) { cfg.TLS.ClientCAFile = "client-ca.crt" },
			wantErr: "requires admin.tls.client_auth",
		},
		{
			name: "authentication CA files without client auth",
			mutate: func(cfg *AdminPlaneConfig) {
				cfg.Authentication.MTLS.Issuer = "configured-client-ca"
				cfg.Authentication.MTLS.CAFiles = []string{"ca.crt"}
			},
			wantErr: "requires admin.tls.client_auth",
		},
		{
			name:    "invalid client auth value",
			mutate:  func(cfg *AdminPlaneConfig) { cfg.TLS.ClientAuth = "mandatory" },
			wantErr: "client_auth must be",
		},
		{
			name: "invalid TLS minimum version",
			mutate: func(cfg *AdminPlaneConfig) {
				cfg.TLS.Mode = adminTLSModeRouter
				cfg.TLS.CertFile = "server.crt"
				cfg.TLS.KeyFile = "server.key"
				cfg.TLS.MinVersion = "1.0"
			},
			wantErr: "min_version",
		},
		{
			name:    "proxy TLS without trusted proxies",
			mutate:  func(cfg *AdminPlaneConfig) { cfg.TLS.Mode = adminTLSModeProxy },
			wantErr: "requires admin.tls.trusted_proxy_cidrs",
		},
		{
			name: "proxy TLS with a malformed CIDR",
			mutate: func(cfg *AdminPlaneConfig) {
				cfg.TLS.Mode = adminTLSModeProxy
				cfg.TLS.TrustedProxyCIDRs = []string{"10.0.0.0/99"}
			},
			wantErr: "invalid CIDR",
		},
		{
			name: "proxy TLS rejects client certificates",
			mutate: func(cfg *AdminPlaneConfig) {
				cfg.TLS.Mode = adminTLSModeProxy
				cfg.TLS.TrustedProxyCIDRs = []string{"10.0.0.0/8"}
				cfg.TLS.ClientAuth = adminClientAuthOptional
				cfg.TLS.ClientCAFile = "client-ca.crt"
			},
			wantErr: "cannot be used with admin.tls.mode proxy",
		},
		{
			name: "http external URL on a non-loopback listener",
			mutate: func(cfg *AdminPlaneConfig) {
				cfg.Listen = "0.0.0.0:8081"
				cfg.AllowPlaintextNonLoopback = true
				cfg.ExternalURL = "http://router.example.test:8081"
			},
			wantErr: "must use https",
		},
		{
			name:   "http external URL on loopback",
			mutate: func(cfg *AdminPlaneConfig) { cfg.ExternalURL = "http://127.0.0.1:8081" },
		},
		{
			name:    "external URL with a query",
			mutate:  func(cfg *AdminPlaneConfig) { cfg.ExternalURL = "https://router.example.test/admin?x=1" },
			wantErr: "query or a fragment",
		},
		{
			name:    "external URL with userinfo",
			mutate:  func(cfg *AdminPlaneConfig) { cfg.ExternalURL = "https://user:pass@router.example.test" },
			wantErr: "userinfo",
		},
		{
			name:    "legacy sunset before the 90-day floor",
			mutate:  func(cfg *AdminPlaneConfig) { cfg.LegacySunset = "2026-12-01T00:00:00Z" },
			wantErr: "on or after",
		},
		{
			name:    "legacy sunset not a timestamp",
			mutate:  func(cfg *AdminPlaneConfig) { cfg.LegacySunset = "next year" },
			wantErr: "RFC3339",
		},
		{
			name:    "negative body limit",
			mutate:  func(cfg *AdminPlaneConfig) { cfg.Limits.MaxBodyBytes = -1 },
			wantErr: "max_body_bytes",
		},
		{
			name:    "oversize body limit",
			mutate:  func(cfg *AdminPlaneConfig) { cfg.Limits.MaxBodyBytes = 1 << 30 },
			wantErr: "max_body_bytes",
		},
		{
			name:    "negative shutdown budget",
			mutate:  func(cfg *AdminPlaneConfig) { cfg.Limits.ShutdownTimeoutMS = -1 },
			wantErr: "shutdown_timeout_ms",
		},
		{
			name: "metrics kept on the serving port while the plane is enabled",
			mutate: func(cfg *AdminPlaneConfig) {
				keep := true
				cfg.Metrics.OnServingPort = &keep
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := enabled()
			if testCase.mutate != nil {
				testCase.mutate(cfg)
			}
			err := validateAdminPlane(cfg)
			if testCase.wantErr == "" {
				if err != nil {
					t.Fatalf("validateAdminPlane() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("validateAdminPlane() = %v, want an error containing %q", err, testCase.wantErr)
			}
		})
	}
}

// ACP-005/ACP-007: a disabled plane never silently removes or moves admin
// handlers, and cannot hide /metrics.
func TestAdminDisabledPlaneRules(t *testing.T) {
	disabled := &AdminPlaneConfig{}
	defaultAdminPlaneConfig(disabled)
	if err := validateAdminPlane(disabled); err != nil {
		t.Fatalf("disabled defaults = %v, want nil", err)
	}
	if disabled.metricsOnServingPort() != true {
		t.Fatal("disabled plane moved /metrics off the serving port")
	}
	if !disabled.legacyRoutesEnabled() {
		t.Fatal("disabled plane changed the legacy-route surface")
	}
	if err := validateAdminPlane(&AdminPlaneConfig{Listen: "127.0.0.1:8081"}); err != nil {
		t.Fatalf("disabled block with listener entries = %v, want only a warning", err)
	}
	drop := false
	if err := validateAdminPlane(&AdminPlaneConfig{Metrics: AdminMetricsConfig{OnServingPort: &drop}}); err == nil ||
		!strings.Contains(err.Error(), "does not serve /metrics in this release") {
		t.Fatalf("disabled plane with metrics off = %v, want an error", err)
	}
}

// ACP-007: /metrics stays on the serving port until the admin listener serves
// it, and the "moved" configuration is refused rather than silently dropping
// the exposition.
func TestAdminMetricsStayOnServingPortUntilAdminServesIt(t *testing.T) {
	cfg := &AdminPlaneConfig{Enabled: true}
	defaultAdminPlaneConfig(cfg)
	if !cfg.metricsOnServingPort() {
		t.Fatal("enabled plane dropped /metrics without an admin-side handler")
	}
	keep := true
	cfg.Metrics.OnServingPort = &keep
	if !cfg.metricsOnServingPort() {
		t.Fatal("explicit compatibility flag did not keep serving-port /metrics")
	}
	drop := false
	cfg.Metrics.OnServingPort = &drop
	if !cfg.metricsOnServingPort() {
		t.Fatal("moved-to-admin configuration dropped serving-port /metrics before an admin handler exists")
	}
	if err := validateAdminPlane(cfg); err == nil || !strings.Contains(err.Error(), "not available yet") {
		t.Fatalf("moved-but-not-served configuration = %v, want a validation error", err)
	}
}

// An absent admin block is exactly v4.7: no listener, v4.7 route surface.
func TestAdminAbsentBlockKeepsV47Surface(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.setDefaults()
	if cfg.Admin.Enabled || cfg.Admin.Listen != "" {
		t.Fatalf("absent admin block produced an enabled plane: %#v", cfg.Admin)
	}
	if !cfg.Admin.metricsOnServingPort() || !cfg.Admin.legacyRoutesEnabled() {
		t.Fatal("absent admin block changed the v4.7 surface")
	}
}

// The proxy allowlist never trusts a default route or a broader-than-site
// prefix, and the plaintext override warns once at startup.
func TestAdminTrustedProxyCIDRWidening(t *testing.T) {
	enabled := func() *AdminPlaneConfig {
		cfg := &AdminPlaneConfig{Enabled: true}
		defaultAdminPlaneConfig(cfg)
		cfg.TLS.Mode = adminTLSModeProxy
		return cfg
	}
	cases := []struct {
		cidr    string
		wantErr string
	}{
		{"0.0.0.0/0", "must not trust every peer"},
		{"::/0", "must not trust every peer"},
		{"10.0.0.0/7", "wider than /8"},
		{"2001:db8::/16", "wider than /32"},
		{"::ffff:0.0.0.0/96", "IPv4-mapped prefix"},
		{"::ffff:10.0.0.0/103", "IPv4-mapped prefix"},
		{"::ffff:10.0.0.0/104", ""},
		{"::ffff:192.0.2.0/120", ""},
		{"10.0.0.0/8", ""},
		{"2001:db8:1:2::/64", ""},
		{"192.0.2.7/32", ""},
	}
	for _, testCase := range cases {
		cfg := enabled()
		cfg.TLS.TrustedProxyCIDRs = []string{testCase.cidr}
		err := validateAdminPlane(cfg)
		if testCase.wantErr == "" {
			if err != nil {
				t.Fatalf("%s = %v, want nil", testCase.cidr, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
			t.Fatalf("%s = %v, want an error containing %q", testCase.cidr, err, testCase.wantErr)
		}
	}
}

func TestAdminPlaintextOverrideWarns(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })

	enabled := true
	cfg := AdminPlaneConfig{
		Enabled:                   true,
		Listen:                    "0.0.0.0:0",
		AllowPlaintextNonLoopback: true,
		LegacyRoutesOnServingPort: &enabled,
		TLS:                       AdminTLSConfig{ListenerTLSConfig: ListenerTLSConfig{Mode: adminTLSModeOff}},
	}
	plane, err := NewAdminPlane(&cfg, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if err != nil {
		t.Fatalf("NewAdminPlane: %v", err)
	}
	if plane == nil {
		t.Fatal("NewAdminPlane returned no plane")
	}
	if !strings.Contains(buf.String(), "allow_plaintext_non_loopback") {
		t.Fatalf("startup log = %q, want the plaintext override warning", buf.String())
	}
}

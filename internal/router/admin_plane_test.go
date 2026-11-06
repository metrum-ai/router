// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Real-listener tests for the admin control plane (#809). Every assertion runs
// against an http.Server boundary: the serving plane through httptest (a real
// listener) and the admin plane through the same AdminPlane the binary uses.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type adminTestFixture struct {
	cfg      *Config
	svc      *Service
	serving  *httptest.Server
	admin    *AdminPlane
	adminURL string
}

// newAdminTestFixture builds a router with the admin plane enabled on an
// ephemeral loopback port and starts both listeners.
func newAdminTestFixture(t *testing.T, mutate func(*Config)) *adminTestFixture {
	t.Helper()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Admin.Enabled = true
	cfg.Admin.Listen = "127.0.0.1:0"
	if mutate != nil {
		mutate(cfg)
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(svc.Close)
	fixture := &adminTestFixture{cfg: cfg, svc: svc}
	fixture.serving = httptest.NewServer(svc.Handler())
	t.Cleanup(fixture.serving.Close)
	handler := svc.AdminHandler()
	if handler == nil {
		t.Fatalf("AdminHandler() = nil, want the hardened plane handler")
	}
	plane, err := NewAdminPlane(&cfg.Admin, handler)
	if err != nil {
		t.Fatalf("NewAdminPlane: %v", err)
	}
	if err := plane.Start(); err != nil {
		t.Fatalf("admin Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = plane.Shutdown(ctx)
	})
	fixture.admin = plane
	fixture.adminURL = "http://" + plane.Addr()
	return fixture
}

func adminGet(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return resp, string(body)
}

func adminDo(t *testing.T, req *http.Request) (*http.Response, string) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", req.URL, err)
	}
	return resp, string(body)
}

func adminProblemResponseCode(t *testing.T, body string) string {
	t.Helper()
	var problem struct {
		Code string `json:"code"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(body), &problem); err != nil {
		t.Fatalf("problem body is not JSON: %v (%q)", err, body)
	}
	if problem.Code == "" || !strings.HasSuffix(problem.Type, problem.Code) {
		t.Fatalf("problem body has no code/type pair: %q", body)
	}
	return problem.Code
}

// ACP-001: the serving socket never routes /admin/v1.
func TestAdminServingSocketHasNoAdminV1(t *testing.T) {
	fixture := newAdminTestFixture(t, nil)
	for _, path := range []string{"/admin/v1", "/admin/v1/", "/admin/v1/config/groups", "/admin/v1/metrics"} {
		resp, body := adminGet(t, fixture.serving.URL+path)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET serving %s = %d, want 404 (body %q)", path, resp.StatusCode, body)
		}
	}
}

// ACP-001 + ACP-N06: the admin socket never routes inference, docs or UI.
func TestAdminSocketHasNoInferenceOrUIRoutes(t *testing.T) {
	fixture := newAdminTestFixture(t, nil)
	paths := []string{
		"/v1/models", "/v1/chat/completions", "/v1/responses", "/anthropic/v1/messages",
		"/docs/", "/", "/admin/reports/", "/admin/reports/static/app.js",
	}
	for _, path := range paths {
		method := http.MethodGet
		if path == "/v1/chat/completions" || path == "/v1/responses" || path == "/anthropic/v1/messages" {
			method = http.MethodPost
		}
		req, err := http.NewRequest(method, fixture.adminURL+path, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp, body := adminDo(t, req)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s admin %s = %d, want 404 (body %q)", method, path, resp.StatusCode, body)
		}
		if strings.Contains(strings.ToLower(body), "<html") || strings.Contains(strings.ToLower(body), "<!doctype") {
			t.Fatalf("admin %s returned HTML: %q", path, body)
		}
		if got := resp.Header.Get("Content-Type"); got != "application/problem+json" {
			t.Fatalf("admin %s content-type = %q, want problem+json", path, got)
		}
	}
}

// ACP-006: the admin plane's own probes carry state only and never change
// serving readiness.
func TestAdminProbeIsStateOnly(t *testing.T) {
	fixture := newAdminTestFixture(t, nil)
	for _, path := range []string{"/healthz", "/readyz"} {
		resp, body := adminGet(t, fixture.adminURL+path)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET admin %s = %d, want 200", path, resp.StatusCode)
		}
		if strings.TrimSpace(body) != `{"ok":true}` {
			t.Fatalf("admin %s body = %q, want state only", path, body)
		}
	}
	// Serving readiness is independent of the admin plane: stop it and check.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := fixture.admin.Shutdown(ctx); err != nil {
		t.Fatalf("admin Shutdown: %v", err)
	}
	resp, body := adminGet(t, fixture.serving.URL+"/readyz")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("serving /readyz after admin shutdown = %d, want 200 (body %q)", resp.StatusCode, body)
	}
}

// ACP-N02: security headers on every admin response, CORS off.
func TestAdminSecurityHeadersAndCORS(t *testing.T) {
	fixture := newAdminTestFixture(t, nil)
	resp, _ := adminGet(t, fixture.adminURL+"/healthz")
	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Cache-Control":          "no-store",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := resp.Header.Get(header); got != want {
			t.Fatalf("admin %s = %q, want %q", header, got, want)
		}
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("admin CSP = %q", csp)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("admin Access-Control-Allow-Origin = %q, want empty (CORS off)", got)
	}
	preflight, err := http.NewRequest(http.MethodOptions, fixture.adminURL+"/admin/v1/config/groups", nil)
	if err != nil {
		t.Fatal(err)
	}
	preflight.Header.Set("Origin", "https://example.test")
	preflight.Header.Set("Access-Control-Request-Method", "GET")
	resp, body := adminDo(t, preflight)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("preflight = %d, want 405 (body %q)", resp.StatusCode, body)
	}
	if code := adminProblemResponseCode(t, body); code != "admin-cors-rejected" {
		t.Fatalf("preflight code = %q, want admin-cors-rejected", code)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("preflight Access-Control-Allow-Origin = %q, want empty", got)
	}
	plainOptions, err := http.NewRequest(http.MethodOptions, fixture.adminURL+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, body = adminDo(t, plainOptions)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("plain OPTIONS = %d, want 405", resp.StatusCode)
	}
	if code := adminProblemResponseCode(t, body); code != "admin-method-not-allowed" {
		t.Fatalf("plain OPTIONS code = %q, want admin-method-not-allowed", code)
	}
}

// ACP-N06: browser credentials are rejected before any handler runs.
func TestAdminRejectsBrowserCredentials(t *testing.T) {
	fixture := newAdminTestFixture(t, nil)
	cookieReq, err := http.NewRequest(http.MethodGet, fixture.adminURL+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	cookieReq.Header.Set("Cookie", "smart_router_admin_session=abc")
	resp, body := adminDo(t, cookieReq)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cookie request = %d, want 401 (body %q)", resp.StatusCode, body)
	}
	if code := adminProblemResponseCode(t, body); code != "unsupported_authentication" {
		t.Fatalf("cookie request code = %q, want unsupported_authentication", code)
	}
	if got := resp.Header.Get("WWW-Authenticate"); got != "" {
		t.Fatalf("cookie request challenge = %q, want none", got)
	}
	basicReq, err := http.NewRequest(http.MethodGet, fixture.adminURL+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	basicReq.SetBasicAuth("admin", "admin")
	resp, body = adminDo(t, basicReq)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("basic request = %d, want 401 (body %q)", resp.StatusCode, body)
	}
	if code := adminProblemResponseCode(t, body); code != "unsupported_authentication" {
		t.Fatalf("basic request code = %q, want unsupported_authentication", code)
	}
	// A machine scheme the listener does not judge (#814 validates it) is not
	// mistaken for a browser credential.
	dpopReq, err := http.NewRequest(http.MethodGet, fixture.adminURL+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	dpopReq.Header.Set("Authorization", "DPoP proof-token")
	resp, body = adminDo(t, dpopReq)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DPoP request = %d, want it to reach the handler (body %q)", resp.StatusCode, body)
	}
}

// ACP-N02: oversize bodies are refused with 413 regardless of how they are
// framed, and the handler never sees them.
func TestAdminBodyLimit413(t *testing.T) {
	fixture := newAdminTestFixture(t, func(cfg *Config) {
		cfg.Admin.Limits.MaxBodyBytes = 1024
	})
	var served bool
	fixture.svc.RegisterAdminRoutes(func(mux *AdminMux) {
		mux.HandleFunc("POST /admin/test/echo", func(w http.ResponseWriter, r *http.Request) {
			served = true
			_, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		})
	})
	// Declared length above the limit is refused before the body is read.
	declared, err := http.NewRequest(http.MethodPost, fixture.adminURL+"/admin/test/echo", bytes.NewReader(make([]byte, 4096)))
	if err != nil {
		t.Fatal(err)
	}
	resp, body := adminDo(t, declared)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("declared oversize body = %d, want 413 (body %q)", resp.StatusCode, body)
	}
	if code := adminProblemResponseCode(t, body); code != "admin-payload-too-large" {
		t.Fatalf("declared oversize code = %q, want admin-payload-too-large", code)
	}
	if served {
		t.Fatal("handler served an oversize declared body")
	}
	// A chunked body with no declared length is cut at the limit.
	chunked, err := http.NewRequest(http.MethodPost, fixture.adminURL+"/admin/test/echo", io.LimitReader(rand.Reader, 8192))
	if err != nil {
		t.Fatal(err)
	}
	resp, body = adminDo(t, chunked)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked oversize body = %d, want 413 (body %q)", resp.StatusCode, body)
	}
	if code := adminProblemResponseCode(t, body); code != "admin-payload-too-large" {
		t.Fatalf("chunked oversize code = %q, want admin-payload-too-large", code)
	}
	small, err := http.NewRequest(http.MethodPost, fixture.adminURL+"/admin/test/echo", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp, body = adminDo(t, small)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("small body = %d, want 200 (body %q)", resp.StatusCode, body)
	}
}

// ACP-N02: admin drains independently while serving keeps answering.
func TestAdminShutdownDrainsWhileServingContinues(t *testing.T) {
	fixture := newAdminTestFixture(t, nil)
	fixture.svc.RegisterAdminRoutes(func(mux *AdminMux) {
		mux.HandleFunc("GET /admin/test/slow", func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(150 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		})
	})
	type result struct {
		status int
		err    error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := http.Get(fixture.adminURL + "/admin/test/slow")
		if err != nil {
			done <- result{err: err}
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		done <- result{status: resp.StatusCode}
	}()
	time.Sleep(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdownErr := fixture.admin.Shutdown(ctx)
	got := <-done
	if shutdownErr != nil {
		t.Fatalf("admin Shutdown: %v", shutdownErr)
	}
	if got.err != nil {
		t.Fatalf("in-flight admin request failed during drain: %v", got.err)
	}
	if got.status != http.StatusOK {
		t.Fatalf("in-flight admin request = %d, want 200", got.status)
	}
	resp, body := adminGet(t, fixture.serving.URL+"/healthz")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("serving /healthz after admin drain = %d (body %q)", resp.StatusCode, body)
	}
}

// ACP-002: a non-loopback plaintext listener refuses to start.
func TestAdminPlaintextNonLoopbackRefusedAtStartup(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Admin.Enabled = true
	cfg.Admin.Listen = "0.0.0.0:0"
	if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "not loopback") {
		t.Fatalf("New() err = %v, want a non-loopback plaintext refusal", err)
	}
	// The same policy holds for a direct plane construction, so a caller that
	// bypasses config loading still cannot open the plane.
	enabled := true
	direct := AdminPlaneConfig{Enabled: true, Listen: "0.0.0.0:0", LegacyRoutesOnServingPort: &enabled, TLS: AdminTLSConfig{Mode: adminTLSModeOff}}
	if _, err := NewAdminPlane(&direct, http.NotFoundHandler()); err == nil {
		t.Fatal("NewAdminPlane() accepted a non-loopback plaintext listener")
	}
	// The explicit override starts, which is what controlled test networks use.
	cfg.Admin.AllowPlaintextNonLoopback = true
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("New() with the plaintext override: %v", err)
	}
	t.Cleanup(svc.Close)
	plane, err := NewAdminPlane(&cfg.Admin, svc.AdminHandler())
	if err != nil {
		t.Fatalf("NewAdminPlane() with the plaintext override: %v", err)
	}
	if err := plane.Start(); err != nil {
		t.Fatalf("admin Start() with the plaintext override: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = plane.Shutdown(ctx)
}

// ACP-002: mode proxy trusts only configured proxies and requires HTTPS.
func TestAdminProxyGate(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Admin.Enabled = true
	cfg.Admin.Listen = "127.0.0.1:0"
	cfg.Admin.TLS.Mode = adminTLSModeProxy
	cfg.Admin.TLS.TrustedProxyCIDRs = []string{"10.0.0.0/8"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(svc.Close)
	handler := svc.AdminHandler()
	cases := []struct {
		name       string
		remoteAddr string
		proto      string
		wantStatus int
		wantCode   string
	}{
		{"loopback port-forward", "127.0.0.1:5000", "", http.StatusOK, ""},
		{"trusted proxy with https", "10.1.2.3:5000", "https", http.StatusOK, ""},
		{"trusted proxy without https", "10.1.2.3:5000", "", http.StatusForbidden, "admin-tls-required"},
		{"untrusted peer", "192.0.2.5:5000", "https", http.StatusForbidden, "admin-proxy-untrusted"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			req.RemoteAddr = testCase.remoteAddr
			if testCase.proto != "" {
				req.Header.Set("X-Forwarded-Proto", testCase.proto)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
			if testCase.wantCode != "" {
				if code := adminProblemResponseCode(t, recorder.Body.String()); code != testCase.wantCode {
					t.Fatalf("code = %q, want %q", code, testCase.wantCode)
				}
			}
		})
	}
}

// ACP-003: router-native TLS verifies client certificates at the handshake,
// optionally or mandatorily, against the current CA snapshot.
func TestAdminMTLSHandshake(t *testing.T) {
	serverCA := newAdminTestCA(t)
	clientCA := newAdminTestCA(t)
	otherCA := newAdminTestCA(t)
	serverCert, serverKey := serverCA.issue(t, "admin.test", nil, nil, true)
	goodClient, goodClientKey := clientCA.issue(t, "client.test", nil, nil, false)
	orphanClient, orphanClientKey := otherCA.issue(t, "client.test", nil, nil, false)

	dir := t.TempDir()
	serverCertPath := writeTestPEM(t, dir, "server.crt", serverCert)
	serverKeyPath := writeTestKey(t, dir, "server.key", serverKey)
	clientCAPath := writeTestPEM(t, dir, "client-ca.crt", clientCA.certPEM)

	start := func(t *testing.T, clientAuth string) (*AdminPlane, string, *x509.CertPool) {
		t.Helper()
		cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
		cfg.Admin.Enabled = true
		cfg.Admin.Listen = "127.0.0.1:0"
		cfg.Admin.TLS = AdminTLSConfig{
			Mode:         adminTLSModeRouter,
			CertFile:     serverCertPath,
			KeyFile:      serverKeyPath,
			ClientCAFile: clientCAPath,
			ClientAuth:   clientAuth,
			MinVersion:   adminTLSMinVersion12,
		}
		svc, err := New(cfg)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		t.Cleanup(svc.Close)
		svc.RegisterAdminRoutes(func(mux *AdminMux) {
			mux.HandleFunc("GET /admin/test/peer", func(w http.ResponseWriter, r *http.Request) {
				chain := AdminPeerCertificates(r)
				subject := ""
				if len(chain) > 0 {
					subject = chain[0].Subject.CommonName
				}
				w.WriteHeader(http.StatusOK)
				fmt.Fprintf(w, `{"subject":%q}`, subject)
			})
		})
		plane, err := NewAdminPlane(&cfg.Admin, svc.AdminHandler())
		if err != nil {
			t.Fatalf("NewAdminPlane: %v", err)
		}
		if err := plane.Start(); err != nil {
			t.Fatalf("admin Start: %v", err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = plane.Shutdown(ctx)
		})
		roots := x509.NewCertPool()
		roots.AddCert(serverCA.cert)
		return plane, "https://" + plane.Addr(), roots
	}

	clientFor := func(roots *x509.CertPool, cert, key []byte) *http.Client {
		tlsCfg := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
		if cert != nil {
			pair, err := tls.X509KeyPair(cert, key)
			if err != nil {
				t.Fatalf("client key pair: %v", err)
			}
			tlsCfg.Certificates = []tls.Certificate{pair}
		}
		return &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}, Timeout: 5 * time.Second}
	}

	t.Run("require rejects a missing client certificate", func(t *testing.T) {
		_, url, roots := start(t, adminClientAuthRequire)
		if _, err := clientFor(roots, nil, nil).Get(url + "/admin/test/peer"); err == nil {
			t.Fatal("handshake without a client certificate succeeded in require mode")
		}
	})
	t.Run("require rejects an unknown CA", func(t *testing.T) {
		_, url, roots := start(t, adminClientAuthRequire)
		if _, err := clientFor(roots, orphanClient, orphanClientKey).Get(url + "/admin/test/peer"); err == nil {
			t.Fatal("handshake with a foreign CA succeeded")
		}
	})
	t.Run("require accepts a mapped certificate", func(t *testing.T) {
		_, url, roots := start(t, adminClientAuthRequire)
		resp, err := clientFor(roots, goodClient, goodClientKey).Get(url + "/admin/test/peer")
		if err != nil {
			t.Fatalf("handshake with a good client certificate: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"subject":"client.test"`) {
			t.Fatalf("peer response = %d %q, want the verified client leaf", resp.StatusCode, body)
		}
	})
	t.Run("optional allows API-key clients without a certificate", func(t *testing.T) {
		_, url, roots := start(t, adminClientAuthOptional)
		resp, err := clientFor(roots, nil, nil).Get(url + "/admin/test/peer")
		if err != nil {
			t.Fatalf("handshake without a client certificate in optional mode: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if strings.TrimSpace(string(body)) != `{"subject":""}` {
			t.Fatalf("peer response = %q, want no verified chain", body)
		}
	})
	t.Run("optional still rejects an unknown CA", func(t *testing.T) {
		_, url, roots := start(t, adminClientAuthOptional)
		if _, err := clientFor(roots, orphanClient, orphanClientKey).Get(url + "/admin/test/peer"); err == nil {
			t.Fatal("optional mode accepted a foreign CA certificate")
		}
	})
	t.Run("CA snapshot reload applies to new handshakes", func(t *testing.T) {
		plane, url, roots := start(t, adminClientAuthRequire)
		if _, err := clientFor(roots, goodClient, goodClientKey).Get(url + "/admin/test/peer"); err != nil {
			t.Fatalf("handshake before reload: %v", err)
		}
		// Rotate the trust to the previously unknown CA.
		pool := x509.NewCertPool()
		pool.AddCert(otherCA.cert)
		plane.ClientCASnapshot().Store(pool)
		if _, err := clientFor(roots, goodClient, goodClientKey).Get(url + "/admin/test/peer"); err == nil {
			t.Fatal("the removed root still verified a client after the snapshot reload")
		}
		resp, err := clientFor(roots, orphanClient, orphanClientKey).Get(url + "/admin/test/peer")
		if err != nil {
			t.Fatalf("handshake with the new root after reload: %v", err)
		}
		resp.Body.Close()
	})
}

// --- test certificate helpers -------------------------------------------------

type adminTestCA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
}

func newAdminTestCA(t *testing.T) *adminTestCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "admin test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &adminTestCA{cert: cert, key: key, certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (ca *adminTestCA) issue(t *testing.T, commonName string, dnsNames []string, uris []string, server bool) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		DNSNames:     dnsNames,
	}
	if server {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	} else {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	for _, raw := range uris {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		template.URIs = append(template.URIs, parsed)
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func writeTestPEM(t *testing.T, dir, name string, raw []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeTestKey(t *testing.T, dir, name string, raw []byte) string {
	t.Helper()
	return writeTestPEM(t, dir, name, raw)
}

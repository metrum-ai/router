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
	"sync"
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
	t.Run("installed CA source drives the handshake", func(t *testing.T) {
		cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
		cfg.Admin.Enabled = true
		cfg.Admin.Listen = "127.0.0.1:0"
		cfg.Admin.TLS = AdminTLSConfig{
			Mode:         adminTLSModeRouter,
			CertFile:     serverCertPath,
			KeyFile:      serverKeyPath,
			ClientCAFile: clientCAPath,
			ClientAuth:   adminClientAuthRequire,
			MinVersion:   adminTLSMinVersion12,
		}
		svc, err := New(cfg)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		t.Cleanup(svc.Close)
		// #828's store is the source; the binary hands it to the plane before
		// Start (this is the same wiring startAdminPlane does).
		store, err := NewAdminClientCAStore([]string{clientCAPath})
		if err != nil {
			t.Fatal(err)
		}
		svc.SetAdminClientCASource(store)
		plane, err := NewAdminPlane(&cfg.Admin, svc.AdminHandler())
		if err != nil {
			t.Fatalf("NewAdminPlane: %v", err)
		}
		if source := svc.AdminClientCASource(); source != nil {
			plane.SetClientCASource(source)
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
		url := "https://" + plane.Addr()
		if _, err := clientFor(roots, goodClient, goodClientKey).Get(url + "/healthz"); err != nil {
			t.Fatalf("handshake with the installed source: %v", err)
		}
		// Publishing a source with no trust anchors withdraws the trust.
		source := &testAdminCASource{}
		source.set(x509.NewCertPool())
		plane.SetClientCASource(source)
		if _, err := clientFor(roots, goodClient, goodClientKey).Get(url + "/healthz"); err == nil {
			t.Fatal("handshake succeeded after the installed source dropped all roots")
		}
	})
	t.Run("CA source reload applies to new handshakes", func(t *testing.T) {
		plane, url, roots := start(t, adminClientAuthRequire)
		if _, err := clientFor(roots, goodClient, goodClientKey).Get(url + "/admin/test/peer"); err != nil {
			t.Fatalf("handshake before reload: %v", err)
		}
		// Rotate the trust to the previously unknown CA through the #828 seam.
		pool := x509.NewCertPool()
		pool.AddCert(otherCA.cert)
		source := &testAdminCASource{}
		source.set(pool)
		plane.SetClientCASource(source)
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

// The listener assigns one server-generated correlation ID per request and
// never echoes a client-supplied one.
func TestAdminRequestIDIsServerGenerated(t *testing.T) {
	fixture := newAdminTestFixture(t, nil)
	req, err := http.NewRequest(http.MethodGet, fixture.adminURL+"/admin/missing", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Request-Id", "client-supplied-id")
	resp, body := adminDo(t, req)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	header := resp.Header.Get("X-Request-Id")
	if header == "" || !strings.HasPrefix(header, "req_") {
		t.Fatalf("X-Request-Id = %q, want a generated req_ id", header)
	}
	if strings.Contains(body, "client-supplied-id") {
		t.Fatalf("problem echoed the client request id: %q", body)
	}
	var problem struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal([]byte(body), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.RequestID != header {
		t.Fatalf("body request_id %q != header %q", problem.RequestID, header)
	}
}

// A fake #810 certificate manager proves the single handshake hook asks the
// certificate source first, then still applies the #814 client-CA snapshot.
type fakeAdminCertificateSource struct {
	names map[string]*tls.Certificate
}

func (f *fakeAdminCertificateSource) CheckSNI(name string) bool {
	_, ok := f.names[name]
	return ok
}

func (f *fakeAdminCertificateSource) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	cert, ok := f.names[hello.ServerName]
	if !ok {
		return nil, fmt.Errorf("unknown SNI")
	}
	return cert, nil
}

// testAdminCASource is a minimal #828 client-CA source with a controllable
// snapshot, used to prove reloads reach new handshakes.
type testAdminCASource struct {
	mu    sync.Mutex
	pool  *x509.CertPool
	hooks []func(*x509.CertPool)
}

func (s *testAdminCASource) Snapshot() *x509.CertPool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pool
}

func (s *testAdminCASource) OnReload(hook func(*x509.CertPool)) {
	if hook == nil {
		return
	}
	s.mu.Lock()
	s.hooks = append(s.hooks, hook)
	current := s.pool
	s.mu.Unlock()
	hook(current)
}

func (s *testAdminCASource) set(pool *x509.CertPool) {
	s.mu.Lock()
	s.pool = pool
	hooks := append([]func(*x509.CertPool){}, s.hooks...)
	s.mu.Unlock()
	for _, hook := range hooks {
		hook(pool)
	}
}

func TestAdminCertificateSourceWithMTLS(t *testing.T) {
	serverCA := newAdminTestCA(t)
	clientCA := newAdminTestCA(t)
	serverCertPEM, serverKeyPEM := serverCA.issue(t, "router.test", []string{"localhost"}, nil, true)
	clientCertPEM, clientKeyPEM := clientCA.issue(t, "client.test", nil, nil, false)

	serverPair, err := tls.X509KeyPair(serverCertPEM, serverKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	clientCAPath := writeTestPEM(t, dir, "client-ca.crt", clientCA.certPEM)
	// The config carries certificate_source: acme with an acme block; the
	// listener has no static certificate and no files.
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Admin.Enabled = true
	cfg.Admin.Listen = "127.0.0.1:0"
	cfg.Admin.TLS = AdminTLSConfig{
		Mode:              adminTLSModeRouter,
		CertificateSource: adminCertificateSourceACME,
		ACME:              map[string]any{"directory": "https://acme.test/directory"},
		MinVersion:        adminTLSMinVersion12,
		ClientCAFile:      clientCAPath,
		ClientAuth:        adminClientAuthRequire,
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(svc.Close)
	handler := svc.AdminHandler()
	if handler == nil {
		t.Fatal("AdminHandler() = nil")
	}
	plane, err := NewAdminPlane(&cfg.Admin, handler)
	if err != nil {
		t.Fatalf("NewAdminPlane: %v", err)
	}
	// Starting without the manager fails closed.
	if err := plane.Start(); err == nil || !strings.Contains(err.Error(), "certificate source") {
		t.Fatalf("Start() without a certificate source = %v, want a refusal", err)
	}
	if err := plane.SetCertificateSource(&fakeAdminCertificateSource{names: map[string]*tls.Certificate{"localhost": &serverPair}}); err != nil {
		t.Fatalf("SetCertificateSource: %v", err)
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
	clientReq := func(t *testing.T) *http.Client {
		t.Helper()
		pair, err := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Client{
			Transport: &http.Transport{TLSClientConfig: &tls.Config{
				RootCAs:      roots,
				Certificates: []tls.Certificate{pair},
				ServerName:   "localhost",
				MinVersion:   tls.VersionTLS12,
			}},
			Timeout: 5 * time.Second,
		}
	}
	url := "https://" + plane.Addr()
	resp, err := clientReq(t).Get(url + "/healthz")
	if err != nil {
		t.Fatalf("handshake with the ACME-style certificate and a client certificate: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin /healthz = %d, want 200", resp.StatusCode)
	}
	// A name the source does not serve fails at the handshake.
	other := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:    roots,
			ServerName: "unknown.test",
			// The unknown SNI never gets a certificate, so skip verification of
			// the server name here and expect the handshake to fail anyway.
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS12,
		}},
		Timeout: 5 * time.Second,
	}
	if _, err := other.Get(url + "/healthz"); err == nil {
		t.Fatal("handshake for an unserved SNI succeeded")
	}
	// Client certificates are still required: the source only supplies the
	// server certificate.
	noClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:    roots,
			ServerName: "localhost",
			MinVersion: tls.VersionTLS12,
		}},
		Timeout: 5 * time.Second,
	}
	if _, err := noClient.Get(url + "/healthz"); err == nil {
		t.Fatal("handshake without a client certificate succeeded with client_auth: require")
	}
}

// fakeAdminAuthenticator is a minimal #814 chain for listener tests: it accepts
// one bearer token and maps it to a principal, and rejects everything else.
type fakeAdminAuthenticator struct {
	token     string
	principal Principal
}

// AuthenticatorFunc adapts a function to the #814 Authenticator interface (test
// double only).
type AuthenticatorFunc func(context.Context, *http.Request) (Principal, error)

func (f AuthenticatorFunc) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	return f(ctx, r)
}

func (f *fakeAdminAuthenticator) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	if err := ctx.Err(); err != nil {
		return Principal{}, ErrInvalidCredential
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return Principal{}, ErrNoCredential
	}
	scheme, value, _ := strings.Cut(header, " ")
	if !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(value) != f.token {
		return Principal{}, ErrInvalidCredential
	}
	return f.principal, nil
}

// The admin listener takes identity only from the installed #814 chain and
// publishes it with WithPrincipal; unauthenticated requests get #814's typed
// problem codes.
func newAdminAuthTestFixture(t *testing.T, authenticate Authenticator) *adminTestFixture {
	t.Helper()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Admin.Enabled = true
	cfg.Admin.Listen = "127.0.0.1:0"
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(svc.Close)
	// The chain must be installed before the plane builds its handler.
	svc.SetAdminAuthenticator(authenticate)
	fixture := &adminTestFixture{cfg: cfg, svc: svc}
	fixture.serving = httptest.NewServer(svc.Handler())
	t.Cleanup(fixture.serving.Close)
	handler := svc.AdminHandler()
	if handler == nil {
		t.Fatal("AdminHandler() = nil")
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

func TestAdminAuthenticatorChainWiring(t *testing.T) {
	want := Principal{Subject: "spiffe://example.test/ops", Kind: "mtls", CredentialID: "cert-1", Issuer: "example-ca"}
	fixture := newAdminAuthTestFixture(t, &fakeAdminAuthenticator{token: "machine-token", principal: want})
	fixture.svc.RegisterAdminRoutes(func(mux *AdminMux) {
		mux.HandleFunc("GET /admin/test/whoami", func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFrom(r.Context())
			if !ok {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"subject":%q,"kind":%q}`, principal.Subject, principal.Kind)
		})
	})
	// Health probes stay unauthenticated and state-only.
	resp, body := adminGet(t, fixture.adminURL+"/healthz")
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(body) != `{"ok":true}` {
		t.Fatalf("admin /healthz = %d %q, want the unauthenticated state-only probe", resp.StatusCode, body)
	}
	// No credential: authentication_required.
	resp, body = adminGet(t, fixture.adminURL+"/admin/test/whoami")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request = %d, want 401 (body %q)", resp.StatusCode, body)
	}
	if code := adminProblemResponseCode(t, body); code != "authentication_required" {
		t.Fatalf("unauthenticated code = %q, want authentication_required", code)
	}
	if challenge := resp.Header.Get("WWW-Authenticate"); !strings.Contains(challenge, "Bearer") {
		t.Fatalf("WWW-Authenticate = %q, want a bearer challenge", challenge)
	}
	// Wrong credential: invalid_credential.
	bad, err := http.NewRequest(http.MethodGet, fixture.adminURL+"/admin/test/whoami", nil)
	if err != nil {
		t.Fatal(err)
	}
	bad.Header.Set("Authorization", "Bearer wrong")
	resp, body = adminDo(t, bad)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong credential = %d, want 401", resp.StatusCode)
	}
	if code := adminProblemResponseCode(t, body); code != "invalid_credential" {
		t.Fatalf("wrong credential code = %q, want invalid_credential", code)
	}
	// The authenticated principal reaches the handler through PrincipalFrom.
	good, err := http.NewRequest(http.MethodGet, fixture.adminURL+"/admin/test/whoami", nil)
	if err != nil {
		t.Fatal(err)
	}
	good.Header.Set("Authorization", "Bearer machine-token")
	resp, body = adminDo(t, good)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated request = %d, want 200 (body %q)", resp.StatusCode, body)
	}
	if !strings.Contains(body, want.Subject) || !strings.Contains(body, `"kind":"mtls"`) {
		t.Fatalf("handler principal = %q, want the chain's principal", body)
	}
	// The browser-credential gate still runs first, before the chain.
	cookieReq, err := http.NewRequest(http.MethodGet, fixture.adminURL+"/admin/test/whoami", nil)
	if err != nil {
		t.Fatal(err)
	}
	cookieReq.Header.Set("Cookie", "a=b")
	cookieReq.Header.Set("Authorization", "Bearer machine-token")
	resp, body = adminDo(t, cookieReq)
	if resp.StatusCode != http.StatusUnauthorized || adminProblemResponseCode(t, body) != "unsupported_authentication" {
		t.Fatalf("cookie request = %d %q, want unsupported_authentication", resp.StatusCode, body)
	}
}

// The chain errors map onto #814's published codes.
func TestAdminAuthenticatorChainUnsupportedCredential(t *testing.T) {
	fixture := newAdminAuthTestFixture(t, AuthenticatorFunc(func(context.Context, *http.Request) (Principal, error) {
		return Principal{}, ErrUnsupportedAuthentication
	}))
	fixture.svc.RegisterAdminRoutes(func(mux *AdminMux) {
		mux.HandleFunc("GET /admin/test/anything", func(w http.ResponseWriter, r *http.Request) {})
	})
	req, err := http.NewRequest(http.MethodGet, fixture.adminURL+"/admin/test/anything", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, body := adminDo(t, req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if code := adminProblemResponseCode(t, body); code != "unsupported_authentication" {
		t.Fatalf("code = %q, want unsupported_authentication", code)
	}
}

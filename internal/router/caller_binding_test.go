// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCallerBindingCIDRASNReportAndEnforce(t *testing.T) {
	config := ClientIPConfig{ASNPrefixes: []ASNPrefixConfig{{CIDR: "198.51.100.0/24", ASN: 64500}}}
	caller := &callerRuntime{cfg: CallerConfig{ID: "worker", Binding: CallerBindingConfig{Mode: "enforce", AllowedCIDRs: []string{"198.51.100.0/24"}, AllowedASNs: []uint32{64500}}}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "198.51.100.10:443"
	ip := resolveClientIP(r, ClientIPConfig{})
	if err := enforceCallerBinding(caller, r, ip, config); err != nil {
		t.Fatalf("matching binding rejected: %v", err)
	}
	r.RemoteAddr = "203.0.113.10:443"
	ip = resolveClientIP(r, ClientIPConfig{})
	if err := enforceCallerBinding(caller, r, ip, config); err == nil {
		t.Fatal("enforce accepted CIDR/ASN mismatch")
	}
	caller.cfg.Binding.Mode = "report"
	if err := enforceCallerBinding(caller, r, ip, config); err != nil {
		t.Fatalf("report blocked mismatch: %v", err)
	}
}

func TestCallerBindingCertificateIdentityAndStartupValidation(t *testing.T) {
	caller := &callerRuntime{cfg: CallerConfig{ID: "worker", Binding: CallerBindingConfig{Mode: "enforce", MTLSSPIFFEID: "spiffe://corp/ns/agents/sa/worker", MTLSSubject: "CN=worker"}}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-Client-Cert", "spiffe://corp/ns/agents/sa/worker")
	if got := callerBindingMismatch(caller, r, clientIPInfo{}, ClientIPConfig{}); got != "client_certificate_missing_or_unverified" {
		t.Fatalf("forwarded certificate header accepted: %q", got)
	}
	u, _ := url.Parse("spiffe://corp/ns/agents/sa/worker")
	rawSAN, _ := asn1.Marshal([]asn1.RawValue{{Class: 2, Tag: 6, Bytes: []byte(u.String())}})
	leaf := &x509.Certificate{Subject: pkix.Name{CommonName: "worker"}, URIs: []*url.URL{u}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, Extensions: []pkix.Extension{{Id: asn1.ObjectIdentifier{2, 5, 29, 17}, Value: rawSAN}}}
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}}}
	if got := callerBindingMismatch(caller, r, clientIPInfo{}, ClientIPConfig{}); got != "" {
		t.Fatalf("matching certificate rejected: %q", got)
	}
	r.TLS.PeerCertificates[0].Subject.CommonName = "other"
	if got := callerBindingMismatch(caller, r, clientIPInfo{}, ClientIPConfig{}); got != "client_certificate_subject_mismatch" {
		t.Fatalf("mismatched certificate accepted: %q", got)
	}
	r.TLS.PeerCertificates[0].ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageAny}
	if got := callerBindingMismatch(caller, r, clientIPInfo{}, ClientIPConfig{}); got != "client_certificate_client_auth_required" {
		t.Fatalf("certificate without explicit clientAuth EKU accepted: %q", got)
	}
	r.TLS.PeerCertificates[0].ExtKeyUsage = nil
	if got := callerBindingMismatch(caller, r, clientIPInfo{}, ClientIPConfig{}); got != "client_certificate_client_auth_required" {
		t.Fatalf("certificate without EKU accepted: %q", got)
	}
	if err := validateCallerBinding(caller.cfg.Binding, "worker", ServerTLSConfig{}, ClientIPConfig{}); err == nil {
		t.Fatal("enforced mTLS binding accepted without verified client TLS")
	}
	if err := validateCallerBinding(caller.cfg.Binding, "worker", ServerTLSConfig{ListenerTLSConfig: ListenerTLSConfig{Mode: "router"}, ClientAuth: "optional", ClientCAFile: "client-ca.pem"}, ClientIPConfig{}); err != nil {
		t.Fatalf("optional verified client TLS rejected: %v", err)
	}
	if err := validateCallerBinding(CallerBindingConfig{AllowedASNs: []uint32{64500}}, "worker", ServerTLSConfig{}, ClientIPConfig{}); err == nil {
		t.Fatal("ASN allow list accepted without an ASN prefix map")
	}
	if err := validateCallerBinding(CallerBindingConfig{Mode: "enforce"}, "worker", ServerTLSConfig{}, ClientIPConfig{}); err == nil {
		t.Fatal("empty enforce binding accepted")
	}
}

func TestConfigValidateRejectsCallerMTLSWithoutServingClientAuth(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()
	newConfig := func() *Config { return testConfig(t, upstream.URL, "provider-key", t.TempDir()) }

	t.Run("binding requires verified serving TLS", func(t *testing.T) {
		cfg := newConfig()
		cfg.Callers[0].Binding = CallerBindingConfig{Mode: "enforce", MTLSSPIFFEID: "spiffe://example/worker"}
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "binding enforce requires server.tls client-auth verification") {
			t.Fatalf("Config.Validate() error = %v, want missing client-auth verification", err)
		}
	})

	for _, clientAuth := range []string{"optional", "require"} {
		t.Run("client_auth "+clientAuth+" needs router mode", func(t *testing.T) {
			cfg := newConfig()
			cfg.Server.TLS = ServerTLSConfig{ListenerTLSConfig: ListenerTLSConfig{Mode: "off"}, ClientAuth: clientAuth, ClientCAFile: "client-ca.pem"}
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "needs mode router") {
				t.Fatalf("Config.Validate() error = %v, want client_auth mode rejection", err)
			}
		})
	}

	t.Run("client CA requires verification mode", func(t *testing.T) {
		cfg := newConfig()
		cfg.Server.TLS.ClientCAFile = "client-ca.pem"
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "client_ca_file needs client_auth optional or require") {
			t.Fatalf("Config.Validate() error = %v, want client CA verification error", err)
		}
	})
}

func TestCallerBindingEnforceEndToEndReportContinues(t *testing.T) {
	var capturedLogs bytes.Buffer
	previousLogWriter := log.Writer()
	log.SetOutput(&capturedLogs)
	defer log.SetOutput(previousLogWriter)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Callers[0].Binding = CallerBindingConfig{Mode: "report", AllowedCIDRs: []string{"192.0.2.0/24"}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	server := httptest.NewServer(svc.Handler())
	defer server.Close()
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("report mismatch status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	if strings.Contains(capturedLogs.String(), testToken) {
		t.Fatal("report-mode binding logs contained the caller token")
	}
}

func TestCallerBindingEnforceEndToEndAndTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Callers[0].Binding = CallerBindingConfig{Mode: "enforce", AllowedCIDRs: []string{"192.0.2.0/24"}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	server := httptest.NewServer(svc.Handler())
	defer server.Close()
	request := func() *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hello"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+testToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := request()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("mismatch status=%d, want 401", resp.StatusCode)
	}
	// An expired request context cannot turn an enforce mismatch into admission.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	cancel()
	timedOut := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hello"}]}`)).WithContext(ctx)
	timedOut.RemoteAddr = "198.51.100.1:443"
	timedOut.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	if _, ok := svc.begin(rr, timedOut, "openai-chat"); ok || rr.Code != http.StatusUnauthorized {
		t.Fatalf("cancelled request bypassed binding: admitted=%v status=%d", ok, rr.Code)
	}
}

func TestCallerBindingMTLSRealTLSHandshakes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"ok","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()

	serverDir := t.TempDir()
	serverCertPath, serverKeyPath := writeTestCertificate(t, serverDir, []string{"router.example.com"})
	serverPair, err := tls.LoadX509KeyPair(serverCertPath, serverKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	serverCertPEM, err := os.ReadFile(serverCertPath)
	if err != nil {
		t.Fatal(err)
	}
	serverRoots := x509.NewCertPool()
	if !serverRoots.AppendCertsFromPEM(serverCertPEM) {
		t.Fatal("could not load serving certificate root")
	}

	clientCA := newAdminTestCA(t)
	clientCAPath := writeTestPEM(t, serverDir, "client-ca.pem", clientCA.certPEM)
	goodCertPEM, goodKeyPEM := clientCA.issue(t, "worker", nil, []string{"spiffe://example/worker"}, false)
	wrongCertPEM, wrongKeyPEM := clientCA.issue(t, "other", nil, []string{"spiffe://example/other"}, false)
	goodPair, err := tls.X509KeyPair(goodCertPEM, goodKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	wrongPair, err := tls.X509KeyPair(wrongCertPEM, wrongKeyPEM)
	if err != nil {
		t.Fatal(err)
	}

	for _, clientAuth := range []string{"optional", "require"} {
		t.Run(clientAuth, func(t *testing.T) {
			dir := t.TempDir()
			cfg := testConfig(t, upstream.URL, "provider-key", dir)
			cfg.Server.TLS = ServerTLSConfig{
				ListenerTLSConfig: ListenerTLSConfig{Mode: "router", CertificateSource: "files", CertFile: serverCertPath, KeyFile: serverKeyPath},
				ClientAuth:        clientAuth, ClientCAFile: clientCAPath,
			}
			cfg.Callers[0].Binding = CallerBindingConfig{Mode: "enforce", MTLSSPIFFEID: "spiffe://example/worker"}
			usageEnabled := true
			cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"), &usageEnabled)
			cfg.Server.AdminReports.Security.Enabled = true
			svc, err := New(cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer svc.Close()

			servingTLS, err := ServingTLSConfig(&tls.Config{Certificates: []tls.Certificate{serverPair}}, cfg.Server.TLS)
			if err != nil {
				t.Fatalf("ServingTLSConfig: %v", err)
			}
			server := httptest.NewUnstartedServer(svc.Handler())
			server.TLS = servingTLS
			server.StartTLS()
			defer server.Close()

			request := func(token string, cert *tls.Certificate) (*http.Response, error) {
				t.Helper()
				tlsConfig := &tls.Config{RootCAs: serverRoots, ServerName: "router.example.com"}
				if cert != nil {
					tlsConfig.Certificates = []tls.Certificate{*cert}
				}
				client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig}}
				req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hello"}]}`))
				if err != nil {
					return nil, err
				}
				req.Header.Set("Authorization", "Bearer "+token)
				return client.Do(req)
			}

			good, err := request(testToken, &goodPair)
			if err != nil {
				t.Fatalf("matching client certificate handshake: %v", err)
			}
			if good.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(good.Body)
				good.Body.Close()
				t.Fatalf("matching client certificate status=%d body=%s, want 200", good.StatusCode, body)
			}
			good.Body.Close()

			mismatched, err := request(testToken, &wrongPair)
			if err != nil {
				t.Fatalf("mismatched identity handshake should complete: %v", err)
			}
			mismatchBody, err := io.ReadAll(mismatched.Body)
			mismatched.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if mismatched.StatusCode != http.StatusUnauthorized {
				t.Fatalf("mismatched client identity status=%d body=%s, want 401", mismatched.StatusCode, mismatchBody)
			}
			var event struct {
				ReasonCode string `gorm:"column:reason_code"`
			}
			requestID := mismatched.Header.Get("X-Request-Id")
			deadline := time.Now().Add(2 * time.Second)
			for {
				err := svc.usage.db.Table("security_access_events").Select("reason_code").Where("request_id = ?", requestID).First(&event).Error
				if err == nil || time.Now().After(deadline) {
					if err != nil {
						t.Fatalf("load binding security event for request %q: %v", requestID, err)
					}
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if event.ReasonCode != "client_certificate_spiffe_mismatch" {
				t.Fatalf("binding security reason=%q, want client_certificate_spiffe_mismatch", event.ReasonCode)
			}

			invalidToken, err := request("invalid-caller-token", &goodPair)
			if err != nil {
				t.Fatalf("invalid token request handshake: %v", err)
			}
			invalidTokenBody, err := io.ReadAll(invalidToken.Body)
			invalidToken.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if invalidToken.StatusCode != http.StatusUnauthorized || string(mismatchBody) != string(invalidTokenBody) {
				t.Fatalf("binding response differs from invalid token: status=%d mismatch=%s invalid=%s", invalidToken.StatusCode, mismatchBody, invalidTokenBody)
			}

			missing, err := request(testToken, nil)
			if clientAuth == "require" {
				if err == nil {
					missing.Body.Close()
					t.Fatal("missing certificate completed a require-client-cert handshake")
				}
				return
			}
			if err != nil {
				t.Fatalf("missing optional client certificate handshake: %v", err)
			}
			missingBody, readErr := io.ReadAll(missing.Body)
			missing.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if missing.StatusCode != http.StatusUnauthorized || string(missingBody) != string(invalidTokenBody) {
				t.Fatalf("missing optional certificate response status=%d body=%s, want same generic 401 as invalid token", missing.StatusCode, missingBody)
			}
		})
	}
}

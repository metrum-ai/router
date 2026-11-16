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
	config := ClientIPConfig{ASNPrefixes: []ASNPrefixConfig{{CIDR: "10.42.0.0/16", ASN: 64500}}}
	caller := &callerRuntime{cfg: CallerConfig{ID: "worker", Binding: CallerBindingConfig{Mode: "enforce", AllowedCIDRs: []string{"10.42.0.0/16"}, AllowedASNs: []uint32{64500}}}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.42.0.10:443"
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
	logs := capturedLogs.String()
	if !strings.Contains(logs, `caller binding mismatch caller_id="alice" reason=cidr_mismatch`) {
		t.Fatalf("report-mode mismatch log missing; logs=%q", logs)
	}
	if strings.Contains(logs, testToken) {
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

func TestCallerBindingExampleConfigAuthenticatesMatchingRequest(t *testing.T) {
	t.Setenv("LOCAL_PI_ADMIN_PASSWORD_HASH", mustBcryptHash(t, "admin"))
	t.Setenv("ROUTER_ID_TRANSFORM_KEY", strings.Repeat("ab", 64))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"ok","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()

	raw, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := replaceExampleCallerTokenHashes(t, string(raw))
	text = strings.Replace(text, "token_id: rtr_metrum_standard_example-project_dev_kreplace", "token_id: rtr_example_standard", 1)
	commentedBinding := "  # binding:\n  #   mode: enforce\n  #   allowed_cidrs: [10.42.0.0/16]\n  #   allowed_asns: [64500]\n  #   mtls_spiffe_id: spiffe://corp/ns/agents/sa/build-agent"
	activeBinding := "  binding:\n    mode: enforce\n    allowed_cidrs: [10.42.0.0/16]\n    allowed_asns: [64500]"
	if !strings.Contains(text, commentedBinding) {
		t.Fatal("config.example.yaml caller-binding example changed; update this matching-request test")
	}
	text = strings.Replace(text, commentedBinding, activeBinding, 1)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig(config.example.yaml): %v", err)
	}

	var exampleCaller *CallerConfig
	for i := range cfg.Callers {
		if cfg.Callers[i].ID == "standard-dev" {
			exampleCaller = &cfg.Callers[i]
			break
		}
	}
	if exampleCaller == nil {
		t.Fatal("config.example.yaml is missing standard-dev caller")
	}
	if len(cfg.Server.ClientIP.ASNPrefixes) != 1 || cfg.Server.ClientIP.ASNPrefixes[0] != (ASNPrefixConfig{CIDR: "10.42.0.0/16", ASN: 64500}) {
		t.Fatalf("example ASN map = %#v, want 10.42.0.0/16 mapped to ASN 64500", cfg.Server.ClientIP.ASNPrefixes)
	}
	if exampleCaller.Binding.Mode != "enforce" || len(exampleCaller.Binding.AllowedCIDRs) != 1 || exampleCaller.Binding.AllowedCIDRs[0] != "10.42.0.0/16" || len(exampleCaller.Binding.AllowedASNs) != 1 || exampleCaller.Binding.AllowedASNs[0] != 64500 {
		t.Fatalf("loaded example binding = %#v, want matching CIDR and ASN constraints", exampleCaller.Binding)
	}

	usageDisabled := false
	cfg.Server.UsageDB.Enable = &usageDisabled
	cfg.Server.AdminReports = AdminReportsConfig{}
	cfg.Server.Listen = ":0"
	cfg.Server.Logging.Path = filepath.Join(dir, "requests.jsonl")
	cfg.StatePath = filepath.Join(dir, "state.json")
	cfg.Provider = map[string]ProviderConfig{"mock": {BaseURL: upstream.URL + "/v1", Dialect: "openai", APIKey: "provider-key"}}
	cfg.Models = map[string]ModelGroup{"default": {Strategy: "static", Targets: []Target{{Provider: "mock", Model: "mock-model"}}}}
	cfg.Server.DefaultModelGroup = "default"
	configuredCaller := *exampleCaller
	configuredCaller.Allow = []string{"default"}
	cfg.Callers = []CallerConfig{configuredCaller}

	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("New(example config): %v", err)
	}
	defer svc.Close()

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hello"}]}`))
	request.RemoteAddr = "10.42.0.10:443"
	request.Header.Set("Authorization", "Bearer rtr_example_standard_test")
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("matching configured example request status=%d body=%s, want 200", response.Code, response.Body.String())
	}
}

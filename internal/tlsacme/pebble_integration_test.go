// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

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
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metrum-ai/router/internal/tlsacme/dns"
)

// TestPebbleDNS01IssuanceAndRenewal exercises ACP-N08a/N08b end to end against
// Pebble, the ACME test CA, with pebble-challtestsrv serving the DNS-01 TXT
// records. It is skipped when the two binaries are not on PATH; CI installs
// them with `go install github.com/letsencrypt/pebble/v2/cmd/...`.
//
// The test proves:
//   - a fresh cache obtains a certificate with no manual steps,
//   - and only through a DNS provider API (DNS-01, no inbound port),
//   - the renewal deadline is two thirds of the (short) Pebble lifetime,
//   - renewal replaces the served certificate,
//   - a failing provider raises the renewal failure metric while the previous
//     certificate keeps being served.
func TestPebbleDNS01IssuanceAndRenewal(t *testing.T) {
	pebbleBin := lookPathOrSkip(t, "pebble")
	challBin := lookPathOrSkip(t, "pebble-challtestsrv")

	dir := t.TempDir()
	certPath, certPEM := writePebbleCertificate(t, dir)

	dnsPort := freeUDPPort(t)
	mgmtPort := freePort(t)
	acmePort := freePort(t)
	pebbleMgmtPort := freePort(t)

	chall := startProcess(t, challBin,
		"-dnsserver", fmt.Sprintf("127.0.0.1:%d", dnsPort),
		"-management", fmt.Sprintf("127.0.0.1:%d", mgmtPort),
		"-http01", "",
		"-https01", "",
		"-doh", "",
		"-tlsalpn01", "",
		"-defaultIPv4", "127.0.0.1",
	)
	waitForPort(t, fmt.Sprintf("127.0.0.1:%d", mgmtPort), 15*time.Second, chall)

	// A 180 second certificate makes the two-thirds deadline (about 120s after
	// issuance) reachable inside one test.
	configPath := filepath.Join(dir, "pebble-config.json")
	config := fmt.Sprintf(`{
  "pebble": {
    "listenAddress": "127.0.0.1:%d",
    "managementListenAddress": "127.0.0.1:%d",
    "certificate": %q,
    "privateKey": %q,
    "httpPort": %d,
    "tlsPort": %d,
    "ocspResponderURL": "",
    "externalAccountBindingRequired": false,
    "retryAfter": {"authz": 1, "order": 1},
    "profiles": {
      "default": {"description": "router test profile", "validityPeriod": 180}
    }
  }
}`, acmePort, pebbleMgmtPort, certPath, filepath.Join(dir, "localhost.key"), freePort(t), freePort(t))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write pebble config: %v", err)
	}
	pebble := startProcessEnv(t, pebbleBin,
		[]string{"-config", configPath, "-dnsserver", fmt.Sprintf("127.0.0.1:%d", dnsPort)},
		// No authorization reuse: every order must solve a fresh DNS-01
		// challenge, which is what makes the provider failure path observable.
		append(os.Environ(), "PEBBLE_VA_NOSLEEP=1", "PEBBLE_WFE_NONCEREJECT=0", "PEBBLE_AUTHZREUSE=0"),
	)

	directoryURL := fmt.Sprintf("https://127.0.0.1:%d/dir", acmePort)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("could not trust the Pebble endpoint certificate")
	}
	httpClient := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}},
	}
	waitForDirectory(t, httpClient, directoryURL, 30*time.Second, pebble)

	t.Setenv("CF_DNS_API_TOKEN", "unused-test-value")
	provider := newChallengeSrvProvider(fmt.Sprintf("http://127.0.0.1:%d", mgmtPort), httpClient)
	now := time.Now()
	clock := func() time.Time { return now }

	cfg := validACMEConfig()
	cfg.CacheDir = filepath.Join(dir, "tls")
	cfg.Domains = []string{"router.example.com"}
	cfg.DirectoryURL = directoryURL
	cfg.DNS.Zone = "example.com"
	cfg.DNS.PropagationNameservers = []string{fmt.Sprintf("127.0.0.1:%d", dnsPort)}
	cfg.DNS.PropagationTimeout = 20 * time.Second
	cfg.StartupTimeout = 60 * time.Second
	// Keep the ratio deadline deterministic; a second manager below enables ARI.
	cfg.Renewal.UseARI = boolPtr(false)

	manager, err := New(cfg,
		WithProvider(provider),
		WithHTTPClient(httpClient),
		WithNow(clock),
	)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := manager.EnsureCertificate(ctx); err != nil {
		t.Fatalf("fresh install did not obtain a certificate: %v", err)
	}
	// Release the frozen clock so validity is judged against the certificate
	// Pebble just minted.
	now = time.Now()
	first := manager.Status()
	if len(first) != 1 || !first[0].Valid {
		t.Fatalf("status after issuance = %+v", first)
	}
	if !containsString(first[0].Domains, "router.example.com") {
		t.Fatalf("issued certificate domains = %v", first[0].Domains)
	}
	record, err := manager.store.LoadCertificate(cfg.Domains)
	if err != nil || record == nil {
		t.Fatalf("certificate was not stored: %v", err)
	}
	lifetime := record.NotAfter.Sub(record.NotBefore)
	if lifetime > 10*time.Minute {
		t.Fatalf("Pebble issued a %v certificate; the short profile was not applied", lifetime)
	}
	wantDeadline := record.NotBefore.Add(time.Duration(float64(lifetime) * (2.0 / 3.0)))
	if delta := first[0].RenewalDue.Sub(wantDeadline); delta > 20*time.Second || delta < -20*time.Second {
		t.Fatalf("renewal due = %v, want about two thirds of the lifetime (%v)", first[0].RenewalDue, wantDeadline)
	}
	firstSerial := leafSerial(t, record.CertificatePEM)

	// Renewal: move the clock past the deadline and let the loop run.
	now = record.NotAfter.Add(-5 * time.Second)
	manager.tick(ctx)
	second, err := manager.store.LoadCertificate(cfg.Domains)
	if err != nil || second == nil {
		t.Fatalf("load renewed certificate: %v", err)
	}
	if leafSerial(t, second.CertificatePEM).Cmp(firstSerial) == 0 {
		t.Fatal("renewal did not replace the certificate")
	}
	if manager.Source() != "issued" {
		t.Fatalf("renewed certificate was not installed: %+v", manager.Status())
	}

	// ARI: with the CA advertising renewal information, the deadline comes from
	// the suggested window rather than the ratio.
	ariCfg := cfg
	ariCfg.CacheDir = filepath.Join(dir, "tls-ari")
	ariCfg.Renewal.UseARI = boolPtr(true)
	ariManager, err := New(ariCfg, WithProvider(provider), WithHTTPClient(httpClient), WithNow(clock))
	if err != nil {
		t.Fatalf("new ARI manager: %v", err)
	}
	if err := ariManager.EnsureCertificate(context.Background()); err != nil {
		t.Fatalf("ARI issuance: %v", err)
	}
	ariRecord, err := ariManager.store.LoadCertificate(ariCfg.Domains)
	if err != nil || ariRecord == nil {
		t.Fatalf("ARI certificate was not stored: %v", err)
	}
	leafBlock, _ := pem.Decode(ariRecord.CertificatePEM)
	leafCert, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		t.Fatalf("parse ARI certificate: %v", err)
	}
	ari := &ariClient{httpClient: httpClient}
	windowStart, windowEnd, arriErr := ari.window(context.Background(), directoryURL, leafCert)
	if arriErr != nil {
		t.Fatalf("Pebble should advertise renewal information: %v", arriErr)
	}
	due := ariManager.Status()[0].RenewalDue
	if due.Before(windowStart.Add(-time.Second)) || due.After(windowEnd.Add(time.Second)) {
		t.Fatalf("renewal due %v is outside the CA's suggested window %v..%v", due, windowStart, windowEnd)
	}
	if got := ariManager.metrics.value(metricARIFailures, ariManager.metrics.baseLabels()); got != 0 {
		t.Fatalf("ARI failures = %v, want 0", got)
	}

	// Failure path: the provider breaks, the renewal fails, the metric rises
	// and the previous certificate keeps being served.
	provider.setFailCreate(true)
	before := manager.record
	// Past the renewal deadline but still inside the certificate lifetime, so a
	// successful order could install but a failing provider must not.
	now = manager.record.NotBefore.Add(time.Duration(float64(manager.record.NotAfter.Sub(manager.record.NotBefore)) * 0.85))
	manager.tick(ctx)
	if got := manager.metrics.value(metricRenewalFailures, mergeLabels(manager.metrics.baseLabels(), [2]string{"reason", "dns"})); got < 1 {
		t.Fatalf("renewal failures for reason dns = %v, want at least 1 (last error %q)", got, manager.Status()[0].LastError)
	}
	if manager.record != before {
		t.Fatal("a failed renewal must keep serving the previous certificate")
	}
	if cert, err := manager.GetCertificate(nil); err != nil || cert == nil {
		t.Fatalf("serving after a failed renewal: %v", err)
	}
	if !strings.Contains(manager.Prometheus(), metricRenewalFailures) {
		t.Fatal("the renewal failure metric is not exported")
	}
}

// challengeSrvProvider is a DNS provider backed by the pebble-challtestsrv
// management API. It exercises the same Provider interface the production
// providers implement.
type challengeSrvProvider struct {
	base   string
	client *http.Client

	mu         sync.Mutex
	records    map[string]string
	failCreate bool
}

func newChallengeSrvProvider(base string, client *http.Client) *challengeSrvProvider {
	return &challengeSrvProvider{base: base, client: client, records: map[string]string{}}
}

func (p *challengeSrvProvider) Name() string { return "challtestsrv" }

func (p *challengeSrvProvider) Zones(context.Context) ([]string, error) {
	return []string{"example.com"}, nil
}

func (p *challengeSrvProvider) Records(context.Context, string) ([]dns.Record, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]dns.Record, 0, len(p.records))
	for name, value := range p.records {
		out = append(out, dns.Record{ID: name + "|" + value, Type: "TXT", Name: strings.TrimSuffix(name, "."), Value: value})
	}
	return out, nil
}

func (p *challengeSrvProvider) Create(ctx context.Context, _ string, record dns.Record) (string, error) {
	p.mu.Lock()
	fail := p.failCreate
	p.mu.Unlock()
	if fail {
		return "", errors.New("dns: simulated provider failure")
	}
	host := record.Name + "."
	if err := p.post(ctx, "/set-txt", map[string]string{"host": host, "value": record.Value}); err != nil {
		return "", err
	}
	p.mu.Lock()
	p.records[host] = record.Value
	p.mu.Unlock()
	return host + "|" + record.Value, nil
}

func (p *challengeSrvProvider) Update(ctx context.Context, zone string, _ string, record dns.Record) error {
	if _, err := p.Create(ctx, zone, record); err != nil {
		return err
	}
	return nil
}

func (p *challengeSrvProvider) Delete(ctx context.Context, _ string, id string) error {
	host, _, _ := strings.Cut(id, "|")
	if err := p.post(ctx, "/clear-txt", map[string]string{"host": host}); err != nil {
		return err
	}
	p.mu.Lock()
	delete(p.records, host)
	p.mu.Unlock()
	return nil
}

func (p *challengeSrvProvider) setFailCreate(fail bool) {
	p.mu.Lock()
	p.failCreate = fail
	p.mu.Unlock()
}

func (p *challengeSrvProvider) post(ctx context.Context, path string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("challtestsrv %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func leafSerial(t *testing.T, certPEM []byte) *big.Int {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("no certificate PEM")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return leaf.SerialNumber
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// lookPathOrSkip skips when the test CA binaries are absent, unless
// TLSACME_REQUIRE_PEBBLE is set: CI sets it so a broken PATH or a failed install
// fails the job instead of turning the suite into a vacuous pass.
func lookPathOrSkip(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		if os.Getenv("TLSACME_REQUIRE_PEBBLE") != "" {
			t.Fatalf("%s is not on PATH and TLSACME_REQUIRE_PEBBLE is set; install it with go install github.com/letsencrypt/pebble/v2/cmd/%s@v2.10.1", name, name)
		}
		t.Skipf("%s is not on PATH; install it with go install github.com/letsencrypt/pebble/v2/cmd/%s@v2.10.1", name, name)
	}
	return path
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free port: %v", err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("find a free UDP port: %v", err)
	}
	defer func() { _ = conn.Close() }()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

// writePebbleCertificate creates the self-signed certificate the Pebble ACME
// endpoint serves, and returns its path and PEM.
func writePebbleCertificate(t *testing.T, dir string) (string, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate pebble key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "pebble"},
		DNSNames:              []string{"localhost"},
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
		t.Fatalf("create pebble certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal pebble key: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	certPath := filepath.Join(dir, "localhost.crt")
	keyPath := filepath.Join(dir, "localhost.key")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write pebble certificate: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write pebble key: %v", err)
	}
	return certPath, certPEM
}

type runningProcess struct {
	cmd  *exec.Cmd
	logs *bytes.Buffer
}

func startProcess(t *testing.T, bin string, args ...string) *runningProcess {
	t.Helper()
	return startProcessEnv(t, bin, args, os.Environ())
}

func startProcessEnv(t *testing.T, bin string, args, env []string) *runningProcess {
	t.Helper()
	logs := &bytes.Buffer{}
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", bin, err)
	}
	process := &runningProcess{cmd: cmd, logs: logs}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
		if t.Failed() {
			t.Logf("%s output:\n%s", filepath.Base(bin), logs.String())
		}
	})
	return process
}

func waitForPort(t *testing.T, address string, timeout time.Duration, process *runningProcess) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s did not listen on %s within %s:\n%s", process.cmd.Path, address, timeout, process.logs.String())
}

func waitForDirectory(t *testing.T, client *http.Client, url string, timeout time.Duration, process *runningProcess) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Pebble directory %s did not become ready within %s: %v\n%s", url, timeout, lastErr, process.logs.String())
}

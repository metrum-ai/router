// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package tlsacme

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/metrum-ai/router/internal/tlsacme/dns"
)

// CertificateStatus describes the certificate a Manager serves. It carries
// metadata only: never key material and never a credential.
type CertificateStatus struct {
	Listener   string
	Domains    []string
	Issuer     string
	NotBefore  time.Time
	NotAfter   time.Time
	RenewalDue time.Time
	Source     string
	Valid      bool
	LastError  string
}

type managerOptions struct {
	logger      *log.Logger
	httpClient  *http.Client
	now         func() time.Time
	roll        func(int64) int64
	store       *Store
	provider    dns.Provider
	certifier   Certifier
	renewTicker time.Duration
}

// Option configures a Manager. Options exist for tests and for the router to
// inject a cached store; production uses them sparingly.
type Option func(*managerOptions)

// WithLogger sets the logger.
func WithLogger(logger *log.Logger) Option { return func(o *managerOptions) { o.logger = logger } }

// WithHTTPClient sets the client used for ACME and provider calls.
func WithHTTPClient(client *http.Client) Option {
	return func(o *managerOptions) { o.httpClient = client }
}

// WithNow overrides the clock.
func WithNow(now func() time.Time) Option { return func(o *managerOptions) { o.now = now } }

// WithRandomInt63 overrides the jitter source.
func WithRandomInt63(roll func(int64) int64) Option {
	return func(o *managerOptions) { o.roll = roll }
}

// WithStore overrides the certificate store.
func WithStore(store *Store) Option { return func(o *managerOptions) { o.store = store } }

// WithProvider overrides the DNS provider.
func WithProvider(provider dns.Provider) Option {
	return func(o *managerOptions) { o.provider = provider }
}

// WithCertifier overrides the ACME certifier.
func WithCertifier(certifier Certifier) Option {
	return func(o *managerOptions) { o.certifier = certifier }
}

// WithRenewTicker overrides the renewal loop interval (tests).
func WithRenewTicker(interval time.Duration) Option {
	return func(o *managerOptions) { o.renewTicker = interval }
}

// Manager owns the certificate lifecycle of one listener.
type Manager struct {
	cfg Config
	opt managerOptions

	store     *Store
	provider  dns.Provider
	solver    challengeSolver
	certifier Certifier
	ari       *ariClient
	metrics   *metrics
	secrets   *secretSet

	mu        sync.RWMutex
	cert      *tls.Certificate
	record    *CertificateRecord
	deadline  time.Time
	loadedMod time.Time
	source    string
	lastError string
	zone      string

	attempts     int
	nextAttempt  time.Time
	ariRetryAt   time.Time
	lockBroken   bool
	reloadTicker time.Duration
}

// New builds a Manager for one listener. It never performs network I/O: the
// first issuance happens in EnsureCertificate and renewals in Run.
func New(cfg Config, opts ...Option) (*Manager, error) {
	normalized := cfg.Normalize()
	if err := normalized.Validate(); err != nil {
		return nil, err
	}
	options := managerOptions{
		now:         time.Now,
		roll:        randomRoll,
		renewTicker: 15 * time.Second,
	}
	for _, opt := range opts {
		opt(&options)
	}
	if options.logger == nil {
		options.logger = log.New(os.Stderr, "tls: ", log.LstdFlags)
	}
	if options.httpClient == nil {
		options.httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	m := &Manager{
		cfg:          normalized,
		opt:          options,
		secrets:      &secretSet{},
		metrics:      newMetrics(normalized.Listener, normalized.ServeDomains()),
		reloadTicker: normalized.ReloadInterval,
	}
	if normalized.Mode == ModeOff {
		return m, nil
	}
	if normalized.Mode == ModeFiles {
		if err := m.loadFiles(); err != nil {
			return nil, err
		}
		return m, nil
	}

	m.store = options.store
	if m.store == nil {
		store, err := OpenStore(normalized.CacheDir)
		if err != nil {
			return nil, err
		}
		m.store = store
	}
	m.ari = &ariClient{httpClient: options.httpClient}
	m.provider = options.provider
	if m.provider == nil {
		provider, err := m.newProvider(context.Background())
		if err != nil {
			return nil, err
		}
		m.provider = provider
	}
	if options.certifier != nil {
		m.certifier = options.certifier
	} else {
		m.certifier = &acmeCertifier{
			directoryURL: normalizeDirectoryURL(normalized.DirectoryURL),
			email:        normalized.Email,
			acceptTOS:    normalized.AcceptTOS,
			keyType:      normalized.KeyType,
			httpClient:   options.httpClient,
			store:        m.store,
			solver:       &lazySolver{manager: m},
			eabKID:       normalized.EAB.KID,
			eabHMACKey:   "",
			now:          options.now,
		}
	}
	return m, nil
}

// newProvider resolves the DNS credentials and builds the configured provider.
// Credential values live only in the provider and are registered for redaction.
func (m *Manager) newProvider(ctx context.Context) (dns.Provider, error) {
	prefix := m.cfg.prefix() + ".dns.credentials"
	creds := m.cfg.DNS.Credentials
	var resolved dns.Credentials
	var err error
	resolve := func(ref SecretRef, label string, target *string) {
		if err != nil {
			return
		}
		*target, err = m.secrets.resolve(ref, label)
	}
	resolve(creds.APIToken, prefix+".api_token", &resolved.APIToken)
	resolve(creds.APIKey, prefix+".api_key", &resolved.APIKey)
	resolve(creds.Email, prefix+".email", &resolved.Email)
	resolve(creds.Token, prefix+".token", &resolved.Token)
	resolve(creds.AccessKeyID, prefix+".access_key_id", &resolved.AccessKeyID)
	resolve(creds.SecretAccessKey, prefix+".secret_access_key", &resolved.SecretAccessKey)
	resolve(creds.SessionToken, prefix+".session_token", &resolved.SessionToken)
	resolved.UseAmbientCredentials = creds.UseAmbientCredentials
	if err != nil {
		return nil, err
	}
	eabHMAC, err := m.secrets.resolve(m.cfg.EAB.HMACKey, m.cfg.prefix()+".eab.hmac_key")
	if err != nil {
		return nil, err
	}
	if certifier, ok := m.certifier.(*acmeCertifier); ok {
		certifier.eabHMACKey = eabHMAC
	}
	return dns.New(ctx, m.cfg.DNS.Provider, resolved, dns.Options{
		Zone:                   m.cfg.DNS.Zone,
		TTL:                    m.cfg.DNS.TTL,
		PropagationTimeout:     m.cfg.DNS.PropagationTimeout,
		PropagationNameservers: m.cfg.DNS.PropagationNameservers,
		HTTPClient:             m.opt.httpClient,
		Logger:                 m.opt.logger,
		Instruments:            dnsInstruments{m},
		Redact:                 m.secrets.redact,
		Now:                    m.opt.now,
	})
}

// dnsInstruments maps DNS provider events onto the TLS metrics.
type dnsInstruments struct{ m *Manager }

func (d dnsInstruments) DNSPresentFailure(provider string) {
	d.m.metrics.add(metricDNSPresentFailures, mergeLabels(d.m.metrics.baseLabels(), [2]string{"provider", provider}), 1)
}

func (d dnsInstruments) DNSConflict(provider, reason string) {
	d.m.metrics.add(metricDNSConflicts, [][2]string{{"provider", provider}, {"reason", reason}}, 1)
}

func (d dnsInstruments) DNSRecordsManaged(provider, recordType string, delta float64) {
	d.m.metrics.add(metricDNSRecordsManaged, [][2]string{{"provider", provider}, {"type", recordType}}, delta)
}

// Mode returns the configured mode.
func (m *Manager) Mode() string { return m.cfg.Mode }

// Source reports how the served certificate was obtained: issued, store or
// files. It is metadata only.
func (m *Manager) Source() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.source
}

// TLSConfig returns the tls.Config for the listener. It is nil in off mode.
// The caller may clone the result and add client authentication (mTLS).
func (m *Manager) TLSConfig() *tls.Config {
	if m == nil || m.cfg.Mode == ModeOff {
		return nil
	}
	minVersion := uint16(tls.VersionTLS12)
	if m.cfg.MinVersion == "1.3" {
		minVersion = tls.VersionTLS13
	}
	return &tls.Config{
		MinVersion:         minVersion,
		NextProtos:         []string{"h2", "http/1.1"},
		GetCertificate:     m.getCertificate,
		GetConfigForClient: m.configForClient,
	}
}

// getCertificate serves the current certificate and reloads it from the store
// when another replica renewed it.
func (m *Manager) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	m.mu.RLock()
	cert := m.cert
	m.mu.RUnlock()
	if cert == nil {
		if hello != nil && m.cfg.Mode == ModeACME {
			// A listener that has not obtained its certificate yet fails the
			// handshake instead of serving plaintext.
			return nil, fmt.Errorf("tls: no certificate available for %s", hello.ServerName)
		}
		return nil, errors.New("tls: no certificate configured")
	}
	if hello != nil && strings.TrimSpace(hello.ServerName) != "" {
		if !m.coversName(hello.ServerName) {
			return nil, fmt.Errorf("tls: no certificate for name %q", hello.ServerName)
		}
	}
	return cert, nil
}

// configForClient lets a replica notice a renewal between handshakes.
func (m *Manager) configForClient(*tls.ClientHelloInfo) (*tls.Config, error) {
	m.maybeReload()
	return nil, nil
}

// coversName reports whether the configured domains cover a server name.
func (m *Manager) coversName(name string) bool {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	for _, domain := range m.cfg.Domains {
		if domain == name {
			return true
		}
		if strings.HasPrefix(domain, "*.") {
			suffix := domain[1:] // ".example.com"
			if strings.HasSuffix(name, suffix) && strings.Count(name, ".") == strings.Count(strings.TrimPrefix(domain, "*."), ".")+1 {
				return true
			}
		}
	}
	return false
}

// EnsureCertificate loads the stored certificate or obtains the first one. A
// failure is fatal for TLS mode: there is no plaintext fallback.
func (m *Manager) EnsureCertificate(ctx context.Context) error {
	switch m.cfg.Mode {
	case ModeOff:
		return nil
	case ModeFiles:
		return m.loadFiles()
	}
	record, err := m.store.LoadCertificate(m.cfg.Domains)
	if err != nil {
		return err
	}
	if record != nil {
		if err := m.install(record, "store"); err != nil {
			return err
		}
		if !m.due(m.opt.now()) {
			return nil
		}
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, m.cfg.StartupTimeout)
	defer cancel()
	if err := m.issue(timeoutCtx); err != nil {
		return err
	}
	return nil
}

// Run renews the certificate until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) {
	if m.cfg.Mode != ModeACME {
		return
	}
	interval := m.opt.renewTicker
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.tick(ctx)
		}
	}
}

func (m *Manager) tick(ctx context.Context) {
	m.maybeReload()
	now := m.opt.now()
	if now.Before(m.nextAttempt) {
		return
	}
	if !m.due(now) {
		return
	}
	if err := m.issue(ctx); err != nil {
		if errors.Is(err, errIssuerBusy) {
			// Normal contention with the issuing replica: not a failure.
			return
		}
		m.attempts++
		m.nextAttempt = now.Add(retryDelay(m.attempts, m.cfg.Renewal, m.opt.roll))
		m.metrics.add(metricRenewalFailures, mergeLabels(m.metrics.baseLabels(), [2]string{"reason", classifyError(err)}), 1)
		m.mu.Lock()
		m.lastError = m.secrets.redact(err.Error())
		m.mu.Unlock()
		m.opt.logger.Printf("certificate renewal for %s failed: %s (retry in %s)", strings.Join(m.cfg.Domains, ","), m.secrets.redact(err.Error()), time.Until(m.nextAttempt).Round(time.Second))
		return
	}
	m.attempts = 0
	m.nextAttempt = time.Time{}
}

// issue runs one ACME order under the issuer lock and installs the result.
func (m *Manager) issue(ctx context.Context) error {
	if m.provider == nil {
		return errors.New("tls: no DNS provider configured")
	}
	if m.lockBroken {
		return ErrLockUnsupported
	}
	release, err := m.store.TryLock()
	switch {
	case errors.Is(err, ErrLockBusy):
		// Another replica is issuing; serve the cached certificate and try
		// again on the next tick.
		return fmt.Errorf("%w: %v", errIssuerBusy, err)
	case errors.Is(err, ErrLockUnsupported):
		m.lockBroken = true
		m.metrics.add(metricIssuerLockFailures, m.metrics.baseLabels(), 1)
		m.opt.logger.Printf("cache_dir %s cannot provide a POSIX lock; this replica will not issue certificates", m.store.Dir())
		return err
	case err != nil:
		return err
	}
	defer release()

	started := m.opt.now()
	issued, err := m.certifier.Issue(ctx, m.cfg.Domains)
	if err != nil {
		m.metrics.add(metricACMEOrders, mergeLabels(m.metrics.baseLabels(), [2]string{"result", "error"}), 1)
		return err
	}
	m.metrics.add(metricACMEOrders, mergeLabels(m.metrics.baseLabels(), [2]string{"result", "success"}), 1)
	record := &CertificateRecord{
		Domains:        append([]string(nil), m.cfg.Domains...),
		CertificatePEM: issued.CertificatePEM,
		PrivateKeyPEM:  issued.PrivateKeyPEM,
		Issuer:         m.cfg.DirectoryURL,
		OrderURL:       issued.OrderURL,
		NotBefore:      issued.NotBefore,
		NotAfter:       issued.NotAfter,
		IssuedAt:       started.UTC(),
		KeyType:        m.cfg.KeyType,
	}
	if err := m.store.SaveCertificate(*record); err != nil {
		return err
	}
	if err := m.install(record, "issued"); err != nil {
		return err
	}
	m.opt.logger.Printf("certificate for %s issued, valid until %s", strings.Join(m.cfg.Domains, ","), record.NotAfter.UTC().Format(time.RFC3339))
	if m.cfg.DNS.Record.Manage {
		m.reconcileRecord(ctx)
	}
	return nil
}

// errIssuerBusy marks normal lock contention, which is not a failure.
var errIssuerBusy = errors.New("tls: another replica is issuing")

// reconcileRecord keeps the optional A/AAAA/CNAME record in sync with the
// configured value.
func (m *Manager) reconcileRecord(ctx context.Context) {
	zone, err := m.resolveZone(ctx)
	if err != nil {
		m.opt.logger.Printf("managed DNS record: %s", m.secrets.redact(err.Error()))
		return
	}
	want := dns.Record{
		Type:  m.cfg.DNS.Record.Type,
		Name:  m.cfg.Domains[0],
		Value: m.cfg.DNS.Record.Value,
		TTL:   m.cfg.DNS.Record.TTL,
	}
	result, err := dns.Reconcile(ctx, m.provider, zone, want, m.cfg.DNS.Record.Owner, dns.Options{
		Logger:      m.opt.logger,
		Instruments: dnsInstruments{m},
		Redact:      m.secrets.redact,
	})
	if err != nil {
		m.opt.logger.Printf("managed DNS record: %s", m.secrets.redact(err.Error()))
		return
	}
	if result.Conflicts > 0 {
		m.opt.logger.Printf("managed DNS record %s %s conflicts with an existing record; left untouched", want.Type, want.Name)
	}
}

// resolveZone picks the DNS zone for the configured domains, asking the
// provider only when no zone is configured.
func (m *Manager) resolveZone(ctx context.Context) (string, error) {
	m.mu.RLock()
	zone := m.zone
	m.mu.RUnlock()
	if zone != "" {
		return zone, nil
	}
	if m.cfg.DNS.Zone != "" {
		m.mu.Lock()
		m.zone = m.cfg.DNS.Zone
		m.mu.Unlock()
		return m.cfg.DNS.Zone, nil
	}
	zones, err := m.provider.Zones(ctx)
	if err != nil {
		return "", err
	}
	resolved, err := dns.MatchZone(m.cfg.Domains, "", zones)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	m.zone = resolved
	m.mu.Unlock()
	return resolved, nil
}

// solverFor resolves the zone and returns a challenge solver.
func (m *Manager) solverFor(ctx context.Context) (challengeSolver, error) {
	zone, err := m.resolveZone(ctx)
	if err != nil {
		return nil, err
	}
	return dns.NewSolver(m.provider, zone, dns.Options{
		TTL:                    m.cfg.DNS.TTL,
		PropagationTimeout:     m.cfg.DNS.PropagationTimeout,
		PropagationNameservers: m.cfg.DNS.PropagationNameservers,
		Logger:                 m.opt.logger,
		Instruments:            dnsInstruments{m},
		Redact:                 m.secrets.redact,
		Now:                    m.opt.now,
	}), nil
}

// lazySolver defers zone resolution until the first challenge, so building a
// Manager stays free of network I/O.
type lazySolver struct{ manager *Manager }

func (l *lazySolver) Present(ctx context.Context, domain, value string) error {
	solver, err := l.manager.solverFor(ctx)
	if err != nil {
		return err
	}
	return solver.Present(ctx, domain, value)
}

func (l *lazySolver) Cleanup(ctx context.Context, domain, value string) error {
	solver, err := l.manager.solverFor(ctx)
	if err != nil {
		return err
	}
	return solver.Cleanup(ctx, domain, value)
}

// install parses a record, makes it the served certificate and schedules its
// renewal.
func (m *Manager) install(record *CertificateRecord, source string) error {
	cert, err := tls.X509KeyPair(record.CertificatePEM, record.PrivateKeyPEM)
	if err != nil {
		return fmt.Errorf("tls: load certificate for %s: %w", strings.Join(record.Domains, ","), err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return fmt.Errorf("tls: parse certificate: %w", err)
	}
	cert.Leaf = leaf
	if err := m.checkDomains(leaf); err != nil {
		return err
	}
	now := m.opt.now()
	if !now.Before(leaf.NotAfter) {
		return fmt.Errorf("tls: stored certificate for %s expired at %s", strings.Join(record.Domains, ","), leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	if leaf.NotBefore.After(now.Add(5 * time.Minute)) {
		return fmt.Errorf("tls: stored certificate for %s is not valid before %s", strings.Join(record.Domains, ","), leaf.NotBefore.UTC().Format(time.RFC3339))
	}
	deadline := renewalDeadline(leaf.NotBefore, leaf.NotAfter, m.cfg.Renewal, m.opt.roll)
	if m.cfg.Renewal.ariEnabled() {
		deadline = m.applyARI(deadline, leaf)
	}
	m.mu.Lock()
	m.cert = &cert
	m.record = record
	m.deadline = deadline
	m.source = source
	m.lastError = ""
	m.mu.Unlock()
	m.metrics.observeCertificate(record, deadline, now)
	return nil
}

// applyARI replaces the ratio deadline with a random point of the CA's
// suggested renewal window. Every failure keeps the ratio deadline.
func (m *Manager) applyARI(ratioDeadline time.Time, leaf *x509.Certificate) time.Time {
	now := m.opt.now()
	m.mu.RLock()
	retryAt := m.ariRetryAt
	m.mu.RUnlock()
	if !retryAt.IsZero() && now.Before(retryAt) {
		return ratioDeadline
	}
	start, end, err := m.ari.window(context.Background(), normalizeDirectoryURL(m.cfg.DirectoryURL), leaf)
	if err != nil {
		m.metrics.add(metricARIFailures, m.metrics.baseLabels(), 1)
		m.mu.Lock()
		m.ariRetryAt = now.Add(time.Hour)
		m.mu.Unlock()
		if !errors.Is(err, errARIUnsupported) {
			m.opt.logger.Printf("renewal information unavailable, using the configured renewal ratio: %s", m.secrets.redact(err.Error()))
		}
		return ratioDeadline
	}
	span := int64(end.Sub(start))
	if span <= 0 {
		return ratioDeadline
	}
	return start.Add(time.Duration(m.opt.roll(span)))
}

// checkDomains verifies that the certificate covers every configured domain.
func (m *Manager) checkDomains(leaf *x509.Certificate) error {
	for _, domain := range m.cfg.Domains {
		if err := leaf.VerifyHostname(domain); err != nil {
			return fmt.Errorf("tls: certificate does not cover %q: %w", domain, err)
		}
	}
	return nil
}

// due reports whether the certificate should be renewed now.
func (m *Manager) due(now time.Time) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.record == nil || m.cert == nil {
		return true
	}
	if m.deadline.IsZero() {
		return false
	}
	return renewalDueOrExpired(m.deadline, now)
}

// maybeReload picks up a certificate another replica wrote.
func (m *Manager) maybeReload() {
	if m.store == nil || m.cfg.Mode != ModeACME {
		return
	}
	modified := m.store.CertificatesModified()
	m.mu.RLock()
	loaded := m.loadedMod
	currentNotAfter := time.Time{}
	if m.record != nil {
		currentNotAfter = m.record.NotAfter
	}
	m.mu.RUnlock()
	if modified.IsZero() || !modified.After(loaded) {
		return
	}
	record, err := m.store.LoadCertificate(m.cfg.Domains)
	if err != nil {
		m.opt.logger.Printf("reload certificate: %s", m.secrets.redact(err.Error()))
		return
	}
	if record == nil || !record.NotAfter.After(currentNotAfter) {
		m.mu.Lock()
		m.loadedMod = modified
		m.mu.Unlock()
		return
	}
	if err := m.install(record, "store"); err != nil {
		m.opt.logger.Printf("reload certificate: %s", m.secrets.redact(err.Error()))
		return
	}
	m.mu.Lock()
	m.loadedMod = modified
	m.mu.Unlock()
	m.opt.logger.Printf("picked up renewed certificate for %s from the shared store", strings.Join(m.cfg.Domains, ","))
}

// loadFiles loads a static certificate and key (mode files).
func (m *Manager) loadFiles() error {
	certPEM, err := os.ReadFile(m.cfg.CertificateFile)
	if err != nil {
		return fmt.Errorf("tls: read certificate_file: %w", err)
	}
	keyPEM, err := os.ReadFile(m.cfg.KeyFile)
	if err != nil {
		return fmt.Errorf("tls: read key_file: %w", err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("tls: load certificate_file/key_file: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return fmt.Errorf("tls: parse certificate_file: %w", err)
	}
	cert.Leaf = leaf
	if err := m.checkDomains(leaf); err != nil {
		return err
	}
	deadline := renewalDeadline(leaf.NotBefore, leaf.NotAfter, m.cfg.Renewal, m.opt.roll)
	m.mu.Lock()
	m.cert = &cert
	m.record = &CertificateRecord{
		Domains:        append([]string(nil), m.cfg.Domains...),
		CertificatePEM: certPEM,
		PrivateKeyPEM:  keyPEM,
		Issuer:         leaf.Issuer.CommonName,
		NotBefore:      leaf.NotBefore,
		NotAfter:       leaf.NotAfter,
		IssuedAt:       leaf.NotBefore,
		KeyType:        m.cfg.KeyType,
	}
	m.deadline = deadline
	m.source = "files"
	m.mu.Unlock()
	m.metrics.observeCertificate(m.record, deadline, m.opt.now())
	return nil
}

// Status returns the served certificate metadata.
func (m *Manager) Status() []CertificateStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.record == nil {
		return nil
	}
	return []CertificateStatus{{
		Listener:   m.cfg.Listener,
		Domains:    append([]string(nil), m.record.Domains...),
		Issuer:     m.record.Issuer,
		NotBefore:  m.record.NotBefore,
		NotAfter:   m.record.NotAfter,
		RenewalDue: m.deadline,
		Source:     m.source,
		Valid:      m.opt.now().Before(m.record.NotAfter) && !m.opt.now().Before(m.record.NotBefore),
		LastError:  m.lastError,
	}}
}

// Prometheus returns the TLS metric series. In off mode it is empty, so a v4.7
// configuration keeps byte-identical /metrics output.
func (m *Manager) Prometheus() string {
	if m == nil || m.cfg.Mode == ModeOff {
		return ""
	}
	return m.metrics.prometheus()
}

// classifyError labels a renewal failure with a bounded reason value.
func classifyError(err error) string {
	switch {
	case errors.Is(err, errIssuerBusy):
		return "busy"
	case errors.Is(err, ErrLockUnsupported):
		return "lock"
	case errors.Is(err, ErrLockBusy):
		return "lock"
	}
	text := err.Error()
	switch {
	case strings.HasPrefix(text, "dns:"):
		return "dns"
	case strings.HasPrefix(text, "acme:"):
		return "acme"
	case strings.HasPrefix(text, "tls:"):
		return "tls"
	}
	return "other"
}

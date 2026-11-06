// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

// Package tlsacme issues, stores and renews Let's Encrypt (or any RFC 8555 CA)
// certificates for the router's serving and admin listeners with DNS-01
// challenges, and optionally reconciles the A/AAAA/CNAME record that points a
// hostname at the router.
//
// The package is deliberately independent of internal/router: the router maps
// its server.tls / admin.tls YAML blocks onto Config, and consumes Manager's
// TLSConfig, Run, Status and Prometheus methods. The only TLS state this
// package touches is certificate serving; client authentication (mTLS) is
// configured by the caller on a clone of the returned tls.Config.
package tlsacme

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

// Modes for a listener's tls block.
const (
	ModeOff   = "off"
	ModeFiles = "files"
	ModeACME  = "acme"
)

// Supported DNS providers.
const (
	ProviderCloudflare   = "cloudflare"
	ProviderDigitalOcean = "digitalocean"
	ProviderRoute53      = "route53"
)

// Listener labels used in metrics and status output.
const (
	ListenerServing = "serving"
	ListenerAdmin   = "admin"
)

// DefaultDirectoryURL is the Let's Encrypt production ACME directory.
const DefaultDirectoryURL = "https://acme-v02.api.letsencrypt.org/directory"

// DefaultCacheDir is where certificates and the ACME account key are stored.
const DefaultCacheDir = "/var/lib/metrum-ai-router/tls"

// Key types accepted by Config.KeyType.
const (
	KeyTypeECDSAP256 = "ecdsa_p256"
	KeyTypeRSA2048   = "rsa_2048"
)

// Config is the resolved TLS configuration of one listener. internal/router
// builds it from YAML and validates it again with Validate.
type Config struct {
	// Listener labels the configuration for metrics and status: serving or admin.
	Listener string
	// ErrorPrefix overrides the prefix of validation errors, so the router can
	// report a configuration key (server.tls) rather than a listener label.
	ErrorPrefix string

	Mode string
	// CertificateFile and KeyFile are used by ModeFiles only.
	CertificateFile string
	KeyFile         string

	// Domains are the DNS names in the single SAN order issued by the ACME CA.
	Domains []string
	// Email is the ACME account contact.
	Email string
	// DirectoryURL is the CA directory; https unless AllowInsecureDirectory.
	DirectoryURL string
	// AcceptTOS records explicit consent to the CA's terms of service. ACME
	// mode refuses to start without it.
	AcceptTOS bool
	// AllowInsecureDirectory permits an http:// directory URL. It exists for
	// local test CAs and must never be set in production.
	AllowInsecureDirectory bool

	// CacheDir holds certificates, the account key and the issuer lock.
	CacheDir string
	// KeyType is ecdsa_p256 (default) or rsa_2048.
	KeyType string
	// MinVersion is the minimum TLS version: 1.2 (default) or 1.3.
	MinVersion string
	// StartupTimeout bounds the first issuance when no certificate is cached.
	StartupTimeout time.Duration
	// ReloadInterval is how often a replica re-reads the shared certificate store.
	ReloadInterval time.Duration

	Renewal RenewalConfig
	EAB     EABConfig
	DNS     DNSConfig
}

// RenewalConfig controls when a certificate is renewed.
type RenewalConfig struct {
	// WindowRatio is the fraction of the certificate lifetime after which the
	// certificate is renewed. The default 0.67 renews at two thirds of life.
	WindowRatio float64
	// Jitter is added to (or subtracted from) every renewal deadline so that a
	// fleet of routers does not renew at the same instant. It is capped at a
	// tenth of the certificate lifetime.
	Jitter time.Duration
	// RetryMax is the ceiling of the renewal retry backoff.
	RetryMax time.Duration
	// UseARI lets the CA's RFC 9773 renewal window override the ratio when the
	// directory advertises one. Failures fall back to WindowRatio. It is on
	// unless explicitly set to false.
	UseARI *bool
}

// EABConfig holds optional ACME external account binding material, required by
// some CAs and never by Let's Encrypt.
type EABConfig struct {
	KID     string
	HMACKey SecretRef
}

// DNSConfig selects and configures the DNS provider used for DNS-01.
type DNSConfig struct {
	Provider string
	// Zone is the DNS zone that holds _acme-challenge records. When empty the
	// longest matching zone of the account is used.
	Zone string
	// TTL is the TTL of challenge TXT records.
	TTL int
	// PropagationTimeout bounds the wait for the TXT record to be visible.
	PropagationTimeout time.Duration
	// PropagationNameservers, when set, are polled instead of the system resolver.
	PropagationNameservers []string

	Credentials Credentials
	Record      RecordConfig

	// domains is set by Config.validate so the zone can be checked against the
	// identifiers without threading them through every call.
	domains []string
}

// Credentials are secret references for a DNS provider. Every field takes
// exactly one of an environment variable or a file; literal values are
// rejected so a credential never lives in a YAML file.
type Credentials struct {
	// Cloudflare.
	APIToken SecretRef
	APIKey   SecretRef
	Email    SecretRef
	// DigitalOcean.
	Token SecretRef
	// Route 53.
	AccessKeyID           SecretRef
	SecretAccessKey       SecretRef
	SessionToken          SecretRef
	UseAmbientCredentials bool
}

// RecordConfig enables optional management of the A/AAAA/CNAME record that
// points the configured hostname at this router (ACP-N08c).
type RecordConfig struct {
	Manage bool
	// Type is A, AAAA or CNAME.
	Type string
	// Value is an address for A/AAAA or the target name for CNAME.
	Value string
	// TTL of the managed record.
	TTL int
	// Owner identifies this deployment in the ownership TXT marker. It
	// defaults to the host name.
	Owner string
}

// cloneNormalized returns a copy with defaults applied. It never mutates the
// caller's value, so repeated validation is deterministic.
func (c Config) cloneNormalized() Config {
	out := c
	out.Listener = strings.TrimSpace(out.Listener)
	if out.Listener == "" {
		out.Listener = ListenerServing
	}
	out.Mode = strings.ToLower(strings.TrimSpace(out.Mode))
	if out.Mode == "" {
		out.Mode = ModeOff
	}
	out.DirectoryURL = strings.TrimSpace(out.DirectoryURL)
	if out.DirectoryURL == "" {
		out.DirectoryURL = DefaultDirectoryURL
	}
	out.CacheDir = strings.TrimSpace(out.CacheDir)
	if out.CacheDir == "" {
		out.CacheDir = DefaultCacheDir
	}
	out.KeyType = strings.ToLower(strings.TrimSpace(out.KeyType))
	if out.KeyType == "" {
		out.KeyType = KeyTypeECDSAP256
	}
	out.MinVersion = strings.TrimSpace(out.MinVersion)
	if out.MinVersion == "" {
		out.MinVersion = "1.2"
	}
	if out.StartupTimeout <= 0 {
		out.StartupTimeout = 120 * time.Second
	}
	if out.ReloadInterval <= 0 {
		out.ReloadInterval = 30 * time.Second
	}
	if out.Renewal.WindowRatio == 0 {
		out.Renewal.WindowRatio = defaultRenewalWindowRatio
	}
	if out.Renewal.Jitter == 0 {
		out.Renewal.Jitter = 15 * time.Minute
	}
	if out.Renewal.RetryMax <= 0 {
		out.Renewal.RetryMax = time.Hour
	}
	out.DNS.Provider = strings.ToLower(strings.TrimSpace(out.DNS.Provider))
	out.DNS.Zone = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(out.DNS.Zone)), ".")
	if out.DNS.TTL <= 0 {
		out.DNS.TTL = 60
	}
	if out.DNS.PropagationTimeout <= 0 {
		out.DNS.PropagationTimeout = 120 * time.Second
	}
	out.DNS.Record.Type = strings.ToUpper(strings.TrimSpace(out.DNS.Record.Type))
	if out.DNS.Record.TTL <= 0 {
		out.DNS.Record.TTL = 300
	}
	out.DNS.Record.Value = strings.TrimSuffix(strings.TrimSpace(out.DNS.Record.Value), ".")
	out.Domains = normalizeDomains(out.Domains)
	return out
}

// Normalize returns the configuration with defaults applied.
func (c Config) Normalize() Config { return c.cloneNormalized() }

func normalizeDomains(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, d := range in {
		d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// Validate checks the configuration and returns an error naming the field but
// never a credential value. It assumes Normalize was applied.
func (c Config) Validate() error {
	switch c.Mode {
	case ModeOff:
		return nil
	case ModeFiles:
		if strings.TrimSpace(c.CertificateFile) == "" || strings.TrimSpace(c.KeyFile) == "" {
			return fmt.Errorf("%s: certificate_file and key_file are required when mode is files", c.prefix())
		}
		return nil
	case ModeACME:
	default:
		return fmt.Errorf("%s: mode must be one of off, files, acme", c.prefix())
	}

	if len(c.Domains) == 0 {
		return fmt.Errorf("%s: domains is required when mode is acme", c.prefix())
	}
	for _, domain := range c.Domains {
		if !validateDomain(domain) {
			return fmt.Errorf("%s: domain %q is not a valid DNS name", c.prefix(), domain)
		}
	}
	if !c.AcceptTOS {
		return fmt.Errorf("%s: accept_tos must be true when mode is acme", c.prefix())
	}
	if !c.AllowInsecureDirectory && !strings.HasPrefix(strings.ToLower(c.DirectoryURL), "https://") {
		return fmt.Errorf("%s: directory_url must be https unless allow_insecure_directory is set", c.prefix())
	}
	if c.Email != "" && !strings.Contains(c.Email, "@") {
		return fmt.Errorf("%s: email %q is not a valid address", c.prefix(), c.Email)
	}
	switch c.KeyType {
	case KeyTypeECDSAP256, KeyTypeRSA2048:
	default:
		return fmt.Errorf("%s: key_type must be one of %s, %s", c.prefix(), KeyTypeECDSAP256, KeyTypeRSA2048)
	}
	switch c.MinVersion {
	case "1.2", "1.3":
	default:
		return fmt.Errorf("%s: min_version must be 1.2 or 1.3", c.prefix())
	}
	if c.Renewal.WindowRatio <= 0 || c.Renewal.WindowRatio >= 1 {
		return fmt.Errorf("%s: renewal.window_ratio must be between 0 and 1 exclusive", c.prefix())
	}
	if c.Renewal.Jitter < 0 {
		return fmt.Errorf("%s: renewal.jitter_seconds cannot be negative", c.prefix())
	}
	if c.Renewal.RetryMax <= 0 {
		return fmt.Errorf("%s: renewal.retry_max_seconds must be positive", c.prefix())
	}
	if strings.TrimSpace(c.CacheDir) == "" {
		return fmt.Errorf("%s: cache_dir is required when mode is acme", c.prefix())
	}
	if err := c.EAB.HMACKey.validate(c.prefix() + ".eab.hmac_key"); err != nil {
		return err
	}
	if (c.EAB.KID == "") != (!c.EAB.HMACKey.set()) {
		return fmt.Errorf("%s: eab.kid and eab.hmac_key_env/hmac_key_file must be set together", c.prefix())
	}
	withDomains := c.DNS
	withDomains.domains = c.Domains
	if err := withDomains.validate(c.prefix()); err != nil {
		return err
	}
	return nil
}

// ariEnabled reports whether RFC 9773 renewal information should be used.
func (r RenewalConfig) ariEnabled() bool { return r.UseARI == nil || *r.UseARI }

func (d DNSConfig) validate(prefix string) error {
	switch d.Provider {
	case ProviderCloudflare, ProviderDigitalOcean, ProviderRoute53:
	case "":
		return fmt.Errorf("%s.dns: provider is required when mode is acme", prefix)
	default:
		return fmt.Errorf("%s.dns: provider must be one of cloudflare, route53, digitalocean", prefix)
	}
	if d.TTL < 0 || d.TTL > 86400 {
		return fmt.Errorf("%s.dns: ttl_seconds must be between 0 and 86400", prefix)
	}
	if d.Zone != "" {
		if !validZoneName(d.Zone) {
			return fmt.Errorf("%s.dns: zone %q is not a valid DNS zone", prefix, d.Zone)
		}
		for _, domain := range d.domains {
			name := strings.TrimPrefix(strings.ToLower(domain), "*.")
			if name != d.Zone && !strings.HasSuffix(name, "."+d.Zone) {
				return fmt.Errorf("%s.dns: zone %q is not a suffix of domain %q", prefix, d.Zone, domain)
			}
		}
	}
	for _, ns := range d.PropagationNameservers {
		host := strings.TrimSuffix(strings.TrimSpace(ns), ".")
		if splitHost, _, err := net.SplitHostPort(host); err == nil {
			host = splitHost
		}
		if net.ParseIP(host) == nil && !validZoneName(host) {
			return fmt.Errorf("%s.dns: propagation_nameservers entry %q is not an address, host name or host:port", prefix, ns)
		}
	}
	if err := d.Record.validate(prefix + ".dns.record"); err != nil {
		return err
	}
	return d.Credentials.validate(prefix+".dns.credentials", d.Provider)
}

func (r RecordConfig) validate(prefix string) error {
	if !r.Manage {
		if r.Value != "" || r.Type != "" {
			return fmt.Errorf("%s: type and value require manage: true", prefix)
		}
		return nil
	}
	switch r.Type {
	case "A", "AAAA", "CNAME":
	case "":
		return fmt.Errorf("%s: type is required when manage is true", prefix)
	default:
		return fmt.Errorf("%s: type must be one of A, AAAA, CNAME", prefix)
	}
	if r.Value == "" {
		return fmt.Errorf("%s: value is required when manage is true", prefix)
	}
	if r.Type == "CNAME" {
		if !validZoneName(r.Value) {
			return fmt.Errorf("%s: value %q is not a valid CNAME target", prefix, r.Value)
		}
		return nil
	}
	ip := net.ParseIP(r.Value)
	if ip == nil {
		return fmt.Errorf("%s: value %q is not a valid IP address", prefix, r.Value)
	}
	if r.Type == "A" && ip.To4() == nil {
		return fmt.Errorf("%s: value %q is not an IPv4 address for type A", prefix, r.Value)
	}
	if r.Type == "AAAA" && ip.To4() != nil {
		return fmt.Errorf("%s: value %q is not an IPv6 address for type AAAA", prefix, r.Value)
	}
	return nil
}

// validate checks the secret references required by one provider.
func (c Credentials) validate(prefix, provider string) error {
	fields := []struct {
		name string
		ref  SecretRef
	}{
		{"api_token", c.APIToken},
		{"api_key", c.APIKey},
		{"email", c.Email},
		{"token", c.Token},
		{"access_key_id", c.AccessKeyID},
		{"secret_access_key", c.SecretAccessKey},
		{"session_token", c.SessionToken},
	}
	for _, field := range fields {
		if err := field.ref.validate(prefix + "." + field.name); err != nil {
			return err
		}
	}
	addressed := func(r SecretRef) bool { return r.set() }
	switch provider {
	case ProviderCloudflare:
		if addressed(c.APIToken) == addressed(c.APIKey) {
			return fmt.Errorf("%s: set exactly one of api_token_env/api_token_file or api_key_env/api_key_file", prefix)
		}
		if addressed(c.APIKey) && !addressed(c.Email) {
			return fmt.Errorf("%s: api_key requires email_env/email_file", prefix)
		}
	case ProviderDigitalOcean:
		if !addressed(c.Token) {
			return fmt.Errorf("%s: token_env or token_file is required for digitalocean", prefix)
		}
	case ProviderRoute53:
		anyStatic := addressed(c.AccessKeyID) || addressed(c.SecretAccessKey) || addressed(c.SessionToken)
		if c.UseAmbientCredentials && anyStatic {
			return fmt.Errorf("%s: use_ambient_credentials cannot be combined with static access keys", prefix)
		}
		if !c.UseAmbientCredentials && (!addressed(c.AccessKeyID) || !addressed(c.SecretAccessKey)) {
			return fmt.Errorf("%s: access_key_id and secret_access_key are required for route53 unless use_ambient_credentials is set", prefix)
		}
	}
	return nil
}

func (c Config) prefix() string {
	if prefix := strings.TrimSpace(c.ErrorPrefix); prefix != "" {
		return prefix
	}
	listener := c.Listener
	if listener == "" {
		listener = ListenerServing
	}
	return listener + ".tls"
}

// ServeDomains returns the domains this configuration issues for, sorted, for
// stable metric labels and store keys.
func (c Config) ServeDomains() []string {
	out := append([]string(nil), c.Domains...)
	sort.Strings(out)
	return out
}

// validateDomain accepts DNS names and a single leading wildcard label.
func validateDomain(domain string) bool {
	if domain == "" || len(domain) > 253 {
		return false
	}
	name := domain
	if strings.HasPrefix(name, "*.") {
		name = name[2:]
	}
	if strings.Contains(name, "*") || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") {
		return false
	}
	return validZoneName(name)
}

func validZoneName(name string) bool {
	if name == "" || len(name) > 253 || net.ParseIP(name) != nil {
		return false
	}
	if strings.ContainsAny(name, " /:@*") {
		return false
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
				return false
			}
		}
	}
	return true
}

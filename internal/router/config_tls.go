// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Automated TLS for the router's listeners (#810). The YAML blocks map onto
// internal/tlsacme.Config; everything about ACME, certificates, DNS providers
// and renewal lives there. This file only parses, validates and converts, so
// configuration errors are reported by the existing Validate path (and the
// existing secret redaction covers the referenced credentials).
//
// The listener lane (#809) owns the tls block vocabulary: mode off|router|proxy,
// cert_file/key_file and, for the admin listener, the client-authentication
// keys. This lane adds the certificate source inside router mode:
// certificate_source files|acme plus the acme sub-block. ListenerTLSConfig is
// the shared type; admin.tls embeds it with yaml:",inline" and adds proxy mode
// and its client keys around it.

import (
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	"github.com/metrum-ai/router/internal/tlsacme"
)

// ListenerTLSConfig is the certificate part of a listener's tls block.
//
// server.tls uses it directly. admin.tls (#809) embeds it inline and adds
// proxy mode, client_ca_file, client_auth and trusted_proxy_cidrs, then calls
// ValidateListenerTLS and NewListenerTLSManager with ListenerAdmin and
// "admin.tls".
type ListenerTLSConfig struct {
	// Mode is off (default) or router. The admin listener also accepts proxy,
	// which means the certificate is terminated elsewhere and this router holds
	// none; NewListenerTLSManager then returns no manager.
	Mode string `yaml:"mode,omitempty"`
	// CertificateSource is files (default) or acme inside router mode.
	CertificateSource string `yaml:"certificate_source,omitempty"`
	// CertFile and KeyFile are the static certificate for source files.
	CertFile string `yaml:"cert_file,omitempty"`
	KeyFile  string `yaml:"key_file,omitempty"`
	// MinVersion is the TLS floor: "1.2" (default) or "1.3".
	MinVersion string `yaml:"min_version,omitempty"`
	// ACME configures certificate_source acme.
	ACME TLSACMEServerConfig `yaml:"acme,omitempty"`
}

// TLSACMEServerConfig is the acme sub-block shared by server.tls and admin.tls.
type TLSACMEServerConfig struct {
	// Domains are the DNS names of the single SAN order.
	Domains []string `yaml:"domains,omitempty"`
	// Email is the ACME account contact.
	Email string `yaml:"email,omitempty"`
	// DirectoryURL is the ACME directory; https unless AllowInsecureDirectory.
	DirectoryURL string `yaml:"directory_url,omitempty"`
	// AcceptTOS records explicit consent to the CA terms of service.
	AcceptTOS bool `yaml:"accept_tos,omitempty"`
	// AllowInsecureDirectory permits an http:// ACME directory (test CAs only).
	AllowInsecureDirectory bool `yaml:"allow_insecure_directory,omitempty"`
	// CacheDir holds certificate and account material, mode 0700.
	CacheDir string `yaml:"cache_dir,omitempty"`
	// KeyType is ecdsa_p256 (default) or rsa_2048.
	KeyType string `yaml:"key_type,omitempty"`
	// StartupTimeoutSeconds bounds the first issuance when nothing is cached.
	StartupTimeoutSeconds int `yaml:"startup_timeout_seconds,omitempty"`
	// ReloadIntervalSeconds is how often a replica re-reads a shared store.
	ReloadIntervalSeconds int `yaml:"reload_interval_seconds,omitempty"`

	Renewal TLSRenewalConfig `yaml:"renewal,omitempty"`
	EAB     TLSEABConfig     `yaml:"eab,omitempty"`
	DNS     TLSDNSConfig     `yaml:"dns,omitempty"`
}

// TLSRenewalConfig controls when certificates are renewed.
type TLSRenewalConfig struct {
	// WindowRatio is the fraction of the certificate lifetime after which
	// renewal happens. The default two thirds leaves a third of the lifetime
	// to notice and repair a failure.
	WindowRatio float64 `yaml:"window_ratio,omitempty"`
	// JitterSeconds displaces each deadline by up to this much, capped at a
	// tenth of the certificate lifetime.
	JitterSeconds int `yaml:"jitter_seconds,omitempty"`
	// RetryMaxSeconds is the ceiling of the renewal retry backoff.
	RetryMaxSeconds int `yaml:"retry_max_seconds,omitempty"`
	// UseARI lets the CA's RFC 9773 renewal window override the ratio. It is
	// on unless explicitly set to false.
	UseARI *bool `yaml:"use_ari,omitempty"`
}

// TLSEABConfig is optional ACME external account binding material.
type TLSEABConfig struct {
	KID         string `yaml:"kid,omitempty"`
	HMACKeyEnv  string `yaml:"hmac_key_env,omitempty"`
	HMACKeyFile string `yaml:"hmac_key_file,omitempty"`
}

// TLSDNSConfig selects and configures the DNS-01 provider.
type TLSDNSConfig struct {
	Provider               string                  `yaml:"provider,omitempty"`
	Zone                   string                  `yaml:"zone,omitempty"`
	TTLSeconds             int                     `yaml:"ttl_seconds,omitempty"`
	PropagationTimeoutSecs int                     `yaml:"propagation_timeout_seconds,omitempty"`
	PropagationNameservers []string                `yaml:"propagation_nameservers,omitempty"`
	Credentials            TLSDNSCredentialsConfig `yaml:"credentials,omitempty"`
	Record                 TLSDNSRecordConfig      `yaml:"record,omitempty"`
}

// TLSDNSCredentialsConfig holds secret references only. Every credential is
// either <field>_env or <field>_file; a literal value is rejected so a
// credential never lives in a configuration file or its backups.
type TLSDNSCredentialsConfig struct {
	APITokenEnv  string `yaml:"api_token_env,omitempty"`
	APITokenFile string `yaml:"api_token_file,omitempty"`
	APIKeyEnv    string `yaml:"api_key_env,omitempty"`
	APIKeyFile   string `yaml:"api_key_file,omitempty"`
	EmailEnv     string `yaml:"email_env,omitempty"`
	EmailFile    string `yaml:"email_file,omitempty"`
	TokenEnv     string `yaml:"token_env,omitempty"`
	TokenFile    string `yaml:"token_file,omitempty"`

	AccessKeyIDEnv      string `yaml:"access_key_id_env,omitempty"`
	AccessKeyIDFile     string `yaml:"access_key_id_file,omitempty"`
	SecretAccessKeyEnv  string `yaml:"secret_access_key_env,omitempty"`
	SecretAccessKeyFile string `yaml:"secret_access_key_file,omitempty"`
	SessionTokenEnv     string `yaml:"session_token_env,omitempty"`
	SessionTokenFile    string `yaml:"session_token_file,omitempty"`
	// UseAmbientCredentials uses the AWS default chain (IRSA, IMDS, profile).
	UseAmbientCredentials bool `yaml:"use_ambient_credentials,omitempty"`
}

// TLSDNSRecordConfig enables optional management of the A/AAAA/CNAME record
// that points the configured hostname at this router.
type TLSDNSRecordConfig struct {
	Manage bool `yaml:"manage,omitempty"`
	// Type is A, AAAA or CNAME.
	Type string `yaml:"type,omitempty"`
	// Value is an address or a CNAME target.
	Value string `yaml:"value,omitempty"`
	// TTLSeconds of the managed record.
	TTLSeconds int `yaml:"ttl_seconds,omitempty"`
	// Owner identifies this deployment in the ownership TXT marker.
	Owner string `yaml:"owner,omitempty"`
}

// tlsListenerModes are the modes a listener's TLS config may use.
func tlsModeSupported(mode string, allowProxy bool) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", tlsacme.ModeOff, tlsacme.ModeRouter:
		return true
	case "proxy":
		return allowProxy
	}
	return false
}

// Enabled reports whether this block asks the listener to terminate TLS itself.
func (c ListenerTLSConfig) Enabled() bool {
	mode := strings.ToLower(strings.TrimSpace(c.Mode))
	return mode == tlsacme.ModeRouter
}

// ValidateListenerTLS validates one listener's TLS block. listener and
// errorPrefix name it in errors ("serving"/"server.tls", "admin"/"admin.tls").
func ValidateListenerTLS(cfg ListenerTLSConfig, listener, errorPrefix string) error {
	return validateListenerTLS(cfg, listener, errorPrefix, false)
}

// ValidateAdminListenerTLS is ValidateListenerTLS for the admin listener, which
// also accepts proxy mode (the listener lane owns that mode's own rules).
func ValidateAdminListenerTLS(cfg ListenerTLSConfig, errorPrefix string) error {
	return validateListenerTLS(cfg, tlsacme.ListenerAdmin, errorPrefix, true)
}

func validateListenerTLS(cfg ListenerTLSConfig, listener, errorPrefix string, allowProxy bool) error {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if !tlsModeSupported(mode, allowProxy) {
		allowed := "off or router"
		if allowProxy {
			allowed = "off, router or proxy"
		}
		return fmt.Errorf("%s: mode must be %s", errorPrefix, allowed)
	}
	if mode == "proxy" {
		// Proxy mode has no router-held certificate; the listener lane validates
		// trusted_proxy_cidrs and the rest of its block.
		return nil
	}
	if mode != tlsacme.ModeRouter {
		if strings.TrimSpace(cfg.CertificateSource) != "" || strings.TrimSpace(cfg.CertFile) != "" ||
			strings.TrimSpace(cfg.KeyFile) != "" || cfg.ACME.configured() {
			return fmt.Errorf("%s: certificate_source and cert_file require mode router", errorPrefix)
		}
		return nil
	}
	converted := cfg.managerConfig(listener, errorPrefix).Normalize()
	return converted.Validate()
}

// NewListenerTLSManager builds a listener's TLS manager, or nil when the
// listener does not terminate TLS itself (mode off or proxy).
func NewListenerTLSManager(cfg ListenerTLSConfig, listener, errorPrefix string) (*tlsacme.Manager, error) {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode != tlsacme.ModeRouter {
		return nil, nil
	}
	manager, err := tlsacme.New(cfg.managerConfig(listener, errorPrefix))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errorPrefix, err)
	}
	return manager, nil
}

// managerConfig converts the YAML block into the tlsacme configuration.
func (c ListenerTLSConfig) managerConfig(listener, errorPrefix string) tlsacme.Config {
	acme := c.ACME
	return tlsacme.Config{
		Listener:               listener,
		ErrorPrefix:            errorPrefix,
		Mode:                   c.Mode,
		CertificateSource:      c.CertificateSource,
		CertFile:               c.CertFile,
		KeyFile:                c.KeyFile,
		Domains:                acme.Domains,
		Email:                  acme.Email,
		DirectoryURL:           acme.DirectoryURL,
		AcceptTOS:              acme.AcceptTOS,
		AllowInsecureDirectory: acme.AllowInsecureDirectory,
		CacheDir:               acme.CacheDir,
		KeyType:                acme.KeyType,
		MinVersion:             c.MinVersion,
		StartupTimeout:         seconds(acme.StartupTimeoutSeconds),
		ReloadInterval:         seconds(acme.ReloadIntervalSeconds),
		Renewal: tlsacme.RenewalConfig{
			WindowRatio: acme.Renewal.WindowRatio,
			Jitter:      seconds(acme.Renewal.JitterSeconds),
			RetryMax:    seconds(acme.Renewal.RetryMaxSeconds),
			UseARI:      acme.Renewal.UseARI,
		},
		EAB: tlsacme.EABConfig{
			KID:     acme.EAB.KID,
			HMACKey: tlsacme.SecretRef{Env: acme.EAB.HMACKeyEnv, File: acme.EAB.HMACKeyFile},
		},
		DNS: tlsacme.DNSConfig{
			Provider:               acme.DNS.Provider,
			Zone:                   acme.DNS.Zone,
			TTL:                    acme.DNS.TTLSeconds,
			PropagationTimeout:     seconds(acme.DNS.PropagationTimeoutSecs),
			PropagationNameservers: acme.DNS.PropagationNameservers,
			Credentials: tlsacme.Credentials{
				APIToken:              ref(acme.DNS.Credentials.APITokenEnv, acme.DNS.Credentials.APITokenFile),
				APIKey:                ref(acme.DNS.Credentials.APIKeyEnv, acme.DNS.Credentials.APIKeyFile),
				Email:                 ref(acme.DNS.Credentials.EmailEnv, acme.DNS.Credentials.EmailFile),
				Token:                 ref(acme.DNS.Credentials.TokenEnv, acme.DNS.Credentials.TokenFile),
				AccessKeyID:           ref(acme.DNS.Credentials.AccessKeyIDEnv, acme.DNS.Credentials.AccessKeyIDFile),
				SecretAccessKey:       ref(acme.DNS.Credentials.SecretAccessKeyEnv, acme.DNS.Credentials.SecretAccessKeyFile),
				SessionToken:          ref(acme.DNS.Credentials.SessionTokenEnv, acme.DNS.Credentials.SessionTokenFile),
				UseAmbientCredentials: acme.DNS.Credentials.UseAmbientCredentials,
			},
			Record: tlsacme.RecordConfig{
				Manage: acme.DNS.Record.Manage,
				Type:   acme.DNS.Record.Type,
				Value:  acme.DNS.Record.Value,
				TTL:    acme.DNS.Record.TTLSeconds,
				Owner:  acme.DNS.Record.Owner,
			},
		},
	}
}

// configured reports whether any acme key is set, so a stray acme block with
// mode off is reported instead of ignored.
func (c TLSACMEServerConfig) configured() bool {
	return len(c.Domains) > 0 || strings.TrimSpace(c.Email) != "" ||
		strings.TrimSpace(c.DirectoryURL) != "" || c.AcceptTOS ||
		c.AllowInsecureDirectory || strings.TrimSpace(c.CacheDir) != "" ||
		strings.TrimSpace(c.KeyType) != "" || c.StartupTimeoutSeconds != 0 ||
		c.ReloadIntervalSeconds != 0 || c.Renewal.configured() ||
		strings.TrimSpace(c.EAB.KID) != "" || c.EAB.HMACKeyEnv != "" || c.EAB.HMACKeyFile != "" ||
		c.DNS.configured()
}

func (c TLSRenewalConfig) configured() bool {
	return c.WindowRatio != 0 || c.JitterSeconds != 0 || c.RetryMaxSeconds != 0 || c.UseARI != nil
}

func (c TLSDNSConfig) configured() bool {
	return strings.TrimSpace(c.Provider) != "" || strings.TrimSpace(c.Zone) != "" ||
		c.TTLSeconds != 0 || c.PropagationTimeoutSecs != 0 || len(c.PropagationNameservers) > 0 ||
		c.Credentials.configured() || c.Record.configured()
}

func (c TLSDNSCredentialsConfig) configured() bool {
	return c.APITokenEnv != "" || c.APITokenFile != "" || c.APIKeyEnv != "" || c.APIKeyFile != "" ||
		c.EmailEnv != "" || c.EmailFile != "" || c.TokenEnv != "" || c.TokenFile != "" ||
		c.AccessKeyIDEnv != "" || c.AccessKeyIDFile != "" || c.SecretAccessKeyEnv != "" ||
		c.SecretAccessKeyFile != "" || c.SessionTokenEnv != "" || c.SessionTokenFile != "" ||
		c.UseAmbientCredentials
}

func (c TLSDNSRecordConfig) configured() bool {
	return c.Manage || strings.TrimSpace(c.Type) != "" || strings.TrimSpace(c.Value) != "" ||
		c.TTLSeconds != 0 || strings.TrimSpace(c.Owner) != ""
}

func ref(env, file string) tlsacme.SecretRef {
	return tlsacme.SecretRef{Env: strings.TrimSpace(env), File: strings.TrimSpace(file)}
}

// seconds converts a YAML duration in seconds; zero means "use the default".
func seconds(value int) time.Duration {
	if value <= 0 {
		return 0
	}
	return time.Duration(value) * time.Second
}

// AdminCertificateSource adapts a TLS manager to the admin listener's handshake
// seam (#809, AdminCertificateSource): the listener asks CheckSNI for a yes/no
// answer and then fetches the certificate, keeping its single
// GetConfigForClient for the client-CA snapshot.
//
//	manager, _ := router.NewListenerTLSManager(adminTLS, "admin", "admin.tls")
//	plane.SetCertificateSource(router.AdminCertificateSource{Manager: manager})
type AdminCertificateSource struct {
	Manager *tlsacme.Manager
}

// GetCertificate serves the certificate for the handshake.
func (s AdminCertificateSource) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	return s.Manager.GetCertificate(hello)
}

// CheckSNI reports whether the manager serves name.
func (s AdminCertificateSource) CheckSNI(name string) bool {
	return s.Manager.ServesName(name)
}

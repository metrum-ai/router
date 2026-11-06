// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Automated TLS for the serving listener (#810). The YAML block maps onto
// internal/tlsacme.Config; everything about ACME, certificates, DNS providers
// and renewal lives there. This file only parses, validates and converts, so
// configuration errors are reported by the existing Validate path (and the
// existing secret redaction covers the referenced credentials).

import (
	"fmt"
	"strings"
	"time"

	"github.com/metrum-ai/router/internal/tlsacme"
)

// TLSServerConfig is the server.tls block. Its zero value (or mode: off) keeps
// the v4.7 behaviour exactly: a plaintext listener with no TLS manager.
type TLSServerConfig struct {
	// Mode is off (default), files or acme.
	Mode string `yaml:"mode,omitempty"`
	// CertificateFile and KeyFile are used by mode files.
	CertificateFile string `yaml:"certificate_file,omitempty"`
	KeyFile         string `yaml:"key_file,omitempty"`
	// Domains are the DNS names of the single SAN order in mode acme.
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
	// MinVersion is the minimum TLS version: "1.2" (default) or "1.3".
	MinVersion string `yaml:"min_version,omitempty"`
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
	// renewal happens. The default 2/3 leaves a third of the lifetime to
	// notice and repair a failure.
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

// Enabled reports whether the block asks for a TLS listener.
func (c TLSServerConfig) Enabled() bool {
	switch strings.ToLower(strings.TrimSpace(c.Mode)) {
	case "", "off":
		return false
	}
	return true
}

// managerConfig converts the YAML block into the tlsacme configuration.
func (c TLSServerConfig) managerConfig(listener, errorPrefix string) tlsacme.Config {
	return tlsacme.Config{
		Listener:               listener,
		ErrorPrefix:            errorPrefix,
		Mode:                   c.Mode,
		CertificateFile:        c.CertificateFile,
		KeyFile:                c.KeyFile,
		Domains:                c.Domains,
		Email:                  c.Email,
		DirectoryURL:           c.DirectoryURL,
		AcceptTOS:              c.AcceptTOS,
		AllowInsecureDirectory: c.AllowInsecureDirectory,
		CacheDir:               c.CacheDir,
		KeyType:                c.KeyType,
		MinVersion:             c.MinVersion,
		StartupTimeout:         seconds(c.StartupTimeoutSeconds),
		ReloadInterval:         seconds(c.ReloadIntervalSeconds),
		Renewal: tlsacme.RenewalConfig{
			WindowRatio: c.Renewal.WindowRatio,
			Jitter:      seconds(c.Renewal.JitterSeconds),
			RetryMax:    seconds(c.Renewal.RetryMaxSeconds),
			UseARI:      c.Renewal.UseARI,
		},
		EAB: tlsacme.EABConfig{
			KID:     c.EAB.KID,
			HMACKey: tlsacme.SecretRef{Env: c.EAB.HMACKeyEnv, File: c.EAB.HMACKeyFile},
		},
		DNS: tlsacme.DNSConfig{
			Provider:               c.DNS.Provider,
			Zone:                   c.DNS.Zone,
			TTL:                    c.DNS.TTLSeconds,
			PropagationTimeout:     seconds(c.DNS.PropagationTimeoutSecs),
			PropagationNameservers: c.DNS.PropagationNameservers,
			Credentials: tlsacme.Credentials{
				APIToken:              ref(c.DNS.Credentials.APITokenEnv, c.DNS.Credentials.APITokenFile),
				APIKey:                ref(c.DNS.Credentials.APIKeyEnv, c.DNS.Credentials.APIKeyFile),
				Email:                 ref(c.DNS.Credentials.EmailEnv, c.DNS.Credentials.EmailFile),
				Token:                 ref(c.DNS.Credentials.TokenEnv, c.DNS.Credentials.TokenFile),
				AccessKeyID:           ref(c.DNS.Credentials.AccessKeyIDEnv, c.DNS.Credentials.AccessKeyIDFile),
				SecretAccessKey:       ref(c.DNS.Credentials.SecretAccessKeyEnv, c.DNS.Credentials.SecretAccessKeyFile),
				SessionToken:          ref(c.DNS.Credentials.SessionTokenEnv, c.DNS.Credentials.SessionTokenFile),
				UseAmbientCredentials: c.DNS.Credentials.UseAmbientCredentials,
			},
			Record: tlsacme.RecordConfig{
				Manage: c.DNS.Record.Manage,
				Type:   c.DNS.Record.Type,
				Value:  c.DNS.Record.Value,
				TTL:    c.DNS.Record.TTLSeconds,
				Owner:  c.DNS.Record.Owner,
			},
		},
	}
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

// validateTLSServerConfig validates the serving listener's TLS block.
func validateTLSServerConfig(cfg TLSServerConfig) error {
	if !cfg.Enabled() {
		return nil
	}
	converted := cfg.managerConfig(tlsacme.ListenerServing, "server.tls").Normalize()
	if err := converted.Validate(); err != nil {
		return err
	}
	return nil
}

// newTLSManager builds the serving listener's TLS manager, or nil when TLS is
// off. It performs no network I/O: first issuance happens in StartTLS.
func newTLSManager(cfg TLSServerConfig) (*tlsacme.Manager, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	manager, err := tlsacme.New(cfg.managerConfig(tlsacme.ListenerServing, "server.tls"))
	if err != nil {
		return nil, fmt.Errorf("server.tls: %w", err)
	}
	return manager, nil
}

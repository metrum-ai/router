// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package tlsacme

import (
	"strings"
	"testing"
	"time"
)

// validACMEConfig is the smallest configuration that passes validation.
func validACMEConfig() Config {
	return Config{
		Listener:     ListenerServing,
		Mode:         ModeACME,
		Domains:      []string{"router.example.com"},
		Email:        "ops@example.com",
		DirectoryURL: "https://acme.example.com/directory",
		AcceptTOS:    true,
		CacheDir:     "/tmp/tls-test",
		DNS: DNSConfig{
			Provider: ProviderCloudflare,
			Credentials: Credentials{
				APIToken: SecretRef{Env: "CF_DNS_API_TOKEN"},
			},
		},
	}
}

func TestConfigDefaults(t *testing.T) {
	cfg := Config{}.Normalize()
	if cfg.Mode != ModeOff {
		t.Fatalf("mode = %q, want off (v4.7 configurations must not change)", cfg.Mode)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default configuration must validate: %v", err)
	}
	if cfg.DirectoryURL != DefaultDirectoryURL {
		t.Fatalf("directory = %q", cfg.DirectoryURL)
	}
	if cfg.CacheDir != DefaultCacheDir {
		t.Fatalf("cache dir = %q", cfg.CacheDir)
	}
	if cfg.KeyType != KeyTypeECDSAP256 || cfg.MinVersion != "1.2" {
		t.Fatalf("key/min = %q/%q", cfg.KeyType, cfg.MinVersion)
	}
	if cfg.Renewal.WindowRatio != 2.0/3.0 {
		t.Fatalf("window ratio = %v, want two thirds of the lifetime", cfg.Renewal.WindowRatio)
	}
	if cfg.Renewal.Jitter != 15*time.Minute {
		t.Fatalf("jitter = %v", cfg.Renewal.Jitter)
	}
	if cfg.Renewal.RetryMax != time.Hour {
		t.Fatalf("retry max = %v", cfg.Renewal.RetryMax)
	}
	if cfg.DNS.TTL != 60 || cfg.DNS.PropagationTimeout != 120*time.Second {
		t.Fatalf("dns defaults = %+v", cfg.DNS)
	}
	if !cfg.Renewal.ariEnabled() {
		t.Fatal("ARI should be on by default")
	}
}

func TestConfigNormalizeDomains(t *testing.T) {
	cfg := Config{Domains: []string{"Router.Example.COM.", "router.example.com", "*.Example.com", ""}}.Normalize()
	want := []string{"router.example.com", "*.example.com"}
	if len(cfg.Domains) != len(want) {
		t.Fatalf("domains = %v, want %v", cfg.Domains, want)
	}
	for i := range want {
		if cfg.Domains[i] != want[i] {
			t.Fatalf("domains = %v, want %v", cfg.Domains, want)
		}
	}
}

func TestConfigValidation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name:    "unknown mode",
			mutate:  func(c *Config) { c.Mode = "auto" },
			wantErr: "mode must be one of off, files, acme",
		},
		{
			name:    "acme without domains",
			mutate:  func(c *Config) { c.Domains = nil },
			wantErr: "domains is required",
		},
		{
			name:    "invalid domain",
			mutate:  func(c *Config) { c.Domains = []string{"not a domain"} },
			wantErr: "not a valid DNS name",
		},
		{
			name:    "tos not accepted",
			mutate:  func(c *Config) { c.AcceptTOS = false },
			wantErr: "accept_tos must be true",
		},
		{
			name:    "http directory",
			mutate:  func(c *Config) { c.DirectoryURL = "http://acme.internal/directory" },
			wantErr: "directory_url must be https",
		},
		{
			name: "http directory allowed for tests",
			mutate: func(c *Config) {
				c.DirectoryURL = "http://acme.internal/directory"
				c.AllowInsecureDirectory = true
			},
		},
		{
			name:    "missing provider",
			mutate:  func(c *Config) { c.DNS.Provider = "" },
			wantErr: "provider is required",
		},
		{
			name:    "unknown provider",
			mutate:  func(c *Config) { c.DNS.Provider = "gandi" },
			wantErr: "provider must be one of cloudflare, route53, digitalocean",
		},
		{
			name:    "cloudflare without credentials",
			mutate:  func(c *Config) { c.DNS.Credentials = Credentials{} },
			wantErr: "set exactly one of api_token_env",
		},
		{
			name: "cloudflare with both token forms",
			mutate: func(c *Config) {
				c.DNS.Credentials.APIKey = SecretRef{Env: "CF_API_KEY"}
				c.DNS.Credentials.Email = SecretRef{Env: "CF_EMAIL"}
			},
			wantErr: "set exactly one of api_token_env",
		},
		{
			name: "credential with env and file",
			mutate: func(c *Config) {
				c.DNS.Credentials.APIToken = SecretRef{Env: "CF_TOKEN", File: "/run/secrets/cf"}
			},
			wantErr: "set exactly one of api_token_env",
		},
		{
			name: "digitalocean needs a token",
			mutate: func(c *Config) {
				c.DNS.Provider = ProviderDigitalOcean
				c.DNS.Credentials = Credentials{}
			},
			wantErr: "token_env or token_file is required for digitalocean",
		},
		{
			name: "route53 needs keys",
			mutate: func(c *Config) {
				c.DNS.Provider = ProviderRoute53
				c.DNS.Credentials = Credentials{}
			},
			wantErr: "access_key_id and secret_access_key are required for route53",
		},
		{
			name: "route53 ambient credentials",
			mutate: func(c *Config) {
				c.DNS.Provider = ProviderRoute53
				c.DNS.Credentials = Credentials{UseAmbientCredentials: true}
			},
		},
		{
			name: "route53 ambient plus static keys",
			mutate: func(c *Config) {
				c.DNS.Provider = ProviderRoute53
				c.DNS.Credentials = Credentials{
					UseAmbientCredentials: true,
					AccessKeyID:           SecretRef{Env: "AWS_ACCESS_KEY_ID"},
				}
			},
			wantErr: "cannot be combined with static access keys",
		},
		{
			name:    "zone not a suffix",
			mutate:  func(c *Config) { c.DNS.Zone = "other.net" },
			wantErr: "is not a suffix of domain",
		},
		{
			name:    "bad key type",
			mutate:  func(c *Config) { c.KeyType = "ed25519" },
			wantErr: "key_type must be one of",
		},
		{
			name:    "bad min version",
			mutate:  func(c *Config) { c.MinVersion = "1.0" },
			wantErr: "min_version must be 1.2 or 1.3",
		},
		{
			name:    "bad window ratio",
			mutate:  func(c *Config) { c.Renewal.WindowRatio = 1.2 },
			wantErr: "window_ratio must be between 0 and 1",
		},
		{
			name:    "record management needs a type",
			mutate:  func(c *Config) { c.DNS.Record = RecordConfig{Manage: true, Value: "203.0.113.10"} },
			wantErr: "type is required",
		},
		{
			name:    "record management needs a value",
			mutate:  func(c *Config) { c.DNS.Record = RecordConfig{Manage: true, Type: "A"} },
			wantErr: "value is required",
		},
		{
			name:    "A record needs an IPv4 address",
			mutate:  func(c *Config) { c.DNS.Record = RecordConfig{Manage: true, Type: "A", Value: "2001:db8::1"} },
			wantErr: "not an IPv4 address",
		},
		{
			name:    "AAAA record needs an IPv6 address",
			mutate:  func(c *Config) { c.DNS.Record = RecordConfig{Manage: true, Type: "AAAA", Value: "203.0.113.10"} },
			wantErr: "not an IPv6 address",
		},
		{
			name:    "record type without manage",
			mutate:  func(c *Config) { c.DNS.Record = RecordConfig{Type: "A", Value: "203.0.113.10"} },
			wantErr: "require manage: true",
		},
		{
			name: "EAB needs both fields",
			mutate: func(c *Config) {
				c.EAB = EABConfig{KID: "kid"}
			},
			wantErr: "eab.kid and eab.hmac_key_env",
		},
		{
			name:    "files mode needs files",
			mutate:  func(c *Config) { c.Mode = ModeFiles },
			wantErr: "certificate_file and key_file are required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validACMEConfig()
			if tc.mutate != nil {
				tc.mutate(&cfg)
			}
			cfg = cfg.Normalize()
			err := cfg.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestConfigPrefixNamesTheListener(t *testing.T) {
	cfg := validACMEConfig()
	cfg.Listener = ListenerAdmin
	cfg.Domains = nil
	cfg = cfg.Normalize()
	err := cfg.Validate()
	if err == nil || !strings.HasPrefix(err.Error(), "admin.tls:") {
		t.Fatalf("error = %v, want an admin.tls prefix", err)
	}
}

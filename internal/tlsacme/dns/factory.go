// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package dns

import (
	"context"
	"fmt"
	"strings"
)

// Credentials are the resolved credential values for one provider. They are
// resolved from SecretRef references by the caller, which also registers them
// for redaction; this package never reads the environment or the filesystem.
type Credentials struct {
	// Cloudflare.
	APIToken string
	APIKey   string
	Email    string
	// DigitalOcean.
	Token string
	// Route 53.
	AccessKeyID           string
	SecretAccessKey       string
	SessionToken          string
	UseAmbientCredentials bool
}

// New builds the provider named provider. Credentials that the selected
// provider does not use are ignored.
func New(ctx context.Context, provider string, creds Credentials, opts Options) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case ProviderNameCloudflare:
		return NewCloudflare(CloudflareCredentials{APIToken: creds.APIToken, APIKey: creds.APIKey, Email: creds.Email}, opts)
	case ProviderNameDigitalOcean:
		return NewDigitalOcean(creds.Token, opts)
	case ProviderNameRoute53:
		return NewRoute53(ctx, Route53Credentials{
			AccessKeyID:           creds.AccessKeyID,
			SecretAccessKey:       creds.SecretAccessKey,
			SessionToken:          creds.SessionToken,
			UseAmbientCredentials: creds.UseAmbientCredentials,
		}, opts)
	default:
		return nil, fmt.Errorf("dns: unsupported provider %q", provider)
	}
}

// Provider names, mirrored from the configuration names.
const (
	ProviderNameCloudflare   = "cloudflare"
	ProviderNameDigitalOcean = "digitalocean"
	ProviderNameRoute53      = "route53"
)

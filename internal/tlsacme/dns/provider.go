// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

// Package dns implements the DNS-01 providers the router uses to obtain
// certificates for private and loopback-fronted hosts, and the optional
// reconciliation of the A/AAAA/CNAME record that points a hostname at the
// router.
//
// Every provider talks to its public API through an injectable HTTP client (or
// an overridable endpoint for Route 53), so tests assert the exact requests and
// payloads instead of accepting a fake that always succeeds.
package dns

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// Record is one DNS record in a provider's zone.
type Record struct {
	// ID is the provider's identifier for the record, or a stable composite for
	// providers without one (Route 53).
	ID string
	// Type is TXT, A, AAAA or CNAME.
	Type string
	// Name is the fully qualified record name without a trailing dot.
	Name string
	// Value is the TXT content without quotes, an address, or a CNAME target.
	Value string
	// TTL in seconds.
	TTL int
}

// Provider is the seam every DNS provider implements. Challenge presentation
// and record reconciliation both use it, so a provider is exercised by the same
// tests whether it serves ACME or A-record management.
type Provider interface {
	// Name is the configuration name: cloudflare, digitalocean or route53.
	Name() string
	// Zones lists the zone names the credentials can manage.
	Zones(ctx context.Context) ([]string, error)
	// Records lists the records of one zone.
	Records(ctx context.Context, zone string) ([]Record, error)
	// Create adds a record and returns the provider's identifier for it.
	Create(ctx context.Context, zone string, record Record) (string, error)
	// Update replaces an existing record.
	Update(ctx context.Context, zone string, id string, record Record) error
	// Delete removes a record.
	Delete(ctx context.Context, zone string, id string) error
}

// Instruments receives DNS events for the TLS metrics. A nil Instruments is
// valid and discards everything.
type Instruments interface {
	DNSPresentFailure(provider string)
	DNSConflict(provider, reason string)
	DNSRecordsManaged(provider, recordType string, delta float64)
}

// Options configures a provider and the challenge solver.
type Options struct {
	// Zone pins the zone that holds _acme-challenge records. Empty means the
	// longest matching zone of the account.
	Zone string
	// TTL of challenge TXT records.
	TTL int
	// PropagationTimeout bounds the wait for a presented record to be visible.
	PropagationTimeout time.Duration
	// PropagationNameservers, when set, are queried instead of the system
	// resolver (split-horizon and test setups).
	PropagationNameservers []string
	// HTTPClient overrides the provider HTTP client (tests).
	HTTPClient HTTPDoer
	// Endpoint overrides the provider API base URL (tests).
	Endpoint string
	// Logger receives provider diagnostics. Credentials are never logged.
	Logger *log.Logger
	// Instruments receives metrics; may be nil.
	Instruments Instruments
	// Redact scrubs credentials out of provider error text.
	Redact func(string) string
	// Now overrides the clock (tests).
	Now func() time.Time
	// LookupTXT overrides DNS propagation lookups (tests).
	LookupTXT func(ctx context.Context, name string) ([]string, error)
}

// HTTPDoer is the subset of *http.Client the providers use.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

func (o *Options) redact(text string) string {
	if o == nil || o.Redact == nil {
		return text
	}
	return o.Redact(text)
}

// errorf builds an error whose text is scrubbed of credentials. Every provider
// error path goes through it, because provider responses can echo a token back
// (for example in an "invalid token" message). The message is formatted first:
// redacting the format string alone would leave a credential that is passed as
// an argument in clear.
func (o *Options) errorf(format string, args ...any) error {
	message := fmt.Sprintf(format, args...)
	if o == nil {
		return errors.New(message)
	}
	return errors.New(o.redact(message))
}

func (o *Options) logf(format string, args ...any) {
	if o == nil || o.Logger == nil {
		return
	}
	o.Logger.Print(o.redact(fmt.Sprintf(format, args...)))
}

func (o *Options) now() time.Time {
	if o == nil || o.Now == nil {
		return time.Now()
	}
	return o.Now()
}

// acquireInstruments returns a non-nil Instruments.
func (o *Options) instruments() Instruments {
	if o == nil || o.Instruments == nil {
		return discard{}
	}
	return o.Instruments
}

type discard struct{}

func (discard) DNSPresentFailure(string)                  {}
func (discard) DNSConflict(string, string)                {}
func (discard) DNSRecordsManaged(string, string, float64) {}

// challengeName returns the RFC 8555 DNS-01 record name for an identifier.
func challengeName(domain string) string {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	domain = strings.TrimPrefix(domain, "*.")
	return "_acme-challenge." + domain
}

// fqdn normalizes a record name to lower case without a trailing dot.
func fqdn(name string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
}

// MatchZone returns the zone that should hold records for domains. An explicit
// zone must be a suffix of every domain; otherwise the longest matching zone of
// the account wins.
func MatchZone(domains []string, configured string, zones []string) (string, error) {
	domains = normalize(domains)
	if len(domains) == 0 {
		return "", fmt.Errorf("dns: no domains to match against a zone")
	}
	configured = fqdn(configured)
	if configured != "" {
		for _, domain := range domains {
			if !inZone(domain, configured) {
				return "", fmt.Errorf("dns: zone %q is not a suffix of domain %q", configured, domain)
			}
		}
		return configured, nil
	}
	best := ""
	for _, zone := range normalize(zones) {
		matched := true
		for _, domain := range domains {
			if !inZone(domain, zone) {
				matched = false
				break
			}
		}
		if matched && len(zone) > len(best) {
			best = zone
		}
	}
	if best == "" {
		return "", fmt.Errorf("dns: no zone of the account contains domains %s", strings.Join(domains, ", "))
	}
	return best, nil
}

func normalize(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = fqdn(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// inZone reports whether name is zone or one of its subdomains.
func inZone(name, zone string) bool {
	name = fqdn(name)
	zone = fqdn(zone)
	return name == zone || strings.HasSuffix(name, "."+zone)
}

// relativeName strips the zone suffix from a record name for providers whose
// API takes names relative to the zone ("@" for the apex).
func relativeName(name, zone string) string {
	name = fqdn(name)
	zone = fqdn(zone)
	if name == zone {
		return "@"
	}
	return strings.TrimSuffix(name, "."+zone)
}

// absoluteName is the inverse of relativeName.
func absoluteName(name, zone string) string {
	name = strings.TrimSpace(name)
	zone = fqdn(zone)
	if name == "" || name == "@" {
		return zone
	}
	name = fqdn(name)
	if inZone(name, zone) {
		return name
	}
	return name + "." + zone
}

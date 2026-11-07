// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

//go:build livestaging

// Package tlsacme's opt-in live check against Let's Encrypt staging.
//
// It is not part of the normal test run and not part of CI (it needs real DNS
// credentials and a real zone). Run it only on an approved test hostname:
//
//	DO_DNS_TOKEN=... LIVE_STAGING_DOMAIN=acme-test.example.com \
//	LIVE_STAGING_ZONE=example.com LIVE_STAGING_EMAIL=ops@example.com \
//	go test -tags livestaging ./internal/tlsacme -run TestLiveStaging -v -count=1
//
// The run touches exactly: the _acme-challenge TXT record for the domain, and,
// when LIVE_STAGING_RECORD_MANAGE=1, the domain's A record plus its
// _metrum-router ownership marker. Everything it creates is removed before the
// test returns, and the final listing filtered to the hostname must be empty.
package tlsacme

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metrum-ai/router/internal/tlsacme/dns"
)

// stagingDirectory is the only ACME directory this check may use.
const stagingDirectory = "https://acme-staging-v02.api.letsencrypt.org/directory"

// countingProvider wraps a provider and counts write calls, so the live run can
// prove that a second record reconciliation writes nothing.
type countingProvider struct {
	dns.Provider
	mu      sync.Mutex
	creates int
	updates int
	deletes int
}

func (p *countingProvider) Create(ctx context.Context, zone string, record dns.Record) (string, error) {
	p.mu.Lock()
	p.creates++
	p.mu.Unlock()
	return p.Provider.Create(ctx, zone, record)
}

func (p *countingProvider) Update(ctx context.Context, zone, id string, record dns.Record) error {
	p.mu.Lock()
	p.updates++
	p.mu.Unlock()
	return p.Provider.Update(ctx, zone, id, record)
}

func (p *countingProvider) Delete(ctx context.Context, zone, id string) error {
	p.mu.Lock()
	p.deletes++
	p.mu.Unlock()
	return p.Provider.Delete(ctx, zone, id)
}

func (p *countingProvider) writes() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.creates + p.updates + p.deletes
}

func TestLiveStaging(t *testing.T) {
	domain := strings.TrimSpace(os.Getenv("LIVE_STAGING_DOMAIN"))
	zone := strings.TrimSpace(os.Getenv("LIVE_STAGING_ZONE"))
	email := strings.TrimSpace(os.Getenv("LIVE_STAGING_EMAIL"))
	if domain == "" || zone == "" {
		t.Skip("set LIVE_STAGING_DOMAIN and LIVE_STAGING_ZONE (and DO_DNS_TOKEN) to run the live staging check")
	}
	if os.Getenv("DO_DNS_TOKEN") == "" {
		t.Skip("set DO_DNS_TOKEN to run the live staging check")
	}
	if !strings.HasSuffix(domain, "."+zone) {
		t.Fatalf("LIVE_STAGING_DOMAIN %q is not inside LIVE_STAGING_ZONE %q", domain, zone)
	}
	manageRecord := os.Getenv("LIVE_STAGING_RECORD_MANAGE") == "1"
	recordValue := strings.TrimSpace(os.Getenv("LIVE_STAGING_A_VALUE"))
	if recordValue == "" {
		recordValue = "192.0.2.1" // TEST-NET-1: never routable
	}
	owner := strings.TrimSpace(os.Getenv("LIVE_STAGING_OWNER"))
	if owner == "" {
		owner = "acme-test"
	}

	// The token is registered with a redactor here too: this check injects its
	// provider, so the manager's own secret set never sees the value, and a
	// provider response that echoes it must still never reach a log.
	token := os.Getenv("DO_DNS_TOKEN")
	redact := func(text string) string {
		if token == "" {
			return text
		}
		return strings.ReplaceAll(text, token, "<redacted>")
	}
	providerOptions := dns.Options{Logger: testLogger(t), Redact: redact}
	provider, err := dns.New(context.Background(), dns.ProviderNameDigitalOcean,
		dns.Credentials{Token: token}, providerOptions)
	if err != nil {
		t.Fatalf("build provider: %v", err)
	}
	counted := &countingProvider{Provider: provider}

	cfg := Config{
		Listener:          ListenerServing,
		Mode:              ModeRouter,
		CertificateSource: SourceACME,
		Domains:           []string{domain},
		Email:             email,
		DirectoryURL:      stagingDirectory,
		AcceptTOS:         true,
		CacheDir:          t.TempDir() + "/tls",
		DNS: DNSConfig{
			Provider:           dns.ProviderNameDigitalOcean,
			Zone:               zone,
			TTL:                60,
			PropagationTimeout: 180 * time.Second,
			Credentials:        Credentials{Token: SecretRef{Env: "DO_DNS_TOKEN"}},
			Record:             RecordConfig{Manage: manageRecord, Type: "A", Value: recordValue, TTL: 300, Owner: owner},
		},
	}.Normalize()
	if cfg.DirectoryURL != stagingDirectory {
		t.Fatalf("this check may only use the staging directory, got %q", cfg.DirectoryURL)
	}

	manager, err := New(cfg, WithProvider(counted), WithLogger(testLogger(t)))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Anything the run creates is removed even if a later assertion fails.
	if manageRecord {
		defer func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cleanupCancel()
			removed, err := dns.DeleteManaged(cleanupCtx, provider, zone,
				dns.Record{Type: "A", Name: domain}, owner, providerOptions)
			if err != nil {
				t.Logf("cleanup of the managed record: %v", err)
			}
			t.Logf("cleanup removed managed record: %v", removed)
		}()
	}

	if err := manager.EnsureCertificate(ctx); err != nil {
		t.Fatalf("staging issuance failed: %v", err)
	}
	status := manager.Status()
	if len(status) != 1 || !status[0].Valid {
		t.Fatalf("status = %+v", status)
	}
	leaf := manager.record.CertificatePEM
	if !strings.Contains(string(leaf), "BEGIN CERTIFICATE") {
		t.Fatal("no certificate was stored")
	}
	if !strings.Contains(status[0].Issuer, "acme-staging") {
		t.Fatalf("issuer = %q, want the staging directory", status[0].Issuer)
	}
	lifetime := status[0].NotAfter.Sub(status[0].NotBefore)
	t.Logf("issued for %s: valid %s .. %s (lifetime %s), renewal due %s",
		domain, status[0].NotBefore.Format(time.RFC3339), status[0].NotAfter.Format(time.RFC3339),
		lifetime, status[0].RenewalDue.Format(time.RFC3339))
	due := status[0].RenewalDue
	if !due.After(status[0].NotBefore) || !due.Before(status[0].NotAfter) {
		t.Fatalf("renewal due %s is outside the certificate lifetime", due)
	}
	// The CA advertises renewal information: the deadline must sit in its
	// suggested window. Without ARI the two-thirds ratio applies.
	leafBlock, _ := pem.Decode(manager.record.CertificatePEM)
	leafCert, parseErr := x509.ParseCertificate(leafBlock.Bytes)
	if parseErr != nil {
		t.Fatalf("parse issued certificate: %v", parseErr)
	}
	windowStart, windowEnd, ariErr := manager.ari.window(ctx, stagingDirectory, leafCert)
	if ariErr == nil {
		t.Logf("CA suggested renewal window %s .. %s", windowStart.Format(time.RFC3339), windowEnd.Format(time.RFC3339))
		if due.Before(windowStart.Add(-time.Minute)) || due.After(windowEnd.Add(time.Minute)) {
			t.Fatalf("renewal due %s is outside the CA's suggested window %s..%s", due, windowStart, windowEnd)
		}
	} else {
		t.Logf("CA renewal information unavailable (%v); checking the two-thirds ratio", ariErr)
		wantDeadline := status[0].NotBefore.Add(time.Duration(float64(lifetime) * (2.0 / 3.0)))
		if delta := due.Sub(wantDeadline); delta > time.Hour || delta < -time.Hour {
			t.Fatalf("renewal due %s is not about two thirds of the lifetime (%s)", due, wantDeadline)
		}
	}

	// A restart reuses the cached certificate: no second order, no CA spend.
	second, err := New(cfg, WithProvider(counted), WithLogger(testLogger(t)))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if err := second.EnsureCertificate(ctx); err != nil {
		t.Fatalf("cached startup failed: %v", err)
	}
	if second.Source() != "store" {
		t.Fatalf("cached startup source = %q, want store", second.Source())
	}

	if manageRecord {
		before := counted.writes()
		result, err := dns.Reconcile(ctx, counted, zone,
			dns.Record{Type: "A", Name: domain, Value: recordValue, TTL: 300}, owner,
			dns.Options{Logger: testLogger(t), Instruments: dnsInstruments{manager}, Redact: redact})
		if err != nil {
			t.Fatalf("reconcile managed record: %v", err)
		}
		t.Logf("record reconciliation: %+v, %d write call(s)", result, counted.writes()-before)

		before = counted.writes()
		again, err := dns.Reconcile(ctx, counted, zone,
			dns.Record{Type: "A", Name: domain, Value: recordValue, TTL: 300}, owner,
			dns.Options{Logger: testLogger(t), Instruments: dnsInstruments{manager}, Redact: redact})
		if err != nil {
			t.Fatalf("second reconcile: %v", err)
		}
		if writes := counted.writes() - before; writes != 0 {
			t.Fatalf("an idempotent re-run made %d write call(s)", writes)
		}
		if again.Unchanged != 1 {
			t.Fatalf("second reconcile = %+v, want unchanged", again)
		}

		removed, err := dns.DeleteManaged(ctx, provider, zone,
			dns.Record{Type: "A", Name: domain}, owner, providerOptions)
		if err != nil || !removed {
			t.Fatalf("delete managed record: removed=%v err=%v", removed, err)
		}
	}

	// Proof for the teardown note: nothing matching the hostname is left.
	records, err := provider.Records(ctx, zone)
	if err != nil {
		t.Fatalf("list zone records: %v", err)
	}
	var left []string
	for _, record := range records {
		if strings.Contains(record.Name, domain) || strings.Contains(record.Name, "_metrum-router."+domain) {
			left = append(left, fmt.Sprintf("%s %s -> %s", record.Type, record.Name, record.Value))
		}
	}
	t.Logf("zone listing filtered to %s after teardown: %v", domain, left)
	if len(left) != 0 {
		t.Fatalf("records created by this run were left behind: %v", left)
	}
}

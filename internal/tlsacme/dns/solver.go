// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package dns

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// Solver presents and cleans up DNS-01 challenge TXT records through a
// Provider and waits until the record is visible to resolvers.
type Solver struct {
	provider Provider
	zone     string
	opts     Options

	mu     sync.Mutex
	active map[string]string // "_acme-challenge.example.com|value" -> record id
}

// NewSolver builds a solver for one zone.
func NewSolver(provider Provider, zone string, opts Options) *Solver {
	if opts.TTL <= 0 {
		opts.TTL = 60
	}
	if opts.PropagationTimeout <= 0 {
		opts.PropagationTimeout = 120 * time.Second
	}
	if opts.LookupTXT == nil {
		opts.LookupTXT = resolverLookup(opts.PropagationNameservers)
	}
	return &Solver{provider: provider, zone: zone, opts: opts, active: map[string]string{}}
}

// Present creates the challenge TXT record and waits for it to propagate.
func (s *Solver) Present(ctx context.Context, domain, value string) error {
	name := challengeName(domain)
	record := Record{Type: "TXT", Name: name, Value: value, TTL: s.opts.TTL}
	id, err := s.provider.Create(ctx, s.zone, record)
	if err != nil {
		s.opts.instruments().DNSPresentFailure(s.provider.Name())
		return fmt.Errorf("dns: present %s through %s: %w", name, s.provider.Name(), err)
	}
	s.mu.Lock()
	s.active[name+"|"+value] = id
	s.mu.Unlock()
	if err := s.waitPropagation(ctx, name, value); err != nil {
		// Leave the record for Cleanup; the ACME order will fail either way.
		return err
	}
	return nil
}

// Cleanup removes the TXT record Present created, and only that value.
func (s *Solver) Cleanup(ctx context.Context, domain, value string) error {
	name := challengeName(domain)
	s.mu.Lock()
	id, ok := s.active[name+"|"+value]
	delete(s.active, name+"|"+value)
	s.mu.Unlock()
	if !ok {
		// Another process (or a previous run) may have created it; only remove
		// an exact name+value match.
		records, err := s.provider.Records(ctx, s.zone)
		if err != nil {
			return fmt.Errorf("dns: list %s for cleanup: %w", name, err)
		}
		for _, record := range records {
			if record.Type == "TXT" && fqdn(record.Name) == name && record.Value == value {
				id = record.ID
				ok = true
				break
			}
		}
	}
	if !ok {
		return nil
	}
	if err := s.provider.Delete(ctx, s.zone, id); err != nil {
		return fmt.Errorf("dns: delete %s through %s: %w", name, s.provider.Name(), err)
	}
	return nil
}

// waitPropagation polls resolvers until the TXT value is visible.
func (s *Solver) waitPropagation(ctx context.Context, name, value string) error {
	deadline := s.opts.now().Add(s.opts.PropagationTimeout)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		values, err := s.opts.LookupTXT(ctx, name)
		for _, candidate := range values {
			if strings.Trim(candidate, "\"") == value {
				return nil
			}
		}
		if !s.opts.now().Before(deadline) {
			s.opts.instruments().DNSPresentFailure(s.provider.Name())
			if err != nil {
				return fmt.Errorf("dns: TLS challenge record %s did not become visible within %s: %w", name, s.opts.PropagationTimeout, err)
			}
			return fmt.Errorf("dns: TLS challenge record %s did not become visible within %s", name, s.opts.PropagationTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// resolverLookup builds the production TXT lookup. When nameservers are given
// they are queried directly, which is what split-horizon and test zones need.
func resolverLookup(nameservers []string) func(ctx context.Context, name string) ([]string, error) {
	var resolver *net.Resolver
	if len(nameservers) == 0 {
		resolver = net.DefaultResolver
	} else {
		dialer := func(ctx context.Context, network, address string) (net.Conn, error) {
			var lastErr error
			for _, server := range nameservers {
				server = strings.TrimSpace(server)
				if server == "" {
					continue
				}
				target := server
				if _, _, err := net.SplitHostPort(server); err != nil {
					target = net.JoinHostPort(server, "53")
				}
				d := net.Dialer{Timeout: 5 * time.Second}
				conn, err := d.DialContext(ctx, network, target)
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			if lastErr == nil {
				lastErr = fmt.Errorf("no propagation nameservers configured")
			}
			return nil, lastErr
		}
		resolver = &net.Resolver{PreferGo: true, Dial: dialer}
	}
	return func(ctx context.Context, name string) ([]string, error) {
		return resolver.LookupTXT(ctx, name)
	}
}

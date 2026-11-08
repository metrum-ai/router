// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"strings"
)

// Trust authority is an explicit configuration association, not a certificate
// DN or a claimed SAN. Sources publish immutable pools, stable until reload.
type adminMTLSTrustDomain struct {
	Issuer   string
	Roots    interface{ Snapshot() *x509.CertPool }
	Subjects map[string]string
}
type adminMTLSIssuerDispatcher struct{ domains []*adminMTLSAuthenticator }

func newAdminMTLSIssuerDispatcher(domains []adminMTLSTrustDomain) (Authenticator, error) {
	if len(domains) == 0 || len(domains) > 32 {
		return nil, errors.New("admin mTLS trust domain count invalid")
	}
	dispatcher := &adminMTLSIssuerDispatcher{}
	seen := map[string]bool{}
	for _, domain := range domains {
		issuer := strings.TrimSpace(domain.Issuer)
		if issuer == "" || seen[issuer] {
			return nil, errors.New("admin mTLS issuer invalid or duplicated")
		}
		seen[issuer] = true
		validated, err := newAdminMTLSAuthenticator(issuer, domain.Roots, domain.Subjects)
		if err != nil {
			return nil, err
		}
		dispatcher.domains = append(dispatcher.domains, validated.(*adminMTLSAuthenticator))
	}
	return dispatcher, nil
}
func (d *adminMTLSIssuerDispatcher) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	if ctx == nil || ctx.Err() != nil || r == nil {
		return Principal{}, ErrInvalidCredential
	}
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return Principal{}, ErrNoCredential
	}
	if len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) > 16 {
		return Principal{}, ErrInvalidCredential
	}
	leaf := r.TLS.PeerCertificates[0]
	if leaf == nil {
		return Principal{}, ErrInvalidCredential
	}
	intermediates := x509.NewCertPool()
	for _, cert := range r.TLS.PeerCertificates[1:] {
		if cert == nil {
			return Principal{}, ErrInvalidCredential
		}
		intermediates.AddCert(cert)
	}
	// First pin all current root pools. Recheck publication after verification and
	// mapping; a removed root cannot survive on an existing TLS connection.
	pools := make([]*x509.CertPool, len(d.domains))
	for i, domain := range d.domains {
		pools[i] = domain.roots.Snapshot()
		if pools[i] == nil {
			return Principal{}, ErrInvalidCredential
		}
	}
	var selected *adminMTLSAuthenticator
	for i, domain := range d.domains {
		if ctx.Err() != nil {
			return Principal{}, ErrInvalidCredential
		}
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: pools[i], Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
			continue
		}
		// Multiple valid trust authorities are ambiguous even if their SAN maps
		// happen to agree. Never choose authority by map preference or slice order.
		if selected != nil {
			return Principal{}, ErrInvalidCredential
		}
		selected = domain
	}
	if selected == nil || !adminLeafExplicitClientAuth(leaf) {
		return Principal{}, ErrInvalidCredential
	}
	p, err := selected.principalForVerifiedLeaf(ctx, leaf)
	if err != nil {
		return Principal{}, err
	}
	for i, domain := range d.domains {
		if domain.roots.Snapshot() != pools[i] {
			return Principal{}, ErrInvalidCredential
		}
	}
	if ctx.Err() != nil {
		return Principal{}, ErrInvalidCredential
	}
	return p, nil
}

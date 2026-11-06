// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

type adminMTLSAuthenticator struct {
	issuer   string // configured trust-domain ID, never a certificate DN
	roots    interface{ Snapshot() *x509.CertPool }
	subjects map[string]string
}

// newAdminMTLSAuthenticator binds an explicitly named trust domain to a root
// snapshot and exact typed SAN mappings. Runtime configuration will construct
// this mechanism; it does not modify serving-port certificate authentication.
func newAdminMTLSAuthenticator(issuer string, roots interface{ Snapshot() *x509.CertPool }, subjects map[string]string) (Authenticator, error) {
	if strings.TrimSpace(issuer) == "" || roots == nil || roots.Snapshot() == nil || len(subjects) == 0 {
		return nil, errors.New("admin mTLS trust domain, roots and subject mappings required")
	}
	a := &adminMTLSAuthenticator{issuer: issuer, roots: roots, subjects: make(map[string]string, len(subjects))}
	for san, subject := range subjects {
		if strings.TrimSpace(subject) == "" || strings.ContainsAny(subject, "\r\n") {
			return nil, errors.New("admin mTLS subject invalid")
		}
		switch {
		case strings.HasPrefix(san, "uri:"):
			value := strings.TrimPrefix(san, "uri:")
			parsed, err := url.Parse(value)
			if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return nil, errors.New("admin mTLS URI SAN mapping invalid")
			}
		case strings.HasPrefix(san, "dns:"):
			value := strings.TrimPrefix(san, "dns:")
			if value == "" || strings.ContainsAny(value, "*@ /\t\r\n") {
				return nil, errors.New("admin mTLS DNS SAN mapping invalid")
			}
		default:
			return nil, errors.New("admin mTLS mapping requires URI or DNS SAN")
		}
		if strings.HasPrefix(san, "dns:") {
			san = "dns:" + strings.ToLower(strings.TrimPrefix(san, "dns:"))
		}
		if existing := a.subjects[san]; existing != "" && existing != subject {
			return nil, errors.New("admin mTLS conflicting normalized SAN mapping")
		}
		a.subjects[san] = subject
	}
	return a, nil
}

func (a *adminMTLSAuthenticator) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	if ctx.Err() != nil || r == nil {
		return Principal{}, ErrInvalidCredential
	}
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return Principal{}, ErrNoCredential
	}
	// A forwarded header can never manufacture a TLS identity. Handshake
	// verification is required but not sufficient: reverify against current roots.
	if len(r.TLS.VerifiedChains) == 0 {
		return Principal{}, ErrInvalidCredential
	}
	leaf := r.TLS.PeerCertificates[0]
	if leaf == nil {
		return Principal{}, ErrInvalidCredential
	}
	roots := a.roots.Snapshot()
	if roots == nil {
		return Principal{}, ErrInvalidCredential
	}
	intermediates := x509.NewCertPool()
	for _, cert := range r.TLS.PeerCertificates[1:] {
		if cert == nil {
			return Principal{}, ErrInvalidCredential
		}
		intermediates.AddCert(cert)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return Principal{}, ErrInvalidCredential
	}
	if !adminLeafExplicitClientAuth(leaf) {
		return Principal{}, ErrInvalidCredential
	}
	// Unmapped extra SANs are ignored. All mapped SANs must agree on exactly
	// one subject; no preference ordering can mask conflicting mapped SANs.
	subject := ""
	match := func(san string) bool {
		mapped := a.subjects[san]
		if mapped == "" {
			return true
		}
		if subject != "" && subject != mapped {
			return false
		}
		subject = mapped
		return true
	}
	uris, err := adminRawCertificateURISANs(leaf)
	if err != nil {
		return Principal{}, ErrInvalidCredential
	}
	for _, uri := range uris {
		if !match("uri:" + uri) {
			return Principal{}, ErrInvalidCredential
		}
	}
	for _, dns := range leaf.DNSNames {
		if !match("dns:" + strings.ToLower(dns)) {
			return Principal{}, ErrInvalidCredential
		}
	}
	if subject == "" || ctx.Err() != nil {
		return Principal{}, ErrInvalidCredential
	}
	fingerprint := sha256.Sum256(leaf.Raw)
	return Principal{Subject: subject, Kind: "mtls", Issuer: a.issuer, CredentialID: hex.EncodeToString(fingerprint[:])}, nil
}

// Read URI GeneralNames from the signed SAN extension bytes, not url.URL's
// reserialization (which can normalize scheme/escape spelling). URI mapping is
// byte-exact; DNS mapping alone is case-insensitive.
func adminRawCertificateURISANs(cert *x509.Certificate) ([]string, error) {
	var uris []string
	for _, ext := range cert.Extensions {
		if !ext.Id.Equal(asn1.ObjectIdentifier{2, 5, 29, 17}) {
			continue
		}
		var names []asn1.RawValue
		rest, err := asn1.Unmarshal(ext.Value, &names)
		if err != nil || len(rest) != 0 {
			return nil, ErrInvalidCredential
		}
		for _, name := range names {
			if name.Class == 2 && name.Tag == 6 {
				if name.IsCompound {
					return nil, ErrInvalidCredential
				}
				uris = append(uris, string(name.Bytes))
			}
		}
	}
	return uris, nil
}

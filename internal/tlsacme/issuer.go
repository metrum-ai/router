// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package tlsacme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/acme"
)

// issuedCertificate is the result of one ACME order.
type issuedCertificate struct {
	CertificatePEM []byte
	PrivateKeyPEM  []byte
	NotBefore      time.Time
	NotAfter       time.Time
	OrderURL       string
}

// Certifier obtains a certificate for a domain set. The ACME implementation is
// the production one; tests substitute a stub.
type Certifier interface {
	Issue(ctx context.Context, domains []string) (issuedCertificate, error)
}

// challengeSolver presents and cleans up DNS-01 challenges.
type challengeSolver interface {
	Present(ctx context.Context, domain, value string) error
	Cleanup(ctx context.Context, domain, value string) error
}

// acmeCertifier runs RFC 8555 orders with DNS-01 challenges through
// golang.org/x/crypto/acme.
type acmeCertifier struct {
	directoryURL string
	email        string
	acceptTOS    bool
	keyType      string
	httpClient   *http.Client
	store        *Store
	solver       challengeSolver
	eabKID       string
	eabHMACKey   string
	now          func() time.Time
}

// Issue runs one order: register the account on first use, authorize every
// identifier with a DNS-01 challenge, finalize the order and return the chain.
func (a *acmeCertifier) Issue(ctx context.Context, domains []string) (issuedCertificate, error) {
	account, accountKey, err := a.account(ctx)
	if err != nil {
		return issuedCertificate{}, err
	}
	client := &acme.Client{
		Key:          accountKey,
		DirectoryURL: a.directoryURL,
		HTTPClient:   a.httpClient,
		KID:          acme.KeyID(account.URL),
	}
	identifiers := make([]acme.AuthzID, 0, len(domains))
	for _, domain := range domains {
		identifiers = append(identifiers, acme.AuthzID{Type: "dns", Value: domain})
	}
	order, err := client.AuthorizeOrder(ctx, identifiers)
	if err != nil {
		return issuedCertificate{}, fmt.Errorf("acme: create order: %w", err)
	}
	// Keep the finalize URL from the order creation: a CA is allowed to omit it
	// from later order representations.
	finalizeURL := order.FinalizeURL
	for _, authzURL := range order.AuthzURLs {
		if err := a.authorize(ctx, client, authzURL); err != nil {
			return issuedCertificate{}, err
		}
	}
	if order, err = client.WaitOrder(ctx, order.URI); err != nil {
		return issuedCertificate{}, fmt.Errorf("acme: wait for order: %w", err)
	}
	if order.FinalizeURL == "" {
		order.FinalizeURL = finalizeURL
	}
	if order.Status != acme.StatusReady && order.Status != acme.StatusValid {
		return issuedCertificate{}, fmt.Errorf("acme: order ended in status %q", order.Status)
	}
	privateKey, keyPEM, err := generateKey(a.keyType)
	if err != nil {
		return issuedCertificate{}, err
	}
	csr, err := certificateRequest(privateKey, domains)
	if err != nil {
		return issuedCertificate{}, err
	}
	// CreateCertFromOrder keeps the order URL, which WaitOrder needs when the
	// CA's finalize response carries no Location header.
	chain, _, err := client.CreateCertFromOrder(ctx, order, csr, true)
	if err != nil {
		return issuedCertificate{}, fmt.Errorf("acme: finalize order: %w", err)
	}
	if len(chain) == 0 {
		return issuedCertificate{}, fmt.Errorf("acme: CA returned no certificate")
	}
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		return issuedCertificate{}, fmt.Errorf("acme: parse issued certificate: %w", err)
	}
	var certPEM []byte
	for _, der := range chain {
		certPEM = append(certPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	return issuedCertificate{
		CertificatePEM: certPEM,
		PrivateKeyPEM:  keyPEM,
		NotBefore:      leaf.NotBefore,
		NotAfter:       leaf.NotAfter,
		OrderURL:       order.URI,
	}, nil
}

// authorize completes the DNS-01 challenge of one authorization.
func (a *acmeCertifier) authorize(ctx context.Context, client *acme.Client, authzURL string) error {
	authz, err := client.GetAuthorization(ctx, authzURL)
	if err != nil {
		return fmt.Errorf("acme: get authorization: %w", err)
	}
	if authz.Status == acme.StatusValid {
		return nil
	}
	var challenge *acme.Challenge
	for _, candidate := range authz.Challenges {
		if candidate.Type == "dns-01" {
			challenge = candidate
			break
		}
	}
	if challenge == nil {
		return fmt.Errorf("acme: CA offered no dns-01 challenge for %s", authz.Identifier.Value)
	}
	value, err := client.DNS01ChallengeRecord(challenge.Token)
	if err != nil {
		return fmt.Errorf("acme: compute dns-01 key authorization: %w", err)
	}
	if err := a.solver.Present(ctx, authz.Identifier.Value, value); err != nil {
		return err
	}
	// Cleanup always runs so a failed order does not leave stale TXT records.
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_ = a.solver.Cleanup(cleanupCtx, authz.Identifier.Value, value)
	}()
	if _, err := client.Accept(ctx, challenge); err != nil {
		return fmt.Errorf("acme: accept dns-01 challenge: %w", err)
	}
	if _, err := client.WaitAuthorization(ctx, authz.URI); err != nil {
		return fmt.Errorf("acme: wait for authorization %s: %w", authz.Identifier.Value, err)
	}
	return nil
}

// account loads (or creates and registers) the ACME account for the directory.
func (a *acmeCertifier) account(ctx context.Context) (*AccountRecord, crypto.Signer, error) {
	record, err := a.store.LoadAccount(a.directoryURL)
	if err != nil {
		return nil, nil, err
	}
	var key crypto.Signer
	if record != nil && len(record.PrivateKeyPEM) > 0 {
		parsed, err := parsePrivateKey(record.PrivateKeyPEM)
		if err != nil {
			return nil, nil, fmt.Errorf("acme: load account key: %w", err)
		}
		key = parsed
	}
	if key == nil {
		generated, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, nil, fmt.Errorf("acme: generate account key: %w", err)
		}
		key = generated
	}
	if record != nil && record.URL != "" {
		return record, key, nil
	}
	client := &acme.Client{Key: key, DirectoryURL: a.directoryURL, HTTPClient: a.httpClient}
	account := &acme.Account{}
	if a.email != "" {
		account.Contact = []string{"mailto:" + a.email}
	}
	if a.eabKID != "" {
		account.ExternalAccountBinding = &acme.ExternalAccountBinding{KID: a.eabKID, Key: []byte(a.eabHMACKey)}
	}
	registered, err := client.Register(ctx, account, func(string) bool { return a.acceptTOS })
	if err != nil && err != acme.ErrAccountAlreadyExists {
		return nil, nil, fmt.Errorf("acme: register account: %w", err)
	}
	url := ""
	if registered != nil {
		url = registered.URI
	}
	if url == "" && record != nil {
		url = record.URL
	}
	if url == "" {
		return nil, nil, fmt.Errorf("acme: registration returned no account URL")
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("acme: encode account key: %w", err)
	}
	saved := AccountRecord{
		DirectoryURL:  a.directoryURL,
		PrivateKeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		URL:           url,
		Contact:       account.Contact,
	}
	if err := a.store.SaveAccount(saved); err != nil {
		return nil, nil, err
	}
	return &saved, key, nil
}

// generateKey returns a new certificate key and its PKCS#8 PEM encoding.
func generateKey(keyType string) (crypto.Signer, []byte, error) {
	var key crypto.Signer
	var err error
	switch keyType {
	case KeyTypeRSA2048:
		key, err = rsa.GenerateKey(rand.Reader, 2048)
	default:
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("acme: generate certificate key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("acme: encode certificate key: %w", err)
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// certificateRequest builds the CSR for one SAN order.
func certificateRequest(key crypto.Signer, domains []string) ([]byte, error) {
	if len(domains) == 0 {
		return nil, fmt.Errorf("acme: no domains for certificate request")
	}
	template := &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: domains[0]},
		DNSNames: append([]string(nil), domains...),
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	if err != nil {
		return nil, fmt.Errorf("acme: create certificate request: %w", err)
	}
	return csr, nil
}

// parsePrivateKey accepts the PKCS#8 and SEC1 encodings we write and the
// PKCS#1 form older tooling stores.
func parsePrivateKey(pemBytes []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if signer, ok := key.(crypto.Signer); ok {
			return signer, nil
		}
		return nil, fmt.Errorf("unsupported private key type %T", key)
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, fmt.Errorf("unsupported private key encoding")
}

// normalizeDirectoryURL trims a directory URL and rejects one that already
// carries an ACME path suffix by accident.
func normalizeDirectoryURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

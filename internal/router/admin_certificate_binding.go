// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"net/http"
)

// validateAdminCertificateBoundToken implements RFC 8705's x5t#S256 possession
// check. The caller must first verify the access token and pass its cnf claim,
// then translate false to sender_constraint_failed. No identity or authorization
// grant is derived here: the verified token supplies the mapped principal.
//
// Native TLS handshake verification is mandatory. The currentRoots callback is
// invoked on every request (including old connections), and its immutable pool
// is used for independent validity/chain/clientAuth revalidation. Forwarded
// certificate headers are never read. Pass the same root source as the listener.
func validateAdminCertificateBoundToken(ctx context.Context, r *http.Request, currentRoots func() *x509.CertPool, thumbprint string) bool {
	if ctx.Err() != nil || r == nil || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 || len(r.TLS.PeerCertificates) > 16 || currentRoots == nil {
		return false
	}
	leaf := r.TLS.PeerCertificates[0]
	if leaf == nil {
		return false
	}
	digest := sha256.Sum256(leaf.Raw)
	// Exact raw-url encoding enforces RFC 8705's no-padding/no-whitespace syntax.
	expected := base64.RawURLEncoding.EncodeToString(digest[:])
	if subtle.ConstantTimeCompare([]byte(expected), []byte(thumbprint)) != 1 {
		return false
	}
	roots := currentRoots()
	if roots == nil {
		return false
	}
	intermediates := x509.NewCertPool()
	for _, cert := range r.TLS.PeerCertificates[1:] {
		if cert == nil {
			return false
		}
		intermediates.AddCert(cert)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return false
	}
	return ctx.Err() == nil
}

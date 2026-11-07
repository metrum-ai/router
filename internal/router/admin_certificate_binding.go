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
// is used for independent validity/chain/clientAuth revalidation. The leaf must
// explicitly contain clientAuth EKU and must not contain anyExtendedKeyUsage.
// Forwarded
// certificate headers are never read. Pass the same root source as the listener:
// immutable pool snapshots must be stable until a new reload publication.
func validateAdminCertificateBoundToken(ctx context.Context, r *http.Request, currentRoots func() *x509.CertPool, thumbprint string) bool {
	// Clear any earlier attempt within the same mechanism; failed possession
	// must never leave a successful factor behind.
	if report := adminCertificateCoverageFrom(ctx); report != nil {
		report.verified.Store(nil)
	}
	if ctx == nil || ctx.Err() != nil || r == nil || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 || len(r.TLS.PeerCertificates) > 16 || currentRoots == nil {
		return false
	}
	authorization := adminSingleAuthorization(r)
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
	if _, ok := verifyAdminNativeClientCertificate(ctx, r, currentRoots); !ok {
		return false
	}
	// This is proof of possession, not certificate-to-subject mapping. Evidence
	// is consumable only by this successful token mechanism on this request.
	recordAdminVerifiedCertificateBinding(ctx, authorization, digest)
	return true
}

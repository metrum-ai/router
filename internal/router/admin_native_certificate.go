// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/x509"
	"net/http"
)

// Native possession only: no identity mapping or token/emergency state. Roots
// must come from the intended listener's stable immutable union publication.
// Returning the observed pool lets a durable consumer recheck it after its ack.
func verifyAdminNativeClientCertificate(ctx context.Context, r *http.Request, currentRoots func() *x509.CertPool) (*x509.CertPool, bool) {
	if ctx == nil || ctx.Err() != nil || r == nil || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 || len(r.TLS.PeerCertificates) > 16 || currentRoots == nil {
		return nil, false
	}
	leaf := r.TLS.PeerCertificates[0]
	if leaf == nil {
		return nil, false
	}
	roots := currentRoots()
	if roots == nil {
		return nil, false
	}
	intermediates := x509.NewCertPool()
	for _, cert := range r.TLS.PeerCertificates[1:] {
		if cert == nil {
			return nil, false
		}
		intermediates.AddCert(cert)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return nil, false
	}
	if !adminLeafExplicitClientAuth(leaf) || currentRoots() != roots || ctx.Err() != nil {
		return nil, false
	}
	return roots, true
}

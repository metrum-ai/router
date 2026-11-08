// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/sha256"
	"net/http"
	"strings"
	"sync/atomic"
)

// Evidence belongs to ONE mechanism invocation, not to Principal, a connection,
// a credential cache, or the next request. No token/certificate is retained.
// Only the RFC 8705 verifier can produce this private typed evidence after
// checking possession. Its caller must already have verified the token/cnf.
type verifiedAdminCertificateBinding struct {
	authorizationDigest [32]byte
	certificateDigest   [32]byte
}
type adminCertificateCoverage struct {
	verified atomic.Pointer[verifiedAdminCertificateBinding]
}
type adminCertificateCoverageKey struct{}

func newAdminCertificateCoverageContext(ctx context.Context) (context.Context, *adminCertificateCoverage) {
	report := &adminCertificateCoverage{}
	return context.WithValue(ctx, adminCertificateCoverageKey{}, report), report
}
func adminCertificateCoverageFrom(ctx context.Context) *adminCertificateCoverage {
	if ctx == nil {
		return nil
	}
	report, _ := ctx.Value(adminCertificateCoverageKey{}).(*adminCertificateCoverage)
	return report
}
func adminSingleAuthorization(r *http.Request) string {
	if r == nil {
		return ""
	}
	var authorization string
	count := 0
	for name, values := range r.Header {
		if strings.EqualFold(name, "Authorization") {
			for _, value := range values {
				authorization = value
				count++
			}
		}
	}
	if count != 1 {
		return ""
	}
	return authorization
}
func recordAdminVerifiedCertificateBinding(ctx context.Context, authorization string, certificateDigest [32]byte) {
	report := adminCertificateCoverageFrom(ctx)
	if report == nil || ctx.Err() != nil || authorization == "" {
		return
	}
	report.verified.Store(&verifiedAdminCertificateBinding{authorizationDigest: sha256.Sum256([]byte(authorization)), certificateDigest: certificateDigest})
}
func (report *adminCertificateCoverage) consume(p Principal, r *http.Request) bool {
	proof := report.verified.Swap(nil)
	if proof == nil || (p.Kind != "oauth" && p.Kind != "federated") || r == nil || r.TLS == nil || len(r.TLS.PeerCertificates) == 0 || r.TLS.PeerCertificates[0] == nil {
		return false
	}
	authorization := adminSingleAuthorization(r)
	return authorization != "" && proof.authorizationDigest == sha256.Sum256([]byte(authorization)) && proof.certificateDigest == sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
}

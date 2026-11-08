// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http/httptest"
	"testing"
)

func TestRuntimeCertificateBindingRejectsMidProofRootPublication(t *testing.T) {
	ca, key, roots := certificateBindingCA(t)
	_, _, replacement := certificateBindingCA(t)
	leaf := certificateBindingLeaf(t, ca, key, "valid")
	r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
	r.Header.Set("Authorization", "Bearer token")
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf, ca}}}
	ctx, coverage := newAdminCertificateCoverageContext(context.Background())
	if !validateAdminCertificateBoundToken(ctx, r, func() *x509.CertPool { return roots }, certificateBindingThumb(leaf)) {
		t.Fatal("positive proof control rejected")
	}
	calls := 0
	source := func() *x509.CertPool {
		calls++
		if calls == 1 {
			return roots
		}
		return replacement
	}
	if validateAdminCertificateBoundToken(ctx, r, source, certificateBindingThumb(leaf)) {
		t.Fatal("root publication during proof verification left valid possession evidence")
	}
	if calls < 2 {
		t.Fatal("current root publication was not rechecked")
	}
	if coverage.consume(Principal{Subject: "token-owner", Kind: "oauth", Issuer: "trusted-issuer", CredentialID: "client"}, r) {
		t.Fatal("failed revalidation retained earlier successful evidence")
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http/httptest"
	"testing"
)

func TestRuntimeMTLSIssuerReloadRejectsOldConnection(t *testing.T) {
	ca, key, roots := certificateBindingCA(t)
	_, _, replacement := certificateBindingCA(t)
	leaf := runtimeMTLSLeaf(t, ca, key)
	current := roots
	source := runtimeMTLSRootSource(func() *x509.CertPool { return current })
	a, err := newAdminMTLSIssuerDispatcher([]adminMTLSTrustDomain{{Issuer: "a", Roots: source, Subjects: map[string]string{"dns:robot.example": "robot"}}})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf, ca}}}
	if p, err := a.Authenticate(r.Context(), r); err != nil || p.Subject != "robot" {
		t.Fatal("active roots rejected")
	}
	current = replacement
	if p, err := a.Authenticate(r.Context(), r); !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) {
		t.Fatal("old connection survived root removal")
	}
	// A publication while verification is in progress also invalidates the
	// captured trust decision. No timing sleeps or concurrent fake-source races.
	requestStarted := false
	calls := 0
	source = runtimeMTLSRootSource(func() *x509.CertPool {
		if !requestStarted {
			return roots
		}
		calls++
		if calls == 1 {
			return roots
		}
		return replacement
	})
	a, err = newAdminMTLSIssuerDispatcher([]adminMTLSTrustDomain{{Issuer: "a", Roots: source, Subjects: map[string]string{"dns:robot.example": "robot"}}})
	if err != nil {
		t.Fatal(err)
	}
	requestStarted = true
	if p, err := a.Authenticate(r.Context(), r); !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) || calls < 2 {
		t.Fatal("reload during authentication returned stale identity")
	}
}

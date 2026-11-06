// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMachineAuthenticatorEmergencyCannotMaskBearer(t *testing.T) {
	r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
	r.Header.Set("Authorization", "Bearer invalid")
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
	emergency := testMachineAuthenticator(func(context.Context, *http.Request) (Principal, error) {
		return Principal{Subject: "emergency-cert", Kind: "emergency"}, nil
	})
	mtls := testMachineAuthenticator(func(context.Context, *http.Request) (Principal, error) {
		return Principal{Subject: "cert", Kind: "mtls"}, nil
	})
	for _, validator := range []Authenticator{emergency, WithEmergencyCredentialType(emergency, EmergencyCertificate)} {
		if p, err := NewAuthenticatorChain(validator, mtls).Authenticate(r.Context(), r); !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) {
			t.Fatalf("emergency certificate masked bearer: %+v %v", p, err)
		}
	}
	// Each explicitly declared emergency class succeeds on its own.
	r.Header.Del("Authorization")
	if p, err := NewAuthenticatorChain(WithEmergencyCredentialType(emergency, EmergencyCertificate)).Authenticate(r.Context(), r); err != nil || p.Kind != "emergency" {
		t.Fatalf("emergency cert rejected: %+v %v", p, err)
	}
	r.TLS = nil
	r.Header.Set("Authorization", "Bearer valid-emergency")
	if p, err := NewAuthenticatorChain(WithEmergencyCredentialType(emergency, EmergencyBearerKey)).Authenticate(r.Context(), r); err != nil || p.Kind != "emergency" {
		t.Fatalf("emergency key rejected: %+v %v", p, err)
	}
}

func TestMachineAuthenticatorCoveredCredentials(t *testing.T) {
	for _, scheme := range []string{"Bearer", "DPoP"} {
		t.Run(scheme, func(t *testing.T) {
			r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
			r.Header.Set("Authorization", scheme+" valid")
			if scheme == "DPoP" {
				r.Header.Set("DPoP", "valid-proof")
			}
			r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
			want := Principal{Subject: "token", Kind: "oauth"}
			chain := NewAuthenticatorChain(
				testMachineAuthenticator(func(context.Context, *http.Request) (Principal, error) { return want, nil }),
				testMachineAuthenticator(func(context.Context, *http.Request) (Principal, error) {
					return Principal{Subject: "certificate", Kind: "mtls"}, nil
				}),
			)
			if got, err := chain.Authenticate(r.Context(), r); err != nil || got != want {
				t.Fatalf("covered credentials rejected: %+v %v", got, err)
			}
		})
	}
}

func TestMachineAuthenticatorUnconsumedCredential(t *testing.T) {
	for _, scheme := range []string{"Bearer", "DPoP"} {
		t.Run(scheme, func(t *testing.T) {
			r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
			r.Header.Set("Authorization", scheme+" invalid")
			if scheme == "DPoP" {
				r.Header.Set("DPoP", "unverified-proof")
			}
			chain := NewAuthenticatorChain(testMachineAuthenticator(func(context.Context, *http.Request) (Principal, error) {
				return Principal{Subject: "cert", Kind: "mtls"}, nil
			}))
			if p, err := chain.Authenticate(r.Context(), r); !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) {
				t.Fatalf("unconsumed token accepted: %+v %v", p, err)
			}
		})
	}
	r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
	r.Header.Set("Authorization", "Bearer valid-key")
	chain := NewAuthenticatorChain(testMachineAuthenticator(func(context.Context, *http.Request) (Principal, error) {
		return Principal{Subject: "key", Kind: "adminkey"}, nil
	}))
	if p, err := chain.Authenticate(r.Context(), r); !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) {
		t.Fatalf("unconsumed certificate accepted: %+v %v", p, err)
	}
}

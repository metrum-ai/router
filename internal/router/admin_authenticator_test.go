// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type testMachineAuthenticator func(context.Context, *http.Request) (Principal, error)

func (f testMachineAuthenticator) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	return f(ctx, r)
}

func TestPrincipalContext(t *testing.T) {
	ctx := context.Background()
	if _, ok := PrincipalFrom(ctx); ok {
		t.Fatal("empty context has principal")
	}
	want := Principal{Subject: "automation", Kind: "oauth", CredentialID: "client-1", Issuer: "https://issuer.example"}
	ctx = WithPrincipal(ctx, want)
	if got, ok := PrincipalFrom(ctx); !ok || got != want {
		t.Fatalf("principal: %+v, %v", got, ok)
	}
	if _, ok := PrincipalFrom(context.Background()); ok {
		t.Fatal("principal leaked to another context")
	}
}

func TestMachineAuthenticatorChain(t *testing.T) {
	first := Principal{Subject: "first", Kind: "mtls"}
	second := Principal{Subject: "second", Kind: "oauth"}
	for _, tc := range []struct {
		name       string
		results    []error
		principals []Principal
		want       Principal
		err        error
		calls      int
	}{
		{"missing", []error{ErrNoCredential, ErrNoCredential}, []Principal{{}, {}}, Principal{}, ErrNoCredential, 2},
		{"skip absent", []error{ErrNoCredential, nil}, []Principal{{}, second}, second, nil, 2},
		{"first wins", []error{nil, nil}, []Principal{first, second}, first, nil, 2},
		{"invalid never falls through", []error{ErrInvalidCredential, nil}, []Principal{{}, second}, Principal{}, ErrInvalidCredential, 1},
		{"invalid extra credential", []error{nil, ErrInvalidCredential}, []Principal{first, {}}, Principal{}, ErrInvalidCredential, 2},
		{"unexpected error fails closed", []error{errors.New("secret must not escape"), nil}, []Principal{{}, second}, Principal{}, ErrInvalidCredential, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var mechanisms []Authenticator
			for i := range tc.results {
				mechanisms = append(mechanisms, testMachineAuthenticator(func(context.Context, *http.Request) (Principal, error) {
					calls++
					return tc.principals[i], tc.results[i]
				}))
			}
			got, err := NewAuthenticatorChain(mechanisms...).Authenticate(context.Background(), httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil))
			if got != tc.want || !errors.Is(err, tc.err) || calls != tc.calls {
				t.Fatalf("got %+v, %v, %d calls; want %+v, %v, %d", got, err, calls, tc.want, tc.err, tc.calls)
			}
		})
	}
}

func TestMachineAuthenticatorRejectsForbiddenEnvelopes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers http.Header
		query   string
	}{
		{"basic", http.Header{"Authorization": {"Basic dXNlcjpwYXNz"}}, ""},
		{"cookie", http.Header{"Cookie": {"session=stolen"}}, ""},
		{"duplicate authorization", http.Header{"Authorization": {"Bearer first", "Bearer second"}}, ""},
		{"unknown scheme", http.Header{"Authorization": {"Digest x"}}, ""},
		{"empty bearer", http.Header{"Authorization": {"Bearer "}}, ""},
		{"query token", nil, "?access_token=stolen"},
		{"oversized bearer", http.Header{"Authorization": {"Bearer " + strings.Repeat("x", maxMachineCredentialBytes)}}, ""},
		{"proof without token", http.Header{"DPoP": {"proof"}}, ""},
		{"dpop without proof", http.Header{"Authorization": {"DPoP token"}}, ""},
		{"bearer with proof", http.Header{"Authorization": {"Bearer token"}, "DPoP": {"proof"}}, ""},
		{"duplicate proof", http.Header{"Authorization": {"DPoP token"}, "DPoP": {"first", "second"}}, ""},
		{"oversized proof", http.Header{"Authorization": {"DPoP token"}, "DPoP": {strings.Repeat("x", maxMachineCredentialBytes+1)}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			chain := NewAuthenticatorChain(testMachineAuthenticator(func(context.Context, *http.Request) (Principal, error) {
				called = true
				return Principal{Subject: "should-not-run", Kind: "adminkey"}, nil
			}))
			r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config"+tc.query, nil)
			r.Header = tc.headers
			p, err := chain.Authenticate(r.Context(), r)
			if err == nil || p != (Principal{}) || called {
				t.Fatalf("forbidden envelope reached authenticator: %+v %v called=%v", p, err, called)
			}
		})
	}
}

func TestMachineAuthenticatorChainEmptyAndCancellation(t *testing.T) {
	r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
	if _, err := NewAuthenticatorChain().Authenticate(r.Context(), r); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("empty chain: %v", err)
	}
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	called := false
	chain := NewAuthenticatorChain(testMachineAuthenticator(func(context.Context, *http.Request) (Principal, error) {
		called = true
		return Principal{Subject: "x", Kind: "oauth"}, nil
	}))
	if p, err := chain.Authenticate(ctx, r); err == nil || p != (Principal{}) || called {
		t.Fatal("cancelled request authenticated")
	}
}

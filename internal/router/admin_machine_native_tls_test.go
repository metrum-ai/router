// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Local TLS/authentication pipeline only: it binds certificate-bound tokens to
// real client chains and root removal. It is not an end-to-end test of the admin
// listener, CLI bootstrap or the durable event adapters.
func TestRuntimeNativeTLSBoundTokenAndRootRemoval(t *testing.T) {
	a, key := runtimeJWTIssuerFixture(t, "token-owner", "federated")
	ca, caKey, roots := certificateBindingCA(t)
	_, _, replacement := certificateBindingCA(t)
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(123), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	var current atomic.Pointer[x509.CertPool]
	current.Store(roots)
	source := runtimeMTLSRootSource(current.Load)
	tokens, err := newAdminMachineJWTDispatcher([]adminMachineJWTBinding{{Authenticator: a}}, nil, source.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := newAdminMTLSIssuerDispatcher([]adminMTLSTrustDomain{{Issuer: "native-trust", Roots: source, Subjects: map[string]string{"dns:unrelated.example": "unrelated"}}})
	if err != nil {
		t.Fatal(err)
	}
	chain := NewAuthenticatorChain(tokens, identity)
	var mu sync.Mutex
	var remoteAddresses []string
	handler := adminMachineAuthenticationMiddleware(chain)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFrom(r.Context())
		if !ok || p.Subject != "token-owner" || p.Kind != "federated" {
			t.Error("wrong authenticated identity")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		remoteAddresses = append(remoteAddresses, r.RemoteAddr)
		mu.Unlock()
		handler.ServeHTTP(w, r)
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}
	server.StartTLS()
	t.Cleanup(server.Close)
	client := server.Client()
	transport := client.Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.Certificates = []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: leafKey}}
	transport.MaxConnsPerHost = 1
	transport.ForceAttemptHTTP2 = false
	client.Transport = transport
	t.Cleanup(transport.CloseIdleConnections)
	claims := runtimeJWTClaims(a.cfg.Issuer)
	claims["cnf"] = map[string]string{"x5t#S256": certificateBindingThumb(leaf)}
	raw := machineSignedJWT(t, key, "known", claims)
	request := func(token string, want int) {
		t.Helper()
		r, err := http.NewRequest("GET", server.URL+"/admin/v1/config", nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal("native TLS request failed", err)
		}
		_, readErr := io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != want {
			t.Errorf("native TLS auth status=%d want=%d", response.StatusCode, want)
		}
	}
	request(raw, http.StatusNoContent)
	request("", http.StatusUnauthorized)
	current.Store(replacement)
	request(raw, http.StatusUnauthorized)
	// All three requests must use the same completed native TLS handshake; the
	// removal denial must not be explained by a fresh handshake or fake TLS state.
	mu.Lock()
	defer mu.Unlock()
	if len(remoteAddresses) != 3 || remoteAddresses[0] != remoteAddresses[1] || remoteAddresses[1] != remoteAddresses[2] {
		t.Fatal("did not exercise the same existing TLS connection")
	}
}

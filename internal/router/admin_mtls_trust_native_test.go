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
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Grouped store's real native TLS + JWT pipeline, not actual AdminPlane
// bootstrap, production events/authorization dispatch or routerctl binary.
func TestRuntimeGroupedUnionNativeTLSRemoval(t *testing.T) {
	caA, keyA, _ := certificateBindingCA(t)
	caB, _, _ := certificateBindingCA(t)
	paths := []string{filepath.Join(t.TempDir(), "a.pem"), filepath.Join(t.TempDir(), "b.pem")}
	trustStoreWriteCA(t, paths[0], caA)
	trustStoreWriteCA(t, paths[1], caB)
	cfg := AdminMTLSTrustConfig{TrustDomains: []AdminMTLSTrustDomainConfig{{Issuer: "a", CAFiles: []string{paths[0]}}, {Issuer: "b", CAFiles: []string{paths[1]}}}}
	store, err := NewAdminMTLSTrustStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(234), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, caA, &leafKey.PublicKey, keyA)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	issuer, key := runtimeJWTIssuerFixture(t, "token-owner", "federated")
	tokens, err := newAdminMachineJWTDispatcher([]adminMachineJWTBinding{{Authenticator: issuer}}, nil, store.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := newAdminMTLSTrustAuthenticator(store)
	if err != nil {
		t.Fatal(err)
	}
	middleware := adminMachineAuthenticationMiddleware(NewAuthenticatorChain(tokens, identity))
	var mu sync.Mutex
	var addresses []string
	dispatches := 0
	operation := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFrom(r.Context())
		if !ok || p.Subject != "token-owner" {
			t.Error("wrong token identity")
		}
		mu.Lock()
		dispatches++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		addresses = append(addresses, r.RemoteAddr)
		mu.Unlock()
		operation.ServeHTTP(w, r)
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: store.Snapshot()}
	server.TLS.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		current := server.TLS.Clone()
		current.GetConfigForClient = nil
		current.ClientCAs = store.Snapshot()
		return current, nil
	}
	server.StartTLS()
	defer server.Close()
	client := server.Client()
	transport := client.Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.Certificates = []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: leafKey}}
	transport.MaxConnsPerHost = 1
	transport.ForceAttemptHTTP2 = false
	client.Transport = transport
	client.Timeout = 5 * time.Second
	defer transport.CloseIdleConnections()
	claims := runtimeJWTClaims(issuer.cfg.Issuer)
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
			t.Fatal(err)
		}
		_, readErr := io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != want {
			t.Fatalf("grouped native status=%d want=%d", response.StatusCode, want)
		}
	}
	request(raw, http.StatusNoContent)
	request("", http.StatusUnauthorized)
	if err := store.ReloadConfig(AdminMTLSTrustConfig{TrustDomains: []AdminMTLSTrustDomainConfig{cfg.TrustDomains[1]}}); err != nil {
		t.Fatal(err)
	}
	request(raw, http.StatusUnauthorized)
	mu.Lock()
	same := len(addresses) == 3 && addresses[0] == addresses[1] && addresses[1] == addresses[2] && dispatches == 1
	mu.Unlock()
	if !same {
		t.Fatal("did not exercise same native connection with exactly one valid dispatch")
	}
	transport.CloseIdleConnections()
	// The current union also governs a NEW handshake, not only HTTP revalidation.
	r, err := http.NewRequest("GET", server.URL+"/admin/v1/config", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+raw)
	if response, err := client.Do(r); err == nil {
		_ = response.Body.Close()
		t.Fatal("removed root accepted at fresh handshake")
	}
}

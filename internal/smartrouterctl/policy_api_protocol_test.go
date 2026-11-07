// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package smartrouterctl

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestPolicyClientUsesFreshHTTP1WhenServerOffersHTTP2(t *testing.T) {
	t.Setenv("ROUTER_ADMIN_KEY", "test-only-policy-protocol-key")
	t.Setenv("ROUTER_ADMIN_KEY_FILE", "")
	var connections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || r.TLS.NegotiatedProtocol != "http/1.1" {
			t.Error("policy TLS did not negotiate HTTP1 ALPN")
		}
		if r.ProtoMajor != 1 {
			t.Error("policy transport negotiated replay-capable HTTP2")
		}
		w.Write([]byte(`{}`))
	}))
	server.EnableHTTP2 = true
	server.TLS = &tls.Config{NextProtos: []string{"h2", "http/1.1"}, MinVersion: tls.VersionTLS12}
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.StartTLS()
	defer server.Close()
	client, err := NewPolicyAPIClient(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	transport := client.admin.HTTP.Transport.(*http.Transport)
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	// Preserve inherited ALPN; only trust this fixture's certificate.
	transport.TLSClientConfig.RootCAs = roots
	transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	for range 2 {
		if _, err := client.Call("get", "one", nil, PolicyCallOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if connections.Load() != 2 {
		t.Fatalf("policy calls reused connections: new=%d", connections.Load())
	}
}

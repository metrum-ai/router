// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestAdminMachineJWTActualDialPolicy(t *testing.T) {
	// Check the real dialer's Control hook, not just URL-literal filtering, so
	// private addresses reached through DNS (including mapped IPv4) are denied.
	for _, address := range []string{"127.0.0.1:443", "10.0.0.1:443", "172.16.0.1:443", "192.168.1.1:443", "169.254.169.254:443", "0.0.0.0:443", "[::]:443", "[::1]:443", "[fc00::1]:443", "[fe80::1]:443", "[::ffff:127.0.0.1]:443", "224.0.0.1:443"} {
		if err := machineJWTDialControl(false)("tcp", address, nil); err == nil {
			t.Fatalf("private/special dial target allowed: %s", address)
		}
		if err := machineJWTDialControl(true)("tcp", address, nil); err != nil {
			t.Fatalf("explicit private-network opt-in ignored: %s", address)
		}
	}
	for _, address := range []string{"8.8.8.8:443", "[2606:4700:4700::1111]:443"} {
		if err := machineJWTDialControl(false)("tcp", address, nil); err != nil {
			t.Fatalf("public dial target rejected: %s", address)
		}
	}
	cfg := adminJWTProfileConfig()
	cfg.allowInsecureLoopback = false
	baseTransport := http.DefaultTransport.(*http.Transport).Clone()
	baseTransport.Proxy = http.ProxyURL(&url.URL{Scheme: "http", Host: "127.0.0.1:1"})
	baseTransport.DialContext = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("unprotected test dialer")
	}
	baseTransport.DialTLSContext = baseTransport.DialContext
	baseTransport.DialTLS = func(string, string) (net.Conn, error) { return nil, errors.New("unprotected test TLS dialer") }
	base := &http.Client{Transport: baseTransport}
	strict, err := newMachineJWTHTTPClient(cfg, base)
	if err != nil {
		t.Fatal(err)
	}
	transport := strict.Transport.(*http.Transport)
	if transport == baseTransport || transport.Proxy != nil || transport.DialTLSContext != nil || transport.DialTLS != nil {
		t.Fatal("transport can bypass issuer-specific address policy")
	}
	if baseTransport.Proxy == nil || baseTransport.DialTLSContext == nil || baseTransport.DialTLS == nil {
		t.Fatal("issuer construction mutated caller transport")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if conn, err := transport.DialContext(ctx, "tcp", "127.0.0.1:443"); !errors.Is(err, errMachineJWTNetworkDestination) {
		if conn != nil {
			conn.Close()
		}
		t.Fatalf("actual dialer bypassed Control hook: %v", err)
	}
	// Enabling the next issuer must not change the strict issuer's transport.
	cfg.AllowPrivateNetwork = true
	relaxed, err := newMachineJWTHTTPClient(cfg, base)
	if err != nil {
		t.Fatal(err)
	}
	if relaxed.Transport == strict.Transport {
		t.Fatal("issuers share mutable transport policy")
	}
	if conn, err := transport.DialContext(ctx, "tcp", "127.0.0.1:443"); !errors.Is(err, errMachineJWTNetworkDestination) {
		if conn != nil {
			conn.Close()
		}
		t.Fatalf("private opt-in leaked to another issuer: %v", err)
	}
}

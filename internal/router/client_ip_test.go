// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http/httptest"
	"testing"
)

func TestResolveClientIPWalksTrustedForwardedChain(t *testing.T) {
	cfg := ClientIPConfig{TrustedProxyCIDRs: []string{"10.0.0.0/8"}, HeaderOrder: []string{"X-Forwarded-For"}}
	tests := []struct {
		name, remote, forwarded, want string
		trusted                       bool
	}{
		{"proxy appends actual peer after client spoof", "10.0.0.2:443", "198.51.100.99, 203.0.113.10", "203.0.113.10", true},
		{"walk across trusted proxy hops", "10.0.0.2:443", "198.51.100.99, 203.0.113.11, 10.0.0.1", "203.0.113.11", true},
		{"all forwarded hops trusted uses leftmost", "10.0.0.2:443", "10.0.0.3, 10.0.0.1", "10.0.0.3", true},
		{"untrusted socket peer cannot assert forwarded address", "203.0.113.20:443", "198.51.100.99, 203.0.113.10", "203.0.113.20", false},
		{"malformed forwarded values fall back to trusted peer", "10.0.0.2:443", "not-an-ip", "10.0.0.2", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.remote
			r.Header.Set("X-Forwarded-For", tc.forwarded)
			got := resolveClientIP(r, cfg)
			if got.Address != tc.want || got.TrustedProxyApplied != tc.trusted {
				t.Fatalf("resolved client IP = %+v, want %s", got, tc.want)
			}
		})
	}
}

func TestResolveClientIPIgnoresForwardedChainWithoutTrustedProxy(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.20:443"
	r.Header.Set("X-Forwarded-For", "198.51.100.99")

	got := resolveClientIP(r, ClientIPConfig{HeaderOrder: []string{"X-Forwarded-For"}})
	if got.Address != "203.0.113.20" || got.TrustedProxyApplied {
		t.Fatalf("resolved client IP = %+v, want untrusted socket peer", got)
	}
}

func TestCallerBindingCIDRMatchesIPv4MappedIPv6(t *testing.T) {
	caller := &callerRuntime{cfg: CallerConfig{ID: "worker", Binding: CallerBindingConfig{
		Mode: "enforce", AllowedCIDRs: []string{"10.0.0.0/8"},
	}}}
	request := httptest.NewRequest("GET", "/", nil)
	if got := callerBindingMismatch(caller, request, clientIPInfo{Address: "::ffff:10.0.0.5"}, ClientIPConfig{}); got != "" {
		t.Fatalf("IPv4-mapped IPv6 address rejected by IPv4 CIDR: %q", got)
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

var errMachineJWTNetworkDestination = errors.New("admin JWT network destination forbidden")

// Special-use ranges must not provide alternate paths into private networks
// (notably IPv4-compatible, translation and transition IPv6 ranges).
var machineJWTSpecialNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"), netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("3fff::/20"),
}

func machineJWTForbiddenIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.Zone() != "" {
		return true
	}
	for _, prefix := range machineJWTSpecialNetworks {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// Control receives the actual resolved socket address. Checking URL names or
// resolving once before Dial is insufficient against DNS rebinding.
func machineJWTDialControl(allowPrivate bool) func(string, string, syscall.RawConn) error {
	return func(_ string, address string, _ syscall.RawConn) error {
		if allowPrivate {
			return nil
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return errMachineJWTNetworkDestination
		}
		ip, err := netip.ParseAddr(host)
		if err != nil || machineJWTForbiddenIP(ip) {
			return errMachineJWTNetworkDestination
		}
		return nil
	}
}

func newMachineJWTHTTPClient(cfg adminMachineJWTConfig, provided *http.Client) (*http.Client, error) {
	if err := normalizeMachineJWTJWKSSettings(&cfg); err != nil {
		return nil, err
	}
	client := &http.Client{}
	if provided != nil {
		*client = *provided
	}
	source := client.Transport
	if source == nil {
		source = http.DefaultTransport
	}
	if transport, ok := source.(*http.Transport); ok {
		transport = transport.Clone()
		// Environment proxies and custom TLS dialers must not bypass the protected
		// origin dialer. Each issuer owns an independent transport/policy snapshot.
		transport.Proxy = nil
		transport.Dial = nil
		transport.DialTLS = nil
		transport.DialTLSContext = nil
		transport.DialContext = (&net.Dialer{Timeout: cfg.JWKSRequestTimeout, KeepAlive: 30 * time.Second, Control: machineJWTDialControl(cfg.AllowPrivateNetwork)}).DialContext
		client.Transport = transport
	} else if !cfg.allowInsecureLoopback {
		// Nonstandard transports are permitted only by the private test hook. The
		// runtime has no configuration option that enables this exception.
		return nil, errors.New("admin JWT HTTP transport invalid")
	}
	client.Jar = nil
	client.Timeout = cfg.JWKSRequestTimeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("admin JWT redirects forbidden") }
	return client, nil
}

func machineJWTAuthority(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.Unmap().String()
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	return net.JoinHostPort(host, port)
}
func machineJWTAllowedHost(raw string) (string, bool) {
	if strings.ContainsAny(raw, "/?#@*") {
		return "", false
	}
	u, err := url.Parse("https://" + raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.TrimSpace(raw) != raw {
		return "", false
	}
	if strings.Contains(u.Hostname(), ":") {
		if _, err := netip.ParseAddr(u.Hostname()); err != nil {
			return "", false
		}
	}
	return machineJWTAuthority(u), true
}
func machineJWTEndpointAllowed(cfg adminMachineJWTConfig, raw string) bool {
	if !machineIssuerEndpoint(raw, cfg.allowInsecureLoopback) {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !cfg.AllowPrivateNetwork && machineJWTForbiddenIP(ip) {
		return false
	}
	issuer, err := url.Parse(cfg.Issuer)
	if err != nil {
		return false
	}
	authority := machineJWTAuthority(u)
	if authority == machineJWTAuthority(issuer) {
		return true
	}
	for _, host := range cfg.AllowedJWKSHosts {
		if allowed, ok := machineJWTAllowedHost(host); ok && allowed == authority {
			return true
		}
	}
	return false
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type egressPolicyError struct {
	label string
	msg   string
}

func (e *egressPolicyError) Error() string {
	return e.label + " " + e.msg
}

// egressLookupIPFunc resolves a hostname for connect-time destination checks.
// Tests inject a stub; production uses the system resolver.
type egressLookupIPFunc func(ctx context.Context, host string) ([]net.IP, error)

type egressHTTPClientOptions struct {
	Timeout     time.Duration
	AllowHosts  []string
	AllowHTTP   bool
	Label       string
	LookupIP    egressLookupIPFunc
	DialContext func(ctx context.Context, network, address string) (net.Conn, error)
}

func validateEgressURL(u *url.URL, allowHosts []string, allowHTTP bool, label string) error {
	if u == nil || u.Hostname() == "" {
		return &egressPolicyError{label: label, msg: "URL is invalid"}
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		if err := validateEgressHost(u.Hostname(), allowHosts, label); err != nil {
			return err
		}
		return validateEgressPort(u, label)
	case "http":
		if !allowHTTP && !egressHostIsTrustedLocal(u.Hostname()) {
			return &egressPolicyError{label: label, msg: "http requires allow_http for non-local hosts"}
		}
		if err := validateEgressHost(u.Hostname(), allowHosts, label); err != nil {
			return err
		}
		return validateEgressPort(u, label)
	default:
		return &egressPolicyError{label: label, msg: "URL scheme must be http or https"}
	}
}

func validateEgressHost(host string, allowHosts []string, label string) error {
	if !scriptHostAllowed(host, allowHosts) {
		return &egressPolicyError{label: label, msg: fmt.Sprintf("host %s is not allowed", host)}
	}
	return nil
}

// validateEgressPort enforces default-deny ports for non-local hosts.
// HTTPS may use only 443 (or the scheme default). HTTP may use only 80 (or the
// scheme default). Trusted local hosts may use any explicit port so loopback
// sidecars and httptest demos keep working.
func validateEgressPort(u *url.URL, label string) error {
	port := u.Port()
	if port == "" {
		return nil
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return &egressPolicyError{label: label, msg: "port is invalid"}
	}
	if egressHostIsTrustedLocal(u.Hostname()) {
		return nil
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "https":
		if p != 443 {
			return &egressPolicyError{label: label, msg: fmt.Sprintf("port %d is not allowed", p)}
		}
	case "http":
		if p != 80 {
			return &egressPolicyError{label: label, msg: fmt.Sprintf("port %d is not allowed", p)}
		}
	default:
		return &egressPolicyError{label: label, msg: "URL scheme must be http or https"}
	}
	return nil
}

func newEgressHTTPClient(timeout time.Duration, allowHosts []string, allowHTTP bool, label string) *http.Client {
	return newEgressHTTPClientWithOptions(egressHTTPClientOptions{
		Timeout:    timeout,
		AllowHosts: allowHosts,
		AllowHTTP:  allowHTTP,
		Label:      label,
	})
}

func newEgressHTTPClientWithOptions(opts egressHTTPClientOptions) *http.Client {
	lookup := opts.LookupIP
	if lookup == nil {
		lookup = defaultEgressLookupIP
	}
	dial := opts.DialContext
	if dial == nil {
		var d net.Dialer
		dial = d.DialContext
	}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, &egressPolicyError{label: opts.Label, msg: "destination address is invalid"}
			}
			if err := validateEgressDialPort(host, port, opts.Label); err != nil {
				return nil, err
			}
			if err := validateEgressDialHost(host, opts.AllowHosts, opts.Label); err != nil {
				return nil, err
			}
			ips, err := lookup(ctx, host)
			if err != nil || len(ips) == 0 {
				return nil, &egressPolicyError{label: opts.Label, msg: "host could not be resolved"}
			}
			var lastErr error
			for _, ip := range ips {
				if !egressIPAllowedForHost(host, ip) {
					lastErr = &egressPolicyError{label: opts.Label, msg: "destination IP is not allowed"}
					continue
				}
				addr := net.JoinHostPort(ip.String(), port)
				conn, dialErr := dial(ctx, network, addr)
				if dialErr == nil {
					return conn, nil
				}
				lastErr = dialErr
			}
			if lastErr == nil {
				lastErr = &egressPolicyError{label: opts.Label, msg: "destination IP is not allowed"}
			}
			return nil, lastErr
		},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{
		Timeout:   opts.Timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return &egressPolicyError{label: opts.Label, msg: "too many redirects"}
			}
			if err := validateEgressURL(req.URL, opts.AllowHosts, opts.AllowHTTP, opts.Label); err != nil {
				return err
			}
			// Strengthen redirect policy: reject hops whose hostname is not on
			// the allowlist even when the URL parser accepted the Location.
			if err := validateEgressHost(req.URL.Hostname(), opts.AllowHosts, opts.Label); err != nil {
				return err
			}
			return nil
		},
	}
}

func validateEgressDialHost(host string, allowHosts []string, label string) error {
	host = strings.TrimSuffix(strings.TrimSpace(host), ".")
	if host == "" {
		return &egressPolicyError{label: label, msg: "URL is invalid"}
	}
	return validateEgressHost(host, allowHosts, label)
}

func validateEgressDialPort(host, port, label string) error {
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return &egressPolicyError{label: label, msg: "port is invalid"}
	}
	if egressHostIsTrustedLocal(host) {
		return nil
	}
	if p != 80 && p != 443 {
		return &egressPolicyError{label: label, msg: fmt.Sprintf("port %d is not allowed", p)}
	}
	return nil
}

func defaultEgressLookupIPImpl(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	out := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		if addr.IP != nil {
			out = append(out, addr.IP)
		}
	}
	return out, nil
}

// defaultEgressLookupIP is the shared hostname resolver for image-URL and
// egress checks. Tests may replace it so parallel suites do not depend on the
// runner's live DNS.
var defaultEgressLookupIP egressLookupIPFunc = defaultEgressLookupIPImpl

// egressIPAllowedForHost applies connect-time destination policy.
// Trusted local hostnames may reach only loopback addresses. All other
// allowlisted hostnames must resolve to global unicast addresses outside
// private, link-local, and metadata ranges.
func egressIPAllowedForHost(host string, ip net.IP) bool {
	if ip == nil {
		return false
	}
	if egressHostIsTrustedLocal(host) {
		return ip.IsLoopback()
	}
	return !egressIPIsBlockedDestination(ip)
}

func egressIPIsBlockedDestination(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	if !ip.IsGlobalUnicast() {
		return true
	}
	for _, network := range egressBlockedDestinationNetworks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// Extra reserved ranges beyond Go's IsPrivate / IsLinkLocal helpers.
// Documentation TEST-NET ranges are intentionally omitted so unit tests can
// inject recorded public addresses without opening real network egress.
var egressBlockedDestinationNetworks = mustParseCIDRs([]string{
	"0.0.0.0/8",
	"100.64.0.0/10",
	"192.0.0.0/24",
	"198.18.0.0/15",
	"240.0.0.0/4",
	"255.255.255.255/32",
	"64:ff9b::/96",
	"64:ff9b:1::/48",
	"100::/64",
	"2001::/23",
	"2002::/16",
})

func auditSafeHTTPError(err error, fallback string) error {
	var policyErr *egressPolicyError
	if errors.As(err, &policyErr) {
		return policyErr
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("%s timed out", fallback)
	}
	return fmt.Errorf("%s", fallback)
}

func egressHostIsTrustedLocal(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

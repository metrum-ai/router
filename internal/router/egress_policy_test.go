// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEgressPT1DisallowedPortRejected(t *testing.T) {
	t.Parallel()
	u, err := url.Parse("https://routing-policy.internal.example:25/route")
	if err != nil {
		t.Fatal(err)
	}
	err = validateEgressURL(u, []string{"routing-policy.internal.example"}, false, "router.fetchJSON")
	if err == nil {
		t.Fatal("expected disallowed port to be rejected")
	}
	var policyErr *egressPolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("expected egressPolicyError, got %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "port 25 is not allowed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEgressPT1DisallowedHostRejected(t *testing.T) {
	t.Parallel()
	u, err := url.Parse("https://evil.example/route")
	if err != nil {
		t.Fatal(err)
	}
	err = validateEgressURL(u, []string{"routing-policy.internal.example"}, false, "external_policy")
	if err == nil {
		t.Fatal("expected disallowed host to be rejected")
	}
	var policyErr *egressPolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("expected egressPolicyError, got %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "host evil.example is not allowed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEgressRejectsURLUserinfo(t *testing.T) {
	t.Parallel()
	u, err := url.Parse("https://user@routing-policy.internal.example/route")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateEgressURL(u, []string{"routing-policy.internal.example"}, false, "external_policy"); err == nil || !strings.Contains(err.Error(), "URL userinfo is not allowed") {
		t.Fatalf("validateEgressURL() err=%v", err)
	}
}

func TestEgressPT3RejectsPrivateDestination(t *testing.T) {
	t.Parallel()
	privateIP := net.ParseIP("10.0.0.5")
	if privateIP == nil {
		t.Fatal("parse private IP")
	}
	if egressIPAllowedForHost("routing-policy.internal.example", privateIP) {
		t.Fatal("private IP must be denied for non-local hosts")
	}

	client := newEgressHTTPClientWithOptions(egressHTTPClientOptions{
		Timeout:    time.Second,
		AllowHosts: []string{"routing-policy.internal.example"},
		AllowHTTP:  true,
		Label:      "external_policy",
		LookupIP: func(ctx context.Context, host string) ([]net.IP, error) {
			if host != "routing-policy.internal.example" {
				t.Fatalf("unexpected host lookup %q", host)
			}
			return []net.IP{privateIP}, nil
		},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			t.Fatalf("dial must not run for blocked destination %s", address)
			return nil, errors.New("unreachable")
		},
	})

	req, err := http.NewRequest(http.MethodGet, "http://routing-policy.internal.example/route", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(req)
	if err == nil {
		t.Fatal("expected private destination to be rejected")
	}
	var policyErr *egressPolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("expected egressPolicyError, got %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "destination IP is not allowed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEgressPT2RedirectToNonAllowlistedHostRejected(t *testing.T) {
	t.Parallel()
	var redirectedReached atomic.Bool
	redirected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedReached.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(redirected.Close)

	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com/secret", http.StatusFound)
	}))
	t.Cleanup(policy.Close)

	policyURL := mustLocalhostURL(t, policy.URL)
	client := newEgressHTTPClient(time.Second, []string{"localhost"}, false, "router.fetchJSON")
	req, err := http.NewRequest(http.MethodGet, policyURL+"/route", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(req)
	if err == nil {
		t.Fatal("expected redirect outside allowlist to fail")
	}
	var policyErr *egressPolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("expected egressPolicyError, got %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "host example.com is not allowed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if redirectedReached.Load() {
		t.Fatal("redirected server was reached")
	}
}

func TestEgressPT2RedirectDisallowedPortRejected(t *testing.T) {
	t.Parallel()
	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://routing-policy.internal.example:25/secret", http.StatusFound)
	}))
	t.Cleanup(policy.Close)

	policyURL := mustLocalhostURL(t, policy.URL)
	client := newEgressHTTPClient(time.Second, []string{"localhost", "routing-policy.internal.example"}, false, "router.fetchJSON")
	req, err := http.NewRequest(http.MethodGet, policyURL+"/route", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(req)
	if err == nil {
		t.Fatal("expected redirect to disallowed port to fail")
	}
	if !strings.Contains(err.Error(), "port 25 is not allowed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEgressPT3RejectsLinkLocalMetadataDestination(t *testing.T) {
	t.Parallel()
	metadataIP := net.ParseIP("169.254.169.254")
	if metadataIP == nil {
		t.Fatal("parse metadata IP")
	}
	if egressIPAllowedForHost("routing-policy.internal.example", metadataIP) {
		t.Fatal("metadata link-local IP must be denied for non-local hosts")
	}

	client := newEgressHTTPClientWithOptions(egressHTTPClientOptions{
		Timeout:    time.Second,
		AllowHosts: []string{"routing-policy.internal.example"},
		AllowHTTP:  true,
		Label:      "router.fetchJSON",
		LookupIP: func(ctx context.Context, host string) ([]net.IP, error) {
			if host != "routing-policy.internal.example" {
				t.Fatalf("unexpected host lookup %q", host)
			}
			return []net.IP{metadataIP}, nil
		},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			t.Fatalf("dial must not run for blocked destination %s", address)
			return nil, errors.New("unreachable")
		},
	})

	req, err := http.NewRequest(http.MethodGet, "http://routing-policy.internal.example/latest/meta-data/", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(req)
	if err == nil {
		t.Fatal("expected metadata destination to be rejected")
	}
	var policyErr *egressPolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("expected egressPolicyError, got %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "destination IP is not allowed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEgressPT4HTTPSAllowlistedHostSucceeds(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)

	policyURL := mustLocalhostURL(t, srv.URL)
	client := newEgressHTTPClient(time.Second, []string{"localhost"}, false, "router.fetchJSON")
	if transport, ok := client.Transport.(*http.Transport); ok {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test-only httptest cert
	}

	req, err := http.NewRequest(http.MethodGet, policyURL+"/route", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("allowlisted HTTPS request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("unexpected body %s", body)
	}
}

func TestEgressPT4HTTPSAllowlistedHostWithRecordedPublicIPSucceeds(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)

	recordedIP := net.ParseIP("203.0.113.10")
	listenerAddr := srv.Listener.Addr().String()
	client := newEgressHTTPClientWithOptions(egressHTTPClientOptions{
		Timeout:    time.Second,
		AllowHosts: []string{"routing-policy.internal.example"},
		Label:      "router.fetchJSON",
		LookupIP: func(ctx context.Context, host string) ([]net.IP, error) {
			if host != "routing-policy.internal.example" {
				t.Fatalf("unexpected host lookup %q", host)
			}
			return []net.IP{recordedIP}, nil
		},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			if host != recordedIP.String() {
				t.Fatalf("expected dial to recorded IP, got %s", address)
			}
			if port != "443" {
				t.Fatalf("expected dial port 443, got %s", port)
			}
			var d net.Dialer
			return d.DialContext(ctx, network, listenerAddr)
		},
	})
	if transport, ok := client.Transport.(*http.Transport); ok {
		transport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // test-only httptest cert
			ServerName:         "127.0.0.1",
		}
	}

	req, err := http.NewRequest(http.MethodGet, "https://routing-policy.internal.example/route", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("allowlisted HTTPS with recorded IP failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestValidateEgressURLAllowsDefaultHTTPSPort(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"https://routing-policy.internal.example/route",
		"https://routing-policy.internal.example:443/route",
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateEgressURL(u, []string{"routing-policy.internal.example"}, false, "router.fetchJSON"); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
}

func mustLocalhostURL(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	u.Host = net.JoinHostPort("localhost", u.Port())
	return u.String()
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"io"
	"net"
	"net/url"
	"strings"
	"syscall"
	"testing"
)

func TestClassifyNetworkErrorOmitsProviderURLAndAddress(t *testing.T) {
	const secretURL = "http://10.20.30.40:8443/private/v1/messages"
	cases := []struct {
		err  error
		want string
	}{
		{&url.Error{Op: "Post", URL: secretURL, Err: io.EOF}, "EOF"},
		{&url.Error{Op: "Post", URL: secretURL, Err: &net.OpError{Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.IPv4(10, 20, 30, 40), Port: 8443}, Err: syscall.ECONNREFUSED}}, "connection refused"},
		{&url.Error{Op: "Post", URL: secretURL, Err: &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}}, "connection reset"},
		{&url.Error{Op: "Post", URL: secretURL, Err: &net.DNSError{Err: "no such host", Name: "internal.provider.example"}}, "dns lookup failed"},
	}
	for _, tc := range cases {
		upErr := classifyContextOrNetworkError(context.Background(), context.Background(), tc.err)
		if upErr.Class != "upstream_network_error" || !upErr.Retryable {
			t.Fatalf("class=%q retryable=%v", upErr.Class, upErr.Retryable)
		}
		for _, leak := range []string{"10.20.30.40", "8443", "private", "internal.provider.example"} {
			if strings.Contains(upErr.Message, leak) {
				t.Fatalf("message %q leaks %q", upErr.Message, leak)
			}
		}
		if !strings.Contains(upErr.Message, tc.want) {
			t.Fatalf("message %q missing %q", upErr.Message, tc.want)
		}
		if upErr.Err != tc.err {
			t.Fatalf("original error not retained")
		}
	}
}

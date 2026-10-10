// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
)

// safeUpstreamNetworkErrorMessage returns a caller-safe summary of a transport
// failure. net/http wraps errors in *url.Error, whose text embeds the provider
// URL (and net.OpError the dial address); caller-visible diagnostics such as
// error.details.last_error must not disclose them (issue #94 HTTP-07). The
// original error stays on upstreamError.Err for internal classification.
func safeUpstreamNetworkErrorMessage(err error) string {
	var dnsErr *net.DNSError
	switch {
	case err == nil:
		return "upstream network error"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "upstream network error: connection closed before response (EOF)"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "upstream network error: connection refused"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE):
		return "upstream network error: connection reset"
	case errors.As(err, &dnsErr):
		return "upstream network error: dns lookup failed"
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "tls:") || strings.Contains(text, "x509:") {
		return "upstream network error: tls failure"
	}
	return "upstream network error: connection failed"
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"time"

	"github.com/metrum-ai/router/internal/router"
)

// httpServerTimeouts holds the connection deadlines applied to the data-plane
// http.Server.
type httpServerTimeouts struct {
	readHeader time.Duration
	read       time.Duration
	write      time.Duration
	idle       time.Duration
	// streamIdle is the progress deadline for event streams. The router extends
	// the write deadline on every streaming write by this window, so a stream
	// that keeps producing data outlives the absolute write deadline while a
	// stalled stream still ends (#759).
	streamIdle time.Duration
}

// serverTimeoutsFor derives the server deadlines from the loaded configuration.
// config.setDefaults has already resolved the zero values, so a zero here only
// happens for a hand-built config in tests; keep the historical fallbacks.
func serverTimeoutsFor(cfg *router.Config) httpServerTimeouts {
	out := httpServerTimeouts{
		readHeader: 10 * time.Second,
		read:       60 * time.Second,
		write:      120 * time.Second,
		idle:       120 * time.Second,
		streamIdle: 120 * time.Second,
	}
	if cfg == nil {
		return out
	}
	if ms := cfg.Server.ReadTimeoutMS; ms > 0 {
		out.read = time.Duration(ms) * time.Millisecond
	}
	if ms := cfg.Server.WriteTimeoutMS; ms > 0 {
		out.write = time.Duration(ms) * time.Millisecond
	}
	if ms := cfg.Server.IdleTimeoutMS; ms > 0 {
		out.idle = time.Duration(ms) * time.Millisecond
	}
	if ms := cfg.Server.StreamIdleTimeoutMS; ms > 0 {
		out.streamIdle = time.Duration(ms) * time.Millisecond
	}
	return out
}

// applyServerTimeouts wires the derived deadlines onto the http.Server.
func applyServerTimeouts(srv *http.Server, timeouts httpServerTimeouts) {
	if srv == nil {
		return
	}
	srv.ReadHeaderTimeout = timeouts.readHeader
	srv.ReadTimeout = timeouts.read
	srv.WriteTimeout = timeouts.write
	srv.IdleTimeout = timeouts.idle
}

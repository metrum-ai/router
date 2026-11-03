// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"strings"
	"testing"
)

// Issue #759: the server deadlines are configurable, validated, and their
// defaults keep the absolute write deadline above the upstream budget.

func TestServerTimeoutDefaultsAndConfig(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	if _, err := New(cfg); err != nil {
		t.Fatal(err)
	}
	// Defaults: the absolute write deadline sits above the upstream budget so
	// the upstream/target timeout is the bound that fires for a slow generation.
	if cfg.Server.Upstream.TimeoutMS != 600000 {
		t.Fatalf("upstream default = %d", cfg.Server.Upstream.TimeoutMS)
	}
	if cfg.Server.ReadTimeoutMS != 60000 {
		t.Fatalf("read default = %d", cfg.Server.ReadTimeoutMS)
	}
	if cfg.Server.WriteTimeoutMS <= cfg.Server.Upstream.TimeoutMS {
		t.Fatalf("write default %d must exceed upstream %d", cfg.Server.WriteTimeoutMS, cfg.Server.Upstream.TimeoutMS)
	}
	if cfg.Server.IdleTimeoutMS != 120000 || cfg.Server.StreamIdleTimeoutMS != 120000 {
		t.Fatalf("idle/stream defaults = %d/%d", cfg.Server.IdleTimeoutMS, cfg.Server.StreamIdleTimeoutMS)
	}

	// Explicit values survive defaulting.
	cfg = testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Server.ReadTimeoutMS = 5000
	cfg.Server.WriteTimeoutMS = 9000
	cfg.Server.IdleTimeoutMS = 7000
	cfg.Server.StreamIdleTimeoutMS = 3000
	if _, err := New(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Server.ReadTimeoutMS != 5000 || cfg.Server.WriteTimeoutMS != 9000 || cfg.Server.IdleTimeoutMS != 7000 || cfg.Server.StreamIdleTimeoutMS != 3000 {
		t.Fatalf("configured timeouts changed: %+v", cfg.Server)
	}
}

func TestServerTimeoutValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"negative-read", func(c *Config) { c.Server.ReadTimeoutMS = -1 }, "read_timeout_ms"},
		{"negative-write", func(c *Config) { c.Server.WriteTimeoutMS = -1 }, "write_timeout_ms"},
		{"negative-idle", func(c *Config) { c.Server.IdleTimeoutMS = -1 }, "idle_timeout_ms"},
		{"negative-stream-idle", func(c *Config) { c.Server.StreamIdleTimeoutMS = -1 }, "stream_idle_timeout_ms"},
		{"oversized-write", func(c *Config) { c.Server.WriteTimeoutMS = 24*60*60*1000 + 1 }, "24 hours"},
		{"oversized-stream-idle", func(c *Config) { c.Server.StreamIdleTimeoutMS = 24*60*60*1000 + 1 }, "24 hours"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
			tc.mutate(cfg)
			_, err := New(cfg)
			if err == nil {
				t.Fatal("expected a validation error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// Zero means "use the default", not "disable".
func TestServerTimeoutZeroSelectsDefault(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Server.ReadTimeoutMS = 0
	cfg.Server.WriteTimeoutMS = 0
	cfg.Server.IdleTimeoutMS = 0
	cfg.Server.StreamIdleTimeoutMS = 0
	if _, err := New(cfg); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]int{
		"read":        cfg.Server.ReadTimeoutMS,
		"write":       cfg.Server.WriteTimeoutMS,
		"idle":        cfg.Server.IdleTimeoutMS,
		"stream_idle": cfg.Server.StreamIdleTimeoutMS,
	} {
		if value <= 0 {
			t.Fatalf("%s timeout resolved to %d, want a positive default", name, value)
		}
	}
}

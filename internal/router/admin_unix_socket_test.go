// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestAdminUnixSocketConfigValidation(t *testing.T) {
	cfg := &AdminPlaneConfig{Enabled: true, Listen: "127.0.0.1:0", UnixSocket: AdminUnixSocketConfig{Path: "relative.sock", Mode: "0600"}}
	if err := validateAdminPlane(cfg); err == nil {
		t.Fatal("accepted relative socket path")
	}
	cfg.UnixSocket.Path = "/tmp/admin.sock"
	cfg.UnixSocket.Mode = "not-octal"
	if err := validateAdminPlane(cfg); err == nil {
		t.Fatal("accepted invalid socket mode")
	}
	cfg.UnixSocket.Mode = "0600"
	owner := -1
	cfg.UnixSocket.Owner = &owner
	if err := validateAdminPlane(cfg); err == nil {
		t.Fatal("accepted negative socket owner")
	}
	cfg.UnixSocket.Owner = nil
	defaultAdminPlaneConfig(cfg)
	if cfg.UnixSocket.Mode != "0600" {
		t.Fatalf("default socket mode=%q", cfg.UnixSocket.Mode)
	}
}

func TestAdminUnixSocketRefusesExistingRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "occupied.sock")
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &AdminPlaneConfig{Enabled: true, UnixSocket: AdminUnixSocketConfig{Path: path, Mode: "0600"}}
	plane, err := NewAdminPlane(cfg, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	if err := plane.Start(); err == nil {
		t.Fatal("socket bind replaced existing regular file")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "keep" {
		t.Fatalf("existing path changed: data=%q err=%v", b, err)
	}
}

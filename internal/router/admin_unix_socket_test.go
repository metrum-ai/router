// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAdminUnixSocketConfigValidation(t *testing.T) {
	if runtime.GOOS == "windows" { cfg := &AdminPlaneConfig{Enabled:true, UnixSocket:AdminUnixSocketConfig{Path:"C:/admin.sock"}}; if err := validateAdminPlane(cfg); err == nil || !strings.Contains(err.Error(), "not supported on Windows") { t.Fatalf("Windows Unix socket validation error=%v", err) }; return }
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
	cfg.TLS.ClientAuth = adminClientAuthRequire
	cfg.TLS.ClientCAFile = "/tmp/client-ca.pem"
	if err := validateAdminPlane(cfg); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("unix socket with required mTLS error=%v", err)
	}
	cfg.TLS.ClientAuth = adminClientAuthNone
	cfg.TLS.ClientCAFile = ""
	defaultAdminPlaneConfig(cfg)
	if cfg.UnixSocket.Mode != "0600" {
		t.Fatalf("default socket mode=%q", cfg.UnixSocket.Mode)
	}
}

func TestAdminUnixSocketRecoversStalePathAndRejectsActiveOwner(t *testing.T) {
	if runtime.GOOS == "windows" { t.Skip("Unix sockets are unavailable on Windows") }
	dir := t.TempDir()
	stalePath := filepath.Join(dir, "stale.sock")
	stale, err := net.Listen("unix", stalePath)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	pl, err := NewAdminPlane(&AdminPlaneConfig{Enabled: true, UnixSocket: AdminUnixSocketConfig{Path: stalePath, Mode: "0600"}}, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	if err := pl.Start(); err != nil {
		t.Fatalf("did not recover stale socket: %v", err)
	}
	info, err := os.Lstat(stalePath)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
		t.Fatalf("socket metadata=%v err=%v", info, err)
	}
	t.Cleanup(func() { _ = pl.Shutdown(t.Context()) })

	activePath := filepath.Join(dir, "active.sock")
	active, err := net.Listen("unix", activePath)
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	other, err := NewAdminPlane(&AdminPlaneConfig{Enabled: true, UnixSocket: AdminUnixSocketConfig{Path: activePath, Mode: "0600"}}, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Start(); err == nil || !strings.Contains(err.Error(), "another process owns this path") {
		t.Fatalf("active socket error=%v", err)
	}
}

func TestAdminUnixSocketRejectsWritableNonStickyParent(t *testing.T) {
	if runtime.GOOS == "windows" { t.Skip("Unix sockets are unavailable on Windows") }
	dir := filepath.Join(t.TempDir(), "unsafe")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "admin.sock")
	pl, err := NewAdminPlane(&AdminPlaneConfig{Enabled: true, UnixSocket: AdminUnixSocketConfig{Path: path, Mode: "0600"}}, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	if err := pl.Start(); err == nil || !strings.Contains(err.Error(), "writable by other users") {
		t.Fatalf("unsafe parent error=%v", err)
	}
}

func TestAdminUnixSocketRefusesExistingRegularFile(t *testing.T) {
	if runtime.GOOS == "windows" { t.Skip("Unix sockets are unavailable on Windows") }
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

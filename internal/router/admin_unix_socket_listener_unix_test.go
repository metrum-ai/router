// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package router

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestAdminUnixSocketListenerRejectsUnsafeParent(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := listenAdminUnixSocket(filepath.Join(dir, "admin.sock")); err == nil || !strings.Contains(err.Error(), "writable by other users") {
		t.Fatalf("listener in world-writable non-sticky parent error=%v", err)
	}
}

func TestAdminUnixSocketListenerReclaimsOnlyStaleSocket(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "admin.sock")
	old, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	old.SetUnlinkOnClose(false)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err := listenAdminUnixSocket(path)
	if err != nil {
		t.Fatalf("reclaim stale socket: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(path)

	activePath := filepath.Join(dir, "active.sock")
	active, err := net.ListenUnix("unix", &net.UnixAddr{Name: activePath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	if _, err := listenAdminUnixSocket(activePath); err == nil || !strings.Contains(err.Error(), "another process owns this path") {
		t.Fatalf("active socket error=%v", err)
	}
}

func TestAdminUnixSocketOtherUIDCannotConnect(t *testing.T) {
	if os.Getenv("ADMIN_SOCKET_PERMISSION_HELPER") == "1" {
		conn, err := net.Dial("unix", os.Getenv("ADMIN_SOCKET_PERMISSION_PATH"))
		if err == nil {
			_ = conn.Close()
			os.Exit(2)
		}
		os.Exit(0)
	}
	if os.Geteuid() != 0 {
		t.Skip("subprocess UID isolation requires a privileged test runner")
	}
	dir, err := os.MkdirTemp("/tmp", "admin-socket-permission-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0711); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "admin.sock")
	listener, err := listenAdminUnixSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("socket mode=%04o, want 0600", got)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAdminUnixSocketOtherUIDCannotConnect$")
	cmd.Env = append(os.Environ(), "ADMIN_SOCKET_PERMISSION_HELPER=1", "ADMIN_SOCKET_PERMISSION_PATH="+path)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534, Groups: []uint32{65534}}}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("other UID socket connect subprocess: %v: %s", err, output)
	}
}

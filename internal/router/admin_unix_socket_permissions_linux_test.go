// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package router

import (
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestAdminUnixSocketPermissionHelper(t *testing.T) {
	path := os.Getenv("ROUTER_TEST_UNIX_SOCKET_PERMISSION_PATH")
	if path == "" {
		return
	}
	conn, err := net.Dial("unix", path)
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil || !(errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) || os.IsPermission(err)) {
		t.Fatalf("unprivileged socket dial error=%v, want permission denied", err)
	}
}

func TestAdminUnixSocketModeBlocksOtherUID(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to start a subprocess as a different UID")
	}
	dir, err := os.MkdirTemp("/tmp", "admin-unix-permission-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "admin.sock")
	pl, err := NewAdminPlane(&AdminPlaneConfig{Enabled: true, UnixSocket: AdminUnixSocketConfig{Path: path, Mode: "0600"}}, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	if err := pl.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pl.Shutdown(t.Context()) })
	cmd := exec.Command(os.Args[0], "-test.run=^TestAdminUnixSocketPermissionHelper$")
	cmd.Env = append(os.Environ(), "ROUTER_TEST_UNIX_SOCKET_PERMISSION_PATH="+path)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534, NoSetGroups: true}}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("different UID connected to mode-0600 socket: err=%v output=%s", err, output)
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package router

import (
	"os"
	"path/filepath"
	"strings"
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

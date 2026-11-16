// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package router

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Umask is process-wide, so serialize its temporary change while binding sockets.
var adminUnixUmaskMu sync.Mutex

func listenAdminUnixSocket(path string) (*net.UnixListener, error) {
	parent := filepath.Dir(path)
	dir, err := os.Stat(parent)
	if err != nil {
		return nil, fmt.Errorf("admin unix socket parent: %w", err)
	}
	if !dir.IsDir() {
		return nil, fmt.Errorf("admin unix socket parent is not a directory")
	}
	if dir.Mode().Perm()&0022 != 0 && dir.Mode()&os.ModeSticky == 0 {
		return nil, fmt.Errorf("admin unix socket parent is writable by other users and is not sticky")
	}

	adminUnixUmaskMu.Lock()
	oldMask := syscall.Umask(0177)
	ln, listenErr := net.Listen("unix", path)
	syscall.Umask(oldMask)
	adminUnixUmaskMu.Unlock()
	if listenErr == nil {
		return ln.(*net.UnixListener), nil
	}
	if !isAddressInUse(listenErr) {
		return nil, listenErr
	}
	current, statErr := os.Lstat(path)
	if statErr != nil || current.Mode()&os.ModeSocket == 0 {
		return nil, listenErr
	}
	conn, dialErr := net.DialTimeout("unix", path, 250*time.Millisecond)
	if dialErr == nil {
		_ = conn.Close()
		return nil, fmt.Errorf("another process owns this path: admin unix socket %q", path)
	}
	if !isConnectionRefused(dialErr) {
		return nil, fmt.Errorf("admin unix socket path %q exists and could not be verified stale: %w", path, dialErr)
	}
	if latest, err := os.Lstat(path); err != nil || latest.Mode()&os.ModeSocket == 0 || !os.SameFile(current, latest) {
		return nil, fmt.Errorf("admin unix socket path changed while checking stale socket")
	}
	if err := os.Remove(path); err != nil {
		return nil, fmt.Errorf("remove stale admin unix socket %q: %w", path, err)
	}
	adminUnixUmaskMu.Lock()
	oldMask = syscall.Umask(0177)
	ln, listenErr = net.Listen("unix", path)
	syscall.Umask(oldMask)
	adminUnixUmaskMu.Unlock()
	if listenErr != nil {
		return nil, fmt.Errorf("rebind stale admin unix socket %q: %w", path, listenErr)
	}
	return ln.(*net.UnixListener), nil
}

func isAddressInUse(err error) bool {
	return err != nil && (os.IsExist(err) || containsErrno(err, syscall.EADDRINUSE))
}

func isConnectionRefused(err error) bool {
	return err != nil && containsErrno(err, syscall.ECONNREFUSED)
}

func containsErrno(err error, target syscall.Errno) bool {
	for err != nil {
		if errno, ok := err.(syscall.Errno); ok && errno == target {
			return true
		}
		unwrapped, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapped.Unwrap()
	}
	return false
}

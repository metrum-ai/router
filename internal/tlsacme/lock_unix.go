// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package tlsacme

import (
	"errors"
	"os"
	"syscall"
)

// lockFile takes an exclusive, non-blocking POSIX lock on path. The lock is
// held by the open file descriptor, which is why the file stays open until the
// returned function runs.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	fd := int(f.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		switch {
		case errors.Is(err, syscall.EWOULDBLOCK), errors.Is(err, syscall.EAGAIN), errors.Is(err, syscall.EACCES):
			return nil, ErrLockBusy
		case errors.Is(err, syscall.EOPNOTSUPP), errors.Is(err, syscall.ENOLCK), errors.Is(err, syscall.EINVAL):
			return nil, ErrLockUnsupported
		default:
			return nil, err
		}
	}
	released := false
	return func() {
		if released {
			return
		}
		released = true
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

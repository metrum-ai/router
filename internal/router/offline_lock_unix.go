// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package router

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

func acquireUsageFileLock(f *os.File, exclusive bool) error {
	return acquireUsageFileLockTimeout(f, exclusive, 30*time.Second)
}

func acquireUsageFileLockTimeout(f *os.File, exclusive bool, timeout time.Duration) error {
	mode := syscall.LOCK_SH | syscall.LOCK_NB
	if exclusive {
		mode = syscall.LOCK_EX | syscall.LOCK_NB
	}
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(f.Fd()), mode)
		if err == nil {
			return nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return fmt.Errorf("offline SQLite operation refused: acquire runtime lock: %w", err)
		}
		if exclusive || !time.Now().Before(deadline) {
			return fmt.Errorf("offline SQLite operation refused: router or another maintenance command holds the runtime lock")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

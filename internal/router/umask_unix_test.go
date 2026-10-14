// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package router

import (
	"syscall"
	"testing"
)

func clearUmaskForTest(t *testing.T) {
	oldUmask := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(oldUmask) })
}

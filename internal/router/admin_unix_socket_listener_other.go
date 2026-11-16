// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package router

import (
	"fmt"
	"net"
)

func listenAdminUnixSocket(string) (*net.UnixListener, error) {
	return nil, fmt.Errorf("admin Unix sockets are not supported on this platform")
}

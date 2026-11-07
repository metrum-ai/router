// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package tlsacme

// lockFile reports that this platform cannot provide the POSIX lock the issuer
// needs. The router then serves a cached certificate and does not issue, which
// is the same behaviour as a shared filesystem without lock support.
func lockFile(string) (func(), error) {
	return nil, ErrLockUnsupported
}

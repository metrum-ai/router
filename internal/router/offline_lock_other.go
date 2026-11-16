// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package router

import (
	"fmt"
	"os"
)

// Serving remains available, while offline mutations fail closed where this
// build has no shared process file-lock primitive.
func acquireUsageFileLock(_ *os.File, exclusive bool) error {
	if exclusive {
		return fmt.Errorf("offline SQLite operations require a platform with shared file locks")
	}
	return nil
}

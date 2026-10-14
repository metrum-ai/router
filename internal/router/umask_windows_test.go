// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "testing"

func clearUmaskForTest(t *testing.T) {
	t.Skip("Unix file permission modes are not supported on Windows")
}

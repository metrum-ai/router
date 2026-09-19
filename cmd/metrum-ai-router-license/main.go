// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

// Temporary Phase A stub: runtime licensing enforcement was removed.
// Phase B deletes this command from packaging.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "metrum-ai-router-license: runtime licensing was removed in 3.0.0; this command is inert and will be deleted in a follow-up release")
	os.Exit(2)
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package buildinfo

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

func TestCurrentReportsRunningBinaryDigest(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(binary)
	info := Current()
	if info.BinarySHA256 != hex.EncodeToString(want[:]) {
		t.Fatalf("binary_sha256=%q, want %x", info.BinarySHA256, want)
	}
	if len(info.BinarySHA256) != 64 {
		t.Fatalf("binary_sha256 has %d hex digits, want 64", len(info.BinarySHA256))
	}
}

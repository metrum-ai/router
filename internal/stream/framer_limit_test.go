// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package stream

import (
	"errors"
	"strings"
	"testing"
)

func TestFramerLineLimit(t *testing.T) {
	for _, size := range []int{65535, 65536, 65537, 1 << 20} {
		f := NewFramer(strings.NewReader("data: "+strings.Repeat("x", size-7)+"\n\n"), 2<<20)
		_, err := f.Next()
		if size > 65536 && !errors.Is(err, ErrFrameTooLarge) {
			t.Fatalf("size %d: %v", size, err)
		}
		if size <= 65536 && err != nil {
			t.Fatal(err)
		}
	}
}

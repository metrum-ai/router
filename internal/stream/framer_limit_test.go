// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package stream

import (
	"bufio"
	"errors"
	"strings"
	"testing"
)

func TestFramerLineLimitWithLargerBufferedReader(t *testing.T) {
	reader := bufio.NewReaderSize(strings.NewReader("data: "+strings.Repeat("x", 65537)+"\n\n"), 128<<10)
	f := NewFramer(reader, 0)
	if _, err := f.Next(); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("larger supplied buffer bypassed line cap: %v", err)
	}
	if f.reader.Size() != 64<<10 {
		t.Fatalf("framer buffer size = %d", f.reader.Size())
	}
}

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

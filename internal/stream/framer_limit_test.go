// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package stream

import (
	"bufio"
	"errors"
	"strings"
	"sync"
	"testing"
)

type testLineBudget struct {
	mu          sync.Mutex
	limit, used int64
}

func (b *testLineBudget) TryAcquire(n int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > b.limit-b.used {
		return false
	}
	b.used += n
	return true
}
func (b *testLineBudget) Release(n int64) {
	b.mu.Lock()
	b.used -= n
	b.mu.Unlock()
}

func TestFramerLineBufferBudget(t *testing.T) {
	budget := &testLineBudget{limit: 1024}
	f := NewFramer(strings.NewReader("data: "+strings.Repeat("x", 2048)+"\n\n"), 0, 4096)
	f.SetLineBufferBudget(budget)
	if _, err := f.Next(); !errors.Is(err, ErrLineBufferBudget) {
		t.Fatalf("line buffer budget error = %v", err)
	}
	if budget.used != 0 {
		t.Fatalf("reservation after rejection = %d", budget.used)
	}

	budget.limit = 8192
	f = NewFramer(strings.NewReader("data: "+strings.Repeat("y", 2048)+"\n\n"), 0, 4096)
	f.SetLineBufferBudget(budget)
	if event, err := f.Next(); err != nil || len(event.Data) != 2048 {
		t.Fatalf("event bytes=%d err=%v", len(event.Data), err)
	}
	if budget.used != 0 {
		t.Fatalf("reservation after line consumed = %d", budget.used)
	}
}

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

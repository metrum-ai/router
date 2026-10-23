// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func emitUsageForTest(t testing.TB, svc *Service, rec logRecord) {
	t.Helper()
	svc.usage.Emit(rec)
	flushUsageForTest(t, svc)
}

func emitSecurityForTest(t testing.TB, svc *Service, event securityAccessEvent) {
	t.Helper()
	svc.usage.EmitSecurityAccessEvent(event)
	flushUsageForTest(t, svc)
}

func flushUsageForTest(t testing.TB, svc *Service) {
	t.Helper()
	if svc.usage == nil || svc.usage.writer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.usage.writer.flush(ctx); err != nil {
		t.Fatal(err)
	}
}

func asyncUsageTestStore(t *testing.T) *usageStore {
	t.Helper()
	s, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	s.writer = newUsageWriter(s)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func usageCount(t *testing.T, s *usageStore) int64 {
	t.Helper()
	var n int64
	if err := s.db.Model(&usageRecord{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestUsageWriterExactBatchTrigger(t *testing.T) {
	batches := make(chan int, 2)
	w := startUsageWriter(func(_ context.Context, rows []any) error { batches <- len(rows); return nil }, time.Hour)
	defer w.close()
	for i := 0; i < usageBatchSize-1; i++ {
		w.record(logRecord{})
	}
	select {
	case n := <-batches:
		t.Fatalf("premature batch %d", n)
	default:
	}
	w.record(logRecord{})
	select {
	case n := <-batches:
		if n != usageBatchSize {
			t.Fatalf("batch=%d", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("size trigger did not flush")
	}
}

func TestUsageWriterTimerTrigger(t *testing.T) {
	batches := make(chan int, 1)
	w := startUsageWriter(func(_ context.Context, rows []any) error { batches <- len(rows); return nil }, 5*time.Millisecond)
	defer w.close()
	w.record(logRecord{})
	select {
	case n := <-batches:
		if n != 1 {
			t.Fatalf("batch=%d", n)
		}
	case <-time.After(time.Second):
		t.Fatal("timer did not flush")
	}
}

func TestUsageWriterBatchAndTimer(t *testing.T) {
	for _, n := range []int{1, 256} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s := asyncUsageTestStore(t)
			for i := 0; i < n; i++ {
				s.Emit(logRecord{RequestID: fmt.Sprint(i), TS: formatUsageTime(time.Now().UTC())})
			}
			deadline := time.Now().Add(30 * time.Second)
			for usageCount(t, s) != int64(n) && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if got := usageCount(t, s); got != int64(n) {
				t.Fatalf("rows=%d want=%d", got, n)
			}
		})
	}
}

func TestUsageWriterFailureMetrics(t *testing.T) {
	w := startUsageWriter(func(context.Context, []any) error { return errors.New("synthetic write failure") }, time.Hour)
	w.record(logRecord{})
	if err := w.close(); err == nil {
		t.Fatal("expected batch failure")
	}
	metrics := w.prometheus()
	for _, want := range []string{"usage_queue_depth 0", "usage_write_dropped_total 1", "usage_write_latency_seconds_sum ", "usage_write_latency_seconds_count 1"} {
		if !strings.Contains(metrics, want) {
			t.Fatalf("missing metric %q: %s", want, metrics)
		}
	}
}

func TestUsageWriterDrainTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := &usageWriter{ch: make(chan any, 1), done: make(chan struct{}), cancel: cancel, drainTimeout: 10 * time.Millisecond}
	start := time.Now()
	if err := w.close(); err == nil {
		t.Fatal("expected timeout")
	}
	if time.Since(start) > time.Second {
		t.Fatal("drain exceeded deadline")
	}
	if ctx.Err() == nil {
		t.Fatal("timeout did not cancel SQL context")
	}
	w.record(logRecord{})
	if w.dropped.Load() != 1 {
		t.Fatal("closed writer must drop")
	}
}

func TestUsageWriterCaptureDeletionDrainsAcceptedEntries(t *testing.T) {
	s := asyncUsageTestStore(t)
	s.writer.record(usageContentCaptureJob{record: &contentCaptureRecord{RequestID: "capture-drain", TS: formatUsageTime(time.Now().UTC()), Scope: contentCaptureScopeRequest, Encrypted: true}})
	n, err := s.DeleteContentCapturesByRequestID("capture-drain", "operator", "public-test-token", "test")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deleted %d queued captures want 1", n)
	}
	var remaining int64
	if err = s.db.Model(&contentCaptureRecord{}).Where("request_id = ?", "capture-drain").Count(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatal("queued capture resurrected after deletion")
	}
}

func TestUsageWriterSQLitePragmas(t *testing.T) {
	s := asyncUsageTestStore(t)
	var mode string
	var busy int
	if err := s.db.Raw("PRAGMA journal_mode").Scan(&mode).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Raw("PRAGMA busy_timeout").Scan(&busy).Error; err != nil {
		t.Fatal(err)
	}
	if mode != "wal" || busy != 5000 {
		t.Fatalf("journal=%s busy=%d", mode, busy)
	}
}

func TestUsageWriterFlushStopsWithWriter(t *testing.T) {
	w := &usageWriter{ch: make(chan any, 1), done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- w.flush(ctx) }()
	select {
	case <-w.ch: // Barrier accepted but not completed before writer cancellation.
	case <-ctx.Done():
		t.Fatal("barrier was not admitted")
	}
	close(w.done)
	if err := <-result; err == nil || err.Error() != "usage writer stopped before flush" {
		t.Fatalf("stopped writer must release its consistency waiter: %v", err)
	}
}

func TestUsageWriterFullChannelFlushDoesNotBlockShutdown(t *testing.T) {
	w := &usageWriter{
		ch: make(chan any, 1), done: make(chan struct{}),
		cancel: func() {}, drainTimeout: 10 * time.Millisecond,
	}
	w.record(logRecord{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// The old blocking admission retained RLock until this deadline, which
	// could prevent close from ever starting its drain timeout.
	if err := w.flush(ctx); err == nil || err.Error() != "usage queue full" {
		t.Fatalf("full-queue consistency barrier must fail without waiting: %v", err)
	}
	if err := w.close(); err == nil || !strings.Contains(err.Error(), "drain timeout") {
		t.Fatalf("shutdown must reach its bounded drain: %v", err)
	}
}

func TestUsageWriterFullChannel(t *testing.T) {
	w := &usageWriter{ch: make(chan any, usageQueueCapacity)}
	for i := 0; i < usageQueueCapacity+1; i++ {
		w.record(logRecord{})
	}
	if w.dropped.Load() != 1 || len(w.ch) != usageQueueCapacity {
		t.Fatal("full queue did not drop exactly one")
	}
}

func TestUsageWriterShutdownFlush(t *testing.T) {
	s := asyncUsageTestStore(t)
	for i := 0; i < 17; i++ {
		s.Emit(logRecord{RequestID: fmt.Sprint(i), TS: formatUsageTime(time.Now().UTC())})
	}
	if err := s.writer.close(); err != nil {
		t.Fatal(err)
	}
	if got := usageCount(t, s); got != 17 {
		t.Fatalf("rows=%d", got)
	}
}

func TestUsageWriterHeldSQLiteLock(t *testing.T) {
	s := asyncUsageTestStore(t)
	sqlDB, _ := s.db.DB()
	conn, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	// Reproduce the previous synchronous request logging while holding the same lock.
	go func() { time.Sleep(350 * time.Millisecond); _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()
	before := time.Now()
	if err := s.writeUsageBatch(context.Background(), []logRecord{{RequestID: "baseline", TS: formatUsageTime(time.Now().UTC())}}); err != nil {
		t.Fatal(err)
	}
	t.Logf("before synchronous logging under 350ms SQLite lock: %s", time.Since(before))
	if _, err = conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.Emit(logRecord{RequestID: fmt.Sprint(i), TS: formatUsageTime(time.Now().UTC())})
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)
	t.Logf("16-way enqueue under SQLite lock: %s", elapsed)
	if elapsed > 100*time.Millisecond {
		t.Fatalf("request logging delayed by SQLite: %s", elapsed)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err = conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if err = s.writer.close(); err != nil {
		t.Fatal(err)
	}
	if usageCount(t, s) != 17 {
		t.Fatal("missing rows after lock released")
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type usageContentCaptureJob struct {
	record  *contentCaptureRecord
	headers []*contentCaptureHeaderRecord
}

const usageQueueCapacity = 10000
const usageBatchSize = 256
const usageFlushInterval = 250 * time.Millisecond
const usageDrainTimeout = 10 * time.Second

// usageWriter is owned by the serving store; offline reporting stores remain synchronous.
type usageWriter struct {
	ch           chan any
	done         chan struct{}
	mu           sync.RWMutex
	closed       bool
	cancel       context.CancelFunc
	dropped      atomic.Int64
	writes       atomic.Int64
	latencyNS    atomic.Int64
	err          error
	drainTimeout time.Duration
}

func newUsageWriter(s *usageStore) *usageWriter {
	return startUsageWriter(s.writeUsageJobs, usageFlushInterval)
}

func startUsageWriter(write func(context.Context, []any) error, interval time.Duration) *usageWriter {
	ctx, cancel := context.WithCancel(context.Background())
	w := &usageWriter{ch: make(chan any, usageQueueCapacity), done: make(chan struct{}), cancel: cancel, drainTimeout: usageDrainTimeout}
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		batch := make([]any, 0, usageBatchSize)
		flush := func() {
			if len(batch) == 0 {
				return
			}
			start := time.Now()
			if err := write(ctx, batch); err != nil {
				w.err = err
				w.dropped.Add(int64(len(batch)))
			}
			w.latencyNS.Add(time.Since(start).Nanoseconds())
			w.writes.Add(1)
			clear(batch)
			batch = batch[:0]
		}
		for {
			select {
			case rec, ok := <-w.ch:
				if !ok {
					flush()
					return
				}
				if barrier, ok := rec.(usageFlushBarrier); ok {
					flush()
					close(barrier.done)
					continue
				}
				batch = append(batch, rec)
				if len(batch) == usageBatchSize {
					flush()
				}
			case <-ticker.C:
				flush()
			case <-ctx.Done():
				w.dropped.Add(int64(len(w.ch) + len(batch)))
				return
			}
		}
	}()
	return w
}

func (w *usageWriter) record(rec any) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		w.dropped.Add(1)
		return
	}
	select {
	case w.ch <- rec:
	default:
		w.dropped.Add(1)
	}
}

// flush waits for all entries accepted before the barrier. Inference logging
// never calls it; maintenance/test readers can explicitly request consistency.
type usageFlushBarrier struct{ done chan struct{} }

func (w *usageWriter) flush(ctx context.Context) error {
	if w == nil {
		return nil
	}
	barrier := usageFlushBarrier{done: make(chan struct{})}
	w.mu.RLock()
	if w.closed {
		w.mu.RUnlock()
		select {
		case <-w.done:
			return w.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case w.ch <- barrier:
		w.mu.RUnlock()
	case <-ctx.Done():
		w.mu.RUnlock()
		return ctx.Err()
	default:
		// Never hold admission open while waiting for a saturated queue: close
		// needs this lock to begin its bounded drain. Maintenance fails safely.
		w.mu.RUnlock()
		return errors.New("usage queue full")
	}
	select {
	case <-barrier.done:
		return nil
	case <-w.done:
		return errors.New("usage writer stopped before flush")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *usageWriter) close() error {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.ch)
	}
	w.mu.Unlock()
	timeout := w.drainTimeout
	if timeout <= 0 {
		timeout = usageDrainTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-w.done:
		w.cancel()
		return w.err
	case <-timer.C:
		w.cancel()
		return errors.New("usage writer drain timeout")
	}
}

func (s *usageStore) writeUsageBatch(ctx context.Context, batch []logRecord) error {
	jobs := make([]any, len(batch))
	for i := range batch {
		jobs[i] = batch[i]
	}
	return s.writeUsageJobs(ctx, jobs)
}

func (s *usageStore) writeUsageJobs(ctx context.Context, batch []any) error {
	// Group by concrete table in first-seen order, retaining parent-before-child ordering.
	groups := map[reflect.Type]reflect.Value{}
	var order []reflect.Type
	var cutoffs []time.Time
	var captures []usageContentCaptureJob
	for _, job := range batch {
		var models []any
		switch rec := job.(type) {
		case logRecord:
			models = usageModels(rec)
		case securityAccessEvent:
			models = []any{securityAccessRecordFromEvent(rec)}
		case time.Time:
			cutoffs = append(cutoffs, rec)
		case usageContentCaptureJob:
			captures = append(captures, rec)
		}
		for _, model := range models {
			typ := reflect.TypeOf(model)
			values, ok := groups[typ]
			if !ok {
				values = reflect.MakeSlice(reflect.SliceOf(typ), 0, len(batch))
				order = append(order, typ)
			}
			groups[typ] = reflect.Append(values, reflect.ValueOf(model))
		}
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, cutoff := range cutoffs {
			if err := tx.Where("ts < ?", formatUsageTime(cutoff.UTC())).Delete(&securityAccessEventRecord{}).Error; err != nil {
				return err
			}
		}
		for _, typ := range order {
			values := groups[typ]
			ptr := reflect.New(values.Type())
			ptr.Elem().Set(values)
			// Bound child-table statements as well as the parent usage batch.
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(ptr.Interface(), usageBatchSize).Error; err != nil {
				return err
			}
		}
		for _, capture := range captures {
			if err := tx.Create(capture.record).Error; err != nil {
				return err
			}
			for _, header := range capture.headers {
				header.CaptureID = capture.record.ID
			}
			if len(capture.headers) > 0 {
				if err := tx.CreateInBatches(capture.headers, usageBatchSize).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (w *usageWriter) prometheus() string {
	if w == nil {
		return ""
	}
	return fmt.Sprintf("# TYPE usage_queue_depth gauge\nusage_queue_depth %d\n# TYPE usage_write_dropped_total counter\nusage_write_dropped_total %d\n# TYPE usage_write_latency_seconds summary\nusage_write_latency_seconds_sum %g\nusage_write_latency_seconds_count %d\n", len(w.ch), w.dropped.Load(), float64(w.latencyNS.Load())/1e9, w.writes.Load())
}

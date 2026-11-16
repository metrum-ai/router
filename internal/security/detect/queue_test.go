// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package detect

import (
	"context"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metrum-ai/router/pkg/detector"
)

type blockingDetector struct {
	started chan struct{}
	gate    chan struct{}
	called  atomic.Int32
}

type timeoutDetector struct{}

func (timeoutDetector) Detect(ctx context.Context, _ detector.Event) ([]detector.Signal, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (d *blockingDetector) Detect(context.Context, detector.Event) ([]detector.Signal, error) {
	d.called.Add(1)
	select {
	case d.started <- struct{}{}:
	default:
	}
	<-d.gate
	return nil, nil
}

func TestQueueOverflowDropsWithoutBlocking(t *testing.T) {
	det := &blockingDetector{started: make(chan struct{}, 1), gate: make(chan struct{})}
	dispatch := NewDispatcher(1, 1, nil, nil)
	event := detector.Event{SchemaVersion: detector.SchemaVersion, Kind: "test"}
	defer func() {
		close(det.gate)
		dispatch.Close()
	}()
	if !dispatch.Submit(context.Background(), "caller", det, event) {
		t.Fatal("first submission dropped")
	}
	select {
	case <-det.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	if !dispatch.Submit(context.Background(), "caller", det, event) {
		t.Fatal("second submission should fit in queue")
	}
	start := time.Now()
	if dispatch.Submit(context.Background(), "caller", det, event) {
		t.Fatal("third submission should be dropped")
	}
	if time.Since(start) > 20*time.Millisecond || dispatch.Dropped() != 1 {
		t.Fatalf("submit blocked or drop counter wrong: duration=%s drops=%d", time.Since(start), dispatch.Dropped())
	}
}

func TestSamplingRateZeroSkipsDetector(t *testing.T) {
	det := &blockingDetector{started: make(chan struct{}, 1), gate: make(chan struct{})}
	dispatch := NewDispatcher(1, 1, map[string]float64{"caller": 0}, nil)
	defer dispatch.Close()
	if dispatch.Submit(context.Background(), "caller", det, detector.Event{SchemaVersion: detector.SchemaVersion, Kind: "test"}) {
		t.Fatal("zero-rate event was queued")
	}
	if det.called.Load() != 0 || dispatch.Dropped() != 0 {
		t.Fatalf("called=%d drops=%d", det.called.Load(), dispatch.Dropped())
	}
}

func TestPolicyTimeoutAndModeReachResultSink(t *testing.T) {
	results := make(chan Result, 1)
	dispatch := NewDispatcher(1, 1, nil, func(result Result) { results <- result })
	defer dispatch.Close()
	event := detector.Event{SchemaVersion: detector.SchemaVersion, Kind: "test"}
	policy := Policy{Mode: "enforce", Timeout: 10 * time.Millisecond, OnError: "report", SampleRate: 1}
	if !dispatch.SubmitWithPolicy(context.Background(), "caller", timeoutDetector{}, event, policy) {
		t.Fatal("submission rejected")
	}
	select {
	case result := <-results:
		if result.Mode != "enforce" || result.Err == nil || result.Err.Error() != "detector failed" {
			t.Fatalf("result=%+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out detector did not report")
	}
}

func TestPolicyRejectsNonFiniteSampleRate(t *testing.T) {
	policy := Policy{Mode: "report", Timeout: time.Second, OnError: "report", SampleRate: math.NaN()}
	if err := policy.Validate(); err == nil {
		t.Fatal("NaN sample_rate accepted")
	}
}

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
	dispatch := NewDispatcher(1, 1, nil)
	event := detector.Event{SchemaVersion: detector.SchemaVersion, Kind: "test"}
	defer func() {
		close(det.gate)
		dispatch.Close()
	}()
	if !dispatch.Submit("test", "caller", det, event, Policy{Mode: "report", Timeout: time.Second, OnError: "fallback", SampleRate: 1}) {
		t.Fatal("first submission dropped")
	}
	select {
	case <-det.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	if !dispatch.Submit("test", "caller", det, event, Policy{Mode: "report", Timeout: time.Second, OnError: "fallback", SampleRate: 1}) {
		t.Fatal("second submission should fit in queue")
	}
	done := make(chan bool, 1)
	go func() {
		done <- dispatch.Submit("test", "caller", det, event, Policy{Mode: "report", Timeout: time.Second, OnError: "fallback", SampleRate: 1})
	}()
	select {
	case accepted := <-done:
		if accepted {
			t.Fatal("third submission should be dropped")
		}
	case <-time.After(time.Second):
		t.Fatal("queue-full submission blocked serving")
	}
	if dispatch.Dropped() != 1 {
		t.Fatalf("drops=%d, want 1", dispatch.Dropped())
	}
}

func TestSamplingRateZeroSkipsDetector(t *testing.T) {
	det := &blockingDetector{started: make(chan struct{}, 1), gate: make(chan struct{})}
	dispatch := NewDispatcher(1, 1, nil)
	defer dispatch.Close()
	if dispatch.Submit("test", "caller", det, detector.Event{SchemaVersion: detector.SchemaVersion, Kind: "test"}, Policy{Mode: "report", Timeout: time.Second, OnError: "fallback", SampleRate: 0}) {
		t.Fatal("zero-rate event was queued")
	}
	if det.called.Load() != 0 || dispatch.Dropped() != 0 {
		t.Fatalf("called=%d drops=%d", det.called.Load(), dispatch.Dropped())
	}
}

func TestPolicyTimeoutAndModeReachResultSink(t *testing.T) {
	results := make(chan Result, 1)
	dispatch := NewDispatcher(1, 1, func(result Result) { results <- result })
	defer dispatch.Close()
	event := detector.Event{SchemaVersion: detector.SchemaVersion, Kind: "test"}
	policy := Policy{Mode: "enforce", Timeout: 10 * time.Millisecond, OnError: "fail_closed", SampleRate: 1}
	if !dispatch.Submit("test", "caller", timeoutDetector{}, event, policy) {
		t.Fatal("submission rejected")
	}
	select {
	case result := <-results:
		if result.Mode != "enforce" || !result.Enforce || !result.FailClosed || result.Err == nil || result.Err.Error() != "detector failed" {
			t.Fatalf("result=%+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out detector did not report")
	}
}

func TestPolicyRejectsNonFiniteSampleRate(t *testing.T) {
	policy := Policy{Mode: "report", Timeout: time.Second, OnError: "fallback", SampleRate: math.NaN()}
	if err := policy.Validate(); err == nil {
		t.Fatal("NaN sample_rate accepted")
	}
}

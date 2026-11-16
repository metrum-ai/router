// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package detect

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/metrum-ai/router/pkg/detector"
)

// Dispatcher evaluates sampled events on a fixed worker pool. Submit never waits for capacity.
type Dispatcher struct {
	queue    chan job
	sink     func(Result)
	drops    atomic.Uint64
	closed   atomic.Bool
	once     sync.Once
	wg       sync.WaitGroup
	randMu   sync.Mutex
	submitMu sync.RWMutex
}

type job struct {
	detectorID string
	callerID   string
	detector   detector.Detector
	event      detector.Event
	policy     Policy
}

// Policy selects how one detector runs and handles its failures.
type Policy struct {
	Mode       string
	Timeout    time.Duration
	OnError    string
	SampleRate float64
}

// Result is delivered off the request path. Error contains no remote response body.
type Result struct {
	DetectorID string
	CallerID   string
	Mode       string
	SampleRate float64
	Enforce    bool
	FailClosed bool
	Signals    []detector.Signal
	Err        error
}

// NewDispatcher starts workers. Queue capacity and worker count must be positive.
func NewDispatcher(workers, capacity int, sink func(Result)) *Dispatcher {
	if workers < 1 || capacity < 1 {
		panic("detector workers and capacity must be positive")
	}
	d := &Dispatcher{queue: make(chan job, capacity), sink: sink}
	for range workers {
		d.wg.Add(1)
		go d.worker()
	}
	return d
}

// Submit adds one detector task without waiting for queue capacity.
func (d *Dispatcher) Submit(detectorID, callerID string, implementation detector.Detector, event detector.Event, policy Policy) bool {
	d.submitMu.RLock()
	defer d.submitMu.RUnlock()
	if d.closed.Load() || detectorID == "" || implementation == nil || detector.ValidateEvent(event) != nil {
		return false
	}
	if err := policy.Validate(); err != nil || policy.Mode == "off" {
		return false
	}
	rate := policy.SampleRate
	if rate <= 0 || rate < 1 && !d.sample(rate) {
		return false
	}
	select {
	case d.queue <- job{detectorID: detectorID, callerID: callerID, detector: implementation, event: event, policy: policy}:
		return true
	default:
		d.drops.Add(1)
		return false
	}
}

func (p Policy) Validate() error {
	if p.Mode != "off" && p.Mode != "report" && p.Mode != "enforce" {
		return errors.New("invalid detector mode")
	}
	if p.OnError != "fail_closed" && p.OnError != "fallback" {
		return errors.New("invalid detector on_error")
	}
	if math.IsNaN(p.SampleRate) || math.IsInf(p.SampleRate, 0) || p.SampleRate < 0 || p.SampleRate > 1 {
		return errors.New("invalid detector sample_rate")
	}
	if p.Timeout <= 0 || p.Timeout > 5*time.Second {
		return errors.New("invalid detector timeout")
	}
	return nil
}

func (d *Dispatcher) sample(rate float64) bool {
	d.randMu.Lock()
	defer d.randMu.Unlock()
	return rand.Float64() < rate
}

func (d *Dispatcher) Dropped() uint64 { return d.drops.Load() }

func (d *Dispatcher) worker() {
	defer d.wg.Done()
	for item := range d.queue {
		ctx, cancel := context.WithTimeout(context.Background(), item.policy.Timeout)
		signals, err := item.detector.Detect(ctx, item.event)
		cancel()
		if err == nil {
			for i := range signals {
				if signals[i].SchemaVersion == "" {
					signals[i].SchemaVersion = detector.SchemaVersion
				}
				if detector.ValidateSignal(signals[i]) != nil {
					err = errors.New("detector failed")
					signals = nil
					break
				}
			}
		}
		if err != nil {
			err = errors.New("detector failed")
		}
		if d.sink != nil {
			d.sink(Result{DetectorID: item.detectorID, CallerID: item.callerID, Mode: item.policy.Mode, SampleRate: item.policy.SampleRate, Enforce: item.policy.Mode == "enforce", FailClosed: err != nil && item.policy.Mode == "enforce" && item.policy.OnError == "fail_closed", Signals: signals, Err: err})
		}
	}
}

func (d *Dispatcher) Close() {
	d.once.Do(func() {
		d.submitMu.Lock()
		d.closed.Store(true)
		close(d.queue)
		d.submitMu.Unlock()
		d.wg.Wait()
	})
}

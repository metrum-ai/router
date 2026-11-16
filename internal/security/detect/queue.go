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
	rates    map[string]float64
	sink     func(Result)
	drops    atomic.Uint64
	closed   atomic.Bool
	once     sync.Once
	wg       sync.WaitGroup
	randMu   sync.Mutex
	submitMu sync.RWMutex
}

type job struct {
	ctx      context.Context
	callerID string
	detector detector.Detector
	event    detector.Event
	policy   Policy
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
	CallerID string
	Mode     string
	Signals  []detector.Signal
	Err      error
}

// NewDispatcher starts workers. Queue capacity and worker count must be positive.
func NewDispatcher(workers, capacity int, sampleRates map[string]float64, sink func(Result)) *Dispatcher {
	if workers < 1 || capacity < 1 {
		panic("detector workers and capacity must be positive")
	}
	rates := make(map[string]float64, len(sampleRates))
	for caller, rate := range sampleRates {
		if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 || rate > 1 {
			panic("detector sample rate must be between zero and one")
		}
		rates[caller] = rate
	}
	d := &Dispatcher{queue: make(chan job, capacity), rates: rates, sink: sink}
	for range workers {
		d.wg.Add(1)
		go d.worker()
	}
	return d
}

// Submit adds an event without blocking. False means sampled out, closed, or dropped.
func (d *Dispatcher) Submit(ctx context.Context, callerID string, implementation detector.Detector, event detector.Event) bool {
	rate, configured := d.rates[callerID]
	if !configured {
		rate = 1
	}
	return d.SubmitWithPolicy(ctx, callerID, implementation, event, Policy{Mode: "report", Timeout: 500 * time.Millisecond, OnError: "report", SampleRate: rate})
}

// SubmitWithPolicy adds one detector task without waiting for queue capacity.
func (d *Dispatcher) SubmitWithPolicy(ctx context.Context, callerID string, implementation detector.Detector, event detector.Event, policy Policy) bool {
	d.submitMu.RLock()
	defer d.submitMu.RUnlock()
	if d.closed.Load() || implementation == nil || ctx == nil || detector.ValidateEvent(event) != nil {
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
	case d.queue <- job{ctx: context.Background(), callerID: callerID, detector: implementation, event: event, policy: policy}:
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
	if p.OnError != "report" && p.OnError != "ignore" {
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
		if item.ctx.Err() != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(item.ctx, item.policy.Timeout)
		signals, err := item.detector.Detect(ctx, item.event)
		cancel()
		if err != nil {
			err = errors.New("detector failed")
		}
		if err != nil && item.policy.OnError == "ignore" {
			continue
		}
		if d.sink != nil && item.ctx.Err() == nil {
			d.sink(Result{CallerID: item.callerID, Mode: item.policy.Mode, Signals: signals, Err: err})
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

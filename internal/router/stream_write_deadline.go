// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// errStreamIdleTimeout reports that a streamed response made no progress within
// the configured stream idle window (#759).
var errStreamIdleTimeout = errors.New("stream idle timeout: no data within the configured stream_idle_timeout_ms")

// streamProgressDeadline turns the absolute http.Server write deadline into a
// progress deadline for event streams (#759).
//
// Go's http.Server.WriteTimeout is an absolute deadline for the whole response,
// so a stream that is still delivering tokens after that window is cut off even
// though it is healthy. For responses whose Content-Type is text/event-stream
// this writer extends the connection write deadline after every successful write
// or flush by the configured stream idle window. A stream that keeps producing
// data therefore outlives the absolute deadline; a downstream that stops
// accepting data hits the deadline. Unary responses are never extended, so their
// absolute bound is unchanged.
type streamProgressDeadline struct {
	http.ResponseWriter
	idle       time.Duration
	streaming  bool
	failed     bool
	controller *http.ResponseController
}

func isEventStreamContentType(value string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "text/event-stream")
}

// maybeStreaming reports whether this response is an event stream, detecting it
// from the header on the first write even when the handler never calls
// WriteHeader explicitly.
func (w *streamProgressDeadline) maybeStreaming() bool {
	if w.streaming {
		return true
	}
	if isEventStreamContentType(w.Header().Get("Content-Type")) {
		w.streaming = true
		return true
	}
	return false
}

func (w *streamProgressDeadline) controllerOrCreate() *http.ResponseController {
	if w.controller == nil {
		w.controller = http.NewResponseController(w.ResponseWriter)
	}
	return w.controller
}

// extend grants the stream another idle window. It runs only after a successful
// write, never before one, so a write that arrives after the window has expired
// is not rescued by it.
func (w *streamProgressDeadline) extend() {
	// A recorder or a test double can reject the deadline; that must not fail
	// the response.
	_ = w.controllerOrCreate().SetWriteDeadline(time.Now().Add(w.idle))
}

func (w *streamProgressDeadline) WriteHeader(code int) {
	w.maybeStreaming()
	w.ResponseWriter.WriteHeader(code)
}

func (w *streamProgressDeadline) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if err == nil && w.idle > 0 && w.maybeStreaming() && !w.failed {
		w.extend()
	}
	return n, err
}

func (w *streamProgressDeadline) Flush() {
	err := w.controllerOrCreate().Flush()
	if err != nil {
		// The connection can no longer take data; do not renew its deadline.
		w.failed = true
		return
	}
	if w.idle > 0 && w.maybeStreaming() && !w.failed {
		w.extend()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *streamProgressDeadline) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// withStreamProgressDeadline installs the progress deadline for event streams.
// A zero or negative stream idle timeout leaves the handler untouched.
func (s *Service) withStreamProgressDeadline(next http.Handler) http.Handler {
	if s == nil || s.cfg == nil {
		return next
	}
	idleMS := s.cfg.Server.StreamIdleTimeoutMS
	if idleMS <= 0 {
		return next
	}
	idle := time.Duration(idleMS) * time.Millisecond
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&streamProgressDeadline{ResponseWriter: w, idle: idle}, r)
	})
}

// streamIdleReader ends a streamed upstream response that stops producing data
// (#759). The write deadline covers a downstream that stops accepting data; this
// reader covers an upstream that stops sending it, so a stalled generation does
// not hold the caller for the whole upstream timeout. A stream that keeps
// producing data is never affected: the timer resets on every read.
type streamIdleReader struct {
	body     io.ReadCloser
	idle     time.Duration
	activity chan struct{}
	stop     chan struct{}
	stalled  atomic.Bool
	closeOne sync.Once
}

func newStreamIdleReader(body io.ReadCloser, idle time.Duration) *streamIdleReader {
	r := &streamIdleReader{
		body:     body,
		idle:     idle,
		activity: make(chan struct{}, 1),
		stop:     make(chan struct{}),
	}
	go r.watch()
	return r
}

func (r *streamIdleReader) watch() {
	// The idle window starts at the first data byte, not at the response
	// headers: a reasoning model or a long edge prefill can send headers and
	// then stay silent for minutes, and that time is bounded by the
	// attempt/upstream timeout instead (#760 review item 1).
	var timer *time.Timer
	var timeout <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-r.stop:
			return
		case <-r.activity:
			if timer == nil {
				timer = time.NewTimer(r.idle)
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(r.idle)
			}
			timeout = timer.C
		case <-timeout:
			r.stalled.Store(true)
			// Closing the body unblocks the pending Read with an error, so the
			// proxy returns and the caller's connection closes now.
			_ = r.body.Close()
			return
		}
	}
}

func (r *streamIdleReader) Read(p []byte) (int, error) {
	n, err := r.body.Read(p)
	if n > 0 {
		select {
		case r.activity <- struct{}{}:
		default:
		}
	}
	if err != nil && r.stalled.Load() {
		return n, errStreamIdleTimeout
	}
	return n, err
}

func (r *streamIdleReader) Close() error {
	r.closeOne.Do(func() { close(r.stop) })
	return r.body.Close()
}

// streamIdleTimeout is the configured progress deadline for event streams.
func (s *Service) streamIdleTimeout() time.Duration {
	if s == nil || s.cfg == nil {
		return 0
	}
	ms := s.cfg.Server.StreamIdleTimeoutMS
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

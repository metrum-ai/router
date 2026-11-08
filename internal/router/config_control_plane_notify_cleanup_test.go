// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"context"
	"testing"
	"time"
)

type delayedRevisionConnectionCleanup struct {
	closed  chan struct{}
	cleaned chan struct{}
}

func (c *delayedRevisionConnectionCleanup) Close(context.Context) error { close(c.closed); return nil }
func (c *delayedRevisionConnectionCleanup) CleanupDone() chan struct{}  { return c.cleaned }
func TestRevisionNotifierAwaitsAsynchronousConnectionCleanup(t *testing.T) {
	conn := &delayedRevisionConnectionCleanup{closed: make(chan struct{}), cleaned: make(chan struct{})}
	done := make(chan struct{})
	go func() { finishRevisionNotificationConnection(conn); close(done) }()
	<-conn.closed
	select {
	case <-done:
		t.Fatal("logical Close returned before asynchronous cleanup")
	case <-time.After(20 * time.Millisecond):
	}
	close(conn.cleaned)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("completed cleanup did not release owner")
	}
}

// A stuck socket must not be able to hold shutdown forever: the bounded wait
// returns once the bound elapses even though cleanup never completes.
func TestRevisionNotifierCleanupWaitIsBounded(t *testing.T) {
	conn := &delayedRevisionConnectionCleanup{closed: make(chan struct{}), cleaned: make(chan struct{})}
	defer close(conn.cleaned)
	const bound = 30 * time.Millisecond
	start := time.Now()
	returned := make(chan struct{})
	go func() { finishRevisionNotificationConnectionWithin(conn, bound); close(returned) }()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("bounded cleanup wait never returned; shutdown could hang")
	}
	if elapsed := time.Since(start); elapsed < bound {
		t.Fatalf("returned after %s, before the %s bound", elapsed, bound)
	}
}

// The bounded wait still waits for cleanup that finishes inside the bound.
func TestRevisionNotifierCleanupWaitHonorsFastCleanup(t *testing.T) {
	conn := &delayedRevisionConnectionCleanup{closed: make(chan struct{}), cleaned: make(chan struct{})}
	go func() { <-conn.closed; close(conn.cleaned) }()
	finished := make(chan struct{})
	go func() { finishRevisionNotificationConnectionWithin(conn, 5*time.Second); close(finished) }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup that completed was not observed")
	}
	select {
	case <-conn.cleaned:
	default:
		t.Fatal("returned before cleanup completed")
	}
}

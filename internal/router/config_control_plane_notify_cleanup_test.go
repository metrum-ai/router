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

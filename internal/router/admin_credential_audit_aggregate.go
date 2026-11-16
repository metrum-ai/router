// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"strings"
	"sync"
	"time"
)

const (
	adminCredentialAuditWindow     = 5 * time.Minute
	adminCredentialAuditMaxPending = 4096
)

type adminCredentialAuditBucket struct {
	event AdminCredentialEvent
	start time.Time
}

type adminCredentialAuditAggregator struct {
	mu      sync.Mutex
	pending map[string]adminCredentialAuditBucket
	timer   *time.Timer
	closed  bool
	write   func(AdminCredentialEvent) error
}

func newAdminCredentialAuditAggregator(write func(AdminCredentialEvent) error) *adminCredentialAuditAggregator {
	return &adminCredentialAuditAggregator{pending: map[string]adminCredentialAuditBucket{}, write: write}
}

func adminCredentialAuditBucketKey(event AdminCredentialEvent, start time.Time) string {
	return strings.Join([]string{
		start.UTC().Format(time.RFC3339), event.Kind, event.CredentialID, event.Subject,
		event.Issuer, event.Outcome, event.ReasonCode, event.ClientIPHash,
	}, "\x00")
}

func (a *adminCredentialAuditAggregator) add(event AdminCredentialEvent) error {
	start := event.At.UTC().Truncate(adminCredentialAuditWindow)
	key := adminCredentialAuditBucketKey(event, start)
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return a.write(event)
	}
	if bucket, ok := a.pending[key]; ok {
		bucket.event.Count++
		bucket.event.At = event.At.UTC()
		a.pending[key] = bucket
		a.scheduleLocked()
		a.mu.Unlock()
		return nil
	}
	if len(a.pending) >= adminCredentialAuditMaxPending {
		a.mu.Unlock()
		// Preserve the success record if a burst exceeds the bounded coalescing
		// map; this uncommon overflow path writes one immutable event directly.
		event.Count = 1
		return a.write(event)
	}
	event.Count = 1
	a.pending[key] = adminCredentialAuditBucket{event: event, start: start}
	a.scheduleLocked()
	a.mu.Unlock()
	return nil
}

func (a *adminCredentialAuditAggregator) scheduleLocked() {
	if a.timer != nil {
		a.timer.Stop()
		a.timer = nil
	}
	if a.closed || len(a.pending) == 0 {
		return
	}
	earliest := time.Time{}
	for _, bucket := range a.pending {
		end := bucket.start.Add(adminCredentialAuditWindow)
		if earliest.IsZero() || end.Before(earliest) {
			earliest = end
		}
	}
	delay := time.Until(earliest)
	if delay < 0 {
		delay = 0
	}
	a.timer = time.AfterFunc(delay, func() { a.flush(time.Now().UTC(), false) })
}

func (a *adminCredentialAuditAggregator) flush(now time.Time, all bool) {
	a.mu.Lock()
	var due []struct {
		key    string
		bucket adminCredentialAuditBucket
	}
	for key, bucket := range a.pending {
		if all || !now.Before(bucket.start.Add(adminCredentialAuditWindow)) {
			due = append(due, struct {
				key    string
				bucket adminCredentialAuditBucket
			}{key, bucket})
			delete(a.pending, key)
		}
	}
	a.scheduleLocked()
	a.mu.Unlock()

	var failed []struct {
		key    string
		bucket adminCredentialAuditBucket
	}
	for _, item := range due {
		item.bucket.event.At = item.bucket.start.Add(adminCredentialAuditWindow)
		item.bucket.event.RequestID = ""
		if err := a.write(item.bucket.event); err != nil {
			failed = append(failed, item)
		}
	}
	if len(failed) == 0 {
		return
	}
	a.mu.Lock()
	for _, item := range failed {
		a.pending[item.key] = item.bucket
	}
	if !a.closed {
		if a.timer != nil {
			a.timer.Stop()
		}
		a.timer = time.AfterFunc(time.Second, func() { a.flush(time.Now().UTC(), false) })
	}
	a.mu.Unlock()
}

func (a *adminCredentialAuditAggregator) flushThrough(now time.Time) {
	a.flush(now.UTC(), false)
}

func (a *adminCredentialAuditAggregator) close() {
	a.mu.Lock()
	a.closed = true
	if a.timer != nil {
		a.timer.Stop()
		a.timer = nil
	}
	a.mu.Unlock()
	a.flush(time.Time{}, true)
}

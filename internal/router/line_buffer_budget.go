// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "sync"

type lineBufferBudget struct {
	mu      sync.Mutex
	limit   int64
	used    int64
	metrics *metricsStore
}

func newLineBufferBudget(limit int64, metrics *metricsStore) *lineBufferBudget {
	b := &lineBufferBudget{limit: limit, metrics: metrics}
	if metrics != nil {
		metrics.SetLineBufferBudget(0, limit, false)
	}
	return b
}

func (b *lineBufferBudget) SetLimit(limit int64) {
	if b == nil || limit <= 0 {
		return
	}
	b.mu.Lock()
	b.limit = limit
	if b.metrics != nil {
		b.metrics.SetLineBufferBudget(b.used, limit, false)
	}
	b.mu.Unlock()
}

func (b *lineBufferBudget) TryAcquire(n int64) bool {
	if b == nil || n <= 0 {
		return true
	}
	b.mu.Lock()
	if n > b.limit-b.used {
		used, limit := b.used, b.limit
		if b.metrics != nil {
			b.metrics.SetLineBufferBudget(used, limit, true)
		}
		b.mu.Unlock()
		return false
	}
	b.used += n
	used, limit := b.used, b.limit
	if b.metrics != nil {
		b.metrics.SetLineBufferBudget(used, limit, false)
	}
	b.mu.Unlock()
	return true
}

func (b *lineBufferBudget) Release(n int64) {
	if b == nil || n <= 0 {
		return
	}
	b.mu.Lock()
	b.used -= n
	if b.used < 0 {
		b.used = 0
	}
	used, limit := b.used, b.limit
	if b.metrics != nil {
		b.metrics.SetLineBufferBudget(used, limit, false)
	}
	b.mu.Unlock()
}

func (b *lineBufferBudget) Used() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

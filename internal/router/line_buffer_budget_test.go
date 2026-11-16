// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"strings"
	"testing"
)

func TestLineBufferBudgetMetricsTrackUseAndRejections(t *testing.T) {
	metrics := newMetricsStore()
	budget := newLineBufferBudget(1024, metrics)
	if !budget.TryAcquire(512) || budget.TryAcquire(1024) {
		t.Fatal("budget acquisition did not enforce its configured limit")
	}
	budget.Release(512)
	scrape := metrics.Prometheus(nil, MigrationStatus{})
	for _, want := range []string{
		"metrum_ai_router_sse_line_buffer_bytes{} 0",
		"metrum_ai_router_sse_line_buffer_limit_bytes{} 1024",
		"metrum_ai_router_sse_line_buffer_rejections_total{} 1",
	} {
		if !strings.Contains(scrape, want) {
			t.Fatalf("metrics missing %q", want)
		}
	}
}

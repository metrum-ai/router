// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"errors"
	"fmt"
	"testing"
)

// OPS-02 (issue #94): committed-stream settlement must say whether the settled
// tokens were provider-reported or the router's reservation estimate.
func TestCommittedStreamUsageProvenance(t *testing.T) {
	req := &IRRequest{Model: "g", MaxTokens: 10}
	reported := upstreamError{Class: "stream_interrupted", Committed: true, PartialUsage: &Usage{InputTokens: 4, OutputTokens: 3, TotalTokens: 7}}
	wrapped := fmt.Errorf("wrap: %w", reported)
	if !committedStreamUsageReported(wrapped) {
		t.Fatal("partial provider usage must count as reported")
	}
	if got := committedStreamUsage(wrapped, req, "openai-chat", 30); got.TotalTokens != 7 {
		t.Fatalf("reported usage = %+v, want total 7", got)
	}
	for _, err := range []error{
		upstreamError{Class: "stream_interrupted", Committed: true},
		upstreamError{Class: "stream_interrupted", Committed: true, PartialUsage: &Usage{}},
		errors.New("plain"),
	} {
		if committedStreamUsageReported(err) {
			t.Fatalf("%v: missing/zero usage must not count as reported", err)
		}
		if got := committedStreamUsage(err, req, "openai-chat", 30); got.TotalTokens <= 0 {
			t.Fatalf("%v: estimate fallback = %+v, want positive", err, got)
		}
	}
}

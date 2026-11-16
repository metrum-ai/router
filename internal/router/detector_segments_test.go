// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"strings"
	"testing"
)

func TestDetectorSegmentsExcludePromptsAndUserMessages(t *testing.T) {
	req := &IRRequest{Raw: map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "private prompt marker"},
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_result", "content": "tool result marker"}}},
		map[string]any{"type": "document", "text": "document marker"},
	}}}
	segments := extractDetectorSegments(req)
	if len(segments) != 2 || segments[0].Kind != "tool_result" || segments[0].Text != "tool result marker" || segments[1].Kind != "document" || segments[1].Text != "document marker" {
		t.Fatalf("segments=%+v", segments)
	}
	for _, segment := range segments {
		if strings.Contains(segment.Text, "private prompt") {
			t.Fatal("prompt text extracted")
		}
	}
}

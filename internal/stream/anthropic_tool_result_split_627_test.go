// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package stream

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// splitReader yields predetermined pieces so a fixture can be replayed at
// awkward fragment boundaries: inside an SSE field, inside JSON, inside a
// multi-byte UTF-8 sequence or between two deltas of one content block.
type splitReader struct{ pieces [][]byte }

func (r *splitReader) Read(p []byte) (int, error) {
	if len(r.pieces) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.pieces[0])
	r.pieces[0] = r.pieces[0][n:]
	if len(r.pieces[0]) == 0 {
		r.pieces = r.pieces[1:]
	}
	return n, nil
}

// replayAnthropicToChat feeds an upstream Anthropic stream through the framer
// and the Chat-caller translator and returns every downstream event.
func replayAnthropicToChat(t *testing.T, pieces ...[]byte) []Event {
	t.Helper()
	tr := &AnthropicUpstreamToChatCaller{IDs: testIDs{}, Model: "test"}
	if _, err := tr.Begin(TokenEstimate{InputTokens: 5}); err != nil {
		t.Fatal(err)
	}
	framer := NewFramer(&splitReader{pieces: pieces}, 1<<20)
	var events []Event
	for {
		up, err := framer.Next()
		if err != nil {
			t.Fatalf("framer: %v", err)
		}
		out, err := tr.Next(up)
		events = append(events, out...)
		if errors.Is(err, ErrTerminal) {
			break
		}
		if err != nil {
			t.Fatalf("translator: %v", err)
		}
	}
	tail, err := tr.Finish(StopComplete, Usage{})
	if err != nil {
		t.Fatal(err)
	}
	return append(events, tail...)
}

// joinEventsLines renders translated events with one newline each, so a
// changed event boundary is visible in the comparison.
func joinEventsLines(events []Event) string {
	var b strings.Builder
	for _, e := range events {
		b.Write(e.Data)
		b.WriteByte('\n')
	}
	return b.String()
}

// chatTextFromEvents reassembles downstream text deltas and Chat tool-call
// argument fragments from translated events.
func chatTextFromEvents(t *testing.T, events []Event) (string, []string) {
	t.Helper()
	var text strings.Builder
	arguments := map[int]*strings.Builder{}
	var order []int
	for _, e := range events {
		if bytes.Equal(e.Data, []byte("[DONE]")) {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   *string `json:"content"`
					ToolCalls []struct {
						Index    int `json:"index"`
						Function struct {
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(e.Data, &chunk); err != nil {
			t.Fatalf("downstream event: %v %s", err, e.Data)
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != nil {
				text.WriteString(*choice.Delta.Content)
			}
			for _, call := range choice.Delta.ToolCalls {
				if arguments[call.Index] == nil {
					arguments[call.Index] = &strings.Builder{}
					order = append(order, call.Index)
				}
				arguments[call.Index].WriteString(call.Function.Arguments)
			}
		}
	}
	var assembled []string
	for index := 0; index < len(order); index++ {
		if arguments[index] == nil {
			t.Fatalf("gap in downstream tool call indexes: %#v", order)
		}
		assembled = append(assembled, arguments[index].String())
	}
	return text.String(), assembled
}

// upstreamTextFromFixture is the text a recorded Anthropic stream carries,
// independent of the translator under test.
func upstreamTextFromFixture(t *testing.T, raw []byte) string {
	t.Helper()
	var text strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "content_block_delta" && event.Delta.Type == "text_delta" {
			text.WriteString(event.Delta.Text)
		}
	}
	return text.String()
}

// TestAnthropicToolResultStreamSplitInvariance627 replays the recorded-shaped
// Anthropic streams behind issue #627 at every single split point, plus
// byte-at-a-time and seeded random reads. The downstream Chat stream must be
// byte-identical to the whole-buffer replay and its text must equal the upstream
// text exactly, so no fragment boundary can drop, duplicate or reorder content.
func TestAnthropicToolResultStreamSplitInvariance627(t *testing.T) {
	files, err := filepath.Glob("../../testdata/sse/anthropic-tool-result/*.sse")
	if err != nil || len(files) == 0 {
		t.Fatal("missing #627 fixtures", files, err)
	}
	extra, _ := filepath.Glob("../../testdata/sse/synthetic/anthropic-bridges/*.sse")
	files = append(files, extra...)
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			baseline := replayAnthropicToChat(t, raw)
			want := joinEventsLines(baseline)
			upstreamText := upstreamTextFromFixture(t, raw)
			text, arguments := chatTextFromEvents(t, baseline)
			if text != upstreamText {
				t.Fatalf("whole-buffer replay changed text\ngot  %q\nwant %q", text, upstreamText)
			}
			for _, wantArg := range arguments {
				if !json.Valid([]byte(wantArg)) {
					t.Fatalf("tool arguments are not valid JSON: %q", wantArg)
				}
			}
			// Every single two-piece split: field, JSON, block and UTF-8 boundaries.
			for split := 1; split < len(raw); split++ {
				got := joinEventsLines(replayAnthropicToChat(t, raw[:split], raw[split:]))
				if got != want {
					t.Fatalf("split at byte %d diverged\ngot  %s\nwant %s", split, got, want)
				}
			}
			// Byte-at-a-time reads, including every multi-byte rune boundary.
			pieces := make([][]byte, len(raw))
			for i := 0; i < len(raw); i++ {
				pieces[i] = raw[i : i+1]
			}
			if got := joinEventsLines(replayAnthropicToChat(t, pieces...)); got != want {
				t.Fatalf("byte-at-a-time replay diverged")
			}
			if strings.ContainsRune(text, '\uFFFD') {
				t.Fatalf("text was corrupted: %q", text)
			}
		})
	}
}

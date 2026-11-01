// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package stream

import (
	"errors"
	"strings"
	"testing"
)

func TestReasoningBridgeStreamOptIn(t *testing.T) {
	anthropic := []string{
		`{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":2,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"visible thought"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"private_signature"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		`{"type":"message_stop"}`,
	}
	responses := []string{
		`{"type":"response.created","response":{"id":"resp_1","created_at":1}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","encrypted_content":"private_cipher"}}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"visible thought"}`,
		`{"type":"response.output_text.delta","output_index":1,"delta":"answer"}`,
		`{"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":2,"output_tokens":2,"total_tokens":4}}}`,
	}
	for _, tc := range []struct {
		name   string
		tr     StreamTranslator
		events []string
		want   string
	}{
		{"anthropic-to-responses", &AnthropicUpstreamToResponsesCaller{ChatUpstreamToResponsesCaller: ChatUpstreamToResponsesCaller{ResponseID: "resp_out", Model: "test", IncludeReasoning: true}}, anthropic, "response.reasoning_summary_text.delta"},
		{"responses-to-anthropic", &ResponsesUpstreamToAnthropicCaller{ChatUpstreamToAnthropicCaller: ChatUpstreamToAnthropicCaller{ResponseID: "msg_out", Model: "test", IncludeReasoning: true}}, responses, "thinking_delta"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events, err := tc.tr.Begin(TokenEstimate{InputTokens: 2})
			if err != nil {
				t.Fatal(err)
			}
			terminal := false
			for _, raw := range tc.events {
				out, err := tc.tr.Next(Event{Data: []byte(raw)})
				events = append(events, out...)
				if errors.Is(err, ErrTerminal) {
					terminal = true
					break
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if !terminal {
				t.Fatal("missing terminal")
			}
			tail, err := tc.tr.Finish(StopComplete, Usage{})
			if err != nil {
				t.Fatal(err)
			}
			events = append(events, tail...)
			var all strings.Builder
			for _, e := range events {
				all.Write(e.Data)
				all.WriteByte('\n')
			}
			got := all.String()
			if !strings.Contains(got, tc.want) || !strings.Contains(got, "visible thought") || !strings.Contains(got, "answer") || strings.Contains(got, "private_signature") || strings.Contains(got, "private_cipher") {
				t.Fatalf("unexpected translated stream: %s", got)
			}
		})
	}
}

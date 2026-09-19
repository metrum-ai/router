// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package stream

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func anthropicTranslator(direction string) StreamTranslator {
	switch direction {
	case "chat-to-anthropic":
		return &ChatUpstreamToAnthropicCaller{IDs: testIDs{}, ResponseID: "msg_test", Model: "test"}
	case "responses-to-anthropic":
		return &ResponsesUpstreamToAnthropicCaller{ChatUpstreamToAnthropicCaller: ChatUpstreamToAnthropicCaller{IDs: testIDs{}, ResponseID: "msg_test", Model: "test"}}
	case "anthropic-to-chat":
		return &AnthropicUpstreamToChatCaller{IDs: testIDs{}, Model: "test"}
	default:
		return &AnthropicUpstreamToResponsesCaller{ChatUpstreamToResponsesCaller: ChatUpstreamToResponsesCaller{IDs: testIDs{}, ResponseID: "resp_test", Model: "test"}}
	}
}
func TestAnthropicBridgesReplay(t *testing.T) {
	for direction, fixture := range map[string]string{"chat-to-anthropic": "openai-chat", "responses-to-anthropic": "responses-to-chat", "anthropic-to-chat": "anthropic-bridges", "anthropic-to-responses": "anthropic-bridges"} {
		files, err := filepath.Glob("../../testdata/sse/synthetic/" + fixture + "/*.sse")
		if err != nil || len(files) == 0 {
			t.Fatal(files, err)
		}
		for _, file := range files {
			for _, size := range []int{1, 7, 4096, -42, -1337} {
				t.Run(fmt.Sprintf("%s/%s/%d", direction, filepath.Base(file), size), func(t *testing.T) {
					raw, err := os.ReadFile(file)
					if err != nil {
						t.Fatal(err)
					}
					r := &chunks{raw: raw, size: size}
					if size < 0 {
						r.rng = rand.New(rand.NewSource(int64(-size)))
					}
					tr := anthropicTranslator(direction)
					events, err := tr.Begin(TokenEstimate{InputTokens: 5})
					if err != nil {
						t.Fatal(err)
					}
					f := NewFramer(r, 1<<20)
					for {
						up, err := f.Next()
						if err != nil {
							t.Fatal(err)
						}
						out, err := tr.Next(up)
						events = append(events, out...)
						if errors.Is(err, ErrTerminal) {
							break
						}
						if err != nil {
							t.Fatal(err)
						}
					}
					tail, err := tr.Finish(StopComplete, Usage{})
					if err != nil {
						t.Fatal(err)
					}
					events = append(events, tail...)
					assertAnthropicBridgeLifecycle(t, direction, events)
					if more, _ := tr.Finish(StopComplete, Usage{}); len(more) > 0 {
						t.Fatal("duplicate finish")
					}
					var got bytes.Buffer
					for _, e := range events {
						got.Write(e.Data)
						got.WriteByte('\n')
					}
					gold := "../../testdata/sse/golden/" + direction + "/" + strings.TrimSuffix(filepath.Base(file), ".sse") + ".jsonl"
					if os.Getenv("UPDATE_GOLDEN") == "1" {
						if err := os.MkdirAll(filepath.Dir(gold), 0755); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(gold, got.Bytes(), 0644); err != nil {
							t.Fatal(err)
						}
					}
					want, err := os.ReadFile(gold)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(got.Bytes(), want) {
						t.Fatalf("golden mismatch\n%s", got.String())
					}
				})
			}
		}
	}
}
func TestAnthropicBridgeRejectsInvalidLifecycle(t *testing.T) {
	for _, raw := range []string{`{`, `{"type":"message_stop"}`, `{"type":"error"}`, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"bad"}}`} {
		tr := &AnthropicUpstreamToChatCaller{}
		tr.Begin(TokenEstimate{})
		if _, err := tr.Next(Event{Data: []byte(raw)}); err == nil {
			t.Fatal(raw)
		}
		if out, _ := tr.Finish(StopError, Usage{}); len(out) > 0 {
			t.Fatal("success on error")
		}
	}
	for _, block := range []string{"thinking", "redacted_thinking"} {
		tr := &AnthropicUpstreamToChatCaller{}
		tr.Begin(TokenEstimate{})
		tr.Next(Event{Data: []byte(`{"type":"message_start","message":{"id":"msg"}}`)})
		if _, err := tr.Next(Event{Data: []byte(fmt.Sprintf(`{"type":"content_block_start","content_block":{"type":%q}}`, block))}); err == nil {
			t.Fatal("synthesized reasoning")
		}
	}
}

func TestAnthropicBridgeEmptyToolAndRawArguments(t *testing.T) {
	for _, args := range []string{"", `{"broken":`} {
		tr := &AnthropicUpstreamToChatCaller{IDs: testIDs{}}
		tr.Begin(TokenEstimate{})
		for _, raw := range []string{`{"type":"message_start","message":{"id":"msg"}}`, `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool","name":"test","input":{}}}`} {
			if _, err := tr.Next(Event{Data: []byte(raw)}); err != nil {
				t.Fatal(err)
			}
		}
		var got []Event
		if args != "" {
			out, err := tr.Next(typedEvent("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": args}}))
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, out...)
		}
		out, err := tr.Next(typedEvent("content_block_stop", map[string]any{"index": 0}))
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, out...)
		want := args
		if want == "" {
			want = "{}"
		}
		quoted, _ := json.Marshal(want)
		if len(got) != 1 || !bytes.Contains(got[0].Data, quoted) {
			t.Fatal("arguments changed", got)
		}
		if _, err := tr.Next(typedEvent("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": "bad"}})); err == nil {
			t.Fatal("delta after block stop")
		}
		if out, _ := tr.Finish(StopCancel, Usage{}); len(out) > 0 {
			t.Fatal("terminator after cancel")
		}
	}
}

func assertAnthropicBridgeLifecycle(t *testing.T, direction string, events []Event) {
	t.Helper()
	open := map[int]bool{}
	roles := 0
	for i, e := range events {
		if string(e.Data) == "[DONE]" {
			if i != len(events)-1 {
				t.Fatal("early DONE")
			}
			continue
		}
		var p map[string]any
		if err := json.Unmarshal(e.Data, &p); err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(direction, "-anthropic") {
			if e.Name != e.Type {
				t.Fatal("missing event name")
			}
			switch e.Type {
			case "message_start":
				if i != 0 {
					t.Fatal("late start")
				}
			case "content_block_start":
				index := int(p["index"].(float64))
				if open[index] {
					t.Fatal("duplicate block")
				}
				open[index] = true
			case "content_block_delta", "content_block_stop":
				index := int(p["index"].(float64))
				if !open[index] {
					t.Fatal("event outside block")
				}
				if e.Type == "content_block_stop" {
					delete(open, index)
				}
			case "message_delta":
				if len(open) != 0 {
					t.Fatal("unclosed blocks")
				}
			}
		} else if direction == "anthropic-to-responses" {
			if p["sequence_number"] != float64(i) {
				t.Fatal("sequence gap")
			}
		} else {
			choices := p["choices"].([]any)
			for _, v := range choices {
				c := v.(map[string]any)
				d := c["delta"].(map[string]any)
				if d["role"] != nil {
					roles++
				}
				if c["finish_reason"] != nil && len(d) != 0 {
					t.Fatal("nonempty finish delta")
				}
			}
		}
	}
	last := events[len(events)-1]
	if strings.HasSuffix(direction, "-anthropic") && last.Type != "message_stop" {
		t.Fatal("missing message_stop")
	}
	if direction == "anthropic-to-chat" && (roles != 1 || string(last.Data) != "[DONE]") {
		t.Fatal("invalid Chat lifecycle")
	}
	if direction == "anthropic-to-responses" && last.Type != "response.completed" && last.Type != "response.incomplete" {
		t.Fatal("missing Responses terminal")
	}
}

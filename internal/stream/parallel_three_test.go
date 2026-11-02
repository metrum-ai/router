// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package stream

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// Three-call synthetic protocol fixtures exercise each translating stream
// direction. They are not captures from a live provider.
func TestParallelThreeStreamTranslations(t *testing.T) {
	cases := []struct {
		name, fixture, caller string
		newTranslator         func() StreamTranslator
	}{
		{"chat-to-responses", "chat-three.sse", "responses", func() StreamTranslator { return &ChatUpstreamToResponsesCaller{ResponseID: "resp_test", Model: "test"} }},
		{"chat-to-messages", "chat-three.sse", "messages", func() StreamTranslator { return &ChatUpstreamToAnthropicCaller{ResponseID: "msg_test", Model: "test"} }},
		{"responses-to-chat", "responses-three.sse", "chat", func() StreamTranslator { return &ResponsesUpstreamToChatCaller{Model: "test"} }},
		{"responses-to-messages", "responses-three.sse", "messages", func() StreamTranslator {
			return &ResponsesUpstreamToAnthropicCaller{ChatUpstreamToAnthropicCaller: ChatUpstreamToAnthropicCaller{ResponseID: "msg_test", Model: "test"}}
		}},
		{"messages-to-chat", "anthropic-three.sse", "chat", func() StreamTranslator { return &AnthropicUpstreamToChatCaller{Model: "test", IncludeUsage: true} }},
		{"messages-to-responses", "anthropic-three.sse", "responses", func() StreamTranslator {
			return &AnthropicUpstreamToResponsesCaller{ChatUpstreamToResponsesCaller: ChatUpstreamToResponsesCaller{ResponseID: "resp_test", Model: "test"}}
		}},
	}
	for _, tc := range cases {
		for _, size := range []int{1, 7, 4096, -42, -1337} {
			t.Run(fmt.Sprintf("%s/chunk-%d", tc.name, size), func(t *testing.T) {
				raw, err := os.ReadFile(filepath.Join("../../testdata/sse/parallel", tc.fixture))
				if err != nil {
					t.Fatal(err)
				}
				reader := &chunks{raw: raw, size: size}
				if size < 0 {
					reader.rng = rand.New(rand.NewSource(int64(-size)))
				}
				tr := tc.newTranslator()
				events, err := tr.Begin(TokenEstimate{InputTokens: 5})
				if err != nil {
					t.Fatal(err)
				}
				framer := NewFramer(reader, 1<<20)
				for {
					up, err := framer.Next()
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
				ids, args, itemIDs := map[int]string{}, map[int]string{}, map[int]string{}
				var order []int
				stop := false
				for _, event := range events {
					if string(event.Data) == "[DONE]" {
						continue
					}
					var p map[string]any
					if err := json.Unmarshal(event.Data, &p); err != nil {
						t.Fatal(err)
					}
					switch tc.caller {
					case "chat":
						choices, _ := p["choices"].([]any)
						for _, c := range choices {
							choice := c.(map[string]any)
							if choice["finish_reason"] == "tool_calls" {
								stop = true
							}
							delta, _ := choice["delta"].(map[string]any)
							calls, _ := delta["tool_calls"].([]any)
							for _, c := range calls {
								call := c.(map[string]any)
								index := int(call["index"].(float64))
								if id, ok := call["id"].(string); ok {
									ids[index] = id
									order = append(order, index)
								}
								if fn, ok := call["function"].(map[string]any); ok {
									if s, ok := fn["arguments"].(string); ok {
										args[index] += s
									}
								}
							}
						}
					case "responses":
						typ, _ := p["type"].(string)
						if typ == "response.completed" {
							stop = true
						}
						if typ == "response.output_item.added" {
							item, _ := p["item"].(map[string]any)
							if item["type"] == "function_call" {
								index := int(p["output_index"].(float64))
								ids[index] = item["call_id"].(string)
								itemIDs[index] = item["id"].(string)
								order = append(order, index)
							}
						}
						if typ == "response.function_call_arguments.delta" {
							index := int(p["output_index"].(float64))
							if itemIDs[index] == "" || p["item_id"] != itemIDs[index] {
								t.Fatalf("unknown item mapping: %#v", p)
							}
							args[index] += p["delta"].(string)
						}
					case "messages":
						typ, _ := p["type"].(string)
						if typ == "message_delta" {
							d, _ := p["delta"].(map[string]any)
							if d["stop_reason"] == "tool_use" {
								stop = true
							}
						}
						if typ == "content_block_start" {
							block, _ := p["content_block"].(map[string]any)
							if block["type"] == "tool_use" {
								index := int(p["index"].(float64))
								ids[index] = block["id"].(string)
								order = append(order, index)
							}
						}
						if typ == "content_block_delta" {
							d, _ := p["delta"].(map[string]any)
							if d["type"] == "input_json_delta" {
								index := int(p["index"].(float64))
								if ids[index] == "" {
									t.Fatalf("delta before tool start: %#v", p)
								}
								args[index] += d["partial_json"].(string)
							}
						}
					}
				}
				if !stop || len(ids) != 3 || len(order) != 3 {
					t.Fatalf("missing tools or finish: ids=%#v order=%#v stop=%v", ids, order, stop)
				}
				seen := map[string]bool{}
				for i, index := range order {
					id := ids[index]
					if id != []string{"call_a", "call_b", "call_c"}[i] || seen[id] {
						t.Fatalf("call IDs lost, reordered or duplicated: %#v", ids)
					}
					seen[id] = true
					var input map[string]any
					if err := json.Unmarshal([]byte(args[index]), &input); err != nil || input["path"] != []string{"a", "b", "c"}[i] {
						t.Fatalf("call %d id=%s args=%q err=%v", i, id, args[index], err)
					}
				}
				if u, ok := tr.(interface{ UsageSnapshot() Usage }); ok {
					usage := u.UsageSnapshot()
					if usage.InputTokens != 5 || usage.OutputTokens != 3 {
						t.Fatalf("usage lost: %#v", usage)
					}
				} else {
					t.Fatal("translator does not expose usage")
				}
			})
		}
	}
}

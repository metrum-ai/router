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

func TestChatUpstreamToResponsesCallerReplay(t *testing.T) {
	files, _ := filepath.Glob("../../testdata/sse/synthetic/openai-chat/*.sse")
	if len(files) == 0 {
		t.Fatal("missing fixtures")
	}
	for _, file := range files {
		for _, size := range []int{1, 7, 4096, -42, -1337} {
			t.Run(fmt.Sprintf("%s/%d", filepath.Base(file), size), func(t *testing.T) {
				raw, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				r := &chunks{raw: raw, size: size}
				if size < 0 {
					r.rng = rand.New(rand.NewSource(int64(-size)))
				}
				tr := &ChatUpstreamToResponsesCaller{IDs: testIDs{}, ResponseID: "resp_test", Model: "test"}
				events, err := tr.Begin(TokenEstimate{})
				if err != nil {
					t.Fatal(err)
				}
				f := NewFramer(r, 1<<20)
				for {
					up, e := f.Next()
					if e != nil {
						t.Fatal(e)
					}
					out, e := tr.Next(up)
					events = append(events, out...)
					if errors.Is(e, ErrTerminal) {
						break
					}
					if e != nil {
						t.Fatal(e)
					}
				}
				out, err := tr.Finish(StopComplete, Usage{})
				if err != nil {
					t.Fatal(err)
				}
				events = append(events, out...)
				if more, _ := tr.Finish(StopComplete, Usage{}); len(more) > 0 {
					t.Fatal("duplicate Finish")
				}
				var got bytes.Buffer
				items := map[string]bool{}
				parts := map[string]bool{}
				for n, e := range events {
					var p map[string]any
					if err := json.Unmarshal(e.Data, &p); err != nil {
						t.Fatal(err)
					}
					if p["sequence_number"] != float64(n) {
						t.Fatal("sequence gap", p)
					}
					switch e.Type {
					case "response.output_item.added":
						items[p["item"].(map[string]any)["id"].(string)] = true
					case "response.content_part.added":
						parts[p["item_id"].(string)] = true
					case "response.output_text.delta":
						if !parts[p["item_id"].(string)] {
							t.Fatal("delta before part")
						}
					case "response.function_call_arguments.delta":
						if !items[p["item_id"].(string)] {
							t.Fatal("unknown tool item")
						}
					}
					got.Write(e.Data)
					got.WriteByte('\n')
				}
				if events[0].Type != "response.created" {
					t.Fatal("missing created")
				}
				terminal := "response.completed"
				if strings.Contains(file, "length") {
					terminal = "response.incomplete"
				}
				if events[len(events)-1].Type != terminal {
					t.Fatal("missing terminal")
				}
				gold := "../../testdata/sse/golden/openai-chat-to-openai-responses/" + strings.TrimSuffix(filepath.Base(file), ".sse") + ".jsonl"
				if os.Getenv("UPDATE_GOLDEN") == "1" {
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
func TestChatUpstreamToResponsesCallerFaults(t *testing.T) {
	for _, raw := range []string{`{`, `{"error":{"message":"failed"}}`, `[DONE]`, `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`} {
		tr := &ChatUpstreamToResponsesCaller{ResponseID: "resp_test"}
		tr.Begin(TokenEstimate{})
		if _, err := tr.Next(Event{Data: []byte(raw)}); err == nil || errors.Is(err, ErrTerminal) {
			t.Fatal("accepted", raw)
		}
		if out, _ := tr.Finish(StopError, Usage{}); len(out) > 0 {
			t.Fatal("fabricated success")
		}
	}
}

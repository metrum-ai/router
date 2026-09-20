// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package stream

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResponsesUpstreamToChatCallerReplay(t *testing.T) {
	files, _ := filepath.Glob("../../testdata/sse/synthetic/responses-to-chat/*.sse")
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
				tr := &ResponsesUpstreamToChatCaller{IDs: testIDs{}, Model: "test"}
				out, err := tr.Begin(TokenEstimate{})
				if err != nil || len(out) != 0 {
					t.Fatal(out, err)
				}
				var got bytes.Buffer
				f := NewFramer(r, 1<<20)
				for {
					up, err := f.Next()
					if err != nil {
						t.Fatal(err)
					}
					out, err = tr.Next(up)
					for _, e := range out {
						got.Write(e.Frame())
					}
					if errors.Is(err, ErrTerminal) {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				out, err = tr.Finish(StopComplete, Usage{})
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range out {
					got.Write(e.Frame())
				}
				if out, _ = tr.Finish(StopComplete, Usage{}); len(out) > 0 {
					t.Fatal("duplicate finish")
				}
				gold := "../../testdata/sse/golden/openai-responses-to-openai-chat/" + strings.TrimSuffix(filepath.Base(file), ".sse") + ".sse"
				if os.Getenv("UPDATE_GOLDEN") == "1" {
					if err := os.WriteFile(gold, got.Bytes(), 0644); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(gold)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(want, got.Bytes()) {
					t.Fatalf("golden mismatch\n%s", got.String())
				}
			})
		}
	}
}
func TestResponsesUpstreamToChatCallerFaults(t *testing.T) {
	for _, raw := range []string{`{`, `[DONE]`, `{"type":"error"}`, `{"type":"response.failed"}`, `{"type":"response.function_call_arguments.delta","delta":"{"}`, `{"type":"response.output_item.added","item":{"type":"function_call"}}`, `{"type":"response.completed"}`} {
		tr := &ResponsesUpstreamToChatCaller{}
		tr.Begin(TokenEstimate{})
		tr.Next(Event{Data: []byte(`{"type":"response.created","response":{"id":"resp_test"}}`)})
		if _, err := tr.Next(Event{Data: []byte(raw)}); err == nil || errors.Is(err, ErrTerminal) {
			t.Fatal("accepted", raw)
		}
		if out, _ := tr.Finish(StopError, Usage{}); len(out) > 0 {
			t.Fatal("fabricated success")
		}
	}
}

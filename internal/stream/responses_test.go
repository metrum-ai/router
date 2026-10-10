// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package stream

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type testIDs map[string]string

func (m testIDs) Encode(s string) string {
	if v, ok := m[s]; ok {
		return v
	}
	v := "mr_TEST"
	if len(m) > 0 {
		v += fmt.Sprintf("_%d", len(m))
	}
	m[s] = v
	return v
}

type chunks struct {
	raw  []byte
	size int
	rng  *rand.Rand
}

func (r *chunks) Read(p []byte) (int, error) {
	if len(r.raw) == 0 {
		return 0, io.EOF
	}
	n := r.size
	if r.rng != nil {
		n = 1 + r.rng.Intn(53)
	}
	if n > len(p) {
		n = len(p)
	}
	if n > len(r.raw) {
		n = len(r.raw)
	}
	copy(p, r.raw[:n])
	r.raw = r.raw[n:]
	return n, nil
}
func TestReplayChunkMatrix(t *testing.T) {
	files, err := filepath.Glob("../../testdata/sse/synthetic/openai-responses/*.sse")
	if err != nil || len(files) == 0 {
		t.Fatal("missing fixtures", err)
	}
	for _, file := range files {
		for _, mode := range []string{"whole-event", "byte", "split-utf8", "split-json", "random-seed-42", "random-seed-1337"} {
			t.Run(filepath.Base(file)+"/"+mode, func(t *testing.T) {
				raw, _ := os.ReadFile(file)
				var r io.Reader = bytes.NewReader(raw)
				switch mode {
				case "whole-event":
					var readers []io.Reader
					for _, frame := range bytes.SplitAfter(raw, []byte("\n\n")) {
						readers = append(readers, bytes.NewReader(frame))
					}
					r = io.MultiReader(readers...)
				case "byte", "split-utf8":
					r = &chunks{raw: raw, size: 1}
				case "split-json":
					r = &chunks{raw: raw, size: 7}
				default:
					seed := int64(42)
					if mode == "random-seed-1337" {
						seed = 1337
					}
					r = &chunks{raw: raw, rng: rand.New(rand.NewSource(seed))}
				}
				tr := &Responses{IDs: testIDs{}}
				tr.Begin(TokenEstimate{})
				f := NewFramer(r, 1<<20)
				var got []any
				for {
					up, e := f.Next()
					if e != nil {
						t.Fatal(e)
					}
					events, e := tr.Next(up)
					for _, event := range events {
						var v any
						if err := json.Unmarshal(event.Data, &v); err != nil {
							t.Fatal(err)
						}
						got = append(got, v)
					}
					if errors.Is(e, ErrTerminal) {
						break
					}
					if e != nil {
						t.Fatal(e)
					}
				}
				if events, e := tr.Finish(StopComplete, Usage{}); e != nil || len(events) != 0 {
					t.Fatal(events, e)
				}
				gold, _ := os.ReadFile("../../testdata/sse/golden/openai-responses-to-openai-responses/" + strings.TrimSuffix(filepath.Base(file), ".sse") + ".jsonl")
				var want []any
				d := json.NewDecoder(bytes.NewReader(gold))
				for {
					var v any
					e := d.Decode(&v)
					if e == io.EOF {
						break
					}
					if e != nil {
						t.Fatal(e)
					}
					want = append(want, v)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatal("golden mismatch")
				}
				if strings.Contains(file, "unknown-event") && tr.UnknownEvents != 1 {
					t.Fatal(tr.UnknownEvents)
				}
			})
		}
	}
}
func TestFramer(t *testing.T) {
	f := NewFramer(strings.NewReader(": comment\r\n\r\nevent:test\r\ndata: {\r\ndata:\"a\":1}\r\n\r\n"), 0)
	e, err := f.Next()
	if err != nil || e.Name != "test" || string(e.Data) != "{\n\"a\":1}" {
		t.Fatal(e, err)
	}
	for _, raw := range []string{"data: {}", "data: {}\n"} {
		_, err = NewFramer(strings.NewReader(raw), 0).Next()
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatal(err)
		}
	}
	_, err = NewFramer(strings.NewReader("data: "+strings.Repeat("x", 100000)), 16).Next()
	if !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
}
func TestResponsesValidationAndIDs(t *testing.T) {
	tr := &Responses{IDs: testIDs{}}
	_, err := tr.Next(Event{Data: []byte(`{`)})
	if err == nil {
		t.Fatal("malformed JSON accepted")
	}
	_, err = tr.Next(Event{Name: "wrong", Data: []byte(`{"type":"response.created"}`)})
	if err == nil {
		t.Fatal("mismatch accepted")
	}
	events, err := tr.Next(Event{Data: []byte(`{"type":"response.completed","response":{"id":"resp_a","output":[{"id":"fc_a","call_id":"call_a","arguments":"call_a","metadata":{"id":"leave_me"}}]}}`)})
	if !errors.Is(err, ErrTerminal) {
		t.Fatal(err)
	}
	if bytes.Contains(events[0].Data, []byte(`"id":"fc_a"`)) || !bytes.Contains(events[0].Data, []byte(`"arguments":"call_a"`)) || !bytes.Contains(events[0].Data, []byte(`"id":"leave_me"`)) {
		t.Fatal(string(events[0].Data))
	}
	if events, _ = tr.Next(Event{}); len(events) != 0 {
		t.Fatal("event after terminal")
	}
}

// eventModels collects every JSON "model" string in the caller events.
func eventModels(t *testing.T, events []Event) []string {
	t.Helper()
	var models []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				if s, ok := child.(string); ok && k == "model" {
					models = append(models, s)
					continue
				}
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	for _, e := range events {
		if bytes.Equal(bytes.TrimSpace(e.Data), []byte("[DONE]")) {
			continue
		}
		var payload any
		if err := json.Unmarshal(e.Data, &payload); err != nil {
			t.Fatalf("event %q: %v", e.Data, err)
		}
		walk(payload)
	}
	return models
}

func runTranslator(t *testing.T, tr StreamTranslator, raw []byte) []Event {
	t.Helper()
	events, err := tr.Begin(TokenEstimate{InputTokens: 5})
	if err != nil {
		t.Fatal(err)
	}
	f := NewFramer(bytes.NewReader(raw), 1<<20)
	for {
		up, err := f.Next()
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
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
	out, err := tr.Finish(StopComplete, Usage{InputTokens: 5, OutputTokens: 3, TotalTokens: 8})
	if err != nil {
		t.Fatal(err)
	}
	return append(events, out...)
}

// Metrum AI issue #256: every caller-facing model a translator emits
// (created, completed and terminal usage chunks) is PublicModel when set and
// the upstream Model otherwise.
func TestTranslatorsEmitPublicModel(t *testing.T) {
	const upstream, public = "gpt-6-luna", "big-coder"
	responsesFrames := strings.Join([]string{
		"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-6-luna\",\"status\":\"in_progress\",\"output\":[]}}",
		"event: response.in_progress\ndata: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-6-luna\",\"status\":\"in_progress\",\"output\":[]}}",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"gpt-6-luna here\"}",
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-6-luna\",\"status\":\"completed\",\"output\":[]}}",
	}, "\n\n") + "\n\n"
	fixture := func(dir string) []byte {
		raw, err := os.ReadFile("../../testdata/sse/synthetic/" + dir + "/tools.sse")
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	cases := []struct {
		name string
		make func(public string) StreamTranslator
		raw  []byte
	}{
		{"responses", func(p string) StreamTranslator { return &Responses{PublicModel: p} }, []byte(responsesFrames)},
		{"chat-to-responses", func(p string) StreamTranslator {
			return &ChatUpstreamToResponsesCaller{ResponseID: "resp_1", Model: upstream, PublicModel: p}
		}, fixture("openai-chat")},
		{"responses-to-chat", func(p string) StreamTranslator {
			return &ResponsesUpstreamToChatCaller{Model: upstream, PublicModel: p}
		}, fixture("responses-to-chat")},
		{"chat-to-anthropic", func(p string) StreamTranslator {
			return &ChatUpstreamToAnthropicCaller{ResponseID: "msg_1", Model: upstream, PublicModel: p}
		}, fixture("openai-chat")},
		{"responses-to-anthropic", func(p string) StreamTranslator {
			return &ResponsesUpstreamToAnthropicCaller{ChatUpstreamToAnthropicCaller: ChatUpstreamToAnthropicCaller{ResponseID: "msg_1", Model: upstream, PublicModel: p}}
		}, fixture("responses-to-chat")},
		{"anthropic-to-chat", func(p string) StreamTranslator {
			return &AnthropicUpstreamToChatCaller{Model: upstream, PublicModel: p}
		}, fixture("anthropic-bridges")},
		{"anthropic-to-responses", func(p string) StreamTranslator {
			return &AnthropicUpstreamToResponsesCaller{ChatUpstreamToResponsesCaller: ChatUpstreamToResponsesCaller{ResponseID: "resp_1", Model: upstream, PublicModel: p}}
		}, fixture("anthropic-bridges")},
	}
	for _, tc := range cases {
		for _, p := range []string{"", public} {
			t.Run(tc.name+"/"+defaultModelLabel(p), func(t *testing.T) {
				events := runTranslator(t, tc.make(p), tc.raw)
				models := eventModels(t, events)
				want := upstream
				if p != "" {
					want = public
				}
				if len(models) < 2 && tc.name != "chat-to-anthropic" && tc.name != "responses-to-anthropic" {
					t.Fatalf("models=%v, want created and terminal model fields", models)
				}
				if len(models) == 0 {
					t.Fatal("no model fields emitted")
				}
				for _, got := range models {
					if got != want {
						t.Fatalf("models=%v, want all %q", models, want)
					}
				}
				if tc.name == "responses" && !bytes.Contains(joinEvents(events), []byte("gpt-6-luna here")) {
					t.Fatal("delta text rewritten")
				}
			})
		}
	}
}

func defaultModelLabel(p string) string {
	if p == "" {
		return "upstream"
	}
	return "public"
}

func joinEvents(events []Event) []byte {
	var out []byte
	for _, e := range events {
		out = append(out, e.Data...)
	}
	return out
}

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

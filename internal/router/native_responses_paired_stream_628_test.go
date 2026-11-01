// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type event628 struct {
	Event string
	Data  map[string]any
}

func frames628(s string) ([]event628, error) {
	var result []event628
	for _, block := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n\n") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		var event string
		var data []string
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "event:") {
				event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			} else if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &v); err != nil {
			return nil, err
		}
		result = append(result, event628{event, v})
	}
	return result, nil
}
func response628(turn int) string {
	var events []event628
	add := func(name string, data map[string]any) {
		data["type"] = name
		data["sequence_number"] = len(events)
		events = append(events, event628{name, data})
	}
	response := func(status string, output []any) map[string]any {
		return map[string]any{"id": fmt.Sprintf("resp_%d", turn), "object": "response", "model": "bridge", "status": status, "output": output}
	}
	add("response.created", map[string]any{"response": response("in_progress", []any{})})
	add("response.in_progress", map[string]any{"response": response("in_progress", []any{})})
	if turn == 1 {
		args := `{"path":"quote.txt"}`
		call := map[string]any{"type": "function_call", "id": "fc_628", "call_id": "call_628", "name": "read", "arguments": "", "status": "in_progress"}
		add("response.output_item.added", map[string]any{"output_index": 0, "item": call})
		// Fourteen fragments match the observed lifecycle without using live content.
		for i := 0; i < 14; i++ {
			start := i * len(args) / 14
			end := (i + 1) * len(args) / 14
			add("response.function_call_arguments.delta", map[string]any{"item_id": "fc_628", "output_index": 0, "delta": args[start:end]})
		}
		add("response.function_call_arguments.done", map[string]any{"item_id": "fc_628", "output_index": 0, "arguments": args})
		done := map[string]any{"type": "function_call", "id": "fc_628", "call_id": "call_628", "name": "read", "arguments": args, "status": "completed"}
		add("response.output_item.done", map[string]any{"output_index": 0, "item": done})
		add("response.completed", map[string]any{"response": response("completed", []any{done})})
	} else {
		text := "The file was empty."
		add("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"id": "msg_628", "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}})
		add("response.content_part.added", map[string]any{"item_id": "msg_628", "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
		for i := 0; i < 13; i++ {
			start := i * len(text) / 13
			end := (i + 1) * len(text) / 13
			add("response.output_text.delta", map[string]any{"item_id": "msg_628", "output_index": 0, "content_index": 0, "delta": text[start:end]})
		}
		add("response.output_text.done", map[string]any{"item_id": "msg_628", "output_index": 0, "content_index": 0, "text": text})
		part := map[string]any{"type": "output_text", "text": text, "annotations": []any{}}
		add("response.content_part.done", map[string]any{"item_id": "msg_628", "output_index": 0, "content_index": 0, "part": part})
		msg := map[string]any{"id": "msg_628", "type": "message", "role": "assistant", "status": "completed", "content": []any{part}}
		add("response.output_item.done", map[string]any{"output_index": 0, "item": msg})
		add("response.completed", map[string]any{"response": response("completed", []any{msg})})
	}
	var out strings.Builder
	for _, e := range events {
		raw, _ := json.Marshal(e.Data)
		fmt.Fprintf(&out, "event: %s\ndata: %s\n\n", e.Event, raw)
	}
	return out.String()
}

// Synthetic two-turn control; it does not reproduce or explain the live model
// behavior. The mock deliberately answers "empty" despite the correct result.
func TestNativeResponses628CompletePairedStream(t *testing.T) {
	var requests []map[string]any
	var streams []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Error(err)
		}
		requests = append(requests, b)
		s := response628(len(requests))
		streams = append(streams, s)
		w.Header().Set("Content-Type", "text/event-stream")
		for off := 0; off < len(s); {
			end := off + 11
			if end > len(s) {
				end = len(s)
			}
			_, _ = io.WriteString(w, s[off:end])
			w.(http.Flusher).Flush()
			off = end
		}
	}))
	defer up.Close()
	target := anthropicToolTarget()
	target.ForceStoreFalse = true
	svc := nativeStreamTestService(t, up.URL, "openai-responses", []Target{target})
	post := func(input []any) *httptest.ResponseRecorder {
		body := map[string]any{"model": "native-stream", "input": input, "stream": true, "store": false, "tools": []any{map[string]any{"type": "function", "name": "read", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}}}
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(string(raw)))
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		return w
	}
	input := []any{map[string]any{"role": "user", "content": "read quote.txt"}}
	first := post(input)
	if first.Code != 200 {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	firstFrames, err := frames628(first.Body.String())
	if err != nil {
		t.Fatal(err)
	}
	var call map[string]any
	for _, e := range firstFrames {
		if e.Event == "response.output_item.done" {
			call, _ = e.Data["item"].(map[string]any)
		}
	}
	if call == nil || call["id"] != "fc_628" || call["call_id"] != "call_628" {
		t.Fatalf("tool call missing: %#v", call)
	}
	var args struct {
		Path string `json:"path"`
	}
	if err = json.Unmarshal([]byte(call["arguments"].(string)), &args); err != nil || args.Path != "quote.txt" {
		t.Fatalf("arguments invalid: %#v %v", args, err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, args.Path)
	if err = os.WriteFile(file, []byte("E2E-TOOL-RESULT-7842\n"), 0600); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	input = append(input, call, map[string]any{"type": "function_call_output", "call_id": call["call_id"], "output": string(content)})
	second := post(input)
	if second.Code != 200 {
		t.Fatalf("second status=%d body=%s", second.Code, second.Body.String())
	}
	if len(requests) != 2 || len(streams) != 2 {
		t.Fatalf("wrong number of requests")
	}
	got := requests[1]["input"].([]any)
	if !reflect.DeepEqual(got, input) {
		t.Fatalf("tool continuation changed: %#v", got)
	}
	for i, down := range []string{first.Body.String(), second.Body.String()} {
		a, err := frames628(streams[i])
		if err != nil {
			t.Fatal(err)
		}
		b, err := frames628(down)
		if err != nil {
			t.Fatal(err)
		}
		want := 20
		if i == 1 {
			want = 21
		}
		if len(a) != want || len(b) != want || !reflect.DeepEqual(a, b) {
			t.Fatalf("turn %d event sequence or payload changed: upstream=%d downstream=%d\n%#v\n%#v", i+1, len(a), len(b), a, b)
		}
	}
	if !strings.Contains(second.Body.String(), "The file was empty.") {
		t.Fatal("mock provider's negative answer was not preserved")
	}
}

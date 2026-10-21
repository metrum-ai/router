// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metrum-ai/router/internal/stream"
)

func TestBareCRStreamedToolContinuationLocalhost(t *testing.T) {
	var turns atomic.Int64
	var sawResult atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body["stream"] != true {
			t.Error("upstream request was not streaming")
		}
		turn := turns.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(chunk map[string]any) {
			raw, _ := json.Marshal(map[string]any{"id": "chat_cr", "model": "bridge", "choices": []any{chunk}})
			// Both a bare CR between fields and CR/CRLF/LF mixed frame boundaries.
			fmt.Fprintf(w, "event: message\rdata: %s\r\r\n\n", raw)
			w.(http.Flusher).Flush()
		}
		if turn == 1 {
			emit(map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call_1", "type": "function", "function": map[string]any{"name": "write", "arguments": "{\"path\":\"created.txt\"}"}}}}})
			emit(map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"})
		} else {
			raw, _ := json.Marshal(body)
			if strings.Contains(string(raw), `"tool_call_id":"call_1"`) && strings.Contains(string(raw), "written") {
				sawResult.Store(true)
			} else {
				t.Errorf("tool continuation lost: %s", raw)
			}
			emit(map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "done"}})
			emit(map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"})
		}
		fmt.Fprint(w, "data: [DONE]\r\n\r\n")
	}))
	defer upstream.Close()
	svc := nativeStreamTestService(t, upstream.URL, "openai-chat", []Target{anthropicToolTarget()})
	var listener net.Listener
	var err error
	for port := 18156; port < 18166; port++ {
		listener, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: svc.Handler()}
	go server.Serve(listener)
	defer server.Close()
	base := "http://" + listener.Addr().String()
	client := &http.Client{Timeout: 15 * time.Second}
	send := func(messages []any) []map[string]any {
		raw, _ := json.Marshal(map[string]any{"model": "native-stream", "stream": true, "max_tokens": 100, "tools": []any{map[string]any{"name": "write", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}}, "messages": messages})
		req, _ := http.NewRequest("POST", base+"/anthropic/v1/messages", strings.NewReader(string(raw)))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("status=%d %s", resp.StatusCode, body)
		}
		if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatal(resp.Header)
		}
		fr := stream.NewFramer(resp.Body, 1<<20)
		var events []map[string]any
		for {
			event, err := fr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			var data map[string]any
			if err := json.Unmarshal(event.Data, &data); err != nil {
				t.Fatalf("event %q: %v", event.Data, err)
			}
			events = append(events, data)
		}
		if len(events) == 0 || events[len(events)-1]["type"] != "message_stop" {
			t.Fatalf("incomplete stream: %#v", events)
		}
		return events
	}
	user := map[string]any{"role": "user", "content": "create a file"}
	first := send([]any{user})
	var tool map[string]any
	var args strings.Builder
	for _, event := range first {
		if block, ok := event["content_block"].(map[string]any); ok && block["type"] == "tool_use" {
			tool = block
		}
		if delta, ok := event["delta"].(map[string]any); ok && delta["type"] == "input_json_delta" {
			args.WriteString(stringValue(delta["partial_json"]))
		}
	}
	if tool == nil || tool["name"] != "write" || tool["id"] != "call_1" {
		t.Fatalf("tool missing: %#v", first)
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(args.String()), &input); err != nil {
		t.Fatal(err)
	}
	if input["path"] != "created.txt" {
		t.Fatalf("unexpected path: %#v", input)
	}
	path := filepath.Join(t.TempDir(), "created.txt")
	if err := os.WriteFile(path, []byte("written"), 0600); err != nil {
		t.Fatal(err)
	}
	tool["input"] = input
	final := send([]any{user, map[string]any{"role": "assistant", "content": []any{tool}}, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": tool["id"], "content": "written"}}}})
	var text strings.Builder
	for _, event := range final {
		if delta, ok := event["delta"].(map[string]any); ok && delta["type"] == "text_delta" {
			text.WriteString(stringValue(delta["text"]))
		}
	}
	if text.String() != "done" || turns.Load() != 2 || !sawResult.Load() {
		t.Fatalf("continuation text=%q turns=%d result=%v", text.String(), turns.Load(), sawResult.Load())
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "written" {
		t.Fatalf("file: %q %v", raw, err)
	}
	t.Logf("%s: bare-CR upstream translated to streamed tool_use; client created created.txt; upstream received call_1 tool result; streamed continuation completed: %q", base, text.String())
}

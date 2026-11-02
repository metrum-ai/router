// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// Synthetic native two-turn controls. The fake provider verifies delivery and
// returns fixed marker text; these tests do not establish model comprehension.
func TestParallelNativeUnaryContinuation(t *testing.T) {
	for _, dialect := range []string{"openai-chat", "openai-responses", "anthropic"} {
		t.Run(dialect, func(t *testing.T) {
			var count atomic.Int32
			var second map[string]any
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input map[string]any
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if count.Add(1) == 1 {
					var output any
					switch dialect {
					case "openai-chat":
						output = map[string]any{"id": "chat_test", "object": "chat.completion", "model": "bridge", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": nil, "tool_calls": nativeChatCalls()}, "finish_reason": "tool_calls"}}, "usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8}}
					case "openai-responses":
						output = map[string]any{"id": "resp_test", "object": "response", "status": "completed", "model": "bridge", "output": nativeResponseCalls(), "usage": map[string]any{"input_tokens": 5, "output_tokens": 3, "total_tokens": 8}}
					case "anthropic":
						output = map[string]any{"id": "msg_test", "type": "message", "role": "assistant", "model": "bridge", "content": nativeAnthropicCalls(), "stop_reason": "tool_use", "usage": map[string]any{"input_tokens": 5, "output_tokens": 3}}
					}
					_ = json.NewEncoder(w).Encode(output)
					return
				}
				second = input
				text := "marker-c-error marker-a-multipart call_b-empty"
				switch dialect {
				case "openai-chat":
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "chat_end", "object": "chat.completion", "model": "bridge", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 8, "completion_tokens": 3, "total_tokens": 11}})
				case "openai-responses":
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "resp_end", "object": "response", "status": "completed", "model": "bridge", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}}, "usage": map[string]any{"input_tokens": 8, "output_tokens": 3, "total_tokens": 11}})
				case "anthropic":
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "msg_end", "type": "message", "role": "assistant", "model": "bridge", "content": []any{map[string]any{"type": "text", "text": text}}, "stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 8, "output_tokens": 3}})
				}
			}))
			defer up.Close()
			svc := nativeStreamTestService(t, up.URL, dialect, []Target{anthropicToolTarget()})
			router := httptest.NewServer(svc.Handler())
			defer router.Close()
			var path string
			var initial map[string]any
			tool := map[string]any{"name": "read", "parameters": map[string]any{"type": "object"}}
			switch dialect {
			case "openai-chat":
				path = "/v1/chat/completions"
				initial = map[string]any{"model": "native-stream", "messages": []any{map[string]any{"role": "user", "content": "read files"}}, "tools": []any{map[string]any{"type": "function", "function": tool}}}
			case "openai-responses":
				path = "/v1/responses"
				initial = map[string]any{"model": "native-stream", "input": "read files", "store": false, "tools": []any{map[string]any{"type": "function", "name": "read", "parameters": map[string]any{"type": "object"}}}}
			case "anthropic":
				path = "/v1/messages"
				initial = map[string]any{"model": "native-stream", "max_tokens": 256, "messages": []any{map[string]any{"role": "user", "content": "read files"}}, "tools": []any{map[string]any{"name": "read", "input_schema": map[string]any{"type": "object"}}}}
			}
			post := func(body map[string]any) map[string]any {
				t.Helper()
				raw, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				r, err := http.NewRequest(http.MethodPost, router.URL+path, bytes.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				r.Header.Set("Authorization", "Bearer "+testToken)
				r.Header.Set("Content-Type", "application/json")
				resp, err := http.DefaultClient.Do(r)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				b, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != 200 {
					t.Fatalf("status %d: %s", resp.StatusCode, b)
				}
				var result map[string]any
				if err := json.Unmarshal(b, &result); err != nil {
					t.Fatal(err)
				}
				return result
			}
			first := post(initial)
			var calls []any
			switch dialect {
			case "openai-chat":
				choice := valueAsSlice(first["choices"])[0].(map[string]any)
				if choice["finish_reason"] != "tool_calls" {
					t.Fatalf("finish: %#v", choice)
				}
				calls = valueAsSlice(choice["message"].(map[string]any)["tool_calls"])
			case "openai-responses":
				calls = valueAsSlice(first["output"])
			case "anthropic":
				if first["stop_reason"] != "tool_use" {
					t.Fatalf("finish: %#v", first)
				}
				calls = valueAsSlice(first["content"])
			}
			if len(calls) != 3 {
				t.Fatalf("expected three calls: %#v", calls)
			}
			usage, ok := first["usage"].(map[string]any)
			if !ok || len(usage) == 0 {
				t.Fatalf("missing usage: %#v", first)
			}
			seen := map[string]bool{}
			for i, c := range calls {
				call := c.(map[string]any)
				id := stringValue(call["id"])
				var argument map[string]any
				switch dialect {
				case "openai-chat":
					fn := call["function"].(map[string]any)
					if err := json.Unmarshal([]byte(stringValue(fn["arguments"])), &argument); err != nil {
						t.Fatal(err)
					}
				case "openai-responses":
					id = stringValue(call["call_id"])
					if err := json.Unmarshal([]byte(stringValue(call["arguments"])), &argument); err != nil {
						t.Fatal(err)
					}
				case "anthropic":
					argument, _ = call["input"].(map[string]any)
				}
				if id != []string{"call_a", "call_b", "call_c"}[i] || seen[id] || argument["path"] != []string{"a", "b", "c"}[i] {
					t.Fatalf("bad call %d: %#v", i, call)
				}
				seen[id] = true
			}
			// Execute synthetic reads against local files, including an empty
			// file and a missing file, before returning the results out of order.
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "a"), []byte("marker-a-multipart"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "b"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			multipart, err := os.ReadFile(filepath.Join(dir, "a"))
			if err != nil || string(multipart) != "marker-a-multipart" {
				t.Fatalf("read a: %q %v", multipart, err)
			}
			empty, err := os.ReadFile(filepath.Join(dir, "b"))
			if err != nil || len(empty) != 0 {
				t.Fatalf("read b: %q %v", empty, err)
			}
			if _, err := os.ReadFile(filepath.Join(dir, "c")); !os.IsNotExist(err) {
				t.Fatalf("read c must fail as missing: %v", err)
			}
			part1, part2 := string(multipart[:9]), string(multipart[9:])
			var want []any
			switch dialect {
			case "openai-chat":
				want = []any{map[string]any{"role": "tool", "tool_call_id": "call_c", "content": "error: marker-c-error"}, map[string]any{"role": "tool", "tool_call_id": "call_a", "content": []any{map[string]any{"type": "text", "text": part1}, map[string]any{"type": "text", "text": part2}}}, map[string]any{"role": "tool", "tool_call_id": "call_b", "content": string(empty)}}
				initial["messages"] = append([]any{map[string]any{"role": "user", "content": "read files"}, map[string]any{"role": "assistant", "tool_calls": calls}}, want...)
			case "openai-responses":
				want = []any{map[string]any{"type": "function_call_output", "call_id": "call_c", "output": "error: marker-c-error"}, map[string]any{"type": "function_call_output", "call_id": "call_a", "output": []any{map[string]any{"type": "input_text", "text": part1}, map[string]any{"type": "input_text", "text": part2}}}, map[string]any{"type": "function_call_output", "call_id": "call_b", "output": string(empty)}}
				initial["input"] = append(append([]any{map[string]any{"role": "user", "content": "read files"}}, calls...), want...)
			case "anthropic":
				want = []any{map[string]any{"type": "tool_result", "tool_use_id": "call_c", "is_error": true, "content": "marker-c-error"}, map[string]any{"type": "tool_result", "tool_use_id": "call_a", "content": []any{map[string]any{"type": "text", "text": part1}, map[string]any{"type": "text", "text": part2}}}, map[string]any{"type": "tool_result", "tool_use_id": "call_b", "content": string(empty)}}
				initial["messages"] = []any{map[string]any{"role": "user", "content": "read files"}, map[string]any{"role": "assistant", "content": calls}, map[string]any{"role": "user", "content": want}}
			}
			last := post(initial)
			raw, _ := json.Marshal(last)
			for _, marker := range []string{"marker-c-error", "marker-a-multipart", "call_b-empty"} {
				if !strings.Contains(string(raw), marker) {
					t.Fatalf("missing fake-provider marker %s: %s", marker, raw)
				}
			}
			if count.Load() != 2 {
				t.Fatalf("upstream calls=%d", count.Load())
			}
			var got []any
			switch dialect {
			case "openai-chat":
				m := valueAsSlice(second["messages"])
				got = m[len(m)-3:]
			case "openai-responses":
				m := valueAsSlice(second["input"])
				got = m[len(m)-3:]
			case "anthropic":
				m := valueAsSlice(second["messages"])
				got = valueAsSlice(m[len(m)-1].(map[string]any)["content"])
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("results changed: got=%s want=%s", fmt.Sprint(got), fmt.Sprint(want))
			}
		})
	}
}

func nativeChatCalls() []any {
	out := []any{}
	for _, id := range []string{"a", "b", "c"} {
		out = append(out, map[string]any{"id": "call_" + id, "type": "function", "function": map[string]any{"name": "read", "arguments": `{"path":"` + id + `"}`}})
	}
	return out
}
func nativeResponseCalls() []any {
	out := []any{}
	for _, id := range []string{"a", "b", "c"} {
		out = append(out, map[string]any{"id": "fc_" + id, "type": "function_call", "call_id": "call_" + id, "name": "read", "arguments": `{"path":"` + id + `"}`})
	}
	return out
}
func nativeAnthropicCalls() []any {
	out := []any{}
	for _, id := range []string{"a", "b", "c"} {
		out = append(out, map[string]any{"id": "call_" + id, "type": "tool_use", "name": "read", "input": map[string]any{"path": id}})
	}
	return out
}

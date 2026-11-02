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
	"strings"
	"sync/atomic"
	"testing"
)

// Synthetic two-turn translated controls; fixed final output is not evidence
// that a provider actually understood its tool results.
func TestMultipartToolResult672Localhost(t *testing.T) {
	for _, tc := range []struct{ caller, upstream string }{
		{"anthropic", "openai-chat"}, {"anthropic", "openai-responses"},
		{"openai-chat", "openai-responses"},
	} {
		t.Run(tc.caller+"-to-"+tc.upstream, func(t *testing.T) {
			var hits atomic.Int32
			var continuation map[string]any
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if hits.Add(1) == 1 {
					switch tc.upstream {
					case "openai-chat":
						_ = json.NewEncoder(w).Encode(map[string]any{"id": "chat_fixture", "object": "chat.completion", "model": "bridge", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": nil, "tool_calls": tool672ChatCalls()}, "finish_reason": "tool_calls"}}, "usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8}})
					case "openai-responses":
						_ = json.NewEncoder(w).Encode(map[string]any{"id": "resp_fixture", "object": "response", "status": "completed", "model": "bridge", "output": tool672ResponseCalls(), "usage": map[string]any{"input_tokens": 5, "output_tokens": 3, "total_tokens": 8}})
					}
					return
				}
				continuation = request
				text := "marker-c-error marker-a-multipart call_b-empty"
				switch tc.upstream {
				case "openai-chat":
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "chat_end", "object": "chat.completion", "model": "bridge", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 8, "completion_tokens": 3, "total_tokens": 11}})
				case "openai-responses":
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "resp_end", "object": "response", "status": "completed", "model": "bridge", "output": []any{map[string]any{"type": "message", "id": "msg_end", "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}}, "usage": map[string]any{"input_tokens": 8, "output_tokens": 3, "total_tokens": 11}})
				case "anthropic":
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "msg_end", "type": "message", "role": "assistant", "model": "bridge", "content": []any{map[string]any{"type": "text", "text": text}}, "stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 8, "output_tokens": 3}})
				}
			}))
			defer provider.Close()
			target := anthropicToolTarget()
			target.ResponsesToChat = ResponsesToChatBridge{Enabled: true, Text: true, FunctionTools: true, ToolChoice: true, Streaming: true}
			target.Bridges.ChatToResponses = DialectBridgeSupport{Enabled: true, Tools: true, ToolChoice: true, ParallelToolCalls: true}
			svc := nativeStreamTestService(t, provider.URL, tc.upstream, []Target{target})
			router := httptest.NewServer(svc.Handler())
			defer router.Close()
			body := map[string]any{"model": "native-stream"}
			var path string
			switch tc.caller {
			case "openai-chat":
				path = "/v1/chat/completions"
				body["messages"] = []any{map[string]any{"role": "user", "content": "read files"}}
				body["max_tokens"] = 256
				body["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "read", "parameters": map[string]any{"type": "object"}}}}
			case "openai-responses":
				path = "/v1/responses"
				body["input"] = "read files"
				body["max_output_tokens"] = 256
				body["store"] = false
				body["tools"] = []any{map[string]any{"type": "function", "name": "read", "parameters": map[string]any{"type": "object"}}}
			case "anthropic":
				path = "/v1/messages"
				body["messages"] = []any{map[string]any{"role": "user", "content": "read files"}}
				body["max_tokens"] = 256
				body["tools"] = []any{map[string]any{"name": "read", "input_schema": map[string]any{"type": "object"}}}
			}
			post := func(input map[string]any) map[string]any {
				t.Helper()
				raw, _ := json.Marshal(input)
				req, _ := http.NewRequest(http.MethodPost, router.URL+path, bytes.NewReader(raw))
				req.Header.Set("Authorization", "Bearer "+testToken)
				req.Header.Set("Content-Type", "application/json")
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				output, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != 200 {
					t.Fatalf("status %d: %s", resp.StatusCode, output)
				}
				var decoded map[string]any
				if err := json.Unmarshal(output, &decoded); err != nil {
					t.Fatal(err)
				}
				return decoded
			}
			first := post(body)
			var calls []any
			switch tc.caller {
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
				t.Fatalf("expected 3 calls: %#v", calls)
			}
			if usage, ok := first["usage"].(map[string]any); !ok || len(usage) == 0 {
				t.Fatalf("missing usage: %#v", first)
			}
			for i, raw := range calls {
				call := raw.(map[string]any)
				id := stringValue(call["id"])
				var args map[string]any
				switch tc.caller {
				case "openai-chat":
					fn := call["function"].(map[string]any)
					if err := json.Unmarshal([]byte(stringValue(fn["arguments"])), &args); err != nil {
						t.Fatal(err)
					}
				case "openai-responses":
					id = stringValue(call["call_id"])
					if err := json.Unmarshal([]byte(stringValue(call["arguments"])), &args); err != nil {
						t.Fatal(err)
					}
				case "anthropic":
					args, _ = call["input"].(map[string]any)
				}
				if id != []string{"call_a", "call_b", "call_c"}[i] || args["path"] != []string{"a", "b", "c"}[i] {
					t.Fatalf("bad call %d: %#v", i, call)
				}
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "a"), []byte("marker-a-multipart"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "b"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(filepath.Join(dir, "a"))
			if err != nil {
				t.Fatal(err)
			}
			empty, err := os.ReadFile(filepath.Join(dir, "b"))
			if err != nil || len(empty) != 0 {
				t.Fatalf("empty read: %v", err)
			}
			if _, err := os.ReadFile(filepath.Join(dir, "c")); !os.IsNotExist(err) {
				t.Fatalf("missing read: %v", err)
			}
			p1, p2 := string(content[:9]), string(content[9:])
			var results []any
			switch tc.caller {
			case "openai-chat":
				results = []any{map[string]any{"role": "tool", "tool_call_id": "call_c", "content": "ERROR marker-c-error"}, map[string]any{"role": "tool", "tool_call_id": "call_a", "content": []any{map[string]any{"type": "text", "text": p1}, map[string]any{"type": "text", "text": p2}}}, map[string]any{"role": "tool", "tool_call_id": "call_b", "content": string(empty)}}
				body["messages"] = append([]any{map[string]any{"role": "user", "content": "read files"}, map[string]any{"role": "assistant", "tool_calls": calls}}, results...)
			case "openai-responses":
				results = []any{map[string]any{"type": "function_call_output", "call_id": "call_c", "output": "ERROR marker-c-error"}, map[string]any{"type": "function_call_output", "call_id": "call_a", "output": []any{map[string]any{"type": "input_text", "text": p1}, map[string]any{"type": "input_text", "text": p2}}}, map[string]any{"type": "function_call_output", "call_id": "call_b", "output": string(empty)}}
				body["input"] = append(append([]any{map[string]any{"role": "user", "content": "read files"}}, calls...), results...)
			case "anthropic":
				results = []any{map[string]any{"type": "tool_result", "tool_use_id": "call_c", "is_error": true, "content": "ERROR marker-c-error"}, map[string]any{"type": "tool_result", "tool_use_id": "call_a", "content": []any{map[string]any{"type": "text", "text": p1}, map[string]any{"type": "text", "text": p2}}}, map[string]any{"type": "tool_result", "tool_use_id": "call_b", "content": string(empty)}}
				body["messages"] = []any{map[string]any{"role": "user", "content": "read files"}, map[string]any{"role": "assistant", "content": calls}, map[string]any{"role": "user", "content": results}}
			}
			last := post(body)
			encoded, _ := json.Marshal(last)
			for _, marker := range []string{"marker-c-error", "marker-a-multipart", "call_b-empty"} {
				if !strings.Contains(string(encoded), marker) {
					t.Fatalf("missing fake marker %s: %s", marker, encoded)
				}
			}
			if hits.Load() != 2 {
				t.Fatalf("upstream hits=%d", hits.Load())
			}
			var upstreamResults []any
			switch tc.upstream {
			case "openai-chat":
				for _, v := range valueAsSlice(continuation["messages"]) {
					entry := v.(map[string]any)
					if entry["role"] == "tool" {
						upstreamResults = append(upstreamResults, entry)
					}
				}
			case "openai-responses":
				for _, v := range valueAsSlice(continuation["input"]) {
					entry := v.(map[string]any)
					if entry["type"] == "function_call_output" {
						upstreamResults = append(upstreamResults, entry)
					}
				}
			case "anthropic":
				for _, v := range valueAsSlice(continuation["messages"]) {
					entry := v.(map[string]any)
					for _, c := range valueAsSlice(entry["content"]) {
						block, ok := c.(map[string]any)
						if ok && block["type"] == "tool_result" {
							upstreamResults = append(upstreamResults, block)
						}
					}
				}
			}
			if len(upstreamResults) != 3 {
				t.Fatalf("upstream results: %#v", continuation)
			}
			for i, v := range upstreamResults {
				result := v.(map[string]any)
				id := stringValue(result["tool_call_id"])
				content := result["content"]
				if tc.upstream == "openai-responses" {
					id = stringValue(result["call_id"])
					content = result["output"]
				}
				if tc.upstream == "anthropic" {
					id = stringValue(result["tool_use_id"])
				}
				var text string
				switch x := content.(type) {
				case string:
					text = x
				case []any:
					for _, part := range x {
						block, ok := part.(map[string]any)
						if !ok {
							t.Fatalf("bad part: %#v", part)
						}
						text += stringValue(block["text"])
					}
				default:
					t.Fatalf("result %d content must be string or text parts, got %#v", i, content)
				}
				if id != []string{"call_c", "call_a", "call_b"}[i] || text != []string{"ERROR marker-c-error", "marker-a-multipart", ""}[i] {
					t.Errorf("result %d: id=%q text=%q body=%s", i, id, text, fmt.Sprint(result))
				}
			}
		})
	}
}

func tool672ChatCalls() []any {
	out := []any{}
	for _, id := range []string{"a", "b", "c"} {
		out = append(out, map[string]any{"id": "call_" + id, "type": "function", "function": map[string]any{"name": "read", "arguments": `{"path":"` + id + `"}`}})
	}
	return out
}

func tool672ResponseCalls() []any {
	out := []any{}
	for _, id := range []string{"a", "b", "c"} {
		out = append(out, map[string]any{"id": "fc_" + id, "type": "function_call", "call_id": "call_" + id, "name": "read", "arguments": `{"path":"` + id + `"}`})
	}
	return out
}

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
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// This verifies native forwarding or a typed pre-dispatch rejection. It does
// not claim that a real model obeys a particular tool-choice policy.
func TestParallelNativeToolControls(t *testing.T) {
	cases := []struct {
		name, dialect string
		choice        any
		parallel      any
		param         string
	}{
		{"chat-auto", "openai-chat", "auto", nil, "tool_choice"},
		{"chat-forced", "openai-chat", map[string]any{"type": "function", "function": map[string]any{"name": "read"}}, nil, "tool_choice"},
		{"chat-no-parallel", "openai-chat", "auto", false, "parallel_tool_calls"},
		{"responses-auto", "openai-responses", "auto", nil, "tool_choice"},
		{"responses-forced", "openai-responses", map[string]any{"type": "function", "name": "read"}, nil, "tool_choice"},
		{"responses-no-parallel", "openai-responses", "auto", false, "parallel_tool_calls"},
		{"messages-auto", "anthropic", map[string]any{"type": "auto"}, nil, "tool_choice"},
		{"messages-no-parallel", "anthropic", map[string]any{"type": "auto", "disable_parallel_tool_use": true}, nil, "tool_choice"},
	}
	for _, tc := range cases {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream-%v", tc.name, stream), func(t *testing.T) {
				var hits atomic.Int32
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						return
					}
					if !reflect.DeepEqual(body["tool_choice"], tc.choice) {
						t.Errorf("tool choice changed: got %#v want %#v", body["tool_choice"], tc.choice)
					}
					if tc.parallel != nil && body["parallel_tool_calls"] != tc.parallel {
						t.Errorf("parallel control changed: %#v", body["parallel_tool_calls"])
					}
					if body["stream"] != stream {
						t.Errorf("stream changed: %#v", body["stream"])
					}
					if !stream {
						w.Header().Set("Content-Type", "application/json")
						switch tc.dialect {
						case "openai-chat":
							fmt.Fprint(w, `{"id":"chat_control","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"control-ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
						case "openai-responses":
							fmt.Fprint(w, `{"id":"resp_control","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"control-ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
						case "anthropic":
							fmt.Fprint(w, `{"id":"msg_control","type":"message","role":"assistant","content":[{"type":"text","text":"control-ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
						}
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					switch tc.dialect {
					case "openai-chat":
						fmt.Fprint(w, "data: {\"id\":\"chat_control\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"control-ok\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					case "openai-responses":
						fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_control\",\"object\":\"response\",\"status\":\"in_progress\",\"output\":[]}}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"control-ok\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_control\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
					case "anthropic":
						fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_control\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"control-ok\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
					}
				}))
				defer up.Close()
				svc := nativeStreamTestService(t, up.URL, tc.dialect, []Target{anthropicToolTarget()})
				router := httptest.NewServer(svc.Handler())
				defer router.Close()
				var path string
				body := map[string]any{"model": "native-stream", "stream": stream, "tool_choice": tc.choice}
				switch tc.dialect {
				case "openai-chat":
					path = "/v1/chat/completions"
					body["messages"] = []any{map[string]any{"role": "user", "content": "read"}}
					body["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "read", "parameters": map[string]any{"type": "object"}}}}
				case "openai-responses":
					path = "/v1/responses"
					body["input"] = "read"
					body["tools"] = []any{map[string]any{"type": "function", "name": "read", "parameters": map[string]any{"type": "object"}}}
				case "anthropic":
					path = "/v1/messages"
					body["max_tokens"] = 256
					body["messages"] = []any{map[string]any{"role": "user", "content": "read"}}
					body["tools"] = []any{map[string]any{"name": "read", "input_schema": map[string]any{"type": "object"}}}
				}
				if tc.parallel != nil {
					body["parallel_tool_calls"] = tc.parallel
				}
				raw, _ := json.Marshal(body)
				req, _ := http.NewRequest(http.MethodPost, router.URL+path, bytes.NewReader(raw))
				req.Header.Set("Authorization", "Bearer "+testToken)
				req.Header.Set("Content-Type", "application/json")
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				result, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode == http.StatusBadRequest {
					if !strings.HasSuffix(tc.name, "no-parallel") {
						t.Fatalf("auto/forced choice must not be rejected: %s", result)
					}
					var envelope struct {
						Error struct {
							Code  string `json:"code"`
							Param string `json:"param"`
						} `json:"error"`
					}
					if json.Unmarshal(result, &envelope) != nil || envelope.Error.Code != "unsupported_capability" || envelope.Error.Param != tc.param || hits.Load() != 0 {
						t.Fatalf("invalid rejection: %d %s hits=%d", resp.StatusCode, result, hits.Load())
					}
					return
				}
				if resp.StatusCode != 200 || hits.Load() != 1 || !strings.Contains(string(result), "control-ok") {
					t.Fatalf("bad response: %d %s hits=%d", resp.StatusCode, result, hits.Load())
				}
			})
		}
	}
}

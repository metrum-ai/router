// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStripUnsignedThinkingDoesNotMutateCaller(t *testing.T) {
	signed := map[string]any{"type": "thinking", "thinking": "keep", "signature": "signed"}
	nullSignature := map[string]any{"type": "thinking", "signature": nil}
	blocks := []any{map[string]any{"type": "thinking", "signature": ""}, signed, nullSignature, map[string]any{"type": "thinking"}}
	originalMessage := map[string]any{"role": "assistant", "content": blocks}
	body := map[string]any{"messages": []any{originalMessage}}
	stripUnsignedAnthropicThinking(body)
	gotMessage := valueAsSlice(body["messages"])[0].(map[string]any)
	if got, want := gotMessage["content"], []any{signed, nullSignature}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected filtered content: got=%#v want=%#v", got, want)
	}
	if !reflect.DeepEqual(originalMessage["content"], blocks) || len(blocks) != 4 {
		t.Fatal("mutated caller history")
	}
}

// A bridge-produced unsigned thinking block must not make a subsequent native
// Claude tool continuation invalid. Signed and redacted state remains intact.
func TestReasoningUnsignedNativeToolContinuation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			var requests []map[string]any
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				requests = append(requests, body)
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"bridge\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
					if len(requests) == 1 {
						fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_local\",\"name\":\"read\",\"input\":{}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"quote.txt\\\"}\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":1}}\n\n")
					} else {
						fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"continued\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
					}
					fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					if len(requests) == 1 {
						fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"bridge","content":[{"type":"tool_use","id":"toolu_local","name":"read","input":{"path":"quote.txt"}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`)
					} else {
						fmt.Fprint(w, `{"id":"msg_2","type":"message","role":"assistant","model":"bridge","content":[{"type":"text","text":"continued"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
					}
				}
			}))
			defer up.Close()
			svc := nativeStreamTestService(t, up.URL, "anthropic", []Target{anthropicToolTarget()})
			post := func(messages []any) string {
				body := map[string]any{"model": "native-stream", "max_tokens": 1024, "stream": stream, "messages": messages, "tools": []any{map[string]any{"name": "read", "input_schema": map[string]any{"type": "object"}}}}
				raw, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				r := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", bytes.NewReader(raw))
				r.Header.Set("Authorization", "Bearer "+testToken)
				w := httptest.NewRecorder()
				svc.Handler().ServeHTTP(w, r)
				if w.Code != 200 {
					t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
				}
				return w.Body.String()
			}
			initial := []any{map[string]any{"role": "user", "content": "read quote.txt"}}
			first := post(initial)
			if !strings.Contains(first, "toolu_local") || !strings.Contains(first, "read") {
				t.Fatalf("missing tool call: %s", first)
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "quote.txt"), []byte("LOCAL-RESULT-8192"), 0600); err != nil {
				t.Fatal(err)
			}
			result, err := os.ReadFile(filepath.Join(dir, "quote.txt"))
			if err != nil {
				t.Fatal(err)
			}
			signed := map[string]any{"type": "thinking", "thinking": "signed-thought", "signature": "opaque-signed", "extra": map[string]any{"keep": true}}
			redacted := map[string]any{"type": "redacted_thinking", "data": "opaque-redacted"}
			tool := map[string]any{"type": "tool_use", "id": "toolu_local", "name": "read", "input": map[string]any{"path": "quote.txt"}}
			content := []any{map[string]any{"type": "thinking", "thinking": "bridge-empty", "signature": ""}, signed, map[string]any{"type": "thinking", "thinking": "bridge-missing"}, redacted, tool}
			messages := append(initial, map[string]any{"role": "assistant", "content": content}, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_local", "content": string(result)}}})
			second := post(messages)
			if !strings.Contains(second, "continued") || len(requests) != 2 {
				t.Fatalf("bad continuation: %s hits=%d", second, len(requests))
			}
			upMessages := valueAsSlice(requests[1]["messages"])
			assistant := upMessages[1].(map[string]any)
			if got, want := assistant["content"], []any{signed, redacted, tool}; !reflect.DeepEqual(got, want) {
				t.Fatalf("native opaque history changed: got=%#v want=%#v", got, want)
			}
			resultMessage := upMessages[2].(map[string]any)
			resultBlock := valueAsSlice(resultMessage["content"])[0].(map[string]any)
			if resultBlock["tool_use_id"] != "toolu_local" || resultBlock["content"] != string(result) {
				t.Fatalf("tool result changed: %#v", resultBlock)
			}
			if len(content) != 5 {
				t.Fatal("caller history mutated")
			}
		})
	}
}

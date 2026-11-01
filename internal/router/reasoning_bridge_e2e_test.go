// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReasoningBridgeUnaryAndStreaming(t *testing.T) {
	for _, dialect := range []string{"anthropic", "openai-responses"} {
		for _, streaming := range []bool{false, true} {
			for _, enabled := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%t/reasoning=%t", dialect, streaming, enabled), func(t *testing.T) {
					var seen map[string]any
					up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
							t.Error(err)
						}
						if !streaming {
							w.Header().Set("Content-Type", "application/json")
							if dialect == "anthropic" {
								fmt.Fprint(w, `{"id":"msg_up","type":"message","role":"assistant","content":[{"type":"thinking","thinking":"visible thought","signature":"private_signature"},{"type":"text","text":"answer"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":2}}`)
							} else {
								fmt.Fprint(w, `{"id":"resp_up","object":"response","status":"completed","output":[{"type":"reasoning","id":"rs_up","encrypted_content":"private_cipher","summary":[{"type":"summary_text","text":"visible thought"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}],"usage":{"input_tokens":2,"output_tokens":2,"total_tokens":4}}`)
							}
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						if dialect == "anthropic" {
							fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_up\",\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"visible thought\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"private_signature\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"answer\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
						} else {
							fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_up\",\"created_at\":1}}\n\nevent: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"rs_up\",\"type\":\"reasoning\",\"encrypted_content\":\"private_cipher\"}}\n\nevent: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"output_index\":0,\"delta\":\"visible thought\"}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":1,\"delta\":\"answer\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_up\",\"usage\":{\"input_tokens\":2,\"output_tokens\":2,\"total_tokens\":4}}}\n\n")
						}
					}))
					defer up.Close()
					target := anthropicBridgeTarget()
					var path, body string
					if dialect == "anthropic" {
						target.DefaultThinking = map[string]any{"type": "adaptive"}
						target.Reasoning = ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlTokenBudget}
						path = "/v1/responses"
						body = fmt.Sprintf(`{"model":"native-stream","input":"hi","reasoning":{"effort":"low"},"max_output_tokens":2048,"stream":%t}`, streaming)
					} else {
						target.Reasoning = ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum, SupportsSummaries: true}
						path = "/anthropic/v1/messages"
						body = fmt.Sprintf(`{"model":"native-stream","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":4096},"max_tokens":8192,"stream":%t}`, streaming)
					}
					if !enabled {
						var payload map[string]any
						if err := json.Unmarshal([]byte(body), &payload); err != nil {
							t.Fatal(err)
						}
						delete(payload, "reasoning")
						delete(payload, "thinking")
						data, err := json.Marshal(payload)
						if err != nil {
							t.Fatal(err)
						}
						body = string(data)
						target.DefaultThinking = nil
					}
					svc := nativeStreamTestService(t, up.URL, dialect, []Target{target})
					r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
					r.Header.Set("Authorization", "Bearer "+testToken)
					r.Header.Set("x-api-key", testToken)
					r.Header.Set("anthropic-version", "2023-06-01")
					w := httptest.NewRecorder()
					svc.Handler().ServeHTTP(w, r)
					got := w.Body.String()
					if w.Code != 200 || strings.Contains(got, "visible thought") != enabled || !strings.Contains(got, "answer") || strings.Contains(got, "private_signature") || strings.Contains(got, "private_cipher") {
						t.Fatalf("status=%d body=%s", w.Code, got)
					}
					if !enabled {
						if _, ok := seen["thinking"]; ok {
							t.Fatalf("unexpected thinking control %#v", seen)
						}
						if _, ok := seen["reasoning"]; ok {
							t.Fatalf("unexpected reasoning control %#v", seen)
						}
						return
					}
					if dialect == "anthropic" {
						think, _ := seen["thinking"].(map[string]any)
						config, _ := seen["output_config"].(map[string]any)
						if think["type"] != "adaptive" || config["effort"] != "low" {
							t.Fatalf("wrong mapped body: %#v", seen)
						}
					} else {
						reasoning, _ := seen["reasoning"].(map[string]any)
						if reasoning["effort"] != "medium" || reasoning["summary"] != "auto" {
							t.Fatalf("wrong mapped body: %#v", seen)
						}
					}
				})
			}
		}
	}
}

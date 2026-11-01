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

func TestReasoningEffort638HTTP(t *testing.T) {
	for _, caller := range []string{"openai-chat", "openai-responses"} {
		for _, out := range []string{"anthropic", caller} {
			for _, streaming := range []bool{false, true} {
				for _, effort := range []string{"omitted", "none", "minimal", "low", "bogus"} {
					t.Run(fmt.Sprintf("%s/%s/stream=%t/%s", caller, out, streaming, effort), func(t *testing.T) {
						hits := 0
						var sent map[string]any
						up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							hits++
							if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
								t.Error(err)
							}
							if streaming {
								w.Header().Set("Content-Type", "text/event-stream")
								fmt.Fprint(w, anthropicBridgeFixture(t, out))
								return
							}
							w.Header().Set("Content-Type", "application/json")
							switch out {
							case "anthropic":
								fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"bridge","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
							case "openai-chat":
								fmt.Fprint(w, `{"id":"chatcmpl_1","object":"chat.completion","model":"bridge","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
							default:
								fmt.Fprint(w, `{"id":"resp_1","object":"response","status":"completed","model":"bridge","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
							}
						}))
						defer up.Close()
						target := anthropicBridgeTarget()
						target.Reasoning = ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlTokenBudget}
						if out != "anthropic" {
							target.Reasoning.Control = reasoningControlEffortEnum
						}
						svc := nativeStreamTestService(t, up.URL, out, []Target{target})
						body := map[string]any{"model": "native-stream", "stream": streaming}
						path := "/v1/chat/completions"
						if caller == "openai-chat" {
							body["messages"] = []any{map[string]any{"role": "user", "content": "hi"}}
							body["max_tokens"] = 2048
							if effort != "omitted" {
								body["reasoning_effort"] = effort
							}
						} else {
							path = "/v1/responses"
							body["input"] = "hi"
							body["max_output_tokens"] = 2048
							if effort != "omitted" {
								body["reasoning"] = map[string]any{"effort": effort}
							}
						}
						raw, _ := json.Marshal(body)
						r := httptest.NewRequest("POST", path, strings.NewReader(string(raw)))
						r.Header.Set("Authorization", "Bearer "+testToken)
						w := httptest.NewRecorder()
						svc.Handler().ServeHTTP(w, r)
						if effort == "bogus" {
							if w.Code != 400 || hits != 0 || !strings.Contains(w.Body.String(), `"type":"invalid_request_error"`) || !strings.Contains(w.Body.String(), `"param":"reasoning_effort"`) {
								t.Fatalf("status=%d hits=%d body=%s", w.Code, hits, w.Body.String())
							}
							return
						}
						if w.Code != 200 || hits != 1 {
							t.Fatalf("status=%d hits=%d body=%s", w.Code, hits, w.Body.String())
						}
						if out == "anthropic" {
							thinking, ok := sent["thinking"].(map[string]any)
							if effort == "none" || effort == "omitted" {
								if ok {
									t.Fatalf("unexpected thinking %#v", thinking)
								}
							} else if !ok || thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(1024) {
								t.Fatalf("wrong thinking %#v", thinking)
							}
						} else if effort != "omitted" {
							if caller == "openai-chat" && sent["reasoning_effort"] != effort {
								t.Fatalf("lost effort: %#v", sent)
							}
							if caller == "openai-responses" {
								reasoning, _ := sent["reasoning"].(map[string]any)
								if reasoning["effort"] != effort {
									t.Fatalf("lost effort: %#v", sent)
								}
							}
						}
					})
				}
			}
		}
	}
}

func TestReasoningMinimalAdaptive638(t *testing.T) {
	target := anthropicBridgeTarget()
	target.Reasoning = ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlTokenBudget}
	target.DefaultThinking = map[string]any{"type": "adaptive"}
	thinking, effort, err := bridgeAnthropicReasoning(ReasoningIntent{Kind: "effort", Effort: "minimal"}, target, 2048)
	if err != nil || thinking["type"] != "adaptive" || effort != "low" {
		t.Fatalf("thinking=%#v effort=%q err=%v", thinking, effort, err)
	}
}

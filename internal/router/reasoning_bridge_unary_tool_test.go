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

func TestReasoningUnaryToolFidelity(t *testing.T) {
	for _, out := range []string{"anthropic", "openai-responses"} {
		t.Run(out, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if out == "anthropic" {
					fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"thinking","thinking":"visible thought","signature":"PRIVATE_SIGNATURE"},{"type":"tool_use","id":"call_1","name":"read","input":{"path":"file.txt"}}],"stop_reason":"tool_use","usage":{"input_tokens":9,"output_tokens":3}}`)
				} else {
					fmt.Fprint(w, `{"id":"resp_1","object":"response","status":"completed","output":[{"type":"reasoning","id":"rs_1","encrypted_content":"PRIVATE_CIPHER","summary":[{"type":"summary_text","text":"visible thought"}]},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":"{\"path\":\"file.txt\"}"}],"usage":{"input_tokens":9,"output_tokens":3,"total_tokens":12}}`)
				}
			}))
			defer up.Close()
			target := anthropicToolTarget()
			path, body := "", ""
			if out == "anthropic" {
				target.DefaultThinking = map[string]any{"type": "adaptive"}
				target.Reasoning = ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlTokenBudget}
				path = "/v1/responses"
				body = `{"model":"native-stream","input":"read","max_output_tokens":2048,"reasoning":{"effort":"low"},"tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}`
			} else {
				target.Reasoning = ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum, SupportsSummaries: true}
				path = "/anthropic/v1/messages"
				body = `{"model":"native-stream","messages":[{"role":"user","content":"read"}],"max_tokens":8192,"thinking":{"type":"enabled","budget_tokens":4096},"tools":[{"name":"read","input_schema":{"type":"object"}}]}`
			}
			svc := nativeStreamTestService(t, up.URL, out, []Target{target})
			svc.cfg.Server.Responses.ModelIdentity = ModelIdentityRequestedGroup
			r := httptest.NewRequest("POST", path, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			if w.Code != 200 || strings.Contains(w.Body.String(), "PRIVATE_") {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			var response map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response["model"] != "native-stream" {
				t.Fatalf("model lost: %#v", response)
			}
			usage, _ := response["usage"].(map[string]any)
			if usage["input_tokens"] != float64(9) || usage["output_tokens"] != float64(3) {
				t.Fatalf("usage lost: %#v", response)
			}
			if out == "anthropic" {
				items := valueAsSlice(response["output"])
				foundReason, foundCall := false, false
				for _, v := range items {
					item, _ := v.(map[string]any)
					switch item["type"] {
					case "reasoning":
						foundReason = true
					case "function_call":
						foundCall = item["call_id"] == "call_1" && item["name"] == "read" && strings.Contains(stringValue(item["arguments"]), "file.txt")
					}
				}
				if !foundReason || !foundCall || response["status"] != "completed" {
					t.Fatalf("reasoning/tool/status lost %#v", response)
				}
			} else {
				blocks := valueAsSlice(response["content"])
				foundReason, foundCall := false, false
				for _, v := range blocks {
					block, _ := v.(map[string]any)
					switch block["type"] {
					case "thinking":
						foundReason = block["thinking"] == "visible thought"
					case "tool_use":
						args, _ := block["input"].(map[string]any)
						foundCall = block["id"] == "call_1" && block["name"] == "read" && args["path"] == "file.txt"
					}
				}
				if !foundReason || !foundCall || response["stop_reason"] != "tool_use" {
					t.Fatalf("reasoning/tool/stop lost %#v", response)
				}
			}
		})
	}
}

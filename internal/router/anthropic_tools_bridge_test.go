// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func anthropicToolTarget() Target {
	target := anthropicBridgeTarget()
	target.ToolSupport = ToolSupport{AnthropicMessages: []string{"client_tools", "tool_choice"}, OpenAIChat: []string{"tools", "tool_choice"}, OpenAIResponses: []string{"function", "tool_choice"}}
	return target
}

func TestAnthropicTranslatedToolStreamInterrupted(t *testing.T) {
	for _, out := range []string{"openai-chat", "openai-responses"} {
		folder := "openai-chat"
		if out == "openai-responses" {
			folder = "responses-to-chat"
		}
		raw, err := os.ReadFile("../../testdata/sse/synthetic/" + folder + "/tools.sse")
		if err != nil {
			t.Fatal(err)
		}
		// Stop mid-tool, after the first argument delta and before choice completion.
		text := string(raw)
		cut := strings.Index(text, "\n\n") + 2
		if out == "openai-responses" {
			marker := strings.Index(text, "event: response.function_call_arguments.delta")
			if marker < 0 {
				t.Fatal("missing delta")
			}
			cut = marker + strings.Index(text[marker:], "\n\n") + 2
		}
		rr := httptest.NewRecorder()
		_, err = anthropicBridgeProxy("anthropic")(context.Background(), rr, strings.NewReader(text[:cut]), out, "test", 1<<20, nil, testIdentifierTransform(t))
		if err == nil || !committedStreamError(err) || strings.Contains(rr.Body.String(), `"type":"message_stop"`) {
			t.Fatalf("truncated tool stream completed: %v %s", err, rr.Body)
		}
	}
}

func TestAnthropicTranslatedToolStreamingAdmission(t *testing.T) {
	for _, out := range []string{"openai-chat", "openai-responses"} {
		for _, validated := range []bool{false, true} {
			t.Run(fmt.Sprint(out, validated), func(t *testing.T) {
				folder := "openai-chat"
				if out == "openai-responses" {
					folder = "responses-to-chat"
				}
				fixture, err := os.ReadFile("../../testdata/sse/synthetic/" + folder + "/tools.sse")
				if err != nil {
					t.Fatal(err)
				}
				attempts := 0
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					attempts++
					var body map[string]any
					json.NewDecoder(r.Body).Decode(&body)
					if body["stream"] != true || len(valueAsSlice(body["tools"])) != 1 {
						t.Error("stream/tool shape lost")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					w.Write(fixture)
				}))
				defer up.Close()
				target := anthropicToolTarget()
				if !validated {
					target.ToolSupport.AnthropicMessages = nil
				}
				svc := nativeStreamTestService(t, up.URL, out, []Target{target})
				req := httptest.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(`{"model":"native-stream","stream":true,"max_tokens":100,"tools":[{"name":"write","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"write"}]}`))
				req.Header.Set("Authorization", "Bearer "+testToken)
				rr := httptest.NewRecorder()
				svc.Handler().ServeHTTP(rr, req)
				if !validated {
					if rr.Code != 502 || attempts != 0 || !strings.Contains(rr.Body.String(), "no-eligible-target") {
						t.Fatalf("not fail closed: %d %s", rr.Code, rr.Body)
					}
					return
				}
				if rr.Code != 200 || attempts != 1 {
					t.Fatalf("stream failed: %d %s", rr.Code, rr.Body)
				}
				body := rr.Body.String()
				for _, event := range []string{"message_start", "input_json_delta", "message_delta", "message_stop"} {
					if !strings.Contains(body, `"type":"`+event+`"`) {
						t.Fatalf("missing %s: %s", event, body)
					}
				}
				if strings.Count(body, `"type":"message_stop"`) != 1 || strings.Count(body, `"type":"message_start"`) != 1 || !strings.Contains(body, `"stop_reason":"tool_use"`) {
					t.Fatal("invalid terminal events", body)
				}
			})
		}
	}
}

func TestAnthropicToolTranslationEvidence(t *testing.T) {
	for _, out := range []string{"openai-chat", "openai-responses"} {
		target := anthropicToolTarget()
		if !targetSupportsToolsForCallerDialect(target, "anthropic", out) {
			t.Fatal("validated bridge rejected")
		}
		if !supportsAnyCapability(anthropicTranslationEvidenceCapabilities(target, out), "tools-omitted", "tools-auto", "tools-forced") {
			t.Fatal("missing evidence")
		}
		target.ToolSupport.AnthropicMessages = nil
		if targetSupportsToolsForCallerDialect(target, "anthropic", out) {
			t.Fatal("upstream tool metadata authorized Anthropic tools")
		}
		target = anthropicToolTarget()
		target.RequestShapeSupport.ValidationStatus = "pending"
		if targetSupportsToolsForCallerDialect(target, "anthropic", out) {
			t.Fatal("unvalidated bridge authorized")
		}
	}
}

// Exercise the actual HTTP path, including the second turn, independently for
// both upstream skins. Streaming protocol state/cancellation also remains covered
// by TestAnthropicBridgeStreamingToolsAndIdentifiers and the stream package.
func TestAnthropicToolsTranslatedContinuation(t *testing.T) {
	for _, out := range []string{"openai-chat", "openai-responses"} {
		for _, choice := range []string{"", `,"tool_choice":{"type":"auto"}`, `,"tool_choice":{"type":"tool","name":"write"}`} {
			t.Run(out+choice, func(t *testing.T) {
				turn := 0
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if len(valueAsSlice(body["tools"])) != 1 {
						t.Error("tools lost")
					}
					if choice != "" && body["tool_choice"] == nil {
						t.Error("choice lost")
					}
					turn++
					if turn == 2 {
						raw, _ := json.Marshal(body)
						if !strings.Contains(string(raw), "call_1") || !strings.Contains(string(raw), "written") {
							t.Errorf("continuation lost: %s", raw)
						}
					}
					w.Header().Set("Content-Type", "application/json")
					if out == "openai-chat" {
						if turn == 1 {
							fmt.Fprint(w, `{"id":"chat_1","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"write","arguments":"{\"path\":\"a\"}"}},{"id":"call_2","type":"function","function":{"name":"write","arguments":"{\"path\":\"b\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":4}}`)
						} else {
							fmt.Fprint(w, `{"id":"chat_2","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`)
						}
					} else {
						if turn == 1 {
							fmt.Fprint(w, `{"id":"resp_1","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"write","arguments":"{\"path\":\"a\"}"},{"type":"function_call","id":"fc_2","call_id":"call_2","name":"write","arguments":"{\"path\":\"b\"}"}],"usage":{"input_tokens":10,"output_tokens":4}}`)
						} else {
							fmt.Fprint(w, `{"id":"resp_2","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`)
						}
					}
				}))
				defer up.Close()
				svc := nativeStreamTestService(t, up.URL, out, []Target{anthropicToolTarget()})
				ids := testIdentifierTransform(t)
				svc.cfg.Server.Identifiers = identifierTestConfig()
				svc.identifiers = ids
				server := httptest.NewServer(svc.Handler())
				defer server.Close()
				tools := `"tools":[{"name":"write","description":"write file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]`
				send := func(messages string) map[string]any {
					req, _ := http.NewRequest("POST", server.URL+"/anthropic/v1/messages", strings.NewReader(`{"model":"native-stream","max_tokens":100,`+tools+choice+`,"messages":`+messages+`}`))
					req.Header.Set("Authorization", "Bearer "+testToken)
					resp, err := http.DefaultClient.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					defer resp.Body.Close()
					raw, _ := io.ReadAll(resp.Body)
					if resp.StatusCode != 200 {
						t.Fatalf("%d %s", resp.StatusCode, raw)
					}
					var result map[string]any
					if err := json.Unmarshal(raw, &result); err != nil {
						t.Fatal(err)
					}
					return result
				}
				first := send(`[{"role":"user","content":"write"}]`)
				blocks := valueAsSlice(first["content"])
				if first["stop_reason"] != "tool_use" || len(blocks) != 2 {
					t.Fatalf("bad tool reply: %#v", first)
				}
				block := blocks[0].(map[string]any)
				if block["id"] != ids.Encode("call_1") || block["name"] != "write" || block["input"].(map[string]any)["path"] != "a" {
					t.Fatalf("bad tool: %#v", block)
				}
				encoded, _ := json.Marshal(blocks)
				final := send(`[{"role":"user","content":"write"},{"role":"assistant","content":` + string(encoded) + `},{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + ids.Encode("call_1") + `","content":"written"},{"type":"tool_result","tool_use_id":"` + ids.Encode("call_2") + `","content":"written"}]}]`)
				if final["stop_reason"] != "end_turn" || !strings.Contains(fmt.Sprint(final["content"]), "done") {
					t.Fatalf("bad final: %#v", final)
				}
			})
		}
	}
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenAIImageDetailToAnthropicHTTP(t *testing.T) {
	for _, caller := range []string{"openai-responses", "openai-chat"} {
		for _, withTools := range []bool{false, true} {
			for _, detail := range []string{"low", "high", "auto"} {
				t.Run(fmt.Sprintf("%s/tools=%t/detail=%s", caller, withTools, detail), func(t *testing.T) {
					var upstream map[string]any
					hits := 0
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						hits++
						if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
							t.Error(err)
						}
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"bridge","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
					}))
					defer server.Close()
					target := anthropicToolTarget()
					target.InputModalities = []string{"text", "image"}
					svc := nativeStreamTestService(t, server.URL, "anthropic", []Target{target})
					path := "/v1/responses"
					body := `{"model":"native-stream","input":[{"role":"user","content":[{"type":"input_text","text":"describe"},{"type":"input_image","image_url":"data:image/png;base64,AAAA","detail":"` + detail + `"}]}]`
					if caller == "openai-chat" {
						path = "/v1/chat/completions"
						body = `{"model":"native-stream","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA","detail":"` + detail + `"}}]}]`
					}
					if withTools {
						if caller == "openai-responses" {
							body += `,"tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]`
						} else {
							body += `,"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}]`
						}
					}
					body += `}`
					r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
					r.Header.Set("Authorization", "Bearer "+testToken)
					w := httptest.NewRecorder()
					svc.Handler().ServeHTTP(w, r)
					if w.Code != 200 || hits != 1 {
						t.Fatalf("status=%d hits=%d body=%s", w.Code, hits, w.Body.String())
					}
					messages := valueAsSlice(upstream["messages"])
					if len(messages) != 1 {
						t.Fatalf("messages=%#v", messages)
					}
					content := valueAsSlice(messages[0].(map[string]any)["content"])
					if len(content) != 2 {
						t.Fatalf("content=%#v", content)
					}
					image := content[1].(map[string]any)
					source := image["source"].(map[string]any)
					if image["type"] != "image" || source["type"] != "base64" || source["media_type"] != "image/png" || source["data"] != "AAAA" {
						t.Fatalf("image changed: %#v", image)
					}
					encoded, _ := json.Marshal(image)
					if strings.Contains(string(encoded), `"detail"`) {
						t.Fatalf("detail forwarded: %s", encoded)
					}
				})
			}
		}
	}
}

func TestImageDetailStreamingAnthropicHTTP(t *testing.T) {
	fixture := anthropicBridgeFixture(t, "anthropic")
	for _, caller := range []string{"openai-chat", "openai-responses"} {
		t.Run(caller, func(t *testing.T) {
			var sent map[string]any
			hits := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, fixture)
			}))
			defer server.Close()
			target := anthropicToolTarget()
			target.InputModalities = []string{"text", "image"}
			svc := nativeStreamTestService(t, server.URL, "anthropic", []Target{target})
			path := "/v1/responses"
			body := `{"model":"native-stream","stream":true,"tools":[{"type":"function","name":"read","parameters":{"type":"object"}}],"input":[{"role":"user","content":[{"type":"input_text","text":"describe"},{"type":"input_image","image_url":"data:image/png;base64,AAAA","detail":"low"}]}]}`
			if caller == "openai-chat" {
				path = "/v1/chat/completions"
				body = `{"model":"native-stream","stream":true,"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA","detail":"low"}}]}]}`
			}
			r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			if w.Code != 200 || hits != 1 || !strings.Contains(w.Body.String(), "data:") {
				t.Fatalf("status=%d hits=%d response=%s", w.Code, hits, w.Body.String())
			}
			messages := valueAsSlice(sent["messages"])
			content := valueAsSlice(messages[0].(map[string]any)["content"])
			image := content[1].(map[string]any)
			if sent["stream"] != true || len(valueAsSlice(sent["tools"])) != 1 || image["source"].(map[string]any)["data"] != "AAAA" {
				t.Fatalf("image/stream changed: %#v", sent)
			}
			if _, ok := image["detail"]; ok {
				t.Fatalf("detail leaked: %#v", image)
			}
			if caller == "openai-responses" && !strings.Contains(w.Body.String(), "response.completed") {
				t.Fatalf("missing Responses terminal: %s", w.Body.String())
			}
			if caller == "openai-chat" && !strings.Contains(w.Body.String(), "[DONE]") {
				t.Fatalf("missing Chat terminal: %s", w.Body.String())
			}
		})
	}
}

// The image-detail translation must also survive a real local tool-result
// continuation; this is a synthetic provider, not a live vision claim.
func TestResponsesImageDetailToolContinuation(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		requests = append(requests, body)
		w.Header().Set("Content-Type", "application/json")
		if len(requests) == 1 {
			fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"bridge","content":[{"type":"tool_use","id":"call_vision","name":"read","input":{"path":"quote.txt"}}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":1}}`)
		} else {
			fmt.Fprint(w, `{"id":"msg_2","type":"message","role":"assistant","model":"bridge","content":[{"type":"text","text":"continued"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
		}
	}))
	defer server.Close()
	target := anthropicToolTarget()
	target.InputModalities = []string{"text", "image"}
	svc := nativeStreamTestService(t, server.URL, "anthropic", []Target{target})
	post := func(input []any) string {
		body := map[string]any{"model": "native-stream", "input": input, "reasoning": map[string]any{"effort": "none"}, "tools": []any{map[string]any{"type": "function", "name": "read", "parameters": map[string]any{"type": "object"}}}}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(raw)))
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	input := []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "read"}, map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAAA", "detail": "low"}}}}
	first := post(input)
	var response map[string]any
	if err := json.Unmarshal([]byte(first), &response); err != nil {
		t.Fatal(err)
	}
	var call map[string]any
	for _, value := range valueAsSlice(response["output"]) {
		item, _ := value.(map[string]any)
		if item["type"] == "function_call" {
			call = item
			break
		}
	}
	if call == nil || call["call_id"] != "call_vision" || call["name"] != "read" {
		t.Fatalf("missing tool call: %s", first)
	}
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(stringValue(call["arguments"])), &args); err != nil || args.Path != "quote.txt" {
		t.Fatalf("unexpected tool arguments: %#v %v", call, err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "quote.txt"), []byte("DETAIL-TOOL-RESULT"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := os.ReadFile(filepath.Join(dir, args.Path))
	if err != nil {
		t.Fatal(err)
	}
	input = append(input, map[string]any{"type": "function_call", "call_id": call["call_id"], "name": call["name"], "arguments": call["arguments"]}, map[string]any{"type": "function_call_output", "call_id": call["call_id"], "output": string(result)})
	second := post(input)
	if len(requests) != 2 || !strings.Contains(second, "continued") {
		t.Fatalf("continuation=%s requests=%d", second, len(requests))
	}
	for _, request := range requests {
		if _, ok := request["thinking"]; ok {
			t.Fatalf("none emitted thinking: %#v", request)
		}
	}
	messages := valueAsSlice(requests[1]["messages"])
	if len(messages) != 3 {
		t.Fatalf("messages=%#v", messages)
	}
	image := valueAsSlice(messages[0].(map[string]any)["content"])[1].(map[string]any)
	if source := image["source"].(map[string]any); source["data"] != "AAAA" || source["media_type"] != "image/png" {
		t.Fatalf("image lost: %#v", image)
	}
	output := valueAsSlice(messages[2].(map[string]any)["content"])[0].(map[string]any)
	parts := valueAsSlice(output["content"])
	if output["tool_use_id"] != "call_vision" || len(parts) != 1 || parts[0].(map[string]any)["text"] != string(result) {
		t.Fatalf("tool result changed: %#v", output)
	}
}

func TestImageDetailRejectsOtherAnthropicMetadata(t *testing.T) {
	for _, tc := range []struct{ caller, raw string }{
		{"openai-responses", `{"model":"m","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.test/image","detail":"low","unknown":"not-preserved"}]}]}`},
		{"openai-chat", `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.test/image","detail":"low","unknown":"not-preserved"}}]}]}`},
		{"openai-responses", `{"model":"m","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.test/image","file_id":"file_1","detail":"low"}]}]}`},
		{"openai-responses", `{"model":"m","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}],"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.test/image","file_id":"file_1","detail":"low"}]}]}`},
	} {
		req, err := decodeRequest(tc.caller, []byte(tc.raw), http.Header{})
		if err != nil {
			t.Fatal(err)
		}
		if !anthropicImageMetadataUnsupported(req, tc.caller) {
			t.Fatalf("%s: unsupported metadata accepted", tc.caller)
		}
	}
}

func TestImageDetailTranslationDiagnostics(t *testing.T) {
	for _, caller := range []string{"openai-responses", "openai-chat"} {
		for _, out := range []string{"anthropic", "openai-chat"} {
			var raw string
			if caller == "openai-responses" {
				raw = `{"model":"m","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.test/private","detail":"low"}]}]}`
			} else {
				raw = `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.test/private","detail":"low"}}]}]}`
			}
			req, err := decodeRequest(caller, []byte(raw), http.Header{})
			if err != nil {
				t.Fatal(err)
			}
			rc := &requestContext{dialect: caller}
			(&Service{}).recordTranslationShapeTelemetry(rc, req, Target{}, ProviderConfig{}, out, "https://upstream.invalid/v1/messages", 1, []byte(`{"model":"upstream","messages":[]}`))
			saw := false
			for _, event := range rc.rec.TranslationFieldEvents {
				if event.Reason == "image-detail-ignored" {
					saw = true
					if event.Action != "stripped" {
						t.Fatalf("event=%#v", event)
					}
				}
				assertShapeRecordDoesNotContain(t, event, "example.test", "private", "low")
			}
			if saw != (out == "anthropic") {
				t.Fatalf("caller=%s out=%s events=%#v", caller, out, rc.rec.TranslationFieldEvents)
			}
		}
	}
}

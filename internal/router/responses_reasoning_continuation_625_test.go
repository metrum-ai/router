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
	"testing"
)

// The provider below is a deterministic emulator, not evidence of provider
// support or cryptographic validity. It deliberately rejects missing state.
const reasoning625 = `{"type":"reasoning","id":"rs_synthetic_1","encrypted_content":"opaque+/=synthetic\\nbytes","summary":[]}`
const function625 = `{"type":"function_call","id":"fc_synthetic_1","call_id":"call_synthetic_1","name":"read","arguments":"{\"path\":\"quote.txt\"}"}`

func TestResponses625ReasoningTwoTurn(t *testing.T) {
	for _, force := range []bool{false, true} {
		for _, include := range []any{nil, []any{}, []any{"message.output_text.logprobs", "reasoning.encrypted_content"}, []any{"reasoning.encrypted_content"}} {
			t.Run(fmt.Sprintf("force=%t/include=%v", force, include), func(t *testing.T) {
				var calls []map[string]any
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					calls = append(calls, body)
					if body["include"] != nil && !reflect.DeepEqual(body["include"], include) {
						t.Errorf("include changed: %#v", body["include"])
					}
					if input, ok := body["input"].([]any); ok && len(input) > 1 {
						reason, ok := input[1].(map[string]any)
						if !ok || reason["id"] != "rs_synthetic_1" || reason["encrypted_content"] != "opaque+/=synthetic\\nbytes" {
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(400)
							fmt.Fprint(w, `{"error":{"message":"missing opaque state","type":"invalid_request_error","code":"invalid_value"}}`)
							return
						}
						if len(input) != 4 {
							t.Errorf("missing tool history: %#v", input)
							w.WriteHeader(400)
							return
						}
						result, ok := input[3].(map[string]any)
						if !ok || result["call_id"] != "call_synthetic_1" || result["output"] != "E2E-TOOL-RESULT-7842\n" {
							t.Errorf("tool result lost: %#v", result)
							w.WriteHeader(400)
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_test2\",\"status\":\"in_progress\",\"output\":[]}}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"item_id\":\"msg_1\",\"delta\":\"E2E-TOOL-RESULT-7842\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test2\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_test\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
					fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n", reasoning625)
					fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":%s}\n\n", function625)
					fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\",\"output\":[%s,%s],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n", reasoning625, function625)
				}))
				defer up.Close()
				target := anthropicToolTarget()
				target.ForceStoreFalse = force
				target.Reasoning = ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum}
				svc := nativeStreamTestService(t, up.URL, "openai-responses", []Target{target})
				input := []any{map[string]any{"role": "user", "content": "Read quote.txt"}}
				body := map[string]any{"model": "native-stream", "input": input, "stream": true, "reasoning": map[string]any{"effort": "low", "context": "all_turns"}}
				if include != nil {
					body["include"] = include
				}
				post := func(b map[string]any) *httptest.ResponseRecorder {
					raw, err := json.Marshal(b)
					if err != nil {
						t.Fatal(err)
					}
					r := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(raw))
					r.Header.Set("Authorization", "Bearer "+testToken)
					w := httptest.NewRecorder()
					svc.Handler().ServeHTTP(w, r)
					return w
				}
				first := post(body)
				if first.Code != 200 {
					t.Fatalf("first status=%d %s", first.Code, first.Body.String())
				}
				var item, call map[string]any
				for _, line := range strings.Split(first.Body.String(), "\n") {
					if strings.HasPrefix(line, "data:") {
						var event map[string]any
						if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event) == nil {
							if i, ok := event["item"].(map[string]any); ok {
								switch i["type"] {
								case "reasoning":
									item = i
								case "function_call":
									call = i
								}
							}
						}
					}
				}
				if item == nil || item["id"] != "rs_synthetic_1" || item["encrypted_content"] != "opaque+/=synthetic\\nbytes" {
					t.Fatalf("missing downstream reasoning item: %#v stream=%s", item, first.Body.String())
				}
				if call == nil || call["name"] != "read" || call["call_id"] != "call_synthetic_1" {
					t.Fatalf("missing tool call: %#v", call)
				}
				var args struct {
					Path string `json:"path"`
				}
				if err := json.Unmarshal([]byte(call["arguments"].(string)), &args); err != nil || args.Path != "quote.txt" {
					t.Fatalf("unexpected tool arguments: %#v %v", args, err)
				}
				dir := t.TempDir()
				file := filepath.Join(dir, args.Path)
				if err := os.WriteFile(file, []byte("E2E-TOOL-RESULT-7842\n"), 0600); err != nil {
					t.Fatal(err)
				}
				content, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				body["input"] = append(input, item, call, map[string]any{"type": "function_call_output", "call_id": call["call_id"], "output": string(content)})
				second := post(body)
				if second.Code != 200 || !strings.Contains(second.Body.String(), "E2E-TOOL-RESULT-7842") {
					t.Fatalf("continuation status=%d body=%s", second.Code, second.Body.String())
				}
				if len(calls) != 2 {
					t.Fatalf("calls=%d", len(calls))
				}
				for _, got := range calls {
					if _, present := got["include"]; present != (include != nil) || (include != nil && !reflect.DeepEqual(got["include"], include)) {
						t.Fatalf("include changed: %#v", got["include"])
					}
					if got["stream"] != true {
						t.Fatalf("not stream: %#v", got)
					}
					reasoning, ok := got["reasoning"].(map[string]any)
					if !ok || reasoning["context"] != "all_turns" {
						t.Fatalf("reasoning context lost: %#v", got["reasoning"])
					}
					if value, ok := got["store"]; ok != force || (ok && value != false) {
						t.Fatalf("target force policy changed: %#v", got)
					}
				}
				gotInput := calls[1]["input"].([]any)
				if !reflect.DeepEqual(gotInput[1], item) || !reflect.DeepEqual(gotInput[2], call) || gotInput[3].(map[string]any)["output"] != "E2E-TOOL-RESULT-7842\n" {
					t.Fatalf("history changed: %#v", gotInput)
				}
				// Direct baseline sends the exact routed provider body to the same mock.
				raw, _ := json.Marshal(calls[1])
				resp, err := http.Post(up.URL+"/v1/responses", "application/json", bytes.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				direct, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 200 || !reflect.DeepEqual(calls[1], calls[2]) || !strings.Contains(string(direct), "E2E-TOOL-RESULT-7842") {
					t.Fatalf("direct baseline mismatch status=%d", resp.StatusCode)
				}
				delete(item, "encrypted_content")
				missing := post(body)
				if missing.Code != 400 {
					t.Fatalf("mock must explicitly reject missing opaque state: status=%d body=%s", missing.Code, missing.Body.String())
				}
			})
		}
	}
}

func TestResponses625IncompatibleIncludeProvider(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprint(force), func(t *testing.T) {
			var got map[string]any
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":{"message":"include not supported by this test provider","type":"invalid_request_error","code":"unsupported_parameter","param":"include"}}`)
			}))
			defer up.Close()
			target := anthropicBridgeTarget()
			target.ForceStoreFalse = force
			svc := nativeStreamTestService(t, up.URL, "openai-responses", []Target{target})
			body := `{"model":"native-stream","input":"hi","stream":true,"include":["reasoning.encrypted_content","message.output_text.logprobs"]}`
			r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			want := []any{"reasoning.encrypted_content", "message.output_text.logprobs"}
			if w.Code != 400 || !reflect.DeepEqual(got["include"], want) || !strings.Contains(w.Body.String(), `"code":"upstream-rejected"`) {
				t.Fatalf("include rejection was lost or changed: status=%d include=%#v body=%s", w.Code, got["include"], w.Body.String())
			}
		})
	}
}

func TestResponses625CapturedPiControl(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/compat/pi-responses-continuation-sanitized.json")
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	if err = json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprint(force), func(t *testing.T) {
			var got map[string]any
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[]}}\n\n")
			}))
			defer up.Close()
			target := anthropicToolTarget()
			target.ForceStoreFalse = force
			svc := nativeStreamTestService(t, up.URL, "openai-responses", []Target{target})
			r := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(raw))
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			if w.Code != 200 || !reflect.DeepEqual(got["input"], input["input"]) || !reflect.DeepEqual(got["tools"], input["tools"]) || got["stream"] != true || got["include"] != nil {
				t.Fatalf("captured shape changed: status=%d body=%#v response=%s", w.Code, got, w.Body.String())
			}
			if force && got["store"] != false {
				t.Fatalf("forced no-store policy lost: %#v", got)
			}
			// Caller-store behavior without the force policy is tracked by #624.
			// Do not freeze its old stripped-field behavior in this control.
		})
	}
}

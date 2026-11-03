// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// issue627Fixture reads a recorded-shaped Anthropic upstream SSE stream.
func issue627Fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/sse/anthropic-tool-result/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// issue627UpstreamText returns the text the recorded upstream stream carries,
// reconstructed from its text_delta events. It is the ground truth a Chat caller
// must receive: the bridge must not invent, drop or duplicate any of it.
func issue627UpstreamText(t *testing.T, raw []byte) string {
	t.Helper()
	var text strings.Builder
	for _, frame := range strings.Split(string(raw), "\n\n") {
		data := ""
		for _, line := range strings.Split(frame, "\n") {
			if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		if data == "" {
			continue
		}
		var event struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatalf("fixture event: %v %s", err, data)
		}
		if event.Type == "content_block_delta" && event.Delta.Type == "text_delta" {
			text.WriteString(event.Delta.Text)
		}
	}
	return text.String()
}

// issue627ChatSSEText reassembles the downstream Chat SSE into its text and
// reports every content delta it saw, so a duplicated or reordered delta is
// visible rather than absorbed by the concatenation.
func issue627ChatSSEText(t *testing.T, body string) (string, []string, bool) {
	t.Helper()
	var text strings.Builder
	var deltas []string
	done := false
	for _, frame := range strings.Split(body, "\n\n") {
		payload := ""
		for _, line := range strings.Split(frame, "\n") {
			if strings.HasPrefix(line, "data: ") {
				payload = strings.TrimPrefix(line, "data: ")
			}
		}
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			done = true
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content *string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("downstream chunk: %v %s", err, payload)
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content == nil || *choice.Delta.Content == "" {
				continue
			}
			deltas = append(deltas, *choice.Delta.Content)
			text.WriteString(*choice.Delta.Content)
		}
	}
	return text.String(), deltas, done
}

// issue627Chunks splits a fixture into writes of the given size; size <= 0 sends
// the whole stream in one write. Every write is flushed so the router observes
// the awkward fragment boundaries (#627).
func issue627Chunks(raw []byte, size int) [][]byte {
	if size <= 0 {
		return [][]byte{raw}
	}
	var out [][]byte
	for len(raw) > 0 {
		n := size
		if n > len(raw) {
			n = len(raw)
		}
		out = append(out, raw[:n])
		raw = raw[n:]
	}
	return out
}

func issue627ChatRequest(t *testing.T, toolResult string) string {
	t.Helper()
	return `{"model":"native-stream","stream":true,"stream_options":{"include_usage":true},"max_tokens":512,` +
		`"tools":[{"type":"function","function":{"name":"read","description":"Read a file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}],` +
		`"messages":[{"role":"user","content":"Read quote.txt and quote it exactly."},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call_627","type":"function","function":{"name":"read","arguments":"{\"path\":\"quote.txt\"}"}}]},` +
		`{"role":"tool","tool_call_id":"call_627","content":` + toolResult + `}]}`
}

// TestPiChatToolResultContinuation627 is the multi-turn Chat-to-Messages
// regression #627 asks for: a Chat caller sends a tool result, the Anthropic
// target answers from a recorded-shaped SSE stream, and the downstream Chat SSE
// must carry exactly the upstream text, with the tool result intact upstream.
func TestPiChatToolResultContinuation627(t *testing.T) {
	fixture := issue627Fixture(t, "quote-marker.sse")
	want := issue627UpstreamText(t, fixture)
	if !strings.Contains(want, "E2E-TOOL-RESULT-7842") {
		t.Fatalf("fixture lost its marker: %q", want)
	}
	cases := []struct{ name, result, text string }{
		{"string-result", `"E2E-TOOL-RESULT-7842\n"`, "E2E-TOOL-RESULT-7842\n"},
		{"array-result", `[{"type":"text","text":"E2E-TOOL-RESULT-7842\n"}]`, "E2E-TOOL-RESULT-7842\n"},
	}
	for _, c := range cases {
		for _, size := range []int{1, 7, 0} {
			t.Run(fmt.Sprintf("%s/write-%d", c.name, size), func(t *testing.T) {
				var mu sync.Mutex
				var upstream map[string]any
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						return
					}
					mu.Lock()
					upstream = body
					mu.Unlock()
					w.Header().Set("Content-Type", "text/event-stream")
					flusher, _ := w.(http.Flusher)
					for _, piece := range issue627Chunks(fixture, size) {
						if _, err := w.Write(piece); err != nil {
							return
						}
						if flusher != nil {
							flusher.Flush()
						}
					}
				}))
				defer up.Close()
				target := anthropicToolTarget()
				svc := nativeStreamTestService(t, up.URL, "anthropic", []Target{target})
				req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(issue627ChatRequest(t, c.result)))
				req.Header.Set("Authorization", "Bearer "+testToken)
				rr := httptest.NewRecorder()
				svc.Handler().ServeHTTP(rr, req)
				if rr.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
				}

				// Upstream fidelity: the tool result reached Anthropic unchanged and
				// paired with the assistant tool_use id from the same request.
				mu.Lock()
				sent := upstream
				mu.Unlock()
				messages := valueAsSlice(sent["messages"])
				if len(messages) != 3 {
					t.Fatalf("upstream messages: %#v", sent["messages"])
				}
				call := valueAsSlice(messages[1].(map[string]any)["content"])[0].(map[string]any)
				result := valueAsSlice(messages[2].(map[string]any)["content"])[0].(map[string]any)
				if call["type"] != "tool_use" || result["type"] != "tool_result" || call["id"] != result["tool_use_id"] {
					t.Fatalf("tool pairing lost upstream: call=%#v result=%#v", call, result)
				}
				blocks := valueAsSlice(result["content"])
				if len(blocks) != 1 || blocks[0].(map[string]any)["type"] != "text" || blocks[0].(map[string]any)["text"] != c.text {
					t.Fatalf("tool result text changed upstream: %#v", result["content"])
				}

				// Downstream fidelity: the Chat caller gets exactly the upstream text.
				got, deltas, done := issue627ChatSSEText(t, rr.Body.String())
				if !done {
					t.Fatalf("missing [DONE]: %s", rr.Body.String())
				}
				if got != want {
					t.Fatalf("downstream diverged from upstream\ngot  %q\nwant %q", got, want)
				}
				if strings.Count(got, "E2E-TOOL-RESULT-7842") != 1 {
					t.Fatalf("marker not carried exactly once: %q", got)
				}
				if got != strings.Join(deltas, "") {
					t.Fatalf("delta order changed: %#v", deltas)
				}
			})
		}
	}
}

// TestPiChatToMessagesToolResultShapes627 pins the offline request shapes #627
// lists: tool-result strings versus arrays, ordered and multipart parts,
// multiple results, and tool input objects. Nothing may be lost or reordered.
func TestPiChatToMessagesToolResultShapes627(t *testing.T) {
	target := Target{RequestShapeSupport: RequestShapeSupport{SupportedInboundDialects: []string{"openai-chat"}, ValidationStatus: "passed"},
		ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}, AnthropicMessages: []string{"client_tools", "tool_choice"}}}
	cases := []struct {
		name    string
		tail    string
		wantIDs []string
		want    [][]string // per tool_result: ordered text blocks
	}{
		{
			name:    "string-result",
			tail:    `{"role":"tool","tool_call_id":"c1","content":"E2E-TOOL-RESULT-7842\n"}`,
			wantIDs: []string{"c1"},
			want:    [][]string{{"E2E-TOOL-RESULT-7842\n"}},
		},
		{
			name:    "single-text-part",
			tail:    `{"role":"tool","tool_call_id":"c1","content":[{"type":"text","text":"E2E-TOOL-RESULT-7842"}]}`,
			wantIDs: []string{"c1"},
			want:    [][]string{{"E2E-TOOL-RESULT-7842"}},
		},
		{
			name: "multipart-ordering",
			// A part that carries no text is an empty string block: dropping it loses no
			// bytes, and the surrounding parts keep their order and exact text.
			tail:    `{"role":"tool","tool_call_id":"c1","content":[{"type":"text","text":"a"},{"type":"text","text":""},{"type":"text","text":"b\n"}]}`,
			wantIDs: []string{"c1"},
			want:    [][]string{{"a", "b\n"}},
		},
		{
			name:    "multiple-results",
			tail:    `{"role":"tool","tool_call_id":"c1","content":"one"},{"role":"tool","tool_call_id":"c2","content":[{"type":"text","text":"two"}]}`,
			wantIDs: []string{"c1", "c2"},
			want:    [][]string{{"one"}, {"two"}},
		},
		{
			name:    "empty-result",
			tail:    `{"role":"tool","tool_call_id":"c1","content":""}`,
			wantIDs: []string{"c1"},
			want:    nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := `{"model":"m","max_tokens":32,"messages":[{"role":"user","content":"read"},` +
				`{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{\"path\":\"quote.txt\"}"}},{"id":"c2","type":"function","function":{"name":"read","arguments":"{\"path\":\"notes.txt\"}"}}]},` +
				c.tail + `]}`
			req, err := decodeRequest("openai-chat", []byte(body), nil)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := encodeOpenAIToolsToAnthropic("m", "openai-chat", req, target)
			if err != nil {
				t.Fatal(err)
			}
			var sent map[string]any
			if err := json.Unmarshal(raw, &sent); err != nil {
				t.Fatal(err)
			}
			messages := valueAsSlice(sent["messages"])
			calls := valueAsSlice(messages[1].(map[string]any)["content"])
			if len(calls) != 2 || calls[0].(map[string]any)["id"] != "c1" || calls[1].(map[string]any)["id"] != "c2" {
				t.Fatalf("tool_use identity: %#v", messages[1])
			}
			if input, _ := calls[0].(map[string]any)["input"].(map[string]any); input["path"] != "quote.txt" {
				t.Fatalf("tool input object changed: %#v", calls[0])
			}
			blocks := valueAsSlice(messages[2].(map[string]any)["content"])
			if len(blocks) != len(c.wantIDs) {
				t.Fatalf("tool_result count: %#v", blocks)
			}
			for i, wantID := range c.wantIDs {
				block := blocks[i].(map[string]any)
				if block["type"] != "tool_result" || block["tool_use_id"] != wantID {
					t.Fatalf("result %d: %#v", i, block)
				}
				if c.want == nil {
					if block["content"] != "" {
						t.Fatalf("empty result changed: %#v", block)
					}
					continue
				}
				parts := valueAsSlice(block["content"])
				if len(parts) != len(c.want[i]) {
					t.Fatalf("result %d parts: %#v", i, block["content"])
				}
				for j, text := range c.want[i] {
					part := parts[j].(map[string]any)
					if part["type"] != "text" || part["text"] != text {
						t.Fatalf("result %d part %d: %#v", i, j, part)
					}
				}
			}
		})
	}
}

// TestPiChatToolResultContinuationUpstreamToolCall627 replays a streamed tool
// call with fragmented JSON argument deltas: the downstream Chat caller must
// receive the same arguments the upstream fragments spell out.
func TestPiChatToolResultContinuationUpstreamToolCall627(t *testing.T) {
	fixture := issue627Fixture(t, "tool-call-fragments.sse")
	for _, size := range []int{1, 5, 0} {
		t.Run(fmt.Sprintf("write-%d", size), func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				flusher, _ := w.(http.Flusher)
				for _, piece := range issue627Chunks(fixture, size) {
					if _, err := w.Write(piece); err != nil {
						return
					}
					if flusher != nil {
						flusher.Flush()
					}
				}
			}))
			defer up.Close()
			svc := nativeStreamTestService(t, up.URL, "anthropic", []Target{anthropicToolTarget()})
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(issue627ChatRequest(t, `"E2E-TOOL-RESULT-7842\n"`)))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			text, _, done := issue627ChatSSEText(t, rr.Body.String())
			if !done {
				t.Fatalf("missing [DONE]: %s", rr.Body.String())
			}
			if text != "Reading both files.\n" {
				t.Fatalf("text changed: %q", text)
			}
			arguments := issue627ToolArguments(t, rr.Body.String())
			if len(arguments) != 2 || arguments[0] != `{"path":"quote.txt"}` || arguments[1] != `{"path":"notes.txt"}` {
				t.Fatalf("streamed tool arguments changed: %#v", arguments)
			}
		})
	}
}

// issue627ToolArguments reassembles downstream Chat tool-call fragments by index.
func issue627ToolArguments(t *testing.T, body string) []string {
	t.Helper()
	fragments := map[int]*strings.Builder{}
	max := -1
	for _, frame := range strings.Split(body, "\n\n") {
		payload := ""
		for _, line := range strings.Split(frame, "\n") {
			if strings.HasPrefix(line, "data: ") {
				payload = strings.TrimPrefix(line, "data: ")
			}
		}
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						Index    int `json:"index"`
						Function struct {
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("downstream chunk: %v %s", err, payload)
		}
		for _, choice := range chunk.Choices {
			for _, call := range choice.Delta.ToolCalls {
				if fragments[call.Index] == nil {
					fragments[call.Index] = &strings.Builder{}
				}
				fragments[call.Index].WriteString(call.Function.Arguments)
				if call.Index > max {
					max = call.Index
				}
			}
		}
	}
	out := make([]string, max+1)
	for index := 0; index <= max; index++ {
		if fragments[index] == nil {
			t.Fatalf("gap in tool call indexes: %#v", fragments)
		}
		out[index] = fragments[index].String()
	}
	return out
}

// TestAnthropicToolResultForms627 pins the valid Anthropic tool-result forms
// #627 lists for the Messages-to-Chat bridge: string and array content, ordered
// parts, is_error and multiple results. Valid forms stay supported, ids pair
// with their tool_use blocks and no text is dropped or reordered. Chat has no
// is_error field, so that hint is not representable on the Chat side; the text
// and the pairing are still exact, which is the limit noted on the issue.
func TestAnthropicToolResultForms627(t *testing.T) {
	cases := []struct {
		name    string
		content string
		isError string
		want    string
	}{
		{"string", `"E2E-TOOL-RESULT-7842\n"`, "", "E2E-TOOL-RESULT-7842\n"},
		{"single-text-part", `[{"type":"text","text":"E2E-TOOL-RESULT-7842\n"}]`, "", "E2E-TOOL-RESULT-7842\n"},
		{"ordered-parts", `[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, "", "ab"},
		{"is-error-false", `"E2E-TOOL-RESULT-7842\n"`, `"is_error":false,`, "E2E-TOOL-RESULT-7842\n"},
		{"is-error-true", `"no such file"`, `"is_error":true,`, "no such file"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := `{"model":"m","max_tokens":32,"messages":[{"role":"user","content":"read"},` +
				`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_627","name":"read","input":{"path":"quote.txt"}}]},` +
				`{"role":"user","content":[{"type":"tool_result",` + c.isError + `"tool_use_id":"toolu_627","content":` + c.content + `}]}]}`
			req, err := decodeRequest("anthropic", []byte(body), nil)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := encodeAnthropicToolsBridge("m", req, anthropicToolTarget(), "openai-chat")
			if err != nil {
				t.Fatalf("valid Anthropic result form rejected: %v", err)
			}
			var sent map[string]any
			if err := json.Unmarshal(raw, &sent); err != nil {
				t.Fatal(err)
			}
			messages := valueAsSlice(sent["messages"])
			if len(messages) != 3 {
				t.Fatalf("messages: %s", raw)
			}
			call := valueAsSlice(messages[1].(map[string]any)["tool_calls"])[0].(map[string]any)
			result := messages[2].(map[string]any)
			if call["id"] != "toolu_627" || result["role"] != "tool" || result["tool_call_id"] != "toolu_627" {
				t.Fatalf("tool pairing lost: %s", raw)
			}
			if result["content"] != c.want {
				t.Fatalf("tool result text changed: %v want %q", result["content"], c.want)
			}
		})
	}
	t.Run("multiple-results", func(t *testing.T) {
		body := `{"model":"m","max_tokens":32,"messages":[{"role":"user","content":"read"},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_a","name":"read","input":{"path":"a"}},{"type":"tool_use","id":"toolu_b","name":"read","input":{"path":"b"}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_a","content":"one"},{"type":"tool_result","tool_use_id":"toolu_b","content":[{"type":"text","text":"two"}]}]}]}`
		req, err := decodeRequest("anthropic", []byte(body), nil)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := encodeAnthropicToolsBridge("m", req, anthropicToolTarget(), "openai-chat")
		if err != nil {
			t.Fatal(err)
		}
		var sent map[string]any
		if err := json.Unmarshal(raw, &sent); err != nil {
			t.Fatal(err)
		}
		messages := valueAsSlice(sent["messages"])
		if len(messages) != 4 {
			t.Fatalf("messages: %s", raw)
		}
		first := messages[2].(map[string]any)
		second := messages[3].(map[string]any)
		if first["tool_call_id"] != "toolu_a" || first["content"] != "one" || second["tool_call_id"] != "toolu_b" || second["content"] != "two" {
			t.Fatalf("multiple results changed or reordered: %s", raw)
		}
	})
}

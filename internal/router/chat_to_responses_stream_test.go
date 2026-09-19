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
	"sync/atomic"
	"testing"
	"time"

	"github.com/metrum-ai/router/internal/stream"
)

const responsesBridgeDelta = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_private\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"first\"}\n\n"
const responsesBridgeEnd = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_private\",\"usage\":{\"input_tokens\":5,\"output_tokens\":3,\"total_tokens\":8}}}\n\n"

func responsesBridgeTarget() Target {
	return Target{Provider: "native", Model: "responses-bridge", Bridges: BridgeSupport{ChatToResponses: DialectBridgeSupport{Enabled: true}}}
}

func TestChatToResponsesStreamingIncrementalAndCancel(t *testing.T) {
	for _, cancelEarly := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelEarly), func(t *testing.T) {
			release := make(chan struct{})
			canceled := make(chan struct{})
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				_, hasOptions := body["stream_options"]
				if body["stream"] != true || hasOptions || r.URL.Path != "/responses" {
					t.Errorf("wrong upstream request: %s %#v", r.URL.Path, body)
				}
				if _, ok := body["previous_response_id"]; ok {
					t.Error("invented session mapping")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, responsesBridgeDelta)
				w.(http.Flusher).Flush()
				select {
				case <-release:
					fmt.Fprint(w, responsesBridgeEnd)
				case <-r.Context().Done():
					close(canceled)
				}
			}))
			defer up.Close()
			svc := nativeStreamTestService(t, up.URL, "openai-responses", []Target{responsesBridgeTarget()})
			server := httptest.NewServer(svc.Handler())
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"native-stream","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set("Authorization", "Bearer "+testToken)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			f := stream.NewFramer(resp.Body, 1<<20)
			for {
				e, err := f.Next()
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(e.Data), `"content":"first"`) {
					break
				}
			}
			// The upstream cannot complete until a caller delta has actually arrived.
			if cancelEarly {
				cancel()
				select {
				case <-canceled:
				case <-time.After(2 * time.Second):
					t.Fatal("upstream not canceled")
				}
				return
			}
			close(release)
			var last stream.Event
			var received strings.Builder
			for {
				e, err := f.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				last = e
				received.Write(e.Data)
			}
			if string(last.Data) != "[DONE]" || !strings.Contains(received.String(), `"total_tokens":8`) {
				t.Fatalf("terminal: %s", last.Data)
			}
		})
	}
}
func TestChatToResponsesStreamingUsageAndFaults(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		success   bool
	}{
		{"complete", responsesBridgeDelta + responsesBridgeEnd, true},
		{"truncated", responsesBridgeDelta, false},
		{"malformed", responsesBridgeDelta + "data: {\n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			result, err := proxyResponsesUpstreamToChatCallerSSE(context.Background(), rr, strings.NewReader(tc.raw), "openai-responses", "test", 1<<20, nil, testIdentifierTransform(t))
			if !result.Committed {
				t.Fatal("not committed")
			}
			if tc.success {
				if err != nil || result.Response.Usage.TotalTokens != 8 {
					t.Fatal(result, err)
				}
			} else {
				if err == nil || !committedStreamError(err) || strings.Contains(rr.Body.String(), "[DONE]") {
					t.Fatal(result, err, rr.Body.String())
				}
			}
		})
	}
}
func TestChatToResponsesStreamingNoFallbackAfterDelta(t *testing.T) {
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, responsesBridgeDelta+"data: {\n\n")
	}))
	defer up.Close()
	target := responsesBridgeTarget()
	svc := nativeStreamTestService(t, up.URL, "openai-responses", []Target{target, target})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"native-stream","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	svc.Handler().ServeHTTP(rr, req)
	if calls.Load() != 1 || !strings.Contains(rr.Body.String(), `"content":"first"`) || strings.Contains(rr.Body.String(), "[DONE]") {
		t.Fatal(calls.Load(), rr.Body.String())
	}
}

func TestChatToResponsesStreamingSynthesized(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["stream"] != false {
			t.Errorf("upstream stream=%v", body["stream"])
		}
		fmt.Fprint(w, `{"id":"resp_test","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"synthetic"}]}]}`)
	}))
	defer up.Close()
	svc := nativeStreamTestService(t, up.URL, "openai-responses", []Target{responsesBridgeTarget()})
	svc.cfg.Server.Streaming.Translator = "synthesized"
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"native-stream","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "synthetic") || !strings.Contains(rr.Body.String(), "[DONE]") {
		t.Fatal(rr.Code, rr.Body.String())
	}
}

func TestChatToResponsesStreamingCommitBoundary(t *testing.T) {
	created := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_test\"}}\n\n"
	for _, raw := range []string{"", created, created + "data: {\n\n"} {
		rr := httptest.NewRecorder()
		result, err := proxyResponsesUpstreamToChatCallerSSE(context.Background(), rr, strings.NewReader(raw), "openai-responses", "test", 1<<20, nil)
		if err == nil || result.Committed || rr.Body.Len() != 0 {
			t.Fatal(result, err, rr.Body.String())
		}
	}
}

func TestChatToResponsesStreamingToolsAndIdentifiers(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/sse/synthetic/responses-to-chat/tools.sse")
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	ids := testIdentifierTransform(t)
	result, err := proxyResponsesUpstreamToChatCallerSSE(context.Background(), rr, strings.NewReader(string(raw)), "openai-responses", "test", 1<<20, nil, ids)
	if err != nil {
		t.Fatal(err)
	}
	u := result.Response.Usage
	if u.TotalTokens != 8 || u.CachedInputTokens == nil || *u.CachedInputTokens != 2 || u.ReasoningTokens == nil || *u.ReasoningTokens != 1 {
		t.Fatal(u)
	}
	if result.Response.ID != "resp_private" {
		t.Fatal("lost upstream session ID")
	}
	f := stream.NewFramer(strings.NewReader(rr.Body.String()), 1<<20)
	args := map[int]string{}
	first := map[int]bool{}
	for {
		e, err := f.Next()
		if err != nil {
			t.Fatal(err)
		}
		if string(e.Data) == "[DONE]" {
			break
		}
		var p struct {
			ID      string
			Choices []struct {
				Delta struct {
					Tools []struct {
						Index    int
						ID       string
						Function struct{ Name, Arguments string }
					} `json:"tool_calls"`
				}
			}
		}
		if err := json.Unmarshal(e.Data, &p); err != nil {
			t.Fatal(err)
		}
		if p.ID != ids.Encode("resp_private") {
			t.Fatal("unencoded response ID", p.ID)
		}
		for _, c := range p.Choices {
			for _, call := range c.Delta.Tools {
				if !first[call.Index] {
					want := "call_a"
					if call.Index == 1 {
						want = "call_b"
					}
					if call.ID != ids.Encode(want) || call.Function.Name == "" {
						t.Fatal("missing first identity", call)
					}
					first[call.Index] = true
				} else if call.ID != "" {
					t.Fatal("repeated ID", call)
				}
				args[call.Index] += call.Function.Arguments
			}
		}
	}
	if args[0] != `{"city":"Paris"}` || args[1] != `{"zone":"UTC"}` {
		t.Fatal(args)
	}
}

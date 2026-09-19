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

const chatBridgeDelta = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first\"}}]}\n\n"
const chatBridgeEnd = "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3,\"total_tokens\":8}}\n\ndata: [DONE]\n\n"

func chatBridgeTarget() Target {
	return Target{Provider: "native", Model: "chat-bridge", ResponsesToChat: ResponsesToChatBridge{Enabled: true, Text: true, Streaming: true}}
}

func TestResponsesToChatStreamingIncrementalAndCancel(t *testing.T) {
	for _, cancelEarly := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelEarly), func(t *testing.T) {
			release := make(chan struct{})
			canceled := make(chan struct{})
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				opts, _ := body["stream_options"].(map[string]any)
				if body["stream"] != true || opts["include_usage"] != true || r.URL.Path != "/chat/completions" {
					t.Errorf("wrong upstream request: %s %#v", r.URL.Path, body)
				}
				if _, ok := body["previous_response_id"]; ok {
					t.Error("invented session mapping")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, chatBridgeDelta)
				w.(http.Flusher).Flush()
				select {
				case <-release:
					fmt.Fprint(w, chatBridgeEnd)
				case <-r.Context().Done():
					close(canceled)
				}
			}))
			defer up.Close()
			svc := nativeStreamTestService(t, up.URL, "openai-chat", []Target{chatBridgeTarget()})
			server := httptest.NewServer(svc.Handler())
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/responses", strings.NewReader(`{"model":"native-stream","stream":true,"input":"hi"}`))
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
				if e.Name == "response.output_text.delta" {
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
			for {
				e, err := f.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				last = e
			}
			if last.Name != "response.completed" || !strings.Contains(string(last.Data), `"total_tokens":8`) {
				t.Fatalf("terminal: %s", last.Data)
			}
		})
	}
}
func TestResponsesToChatStreamingUsageAndFaults(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		success   bool
	}{
		{"complete", chatBridgeDelta + chatBridgeEnd, true},
		{"truncated", chatBridgeDelta, false},
		{"malformed", chatBridgeDelta + "data: {\n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			result, err := proxyChatUpstreamToResponsesCallerSSE(context.Background(), rr, strings.NewReader(tc.raw), "openai-chat", "test", 1<<20, nil, testIdentifierTransform(t))
			if !result.Committed {
				t.Fatal("not committed")
			}
			if tc.success {
				if err != nil || result.Response.Usage.TotalTokens != 8 {
					t.Fatal(result, err)
				}
			} else {
				if err == nil || !committedStreamError(err) || strings.Contains(rr.Body.String(), "response.completed") {
					t.Fatal(result, err, rr.Body.String())
				}
			}
		})
	}
}
func TestResponsesToChatStreamingNoFallbackAfterCreated(t *testing.T) {
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, "data: {\n\n") }))
	defer up.Close()
	target := chatBridgeTarget()
	svc := nativeStreamTestService(t, up.URL, "openai-chat", []Target{target, target})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"native-stream","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	svc.Handler().ServeHTTP(rr, req)
	if calls.Load() != 1 || !strings.Contains(rr.Body.String(), "response.created") || strings.Contains(rr.Body.String(), "response.completed") {
		t.Fatal(calls.Load(), rr.Body.String())
	}
}

func TestResponsesToChatStreamingToolsAndPartialUsage(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/sse/synthetic/openai-chat/tools.sse")
	if err != nil {
		t.Fatal(err)
	}
	for _, complete := range []bool{true, false} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			body := string(raw)
			if !complete {
				body = strings.TrimSuffix(body, "data: [DONE]\n\n")
			}
			rr := httptest.NewRecorder()
			result, err := proxyChatUpstreamToResponsesCallerSSE(context.Background(), rr, strings.NewReader(body), "openai-chat", "test", 1<<20, nil, testIdentifierTransform(t))
			if (err == nil) != complete {
				t.Fatal(err)
			}
			u := result.Response.Usage
			if u.TotalTokens != 8 || u.CachedInputTokens == nil || *u.CachedInputTokens != 2 || u.ReasoningTokens == nil || *u.ReasoningTokens != 1 {
				t.Fatal(u)
			}
			if !strings.Contains(rr.Body.String(), "response.function_call_arguments.delta") || strings.Contains(rr.Body.String(), "call_weather") || strings.Contains(rr.Body.String(), "chatcmpl_private") {
				t.Fatal(rr.Body.String())
			}
			if !complete && strings.Contains(rr.Body.String(), "response.completed") {
				t.Fatal("fabricated success")
			}
		})
	}
}

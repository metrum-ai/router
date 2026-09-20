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

func anthropicBridgeRequest(caller string) (string, string) {
	switch caller {
	case "anthropic":
		return "/anthropic/v1/messages", `{"model":"native-stream","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	case "openai-responses":
		return "/v1/responses", `{"model":"native-stream","stream":true,"input":"hi"}`
	default:
		return "/v1/chat/completions", `{"model":"native-stream","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	}
}
func anthropicBridgeFixture(t *testing.T, upstream string) string {
	t.Helper()
	folder := "anthropic-bridges"
	if upstream == "openai-chat" {
		folder = "openai-chat"
	}
	if upstream == "openai-responses" {
		folder = "responses-to-chat"
	}
	raw, err := os.ReadFile("../../testdata/sse/synthetic/" + folder + "/text.sse")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func anthropicBridgeTarget() Target {
	return Target{Provider: "native", Model: "bridge", RequestShapeSupport: RequestShapeSupport{SupportedInboundDialects: []string{"anthropic", "openai-chat", "openai-responses"}, ValidationStatus: "passed"}}
}
func TestAnthropicBridgeStreamingIncrementalAndCancel(t *testing.T) {
	for _, pair := range [][2]string{{"anthropic", "openai-chat"}, {"anthropic", "openai-responses"}, {"openai-chat", "anthropic"}, {"openai-responses", "anthropic"}} {
		for _, cancelEarly := range []bool{false, true} {
			t.Run(fmt.Sprint(pair, cancelEarly), func(t *testing.T) {
				raw := anthropicBridgeFixture(t, pair[1])
				marker := "data: [DONE]"
				if pair[1] == "anthropic" {
					marker = "event: message_delta"
				}
				if pair[1] == "openai-responses" {
					marker = "event: response.completed"
				}
				split := strings.Index(raw, marker)
				if split < 0 {
					t.Fatal("no terminal marker")
				}
				release, canceled := make(chan struct{}), make(chan struct{})
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body["stream"] != true {
						t.Errorf("not streaming: %v", body)
					}
					if pair[1] == "openai-chat" {
						opts, _ := body["stream_options"].(map[string]any)
						if opts["include_usage"] != true {
							t.Error("missing usage option")
						}
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, raw[:split])
					w.(http.Flusher).Flush()
					select {
					case <-release:
						fmt.Fprint(w, raw[split:])
					case <-r.Context().Done():
						close(canceled)
					}
				}))
				defer up.Close()
				svc := nativeStreamTestService(t, up.URL, pair[1], []Target{anthropicBridgeTarget()})
				server := httptest.NewServer(svc.Handler())
				defer server.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				path, body := anthropicBridgeRequest(pair[0])
				req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+path, strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+testToken)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != 200 {
					data, _ := io.ReadAll(resp.Body)
					t.Fatalf("%d %s", resp.StatusCode, data)
				}
				f := stream.NewFramer(resp.Body, 1<<20)
				for {
					e, err := f.Next()
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(e.Data), `"text_delta"`) || strings.Contains(string(e.Data), `"content":`) || e.Type == "response.output_text.delta" {
						break
					}
				}
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
				rest, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				terminal := "[DONE]"
				if pair[0] == "anthropic" {
					terminal = "message_stop"
				}
				if pair[0] == "openai-responses" {
					terminal = "response.completed"
				}
				if !strings.Contains(string(rest), terminal) {
					t.Fatalf("missing terminal %s", rest)
				}
			})
		}
	}
}
func TestAnthropicBridgeStreamingNoFallbackAfterFrame(t *testing.T) {
	for _, pair := range [][2]string{{"anthropic", "openai-chat"}, {"anthropic", "openai-responses"}, {"openai-chat", "anthropic"}, {"openai-responses", "anthropic"}} {
		t.Run(fmt.Sprint(pair), func(t *testing.T) {
			var calls atomic.Int64
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if pair[1] == "anthropic" {
					fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg\"}}\n\n")
				}
				fmt.Fprint(w, "data: {\n\n")
			}))
			defer up.Close()
			target := anthropicBridgeTarget()
			svc := nativeStreamTestService(t, up.URL, pair[1], []Target{target, target})
			path, body := anthropicBridgeRequest(pair[0])
			req := httptest.NewRequest("POST", path, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if calls.Load() != 1 || rr.Body.Len() == 0 || strings.Contains(rr.Body.String(), "message_stop") || strings.Contains(rr.Body.String(), "[DONE]") || strings.Contains(rr.Body.String(), "response.completed") {
				t.Fatal(calls.Load(), rr.Body.String())
			}
		})
	}
}
func TestAnthropicBridgeStreamingSynthesized(t *testing.T) {
	for _, pair := range [][2]string{{"anthropic", "openai-chat"}, {"anthropic", "openai-responses"}, {"openai-chat", "anthropic"}, {"openai-responses", "anthropic"}} {
		t.Run(fmt.Sprint(pair), func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				if body["stream"] != false {
					t.Error(body)
				}
				switch pair[1] {
				case "anthropic":
					fmt.Fprint(w, `{"id":"msg","content":[{"type":"text","text":"synthetic"}],"stop_reason":"end_turn"}`)
				case "openai-chat":
					fmt.Fprint(w, `{"id":"chat","choices":[{"message":{"content":"synthetic"},"finish_reason":"stop"}]}`)
				default:
					fmt.Fprint(w, `{"id":"resp","output":[{"type":"message","content":[{"type":"output_text","text":"synthetic"}]}]}`)
				}
			}))
			defer up.Close()
			svc := nativeStreamTestService(t, up.URL, pair[1], []Target{anthropicBridgeTarget()})
			svc.cfg.Server.Streaming.Translator = "synthesized"
			path, body := anthropicBridgeRequest(pair[0])
			req := httptest.NewRequest("POST", path, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != 200 || !strings.Contains(rr.Body.String(), "synthetic") {
				t.Fatal(rr.Code, rr.Body.String())
			}
		})
	}
}

func TestAnthropicBridgeStreamingToolsAndIdentifiers(t *testing.T) {
	for _, pair := range [][2]string{{"anthropic", "openai-chat"}, {"anthropic", "openai-responses"}, {"openai-chat", "anthropic"}, {"openai-responses", "anthropic"}} {
		t.Run(fmt.Sprint(pair), func(t *testing.T) {
			folder := "anthropic-bridges"
			if pair[1] == "openai-chat" {
				folder = "openai-chat"
			}
			if pair[1] == "openai-responses" {
				folder = "responses-to-chat"
			}
			raw, err := os.ReadFile("../../testdata/sse/synthetic/" + folder + "/tools.sse")
			if err != nil {
				t.Fatal(err)
			}
			ids := testIdentifierTransform(t)
			rr := httptest.NewRecorder()
			result, err := anthropicBridgeProxy(pair[0])(context.Background(), rr, strings.NewReader(string(raw)), pair[1], "test", 1<<20, nil, ids)
			if err != nil || result.Response.Usage.TotalTokens != 8 {
				t.Fatal(result, err)
			}
			names := []string{"call_weather", "call_clock"}
			if pair[1] == "openai-responses" {
				names = []string{"call_a", "call_b"}
			}
			for _, name := range names {
				if strings.Contains(rr.Body.String(), name) || !strings.Contains(rr.Body.String(), ids.Encode(name)) {
					t.Fatal("tool identity not transformed on first fragment", rr.Body.String())
				}
			}
		})
	}
}
func TestAnthropicBridgeReasoningAndToolGates(t *testing.T) {
	target := anthropicBridgeTarget()
	req := &IRRequest{Reasoning: ReasoningIntent{Requested: true}}
	for _, pair := range [][2]string{{"anthropic", "openai-chat"}, {"anthropic", "openai-responses"}, {"openai-chat", "anthropic"}, {"openai-responses", "anthropic"}} {
		if got := requestShapeFilterReason(target, req, pair[0], pair[1], requestTokenEstimateLogRecord{}, targetRequestShapeFit{}); got != "anthropic-bridge-reasoning-unsupported" {
			t.Fatal(pair, got)
		}
		if targetSupportsToolsForCallerDialect(target, pair[0], pair[1]) {
			t.Fatal("widened tool admission", pair)
		}
	}
	if got := requestShapeFilterReason(target, req, "anthropic", "anthropic", requestTokenEstimateLogRecord{}, targetRequestShapeFit{}); got == "anthropic-bridge-reasoning-unsupported" {
		t.Fatal("blocked native reasoning")
	}
}

func TestAnthropicBridgeInterruptedUsage(t *testing.T) {
	raw := anthropicBridgeFixture(t, "anthropic")
	raw = raw[:strings.Index(raw, "event: message_stop")]
	for _, caller := range []string{"openai-chat", "openai-responses"} {
		rr := httptest.NewRecorder()
		result, err := anthropicBridgeProxy(caller)(context.Background(), rr, strings.NewReader(raw), "anthropic", "test", 1<<20, nil)
		if err == nil || !committedStreamError(err) || result.Response.Usage.TotalTokens != 8 {
			t.Fatal(result, err)
		}
		if strings.Contains(rr.Body.String(), "[DONE]") || strings.Contains(rr.Body.String(), "response.completed") {
			t.Fatal("success on truncation")
		}
	}
}

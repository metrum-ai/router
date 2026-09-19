// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestST1OpenAIChatNativeStreamFlushesIncrementally(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i, text := range []string{"one", "two", "three", "four"} {
			fmt.Fprintf(w, "data: {\"id\":\"chat_st1\",\"model\":\"native-chat\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"finish_reason\":null}]}\n\n", text)
			flusher.Flush()
			if i < 3 {
				time.Sleep(60 * time.Millisecond)
			}
		}
		fmt.Fprint(w, "data: {\"id\":\"chat_st1\",\"model\":\"native-chat\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	svc := nativeStreamTestService(t, upstream.URL, "openai-chat", []Target{{Provider: "native", Model: "native-chat"}})
	routerServer := httptest.NewServer(svc.Handler())
	defer routerServer.Close()

	req, err := http.NewRequest(http.MethodPost, routerServer.URL+"/v1/chat/completions", strings.NewReader(`{"model":"native-stream","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	firstDelta := time.Duration(0)
	chunks := 0
	for {
		line, readErr := reader.ReadString('\n')
		if strings.HasPrefix(line, "data: ") && !strings.Contains(line, "[DONE]") {
			chunks++
			if firstDelta == 0 {
				firstDelta = time.Since(start)
			}
		}
		if strings.Contains(line, "[DONE]") || readErr != nil {
			break
		}
	}
	total := time.Since(start)
	if upstreamBody["stream"] != true {
		t.Fatalf("upstream stream=%#v, want true", upstreamBody["stream"])
	}
	if chunks <= 2 {
		t.Fatalf("chunks=%d, want more than 2", chunks)
	}
	if firstDelta == 0 || float64(firstDelta)/float64(total) >= 0.75 {
		t.Fatalf("first delta=%s total=%s ratio=%.2f, want incremental delivery", firstDelta, total, float64(firstDelta)/float64(total))
	}
}

func TestST2AnthropicNativeStreamPreservesToolUseEvents(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, frame := range []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_st2","model":"native-anthropic","usage":{"input_tokens":7,"output_tokens":0}}}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_st2","name":"Bash","input":{}}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"pwd\"}"}}`,
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}`,
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
		} {
			fmt.Fprint(w, frame+"\n\n")
		}
	}))
	defer upstream.Close()

	svc := nativeStreamTestService(t, upstream.URL, "anthropic", []Target{{
		Provider: "native",
		Model:    "native-anthropic",
		ToolSupport: ToolSupport{
			AnthropicMessages: []string{"client_tools"},
		},
	}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"native-stream","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"run pwd"}],"tools":[{"name":"Bash","input_schema":{"type":"object"}}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)

	if upstreamBody["stream"] != true {
		t.Fatalf("upstream stream=%#v, want true", upstreamBody["stream"])
	}
	for _, want := range []string{"event: content_block_delta", `"type":"tool_use"`, `"type":"input_json_delta"`, `\"command\":\"pwd\"`, `"input_tokens":7`, `"output_tokens":3`} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Fatalf("native Anthropic stream missing %q:\n%s", want, rr.Body.String())
		}
	}
}

func TestSTResponsesNativeStreamFlushesIncrementally(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i, text := range []string{"one", "two", "three"} {
			fmt.Fprintf(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\n", text)
			flusher.Flush()
			if i < 2 {
				time.Sleep(60 * time.Millisecond)
			}
		}
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_st\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"native-responses\",\"usage\":{\"input_tokens\":2,\"output_tokens\":3,\"total_tokens\":5}}}\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	svc := nativeStreamTestService(t, upstream.URL, "openai-responses", []Target{{Provider: "native", Model: "native-responses"}})
	routerServer := httptest.NewServer(svc.Handler())
	defer routerServer.Close()

	req, err := http.NewRequest(http.MethodPost, routerServer.URL+"/v1/responses", strings.NewReader(`{"model":"native-stream","stream":true,"input":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	firstDelta := time.Duration(0)
	deltas := 0
	sawCompleted := false
	for {
		line, readErr := reader.ReadString('\n')
		if strings.Contains(line, "response.output_text.delta") {
			deltas++
			if firstDelta == 0 {
				firstDelta = time.Since(start)
			}
		}
		if strings.Contains(line, "response.completed") {
			sawCompleted = true
		}
		if sawCompleted || readErr != nil {
			break
		}
	}
	total := time.Since(start)
	if upstreamBody["stream"] != true {
		t.Fatalf("upstream stream=%#v, want true", upstreamBody["stream"])
	}
	if deltas < 2 {
		t.Fatalf("deltas=%d, want at least 2", deltas)
	}
	if !sawCompleted {
		t.Fatal("missing response.completed")
	}
	if firstDelta == 0 || float64(firstDelta)/float64(total) >= 0.75 {
		t.Fatalf("first delta=%s total=%s ratio=%.2f, want incremental delivery", firstDelta, total, float64(firstDelta)/float64(total))
	}
}

func TestST3ResponsesToChatBridgeRemainsUnary(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Error(err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "chat_bridge", "model": "chat-bridge",
			"choices": []map[string]any{{"message": map[string]any{"content": "ok"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 2, "completion_tokens": 1, "total_tokens": 3},
		})
	}))
	defer upstream.Close()

	target := Target{Provider: "native", Model: "chat-bridge", ResponsesToChat: ResponsesToChatBridge{Enabled: true, Text: true, Streaming: true}}
	svc := nativeStreamTestService(t, upstream.URL, "openai-chat", []Target{target})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"native-stream","stream":true,"input":"hello"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)

	if upstreamBody["stream"] != false {
		t.Fatalf("bridge upstream stream=%#v, want false", upstreamBody["stream"])
	}
	if !strings.Contains(rr.Body.String(), "response.created") || !strings.Contains(rr.Body.String(), "response.completed") {
		t.Fatalf("bridge did not synthesize Responses SSE: %s", rr.Body.String())
	}
}

func TestST4ClientCancelStopsNativeUpstream(t *testing.T) {
	upstreamCanceled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"chat_cancel\",\"choices\":[{\"delta\":{\"content\":\"first\"},\"finish_reason\":null}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(upstreamCanceled)
	}))
	defer upstream.Close()

	svc := nativeStreamTestService(t, upstream.URL, "openai-chat", []Target{{Provider: "native", Model: "native-chat"}})
	routerServer := httptest.NewServer(svc.Handler())
	defer routerServer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, routerServer.URL+"/v1/chat/completions", strings.NewReader(`{"model":"native-stream","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if strings.HasPrefix(line, "data:") || err != nil {
			break
		}
	}
	cancel()
	_ = resp.Body.Close()
	select {
	case <-upstreamCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream context was not canceled")
	}
}

func TestST5CommittedToolChunkStopsFallback(t *testing.T) {
	var fallbackCalls atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"chat_tool","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"shell","arguments":"{}"}}]},"finish_reason":null}]}`+"\n\n")
		w.(http.Flusher).Flush()
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls.Add(1)
	}))
	defer fallback.Close()

	cfg := testConfig(t, primary.URL, "provider-key", t.TempDir())
	cfg.Provider["primary"] = ProviderConfig{BaseURL: primary.URL, Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Provider["fallback"] = ProviderConfig{BaseURL: fallback.URL, Dialect: "openai-chat", APIKey: "provider-key"}
	toolSupport := ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}
	cfg.Models["native-stream"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "primary", Model: "primary", ToolSupport: toolSupport},
		{Provider: "fallback", Model: "fallback", ToolSupport: toolSupport},
	}}
	cfg.Callers[0].Allow = []string{"native-stream"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"native-stream","stream":true,"messages":[{"role":"user","content":"run"}],"tools":[{"type":"function","function":{"name":"shell","parameters":{"type":"object"}}}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if fallbackCalls.Load() != 0 {
		t.Fatalf("fallback calls=%d, want zero after committed tool chunk", fallbackCalls.Load())
	}
	if strings.Count(rr.Body.String(), `"id":"chat_tool"`) != 1 {
		t.Fatalf("tool stream was replayed or lost: %s", rr.Body.String())
	}
}

func TestST7TruncatedStreamSettlesEstimatedUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"id\":\"chat_trunc\",\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n")
		flusher.Flush()
		// End without terminal event after committing output.
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Provider["native"] = ProviderConfig{BaseURL: upstream.URL, Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["native-stream"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "native", Model: "native-chat"}}}
	cfg.Callers[0].Allow = []string{"native-stream"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"native-stream","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)

	svc.quota.mu.Lock()
	lifetime := svc.quota.state.Callers["alice"].LifetimeTokens
	unhealthy := svc.quota.state.PersistenceUnhealthy
	svc.quota.mu.Unlock()
	if lifetime <= 0 {
		t.Fatalf("truncated stream must settle nonzero usage liability, got %d", lifetime)
	}
	if unhealthy {
		t.Fatal("successful settlement should leave persistence healthy")
	}
}

func TestST6OpenAIChatIncludeUsageFinalChunk(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"chat_usage\",\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"chat_usage\",\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	svc := nativeStreamTestService(t, upstream.URL, "openai-chat", []Target{{Provider: "native", Model: "native-chat"}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"native-stream","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)

	options, _ := upstreamBody["stream_options"].(map[string]any)
	if options["include_usage"] != true {
		t.Fatalf("upstream stream_options=%#v", upstreamBody["stream_options"])
	}
	usageAt := strings.Index(rr.Body.String(), `"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}`)
	doneAt := strings.Index(rr.Body.String(), "[DONE]")
	if usageAt < 0 || doneAt < 0 || usageAt > doneAt {
		t.Fatalf("usage final chunk missing or out of order: %s", rr.Body.String())
	}
}

func TestOpenAIChatTerminalFinishReasonWithoutDonePersistsAccounting(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"chat_terminal\",\"model\":\"native-chat\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"chat_terminal\",\"model\":\"native-chat\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"chat_terminal\",\"model\":\"native-chat\",\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}}\n\n")
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Provider["native"] = ProviderConfig{BaseURL: upstream.URL, Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["native-stream"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "native", Model: "native-chat"}}}
	cfg.Callers[0].Allow = []string{"native-stream"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"native-stream","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"finish_reason":"stop"`) {
		t.Fatalf("terminal stream status=%d body=%s", rr.Code, rr.Body.String())
	}
	var usage usageRecord
	if err := svc.usage.db.First(&usage).Error; err != nil {
		t.Fatal(err)
	}
	if usage.Status != http.StatusOK || usage.Error != "" || usage.InputTokens != 4 || usage.OutputTokens != 2 || usage.TotalTokens != 6 || usage.Attempts != 1 || usage.FallbackUsed {
		t.Fatalf("terminal stream accounting=%#v", usage)
	}
	svc.quota.mu.Lock()
	lifetimeTokens := svc.quota.state.Callers["alice"].LifetimeTokens
	svc.quota.mu.Unlock()
	if lifetimeTokens != 6 {
		t.Fatalf("quota lifetime tokens=%d, want 6", lifetimeTokens)
	}
}

func TestNativeStreamPIIRestoreModePreservesPlaceholders(t *testing.T) {
	cfg := testConfig(t, "http://localhost", "provider-key", t.TempDir())
	group := cfg.Models["default"]
	group.PIIFilter = testPIIFilterConfig("redact_and_restore")
	cfg.Models["default"] = group
	if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "F-011") {
		t.Fatalf("expected F-011, got %v", err)
	}
}

func nativeStreamTestService(t *testing.T, upstreamURL, dialect string, targets []Target) *Service {
	t.Helper()
	cfg := testConfig(t, upstreamURL, "provider-key", t.TempDir())
	cfg.Provider["native"] = ProviderConfig{BaseURL: upstreamURL, Dialect: dialect, APIKey: "provider-key"}
	cfg.Models["native-stream"] = ModelGroup{Strategy: "static", Targets: targets}
	cfg.Callers[0].Allow = []string{"native-stream"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc
}

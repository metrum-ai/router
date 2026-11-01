// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Opt in with an absolute local Pi binary. Both router and upstream bind only
// loopback; the child gets an isolated home and no provider credentials.
// This is a Harbor-inspired control, not a replay of the missing incident JSON.
func TestPiResponsesRealAgentLocalReadContinuation(t *testing.T) {
	pi := os.Getenv("ROUTER_TEST_PI_BINARY")
	if pi == "" {
		t.Skip("set ROUTER_TEST_PI_BINARY to run the isolated local Pi control")
	}
	if !filepath.IsAbs(pi) {
		t.Fatal("ROUTER_TEST_PI_BINARY must be absolute")
	}
	var calls atomic.Int32
	upstreamRequests := make(chan map[string]any, 4)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		upstreamRequests <- request
		n := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			fmt.Fprint(w, "data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_harbor_read\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\\\"quote.txt\\\"}\"}}]},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
		} else if n == 2 {
			fmt.Fprint(w, "data: {\"id\":\"chat_2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"LOCAL-CONTINUED\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chat_2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		} else {
			t.Error("unexpected additional upstream request")
		}
	}))
	defer up.Close()
	target := anthropicToolTarget()
	target.ResponsesToChat = ResponsesToChatBridge{Enabled: true, Text: true, FunctionTools: true, ToolChoice: true, Streaming: true, ValidationStatus: "passed"}
	svc := nativeStreamTestService(t, up.URL, "openai-chat", []Target{target})
	var mu sync.Mutex
	var clientRequests []map[string]any
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/responses" {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var req map[string]any
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			clientRequests = append(clientRequests, req)
			mu.Unlock()
		}
		svc.Handler().ServeHTTP(w, r)
	}))
	defer router.Close()
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "agent")
	if err := os.Mkdir(agentDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "quote.txt"), []byte("HARBOR-LOCAL-READ-7842"), 0600); err != nil {
		t.Fatal(err)
	}
	models := map[string]any{"providers": map[string]any{"router-test": map[string]any{"baseUrl": router.URL + "/v1", "api": "openai-responses", "apiKey": testToken, "models": []any{map[string]any{"id": "native-stream", "name": "local", "reasoning": false, "input": []string{"text"}, "contextWindow": 16384, "maxTokens": 512, "cost": map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}}}}}}
	raw, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, pi, "--provider", "router-test", "--model", "native-stream", "--mode", "json", "--tools", "read", "--no-extensions", "--no-mcp", "--no-context-files", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-session", "--no-approve", "--offline", "Read quote.txt then finish.")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "PI_CODING_AGENT_DIR=" + agentDir}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Pi failed: %v output=%s", err, output)
	}
	if !bytes.Contains(output, []byte("LOCAL-CONTINUED")) || !bytes.Contains(output, []byte("tool_execution_end")) {
		t.Fatalf("missing successful continuation; upstream calls=%d: %s", calls.Load(), output)
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls=%d output=%s", calls.Load(), output)
	}
	first := <-upstreamRequests
	second := <-upstreamRequests
	if first["store"] != false || second["store"] != false {
		t.Fatalf("explicit no-store lost in Pi requests: first=%#v second=%#v", first["store"], second["store"])
	}
	messages := valueAsSlice(second["messages"])
	found := false
	for _, value := range messages {
		m, _ := value.(map[string]any)
		if m["role"] == "tool" && m["tool_call_id"] == "call_harbor_read" && strings.Contains(stringValue(m["content"]), "HARBOR-LOCAL-READ-7842") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing local file result and call ID in upstream continuation: %#v", messages)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(clientRequests) != 2 {
		t.Fatalf("Pi Responses requests=%d", len(clientRequests))
	}
	input := valueAsSlice(clientRequests[1]["input"])
	found = false
	for _, value := range input {
		m, _ := value.(map[string]any)
		if m["type"] == "function_call_output" && m["call_id"] == "call_harbor_read" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Pi continuation missing function_call_output")
	}
}

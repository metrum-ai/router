// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type capture627Writer struct {
	http.ResponseWriter
	body bytes.Buffer
}

func (w *capture627Writer) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}
func (w *capture627Writer) Flush() { w.ResponseWriter.(http.Flusher).Flush() }

// A deterministic localhost control. It is not a replay of the unavailable
// live upstream SSE and cannot establish the cause of the intermittent event.
func TestChatAnthropic627PiPairedStreams(t *testing.T) {
	pi := os.Getenv("ROUTER_TEST_PI_BINARY")
	if pi == "" {
		t.Skip("set ROUTER_TEST_PI_BINARY for isolated Pi control")
	}
	if !filepath.IsAbs(pi) {
		t.Fatal("Pi path must be absolute")
	}
	first := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":5,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_local_1","name":"read","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"quote.txt\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}

`
	second := `event: message_start
data: {"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":6,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"E2E-TOOL-"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"RESULT-7842"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":3}}

event: message_stop
data: {"type":"message_stop"}

`
	var mu sync.Mutex
	var upstreamBodies []map[string]any
	var upstreamSSE, downstreamSSE []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		upstreamBodies = append(upstreamBodies, b)
		n := len(upstreamBodies)
		sse := first
		if n == 2 {
			sse = second
		}
		upstreamSSE = append(upstreamSSE, sse)
		mu.Unlock()
		if n > 2 {
			t.Error("unexpected extra upstream request")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		// Exercise boundaries inside event names, JSON tokens and text deltas.
		for off := 0; off < len(sse); {
			end := off + 7
			if end > len(sse) {
				end = len(sse)
			}
			_, _ = io.WriteString(w, sse[off:end])
			w.(http.Flusher).Flush()
			off = end
		}
	}))
	defer up.Close()
	target := anthropicToolTarget()
	svc := nativeStreamTestService(t, up.URL, "anthropic", []Target{target})
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cw := &capture627Writer{ResponseWriter: w}
		svc.Handler().ServeHTTP(cw, r)
		mu.Lock()
		downstreamSSE = append(downstreamSSE, cw.body.String())
		mu.Unlock()
	}))
	defer router.Close()
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "agent")
	if err := os.Mkdir(agentDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "quote.txt"), []byte("E2E-TOOL-RESULT-7842\n"), 0600); err != nil {
		t.Fatal(err)
	}
	models := map[string]any{"providers": map[string]any{"router-test": map[string]any{"baseUrl": router.URL + "/v1", "api": "openai-completions", "apiKey": testToken, "models": []any{map[string]any{"id": "native-stream", "name": "local", "reasoning": false, "input": []string{"text"}, "contextWindow": 16384, "maxTokens": 512, "cost": map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}}}}}}
	raw, _ := json.Marshal(models)
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, pi, "--provider", "router-test", "--model", "native-stream", "--mode", "json", "--tools", "read", "--no-extensions", "--no-mcp", "--no-context-files", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-session", "--no-approve", "--offline", "Read quote.txt then quote its exact contents.")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "PI_CODING_AGENT_DIR=" + agentDir}
	output, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("tool_execution_end")) || !bytes.Contains(output, []byte("E2E-TOOL-RESULT-7842")) {
		t.Fatalf("Pi continuation failed: %v output=%s", err, output)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(upstreamBodies) != 2 || len(upstreamSSE) != 2 || len(downstreamSSE) != 2 {
		t.Fatalf("capture count upstream=%d stream=%d downstream=%d", len(upstreamBodies), len(upstreamSSE), len(downstreamSSE))
	}
	var toolUse, toolResult map[string]any
	for _, value := range valueAsSlice(upstreamBodies[1]["messages"]) {
		message, _ := value.(map[string]any)
		for _, value := range valueAsSlice(message["content"]) {
			block, _ := value.(map[string]any)
			switch block["type"] {
			case "tool_use":
				toolUse = block
			case "tool_result":
				toolResult = block
			}
		}
	}
	if toolUse == nil || toolResult == nil || toolUse["id"] != "toolu_local_1" || toolResult["tool_use_id"] != "toolu_local_1" {
		t.Fatalf("translated tool identifiers missing or mismatched")
	}
	toolInput, _ := toolUse["input"].(map[string]any)
	resultBlocks := valueAsSlice(toolResult["content"])
	if toolInput["path"] != "quote.txt" || len(resultBlocks) != 1 || resultBlocks[0].(map[string]any)["text"] != "E2E-TOOL-RESULT-7842\n" {
		t.Fatalf("translated tool input/result missing or reordered")
	}
	var upText, downText strings.Builder
	for _, line := range strings.Split(upstreamSSE[1], "\n") {
		if strings.HasPrefix(line, "data:") {
			var e map[string]any
			if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &e) == nil {
				if d, ok := e["delta"].(map[string]any); ok && d["type"] == "text_delta" {
					upText.WriteString(stringValue(d["text"]))
				}
			}
		}
	}
	for _, line := range strings.Split(downstreamSSE[1], "\n") {
		if strings.HasPrefix(line, "data:") {
			var e map[string]any
			if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &e) == nil {
				for _, v := range valueAsSlice(e["choices"]) {
					if c, ok := v.(map[string]any); ok {
						if d, ok := c["delta"].(map[string]any); ok {
							downText.WriteString(stringValue(d["content"]))
						}
					}
				}
			}
		}
	}
	if upText.String() != "E2E-TOOL-RESULT-7842" || downText.String() != upText.String() {
		t.Fatalf("upstream/downstream divergence: upstream=%q downstream=%q", upText.String(), downText.String())
	}
	// Both streams contain synthetic test data only; save paired raw SSE in the
	// verbose test evidence, not user content from real provider traffic.
	t.Logf("synthetic upstream continuation SSE:\n%s\nsynthetic downstream continuation SSE:\n%s", upstreamSSE[1], downstreamSSE[1])
}

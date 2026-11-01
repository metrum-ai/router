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
	"testing"
	"time"
)

// Real Pi and a scripted Anthropic provider use localhost only. This exercises
// the actual developer-role request and tool continuation, not a live provider.
func TestPiReasoningDeveloper642LocalToolContinuation(t *testing.T) {
	pi := os.Getenv("ROUTER_TEST_PI_BINARY")
	if pi == "" {
		t.Skip("set ROUTER_TEST_PI_BINARY for isolated local Pi")
	}
	if !filepath.IsAbs(pi) {
		t.Fatal("Pi binary must be absolute")
	}
	var mu sync.Mutex
	var upstream []map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		upstream = append(upstream, body)
		attempt := len(upstream)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_up\",\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"visible thought\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"private_signature\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		if attempt == 1 {
			fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_642\",\"name\":\"write\",\"input\":{}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"quote.txt\\\",\\\"content\\\":\\\"642-LOCAL-FILE\\\"}\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":2}}\n\n")
		} else {
			fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"LOCAL-CONTINUED\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n")
		}
		fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer up.Close()
	target := anthropicToolTarget()
	target.DefaultThinking = map[string]any{"type": "adaptive"}
	target.Reasoning = ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlTokenBudget}
	svc := nativeStreamTestService(t, up.URL, "anthropic", []Target{target})
	var client []map[string]any
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/responses" {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			var body map[string]any
			if err = json.Unmarshal(raw, &body); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			client = append(client, body)
			mu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		svc.Handler().ServeHTTP(w, r)
	}))
	defer router.Close()
	dir := t.TempDir()
	agent := filepath.Join(dir, "agent")
	if err := os.Mkdir(agent, 0700); err != nil {
		t.Fatal(err)
	}
	models := map[string]any{"providers": map[string]any{"router-test": map[string]any{"baseUrl": router.URL + "/v1", "api": "openai-responses", "apiKey": testToken, "models": []any{map[string]any{"id": "native-stream", "name": "local", "reasoning": true, "input": []string{"text"}, "contextWindow": 16384, "maxTokens": 2048, "cost": map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}}}}}}
	encoded, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(agent, "models.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, pi, "--provider", "router-test", "--model", "native-stream", "--mode", "json", "--tools", "write", "--no-extensions", "--no-mcp", "--no-context-files", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-session", "--no-approve", "--offline", "--thinking", "low", "Write 642-LOCAL-FILE to quote.txt then finish.")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "PI_CODING_AGENT_DIR=" + agent}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Pi failed: %v output=%s", err, output)
	}
	if !bytes.Contains(output, []byte("LOCAL-CONTINUED")) || !bytes.Contains(output, []byte("tool_execution_end")) {
		t.Fatalf("continuation missing: %s", output)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(client) != 2 || len(upstream) != 2 {
		t.Fatalf("client=%d upstream=%d output=%s", len(client), len(upstream), output)
	}
	for i, req := range client {
		include := valueAsSlice(req["include"])
		input := valueAsSlice(req["input"])
		if len(include) != 1 || include[0] != "reasoning.encrypted_content" || len(input) == 0 {
			t.Fatal("Pi captured shape missing include or input")
		}
		first, ok := input[0].(map[string]any)
		if !ok || first["role"] != "developer" || upstream[i]["system"] != first["content"] {
			t.Fatal("Pi developer prompt was not preserved in Anthropic system")
		}
	}
	for _, body := range upstream {
		if _, ok := body["include"]; ok {
			t.Fatal("include forwarded to Anthropic")
		}
		if strings.Contains(fmt.Sprint(body), "private_signature") {
			t.Fatal("foreign signature replayed")
		}
	}
	file, err := os.ReadFile(filepath.Join(dir, "quote.txt"))
	if err != nil || string(file) != "642-LOCAL-FILE" {
		t.Fatalf("local file effect=%q err=%v", file, err)
	}
	if !strings.Contains(fmt.Sprint(upstream[1]), "Successfully wrote") {
		t.Fatalf("write result missing: %#v", upstream[1])
	}
}

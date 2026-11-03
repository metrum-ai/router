// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

// Claude Code turn-2 payload with tool_use, tool_result, thinking+signature, and cache_control.
const anthropicFidelityTurn2Raw = `{
	"model": "claude-tools-smoke",
	"max_tokens": 256,
	"stream": true,
	"messages": [
		{"role": "user", "content": "write solver"},
		{
			"role": "assistant",
			"content": [
				{
					"type": "thinking",
					"thinking": "Plan the Bash call.",
					"signature": "sig_abc123"
				},
				{
					"type": "tool_use",
					"id": "toolu_1",
					"name": "Bash",
					"input": {"command": "cat > /app/solver.py"},
					"cache_control": {"type": "ephemeral"}
				}
			]
		},
		{
			"role": "user",
			"content": [
				{
					"type": "tool_result",
					"tool_use_id": "toolu_1",
					"content": "wrote solver.py"
				},
				{"type": "text", "text": "continue"}
			]
		}
	],
	"tools": [{"name": "Bash", "description": "run shell", "input_schema": {"type": "object"}}]
}`

func TestAnthropicPassthroughPreservesClaudeTurn2Fidelity(t *testing.T) {
	// AF-1..AF-4: decode + encodeAnthropicPassthrough preserves tool_use,
	// tool_result+tool_use_id, thinking+signature, and cache_control ephemeral.
	req, err := decodeRequest("anthropic", []byte(anthropicFidelityTurn2Raw), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeAnthropicPassthrough("claude-upstream", req, Target{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "claude-upstream" {
		t.Fatalf("model=%#v, want claude-upstream", body["model"])
	}
	if body["stream"] != false {
		t.Fatalf("stream=%#v, want false", body["stream"])
	}
	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) != 3 {
		t.Fatalf("messages=%#v, want 3 preserved turns", body["messages"])
	}

	assistant := msgs[1].(map[string]any)
	assistantContent, ok := assistant["content"].([]any)
	if !ok || len(assistantContent) != 2 {
		t.Fatalf("assistant content=%#v", assistant["content"])
	}

	// AF-3: thinking + signature
	thinking := assistantContent[0].(map[string]any)
	if thinking["type"] != "thinking" || thinking["thinking"] != "Plan the Bash call." || thinking["signature"] != "sig_abc123" {
		t.Fatalf("thinking block lost: %#v", thinking)
	}

	// AF-1 + AF-4: tool_use + cache_control ephemeral
	toolUse := assistantContent[1].(map[string]any)
	if toolUse["type"] != "tool_use" || toolUse["id"] != "toolu_1" || toolUse["name"] != "Bash" {
		t.Fatalf("tool_use block lost: %#v", toolUse)
	}
	cacheControl, ok := toolUse["cache_control"].(map[string]any)
	if !ok || cacheControl["type"] != "ephemeral" {
		t.Fatalf("cache_control ephemeral lost: %#v", toolUse["cache_control"])
	}
	input, ok := toolUse["input"].(map[string]any)
	if !ok || input["command"] != "cat > /app/solver.py" {
		t.Fatalf("tool_use input lost: %#v", toolUse["input"])
	}

	// AF-2: tool_result + tool_use_id
	userFollow := msgs[2].(map[string]any)
	userContent, ok := userFollow["content"].([]any)
	if !ok || len(userContent) != 2 {
		t.Fatalf("user follow-up content=%#v", userFollow["content"])
	}
	toolResult := userContent[0].(map[string]any)
	if toolResult["type"] != "tool_result" || toolResult["tool_use_id"] != "toolu_1" || toolResult["content"] != "wrote solver.py" {
		t.Fatalf("tool_result block lost: %#v", toolResult)
	}
}

func TestAnthropicPassthroughPreservesImageAndText(t *testing.T) {
	// AF-5: image+text still works through Anthropic passthrough.
	req, err := decodeRequest("anthropic", []byte(`{
		"model": "vision",
		"max_tokens": 512,
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "Read the receipt."},
				{"type": "image", "source": {"type": "url", "url": "https://cdn.learnopencv.com/wp-content/uploads/2018/06/04100007/receipt.png"}}
			]
		}]
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeAnthropicPassthrough("vision-upstream", req, Target{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	msgs := body["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content parts=%d, want text+image", len(content))
	}
	if content[0].(map[string]any)["type"] != "text" {
		t.Fatalf("text part lost: %#v", content[0])
	}
	image := content[1].(map[string]any)
	if image["type"] != "image" {
		t.Fatalf("image part lost: %#v", image)
	}
	source := image["source"].(map[string]any)
	if source["type"] != "url" || source["url"] == "" {
		t.Fatalf("image source lost: %#v", source)
	}
}

func TestAnthropicPassthroughPreservesFirstTurnTools(t *testing.T) {
	// AF-6: first-turn tools survive encodeAnthropicPassthrough.
	// SSE tool_use streaming remains covered by TestAnthropicToolPassthroughPreservesToolsAndStreamsToolUse.
	req, err := decodeRequest("anthropic", []byte(`{
		"model": "claude-tools-smoke",
		"max_tokens": 128,
		"stream": true,
		"messages": [{"role": "user", "content": "list files"}],
		"tools": [{"name": "Bash", "description": "run shell", "input_schema": {"type": "object", "properties": {"command": {"type": "string"}}}}],
		"tool_choice": {"type": "auto"}
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeAnthropicPassthrough("claude-upstream", req, Target{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools=%#v, want one preserved tool", body["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "Bash" {
		t.Fatalf("tool name=%#v, want Bash", tool["name"])
	}
	schema, ok := tool["input_schema"].(map[string]any)
	if !ok || schema["type"] != "object" {
		t.Fatalf("input_schema lost: %#v", tool["input_schema"])
	}
	if body["stream"] != false {
		t.Fatalf("stream=%#v, want false for unary upstream", body["stream"])
	}
	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("messages=%#v, want single user turn", body["messages"])
	}
}

func TestAnthropicPassthroughInjectsDefaultThinkingWhenRawLacksIt(t *testing.T) {
	// AF-7: thinking default injection still works when Raw lacks thinking.
	req, err := decodeRequest("anthropic", []byte(`{
		"model": "claude-tools-smoke",
		"max_tokens": 128,
		"messages": [{"role": "user", "content": "hi"}],
		"tool_choice": {"type": "tool", "name": "Bash"}
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeAnthropicPassthrough("claude-upstream", req, Target{
		DefaultThinking: map[string]any{"type": "enabled", "budget_tokens": 512},
	})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	thinking, ok := body["thinking"].(map[string]any)
	if !ok || thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(512) {
		t.Fatalf("default thinking not injected: %#v", body["thinking"])
	}
	if _, present := body["tool_choice"]; present {
		t.Fatalf("forced tool_choice should be removed when default thinking is injected: %#v", body["tool_choice"])
	}
}

func TestTranslatedEncodeUpstreamForwardsStop(t *testing.T) {
	// AF-8: openai-chat translated encodeUpstream sets stop from req.Stop.
	req, err := decodeRequest("openai-chat", []byte(`{
		"model": "default",
		"stop": ["###", "END"],
		"messages": [{"role": "user", "content": "hi"}]
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(req.Stop, []string{"###", "END"}) {
		t.Fatalf("Stop=%#v, want [### END]", req.Stop)
	}
	raw, err := encodeUpstream("openai-chat", "chat-model", req)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	got, ok := body["stop"].([]any)
	if !ok || len(got) != 2 || got[0] != "###" || got[1] != "END" {
		t.Fatalf("stop=%#v, want [### END]", body["stop"])
	}
}

func TestTranslatedEncodeUpstreamForwardsStopSequences(t *testing.T) {
	// AF-9: anthropic translated encodeUpstream sets stop_sequences from req.Stop.
	req, err := decodeRequest("anthropic", []byte(`{
		"model": "default",
		"max_tokens": 32,
		"stop_sequences": ["END", "STOP"],
		"messages": [{"role": "user", "content": "hi"}]
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(req.Stop, []string{"END", "STOP"}) {
		t.Fatalf("Stop=%#v, want [END STOP]", req.Stop)
	}
	raw, err := encodeUpstream("anthropic", "anthropic-model", req)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	got, ok := body["stop_sequences"].([]any)
	if !ok || len(got) != 2 || got[0] != "END" || got[1] != "STOP" {
		t.Fatalf("stop_sequences=%#v, want [END STOP]", body["stop_sequences"])
	}
}

func TestDecodeStopAcceptsStringAndArray(t *testing.T) {
	chatString, err := decodeRequest("openai-chat", []byte(`{
		"model": "default",
		"stop": "END",
		"messages": [{"role": "user", "content": "hi"}]
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(chatString.Stop, []string{"END"}) {
		t.Fatalf("chat string stop=%#v", chatString.Stop)
	}

	responses, err := decodeRequest("openai-responses", []byte(`{
		"model": "default",
		"stop": ["A", "B"],
		"input": "hi"
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(responses.Stop, []string{"A", "B"}) {
		t.Fatalf("responses stop=%#v", responses.Stop)
	}
}

func TestChatAndResponsesPassthroughForwardRawStop(t *testing.T) {
	// AF-10: Chat/Responses passthrough still forwards Raw stop.
	chatReq, err := decodeRequest("openai-chat", []byte(`{
		"model": "default",
		"stop": ["###"],
		"messages": [{"role": "user", "content": "hi"}]
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	chatRaw, err := encodeChatPassthrough("chat-upstream", chatReq, Target{})
	if err != nil {
		t.Fatal(err)
	}
	var chatBody map[string]any
	if err := json.Unmarshal(chatRaw, &chatBody); err != nil {
		t.Fatal(err)
	}
	chatStop, ok := chatBody["stop"].([]any)
	if !ok || len(chatStop) != 1 || chatStop[0] != "###" {
		t.Fatalf("chat passthrough stop=%#v", chatBody["stop"])
	}

	responsesReq, err := decodeRequest("openai-responses", []byte(`{
		"model": "default",
		"stop": ["END"],
		"input": "hi"
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	responsesRaw, err := encodeResponsesPassthrough("responses-upstream", responsesReq, Target{})
	if err != nil {
		t.Fatal(err)
	}
	var responsesBody map[string]any
	if err := json.Unmarshal(responsesRaw, &responsesBody); err != nil {
		t.Fatal(err)
	}
	responsesStop, ok := responsesBody["stop"].([]any)
	if !ok || len(responsesStop) != 1 || responsesStop[0] != "END" {
		t.Fatalf("responses passthrough stop=%#v", responsesBody["stop"])
	}
}

func TestCacheKeyHashesDecodedStop(t *testing.T) {
	// AF-11: cache key still hashes req.Stop when stop is decoded.
	withStop, err := decodeRequest("openai-chat", []byte(`{
		"model": "default",
		"temperature": 0,
		"stop": ["END"],
		"messages": [{"role": "user", "content": "hi"}]
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	withoutStop, err := decodeRequest("openai-chat", []byte(`{
		"model": "default",
		"temperature": 0,
		"messages": [{"role": "user", "content": "hi"}]
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(withStop.Stop, []string{"END"}) {
		t.Fatalf("decoded Stop=%#v, want [END]", withStop.Stop)
	}
	if len(withoutStop.Stop) != 0 {
		t.Fatalf("decoded Stop without field=%#v, want empty", withoutStop.Stop)
	}
	target := Target{Provider: "mock", Model: "chat-model"}
	withKey := cacheKey(withStop, target, "caller-a", "proj", "")
	withoutKey := cacheKey(withoutStop, target, "caller-a", "proj", "")
	if withKey == withoutKey {
		t.Fatalf("cache key ignored decoded stop: %s", withKey)
	}

	otherStop, err := decodeRequest("anthropic", []byte(`{
		"model": "default",
		"max_tokens": 32,
		"temperature": 0,
		"stop_sequences": ["STOP"],
		"messages": [{"role": "user", "content": "hi"}]
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	samePromptNoStop, err := decodeRequest("anthropic", []byte(`{
		"model": "default",
		"max_tokens": 32,
		"temperature": 0,
		"messages": [{"role": "user", "content": "hi"}]
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	anthropicTarget := Target{Provider: "anthropic", Model: "claude"}
	if cacheKey(otherStop, anthropicTarget, "caller-a", "proj", "") == cacheKey(samePromptNoStop, anthropicTarget, "caller-a", "proj", "") {
		t.Fatal("anthropic cache key ignored decoded stop_sequences")
	}
}

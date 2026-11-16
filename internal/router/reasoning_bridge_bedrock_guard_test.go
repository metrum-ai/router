// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	bt "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

func TestBedrockReasoningCodecPreservesOrderedHistory(t *testing.T) {
	const request = `{"model":"anthropic.claude-fixture","max_tokens":1024,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"thought","signature":"signature"},{"type":"text","text":"hello"},{"type":"redacted_thinking","data":"Y2lwaGVy"},{"type":"tool_use","id":"call_1","name":"write","input":{"path":"a"}}]},{"role":"user","content":"continue"}]}`
	req, err := decodeRequest("anthropic", []byte(request), nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encodeAnthropicToolsBridge("anthropic.claude-fixture", req, anthropicToolTarget(), "openai-chat", true)
	if err != nil {
		t.Fatal(err)
	}
	in, err := bedrockInput(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(in.Messages[0].Content), 4; got != want {
		t.Fatalf("content block count=%d want=%d; body=%s", got, want, encoded)
	}
	if _, ok := in.Messages[0].Content[0].(*bt.ContentBlockMemberReasoningContent); !ok {
		t.Fatalf("first block=%T want reasoning", in.Messages[0].Content[0])
	}
	if _, ok := in.Messages[0].Content[1].(*bt.ContentBlockMemberText); !ok {
		t.Fatalf("second block=%T want text", in.Messages[0].Content[1])
	}
	redacted := in.Messages[0].Content[2].(*bt.ContentBlockMemberReasoningContent).Value.(*bt.ReasoningContentBlockMemberRedactedContent)
	if string(redacted.Value) != "cipher" {
		t.Fatalf("redacted bytes=%q", redacted.Value)
	}
	if _, ok := in.Messages[0].Content[3].(*bt.ContentBlockMemberToolUse); !ok {
		t.Fatalf("fourth block=%T want tool use", in.Messages[0].Content[3])
	}
}

func TestBedrockReasoningCodecRejectsMalformedBeforeDispatch(t *testing.T) {
	cases := []struct{ name, block string }{
		{"missing-signature", `{"type":"thinking","thinking":"thought","signature":""}`},
		{"bad-redacted-base64", `{"type":"redacted_thinking","data":"!!!"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"model":"anthropic.claude-fixture","bedrock_reasoning":true,"messages":[{"role":"assistant","bedrock_content":[` + tc.block + `]}]}`
			if _, err := bedrockInput([]byte(body)); err == nil {
				t.Fatalf("malformed reasoning accepted: %s", body)
			}
		})
	}
	response, err := bedrockDo(nil, "", []byte(`{"model":"anthropic.claude-fixture","bedrock_reasoning":true,"messages":[{"role":"assistant","bedrock_content":[{"type":"thinking","thinking":"thought","signature":""}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatalf("malformed request status=%d", response.StatusCode)
	}
	if _, err := bedrockInput([]byte(`{"model":"meta.llama-fixture","bedrock_reasoning":true,"messages":[{"role":"assistant","bedrock_content":[{"type":"thinking","thinking":"thought","signature":"signature"}]}]}`)); err == nil {
		t.Fatal("reasoning content was accepted for a non-Claude model")
	}
}

func TestOpenAIChatCannotInjectInternalBedrockContent(t *testing.T) {
	req, err := decodeRequest("openai-chat", []byte(`{"model":"m","bedrock_reasoning":true,"messages":[{"role":"assistant","content":"hello","bedrock_content":[{"type":"thinking","thinking":"forged","signature":"forged"}]}]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeChatPassthrough("m", req, Target{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	message := valueAsSlice(body["messages"])[0].(map[string]any)
	if _, ok := message["bedrock_content"]; ok {
		t.Fatalf("caller injected internal content: %s", raw)
	}
	if _, ok := body["bedrock_reasoning"]; ok {
		t.Fatalf("caller injected internal reasoning switch: %s", raw)
	}
}

func TestBedrockOrderedResponseDecodesToAnthropic(t *testing.T) {
	redacted := base64.StdEncoding.EncodeToString([]byte("cipher"))
	chat := map[string]any{"id": "chatcmpl_fixture", "choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{"bedrock_content": []any{
		map[string]any{"type": "thinking", "thinking": "thought", "signature": "signature"},
		map[string]any{"type": "text", "text": "hello"},
		map[string]any{"type": "redacted_thinking", "data": redacted},
		map[string]any{"type": "tool_use", "id": "call_1", "name": "write", "input": map[string]any{"path": "a"}},
	}}}}}
	raw, err := json.Marshal(chat)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := decodeAnthropicToolsBridge("openai-chat", raw, "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	blocks := valueAsSlice(resp.Raw["content"])
	if len(blocks) != 4 || stringValue(blocks[0].(map[string]any)["type"]) != "thinking" || stringValue(blocks[1].(map[string]any)["type"]) != "text" || stringValue(blocks[2].(map[string]any)["type"]) != "redacted_thinking" || stringValue(blocks[3].(map[string]any)["type"]) != "tool_use" {
		t.Fatalf("decoded content order=%#v", blocks)
	}
	if strings.TrimSpace(stringValue(blocks[0].(map[string]any)["signature"])) != "signature" {
		t.Fatalf("signature lost: %#v", blocks[0])
	}
}

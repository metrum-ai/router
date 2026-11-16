// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	bt "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

func TestBedrockReasoningCodecPreservesOrderedHistory(t *testing.T) {
	const request = `{"model":"anthropic.claude-fixture","max_tokens":1024,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"thought","signature":"signature"},{"type":"text","text":"hello"},{"type":"redacted_thinking","data":"Y2lwaGVy"},{"type":"tool_use","id":"call_1","name":"write","input":{"path":"a"}}]},{"role":"user","content":"continue"}]}`
	req, err := decodeRequest("anthropic", []byte(request), nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encodeAnthropicToolsBridge("anthropic.claude-fixture", req, anthropicToolTarget(), "openai-chat", ProviderConfig{AuthScheme: "bedrock"})
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
	reasoning := in.Messages[0].Content[0].(*bt.ContentBlockMemberReasoningContent).Value.(*bt.ReasoningContentBlockMemberReasoningText).Value
	if !bytes.Equal([]byte(aws.ToString(reasoning.Text)), []byte("thought")) || !bytes.Equal([]byte(aws.ToString(reasoning.Signature)), []byte("signature")) {
		t.Fatalf("reasoning round trip changed bytes: text=%q signature=%q", aws.ToString(reasoning.Text), aws.ToString(reasoning.Signature))
	}
	if _, ok := in.Messages[0].Content[1].(*bt.ContentBlockMemberText); !ok {
		t.Fatalf("second block=%T want text", in.Messages[0].Content[1])
	}
	redacted := in.Messages[0].Content[2].(*bt.ContentBlockMemberReasoningContent).Value.(*bt.ReasoningContentBlockMemberRedactedContent)
	if !bytes.Equal(redacted.Value, []byte("cipher")) {
		t.Fatalf("redacted bytes=%q", redacted.Value)
	}
	if _, ok := in.Messages[0].Content[3].(*bt.ContentBlockMemberToolUse); !ok {
		t.Fatalf("fourth block=%T want tool use", in.Messages[0].Content[3])
	}
}

func TestAnthropicToolsBridgeDoesNotEnableBedrockReasoningForNonClaude(t *testing.T) {
	for _, model := range []string{"mistral.mistral-large-3-675b-instruct", "amazon.nova-pro-v1:0", "meta.llama3-3-70b-instruct-v1:0"} {
		t.Run(model, func(t *testing.T) {
			request := `{"model":"client-model","max_tokens":64,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"thought","signature":"signature"},{"type":"text","text":"working"},{"type":"tool_use","id":"call_1","name":"write","input":{"path":"a"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"done"}]}],"tools":[{"name":"write","input_schema":{"type":"object"}}]}`
			req, err := decodeRequest("anthropic", []byte(request), nil)
			if err != nil {
				t.Fatal(err)
			}
			target := anthropicToolTarget()
			target.Provider = "bedrock-runtime-primary"
			encoded, err := encodeAnthropicToolsBridge(model, req, target, "openai-chat", ProviderConfig{AuthScheme: "bedrock"})
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal(encoded, &body); err != nil {
				t.Fatal(err)
			}
			if body["bedrock_reasoning"] != nil {
				t.Fatalf("non-Claude Bedrock request enabled reasoning codec: %s", encoded)
			}
			messages := valueAsSlice(body["messages"])
			assistant := messages[0].(map[string]any)
			if assistant["bedrock_content"] != nil || len(valueAsSlice(assistant["tool_calls"])) != 1 {
				t.Fatalf("non-Claude tool continuation was not encoded generically: %s", encoded)
			}
		})
	}
}

func TestBedrockOpaqueReasoningGuardOnlyYieldsToClaudeToolsCodec(t *testing.T) {
	cases := []struct {
		name      string
		model     string
		tools     bool
		wantGuard bool
	}{
		{name: "Claude tool codec", model: "us.anthropic.claude-haiku-4-5-20251001-v1:0", tools: true},
		{name: "Claude without tool codec", model: "us.anthropic.claude-haiku-4-5-20251001-v1:0", wantGuard: true},
		{name: "non-Claude Bedrock", model: "mistral.mistral-large-3-675b-instruct", tools: true, wantGuard: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := anthropicToolTarget()
			target.Model = tc.model
			target.Reasoning = ReasoningSupport{Supported: true, Mode: "opt_in", Control: "token_budget", MinBudgetTokens: 1024, MaxBudgetTokens: 1024, BudgetMustBeLessThanMaxTokens: true}
			if !tc.tools {
				target.ToolSupport = ToolSupport{}
			}
			svc := nativeStreamTestService(t, "https://bedrock-runtime.us-east-1.amazonaws.com", "openai-chat", []Target{target}, func(cfg *Config) {
				provider := cfg.Provider["native"]
				provider.AuthScheme = "bedrock"
				cfg.Provider["native"] = provider
			})
			request := `{"model":"client-model","max_tokens":1152,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"thought","signature":"signature"},{"type":"tool_use","id":"call_1","name":"write","input":{"path":"a"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"done"}]}],"tools":[{"name":"write","input_schema":{"type":"object"}}]}`
			req, err := decodeRequest("anthropic", []byte(request), nil)
			if err != nil {
				t.Fatal(err)
			}
			fit := svc.targetRequestShapeFit(target, req, "anthropic", "openai-chat", requestTokenEstimateLogRecord{})
			gotGuard := fit.FilterReason == "bedrock-opaque-reasoning-unsupported"
			if gotGuard != tc.wantGuard {
				t.Fatalf("filter reason=%q want bedrock guard=%v", fit.FilterReason, tc.wantGuard)
			}
		})
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

func TestBedrockInternalContentIsNotReturnedToOpenAIChat(t *testing.T) {
	raw := map[string]any{
		"id": "chat_fixture", "object": "chat.completion", "model": "bedrock-model", "bedrock_reasoning": true,
		"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "answer", "bedrock_content": []any{map[string]any{"type": "thinking", "thinking": "private text", "signature": "private signature"}}}}},
	}
	resp := encodeChatResponse(&IRResponse{Raw: raw, RawResponse: true}, "public-model")
	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "bedrock_content") || strings.Contains(string(encoded), "bedrock_reasoning") || strings.Contains(string(encoded), "private text") || strings.Contains(string(encoded), "private signature") {
		t.Fatalf("internal Bedrock reasoning leaked to OpenAI Chat: %s", encoded)
	}
	if _, ok := raw["bedrock_reasoning"]; !ok || rawChoicesMessage(raw)["bedrock_content"] == nil {
		t.Fatal("response sanitizer mutated cached upstream data")
	}
}

func rawChoicesMessage(raw map[string]any) map[string]any {
	choices := valueAsSlice(raw["choices"])
	return choices[0].(map[string]any)["message"].(map[string]any)
}

func TestBedrockClaudeModelRecognizesRegionalInferenceProfiles(t *testing.T) {
	for _, model := range []string{
		"jp.anthropic.claude-haiku-4-5-20251001-v1:0",
		"au.anthropic.claude-haiku-4-5-20251001-v1:0",
		"us-gov.anthropic.claude-haiku-4-5-20251001-v1:0",
		"global.anthropic.claude-haiku-4-5-20251001-v1:0",
	} {
		if !isBedrockClaudeModel(model) {
			t.Errorf("%q was not recognized as a Bedrock Claude model", model)
		}
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
	if stringValue(blocks[0].(map[string]any)["thinking"]) != "thought" {
		t.Fatalf("thinking text changed: %#v", blocks[0])
	}
	if got := stringValue(blocks[2].(map[string]any)["data"]); got != redacted {
		t.Fatalf("redacted data bytes changed: got %q want %q", got, redacted)
	}
}

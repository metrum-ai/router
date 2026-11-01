// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"testing"
)

const receiptImageURL = "https://cdn.learnopencv.com/wp-content/uploads/2018/06/04100007/receipt.png"

func TestNativeImagePayloadsEncodeForEachDialect(t *testing.T) {
	tests := []struct {
		name    string
		dialect string
		raw     string
		assert  func(t *testing.T, body map[string]any)
	}{
		{
			name:    "openai chat image_url",
			dialect: "openai-chat",
			raw: `{
				"model":"vision",
				"messages":[{
					"role":"user",
					"content":[
						{"type":"text","text":"Read it."},
						{"type":"image_url","image_url":{"url":"` + receiptImageURL + `","detail":"high"}}
					]
				}],
				"max_tokens":512
			}`,
			assert: func(t *testing.T, body map[string]any) {
				t.Helper()
				msgs := body["messages"].([]any)
				parts := msgs[0].(map[string]any)["content"].([]any)
				image := parts[1].(map[string]any)["image_url"].(map[string]any)
				if image["url"] != receiptImageURL || image["detail"] != "high" {
					t.Fatalf("chat image payload=%#v", image)
				}
				if body["max_tokens"] != float64(512) {
					t.Fatalf("chat max_tokens=%#v", body["max_tokens"])
				}
			},
		},
		{
			name:    "openai responses input_image",
			dialect: "openai-responses",
			raw: `{
				"model":"vision",
				"input":[{
					"role":"user",
					"content":[
						{"type":"input_text","text":"Read it."},
						{"type":"input_image","image_url":"` + receiptImageURL + `","detail":"high"}
					]
				}],
				"max_output_tokens":512
			}`,
			assert: func(t *testing.T, body map[string]any) {
				t.Helper()
				input := body["input"].([]any)
				parts := input[0].(map[string]any)["content"].([]any)
				image := parts[1].(map[string]any)
				if image["type"] != "input_image" || image["image_url"] != receiptImageURL || image["detail"] != "high" {
					t.Fatalf("responses image payload=%#v", image)
				}
				if body["max_output_tokens"] != float64(512) {
					t.Fatalf("responses max_output_tokens=%#v", body["max_output_tokens"])
				}
			},
		},
		{
			name:    "anthropic url image",
			dialect: "anthropic",
			raw: `{
				"model":"vision",
				"max_tokens":512,
				"messages":[{
					"role":"user",
					"content":[
						{"type":"text","text":"Read it."},
						{"type":"image","source":{"type":"url","url":"` + receiptImageURL + `"}}
					]
				}]
			}`,
			assert: func(t *testing.T, body map[string]any) {
				t.Helper()
				msgs := body["messages"].([]any)
				parts := msgs[0].(map[string]any)["content"].([]any)
				source := parts[1].(map[string]any)["source"].(map[string]any)
				if source["type"] != "url" || source["url"] != receiptImageURL {
					t.Fatalf("anthropic image source=%#v", source)
				}
				if body["max_tokens"] != float64(512) {
					t.Fatalf("anthropic max_tokens=%#v", body["max_tokens"])
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := decodeRequest(tc.dialect, []byte(tc.raw), http.Header{})
			if err != nil {
				t.Fatal(err)
			}
			if !requestHasImages(req) {
				t.Fatalf("decoded request missing image metadata: %#v", req)
			}
			encoded, err := encodeUpstream(tc.dialect, "vision-upstream", req)
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal(encoded, &body); err != nil {
				t.Fatal(err)
			}
			if body["model"] != "vision-upstream" {
				t.Fatalf("model=%#v, want vision-upstream", body["model"])
			}
			tc.assert(t, body)
		})
	}
}

func TestOpenAIChatImageTranslatesToResponsesAndAnthropic(t *testing.T) {
	req, err := decodeRequest("openai-chat", []byte(`{
		"model": "default",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "Read the receipt."},
				{"type": "image_url", "image_url": {"url": "`+receiptImageURL+`", "detail": "high"}}
			]
		}]
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	if !requestHasImages(req) {
		t.Fatalf("expected image request: %#v", req)
	}

	responsesRaw, err := encodeUpstream("openai-responses", "vision-model", req)
	if err != nil {
		t.Fatal(err)
	}
	var responses map[string]any
	if err := json.Unmarshal(responsesRaw, &responses); err != nil {
		t.Fatal(err)
	}
	msgs := responses["input"].([]any)
	content := msgs[0].(map[string]any)["content"].([]any)
	if got := content[1].(map[string]any)["image_url"]; got != receiptImageURL {
		t.Fatalf("responses image_url=%v", got)
	}

	anthropicRaw, err := encodeUpstream("anthropic", "vision-model", req)
	if err != nil {
		t.Fatal(err)
	}
	var anthropic map[string]any
	if err := json.Unmarshal(anthropicRaw, &anthropic); err != nil {
		t.Fatal(err)
	}
	anthropicMsgs := anthropic["messages"].([]any)
	anthropicContent := anthropicMsgs[0].(map[string]any)["content"].([]any)
	source := anthropicContent[1].(map[string]any)["source"].(map[string]any)
	if source["type"] != "url" || source["url"] != receiptImageURL {
		t.Fatalf("anthropic image source=%#v", source)
	}
}

func TestAnthropicMaxTokensHonorsCallerValue(t *testing.T) {
	req, err := decodeRequest("anthropic", []byte(`{
		"model": "vision",
		"max_tokens": 1,
		"messages": [{"role": "user", "content": "write a long essay"}]
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeUpstream("anthropic", "vision-model", req)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if got := body["max_tokens"]; got != float64(1) {
		t.Fatalf("max_tokens=%#v, want 1", got)
	}
}

func TestAnthropicMaxTokensDefaultsOnlyWhenOmitted(t *testing.T) {
	req, err := decodeRequest("anthropic", []byte(`{
		"model": "vision",
		"messages": [{"role": "user", "content": "write a long essay"}]
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeUpstream("anthropic", "vision-model", req)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if got := body["max_tokens"]; got != float64(1024) {
		t.Fatalf("max_tokens=%#v, want default 1024", got)
	}
}

func TestOpenAIChatMaxCompletionTokensDecodesCanonicalMaxTokens(t *testing.T) {
	req, err := decodeRequest("openai-chat", []byte(`{
		"model": "default",
		"max_completion_tokens": 1,
		"messages": [{"role": "user", "content": "write a long essay"}]
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	if req.MaxTokens != 1 {
		t.Fatalf("MaxTokens=%d, want 1", req.MaxTokens)
	}
	if req.MaxTokensField != "max_completion_tokens" {
		t.Fatalf("MaxTokensField=%q, want max_completion_tokens", req.MaxTokensField)
	}
}

func TestOpenAIChatMaxCompletionTokensForwardsOutputCap(t *testing.T) {
	req, err := decodeRequest("openai-chat", []byte(`{
		"model": "default",
		"max_completion_tokens": 1,
		"messages": [{"role": "user", "content": "write a long essay"}]
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeUpstream("openai-chat", "chat-model", req)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if got := body["max_tokens"]; got != float64(1) {
		t.Fatalf("max_tokens=%#v, want 1; body=%#v", got, body)
	}
	if _, ok := body["max_completion_tokens"]; ok {
		t.Fatalf("max_completion_tokens should be normalized for ordinary OpenAI-compatible targets; body=%#v", body)
	}
}

func TestOpenAIChatMaxTokensTakesPrecedenceOverMaxCompletionTokens(t *testing.T) {
	req, err := decodeRequest("openai-chat", []byte(`{
		"model": "default",
		"max_tokens": 2,
		"max_completion_tokens": 1,
		"messages": [{"role": "user", "content": "write a long essay"}]
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	if req.MaxTokens != 2 {
		t.Fatalf("MaxTokens=%d, want 2", req.MaxTokens)
	}
	if req.MaxTokensField != "max_tokens" {
		t.Fatalf("MaxTokensField=%q, want max_tokens", req.MaxTokensField)
	}
	raw, err := encodeUpstream("openai-chat", "chat-model", req)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if got := body["max_tokens"]; got != float64(2) {
		t.Fatalf("max_tokens=%#v, want 2; body=%#v", got, body)
	}
	if _, ok := body["max_completion_tokens"]; ok {
		t.Fatalf("max_completion_tokens should not be emitted when max_tokens wins; body=%#v", body)
	}
}

func TestResponsesMaxOutputTokensTranslatesToChatMaxTokens(t *testing.T) {
	req, err := decodeRequest("openai-responses", []byte(`{
		"model": "vision",
		"max_output_tokens": 1,
		"input": "write a long essay"
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeUpstream("openai-chat", "vision-model", req)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if got := body["max_tokens"]; got != float64(1) {
		t.Fatalf("max_tokens=%#v, want 1", got)
	}
}

func TestResponsesImageMessageArrayTranslatesToChatAndAnthropicBase64(t *testing.T) {
	dataURL := "data:image/png;base64,aGVsbG8="
	req, err := decodeRequest("openai-responses", []byte(`{
		"model": "default",
		"input": [{
			"role": "user",
			"content": [
				{"type": "input_text", "text": "Read the receipt."},
				{"type": "input_image", "image_url": "`+dataURL+`"}
			]
		}]
	}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	if !requestHasImages(req) || len(req.Messages) != 1 || !hasImageParts(req.Messages[0].Parts) {
		t.Fatalf("responses image input not decoded: %#v", req)
	}

	chatRaw, err := encodeUpstream("openai-chat", "vision-model", req)
	if err != nil {
		t.Fatal(err)
	}
	var chat map[string]any
	if err := json.Unmarshal(chatRaw, &chat); err != nil {
		t.Fatal(err)
	}
	msgs := chat["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].([]any)
	img := content[1].(map[string]any)["image_url"].(map[string]any)
	if img["url"] != dataURL {
		t.Fatalf("chat image url=%#v", img)
	}

	anthropicRaw, err := encodeUpstream("anthropic", "vision-model", req)
	if err != nil {
		t.Fatal(err)
	}
	var anthropic map[string]any
	if err := json.Unmarshal(anthropicRaw, &anthropic); err != nil {
		t.Fatal(err)
	}
	anthropicMsgs := anthropic["messages"].([]any)
	anthropicContent := anthropicMsgs[0].(map[string]any)["content"].([]any)
	source := anthropicContent[1].(map[string]any)["source"].(map[string]any)
	if source["type"] != "base64" || source["media_type"] != "image/png" || source["data"] != "aGVsbG8=" {
		t.Fatalf("anthropic base64 source=%#v", source)
	}
}

func TestEncodeChatPassthroughStoreAndMaxTokensByTargetMetadata(t *testing.T) {
	req := &IRRequest{
		MaxTokens:      16,
		MaxTokensField: "max_tokens",
		Messages:       []IRMessage{{Role: "user", Content: "hi"}},
	}
	defaultBody, err := encodeChatPassthrough("zai/GLM-5.2", req, Target{})
	if err != nil {
		t.Fatal(err)
	}
	var defaultTarget map[string]any
	if err := json.Unmarshal(defaultBody, &defaultTarget); err != nil {
		t.Fatal(err)
	}
	if _, ok := defaultTarget["store"]; ok {
		t.Fatalf("default target body should omit store: %#v", defaultTarget)
	}
	if defaultTarget["max_tokens"] != float64(16) {
		t.Fatalf("default target max_tokens=%#v", defaultTarget["max_tokens"])
	}

	metadataBody, err := encodeChatPassthrough("gpt-5.4-nano", req, Target{
		ForceStoreFalse:  true,
		OutputTokenField: "max_completion_tokens",
	})
	if err != nil {
		t.Fatal(err)
	}
	var metadataTarget map[string]any
	if err := json.Unmarshal(metadataBody, &metadataTarget); err != nil {
		t.Fatal(err)
	}
	if metadataTarget["store"] != false {
		t.Fatalf("metadata target store=%#v, want false", metadataTarget["store"])
	}
	if metadataTarget["max_completion_tokens"] != float64(16) {
		t.Fatalf("metadata target max_completion_tokens=%#v", metadataTarget["max_completion_tokens"])
	}
	if _, ok := metadataTarget["max_tokens"]; ok {
		t.Fatalf("metadata target body should translate max_tokens: %#v", metadataTarget)
	}
	completionReq := &IRRequest{
		MaxTokens:      9,
		MaxTokensField: "max_completion_tokens",
		Messages:       []IRMessage{{Role: "user", Content: "hi"}},
	}
	maxTokensBody, err := encodeChatPassthrough("ordinary-chat", completionReq, Target{OutputTokenField: "max_tokens"})
	if err != nil {
		t.Fatal(err)
	}
	var maxTokensTarget map[string]any
	if err := json.Unmarshal(maxTokensBody, &maxTokensTarget); err != nil {
		t.Fatal(err)
	}
	if maxTokensTarget["max_tokens"] != float64(9) {
		t.Fatalf("max_tokens target max_tokens=%#v", maxTokensTarget["max_tokens"])
	}
	if _, ok := maxTokensTarget["max_completion_tokens"]; ok {
		t.Fatalf("max_tokens target body should translate max_completion_tokens: %#v", maxTokensTarget)
	}

}

func TestEncodeResponsesPassthroughStoreByTargetMetadata(t *testing.T) {
	req := &IRRequest{
		Raw: map[string]any{
			"input": "hi",
			"store": true,
		},
	}
	defaultBody, err := encodeResponsesPassthrough("responses-model", req, Target{})
	if err != nil {
		t.Fatal(err)
	}
	var defaultTarget map[string]any
	if err := json.Unmarshal(defaultBody, &defaultTarget); err != nil {
		t.Fatal(err)
	}
	if defaultTarget["store"] != true {
		t.Fatalf("default target must forward caller store:true: %#v", defaultTarget)
	}

	forcedBody, err := encodeResponsesPassthrough("responses-model", req, Target{ForceStoreFalse: true})
	if err != nil {
		t.Fatal(err)
	}
	var forcedTarget map[string]any
	if err := json.Unmarshal(forcedBody, &forcedTarget); err != nil {
		t.Fatal(err)
	}
	if forcedTarget["store"] != false {
		t.Fatalf("forced target store=%#v, want false", forcedTarget["store"])
	}
}

func TestEncodeUpstreamOpenAIChatAppliesTargetEncodingMetadata(t *testing.T) {
	req := &IRRequest{
		MaxTokens:      16,
		MaxTokensField: "max_tokens",
		Messages:       []IRMessage{{Role: "user", Content: "hi"}},
	}
	raw, err := encodeUpstreamForTarget("openai-chat", "chat-model", req, Target{
		ForceStoreFalse:  true,
		OutputTokenField: "max_completion_tokens",
	})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["store"] != false {
		t.Fatalf("store=%#v, want false", body["store"])
	}
	if body["max_completion_tokens"] != float64(16) {
		t.Fatalf("max_completion_tokens=%#v", body["max_completion_tokens"])
	}
	if _, ok := body["max_tokens"]; ok {
		t.Fatalf("body should translate max_tokens: %#v", body)
	}
}

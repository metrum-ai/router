// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func decodeRequest(dialect string, body []byte, h http.Header) (*IRRequest, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	// json.Unmarshal accepts top-level null into a nil map; treat it as invalid.
	if raw == nil {
		return nil, fmt.Errorf("request body must be a JSON object")
	}
	req := &IRRequest{Raw: raw}
	if v, _ := raw["model"].(string); v != "" {
		req.Model = v
	}
	if stream, ok := raw["stream"].(bool); ok {
		req.Stream = stream
	}
	if maxTokens, ok := numberAsInt(raw["max_tokens"]); ok {
		req.MaxTokens = maxTokens
		req.MaxTokensField = "max_tokens"
	}
	if maxCompletionTokens, ok := numberAsInt(raw["max_completion_tokens"]); ok && dialect == "openai-chat" && req.MaxTokens == 0 {
		req.MaxTokens = maxCompletionTokens
		req.MaxTokensField = "max_completion_tokens"
	}
	if maxCompletionTokens, ok := numberAsInt(raw["max_completion_tokens"]); ok && req.MaxTokens == 0 {
		req.MaxTokens = maxCompletionTokens
	}
	if maxOutputTokens, ok := numberAsInt(raw["max_output_tokens"]); ok && req.MaxTokens == 0 {
		req.MaxTokens = maxOutputTokens
		req.MaxTokensField = "max_output_tokens"
	}
	if temp, ok := numberAsFloat(raw["temperature"]); ok {
		req.Temperature = &temp
	}
	req.Stop = decodeStopSequences(raw)
	if h.Get("Cache-Control") == "no-cache" || strings.Contains(strings.ToLower(h.Get("Cache-Control")), "no-store") {
		req.NoCache = true
	}
	switch dialect {
	case "anthropic":
		req.System = contentToText(raw["system"])
		req.Messages = decodeMessages(raw["messages"])
		if tools, ok := raw["tools"].([]any); ok {
			req.Tools = anySliceToMaps(tools)
		}
		if thinking, ok := raw["thinking"].(map[string]any); ok {
			req.Thinking = thinking
		}
	case "openai-chat":
		msgs := decodeMessages(raw["messages"])
		for _, msg := range msgs {
			if msg.Role == "system" || msg.Role == "developer" {
				if req.System != "" {
					req.System += "\n"
				}
				req.System += msg.Content
			} else {
				req.Messages = append(req.Messages, msg)
			}
		}
		if tools, ok := raw["tools"].([]any); ok {
			req.Tools = anySliceToMaps(tools)
		}
	case "openai-responses":
		req.Input = contentToText(raw["input"])
		req.InputParts = decodeContentParts(raw["input"])
		req.Messages = decodeResponsesMessages(raw["input"])
		if len(req.Messages) == 0 && req.Input != "" {
			req.Messages = []IRMessage{{Role: "user", Content: req.Input, Parts: req.InputParts}}
		}
		if inst, _ := raw["instructions"].(string); inst != "" {
			req.System = inst
		}
		if tools, ok := raw["tools"].([]any); ok {
			req.Tools = anySliceToMaps(tools)
		}
		if reasoning, ok := raw["reasoning"].(map[string]any); ok {
			req.Thinking = reasoning
		}
	default:
		return nil, fmt.Errorf("unknown dialect %s", dialect)
	}
	req.Reasoning = detectReasoningIntent(dialect, raw)
	return req, nil
}

func encodeUpstream(dialect, model string, req *IRRequest) ([]byte, error) {
	return encodeUpstreamForTarget(dialect, model, req, Target{})
}

func encodeUpstreamForTarget(dialect, model string, req *IRRequest, target Target) ([]byte, error) {
	switch dialect {
	case "anthropic":
		msgs := []map[string]any{}
		for _, m := range req.Messages {
			if m.Role == "system" || m.Role == "developer" {
				continue
			}
			msgs = append(msgs, map[string]any{"role": m.Role, "content": encodeAnthropicContent(m)})
		}
		body := map[string]any{"model": model, "messages": msgs, "max_tokens": effectiveMaxTokens(req, 1024), "stream": false}
		if req.System != "" {
			body["system"] = req.System
		}
		if req.Temperature != nil {
			body["temperature"] = *req.Temperature
		}
		if len(req.Stop) > 0 {
			body["stop_sequences"] = req.Stop
		}
		if err := applyReasoningToAnthropic(body, req, target); err != nil {
			return nil, err
		}
		return json.Marshal(body)
	case "openai-responses":
		body := map[string]any{"model": model, "input": encodeResponsesInput(req), "stream": false}
		if req.System != "" {
			body["instructions"] = req.System
		}
		if req.MaxTokens > 0 {
			body["max_output_tokens"] = req.MaxTokens
		}
		if previousResponseID := strings.TrimSpace(stringValue(req.Raw["previous_response_id"])); previousResponseID != "" {
			body["previous_response_id"] = previousResponseID
		}
		if req.Temperature != nil {
			body["temperature"] = *req.Temperature
		}
		if topP, ok := numberAsFloat(req.Raw["top_p"]); ok {
			body["top_p"] = topP
		}
		if parallelToolCalls, ok := req.Raw["parallel_tool_calls"].(bool); ok {
			body["parallel_tool_calls"] = parallelToolCalls
		}
		if target.ForceStoreFalse {
			body["store"] = false
		}
		if err := applyReasoningToOpenAIResponses(body, req, target); err != nil {
			return nil, err
		}
		return json.Marshal(body)
	case "replicate":
		prompt := requestText(req)
		if req.System != "" {
			prompt = req.System + "\n\n" + prompt
		}
		body := map[string]any{
			"input": map[string]any{
				"prompt": prompt,
			},
			"stream": false,
		}
		if req.MaxTokens > 0 {
			body["input"].(map[string]any)["max_tokens"] = req.MaxTokens
		}
		if req.Temperature != nil {
			body["input"].(map[string]any)["temperature"] = *req.Temperature
		}
		return json.Marshal(body)
	case "gemini-generate-content":
		return encodeGeminiGenerateContent(req)
	case "openai-chat":
		msgs := []map[string]any{}
		if req.System != "" {
			msgs = append(msgs, map[string]any{"role": "system", "content": req.System})
		}
		for _, m := range req.Messages {
			msgs = append(msgs, map[string]any{"role": m.Role, "content": encodeOpenAIChatContent(m)})
		}
		if len(msgs) == 0 && req.Input != "" {
			msgs = append(msgs, map[string]any{"role": "user", "content": req.Input})
		}
		body := map[string]any{"model": model, "messages": msgs, "stream": false}
		applyOpenAIChatMaxTokens(body, req, false)
		applyTargetOpenAIChatEncoding(body, target)
		applyDefaultOpenAIChatThinking(body, target)
		if req.Temperature != nil {
			body["temperature"] = *req.Temperature
		}
		if len(req.Stop) > 0 {
			body["stop"] = req.Stop
		}
		if err := applyReasoningToOpenAIChat(body, req, target); err != nil {
			return nil, err
		}
		return json.Marshal(body)
	default:
		return nil, fmt.Errorf("unsupported upstream dialect %q", dialect)
	}
}

func encodeGeminiGenerateContent(req *IRRequest) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("gemini generateContent request is required")
	}
	if !geminiGenerateContentTextEligible(req) {
		return nil, fmt.Errorf("gemini generateContent codec supports non-streaming text requests only")
	}
	contents := make([]map[string]any, 0, len(req.Messages)+1)
	for _, message := range req.Messages {
		role := message.Role
		switch role {
		case "assistant":
			role = "model"
		case "user":
		default:
			return nil, fmt.Errorf("gemini generateContent codec does not support message role %q", message.Role)
		}
		text := message.Content
		if text == "" {
			text = textFromIRParts(message.Parts)
		}
		if text != "" {
			contents = append(contents, map[string]any{"role": role, "parts": []map[string]any{{"text": text}}})
		}
	}
	if len(contents) == 0 && req.Input != "" {
		contents = append(contents, map[string]any{"role": "user", "parts": []map[string]any{{"text": req.Input}}})
	}
	if len(contents) == 0 {
		return nil, fmt.Errorf("gemini generateContent request has no text content")
	}
	body := map[string]any{"contents": contents}
	if req.System != "" {
		body["systemInstruction"] = map[string]any{"parts": []map[string]any{{"text": req.System}}}
	}
	generationConfig := map[string]any{}
	if req.MaxTokens > 0 {
		generationConfig["maxOutputTokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		generationConfig["temperature"] = *req.Temperature
	}
	if len(req.Stop) > 0 {
		generationConfig["stopSequences"] = req.Stop
	}
	if len(generationConfig) > 0 {
		body["generationConfig"] = generationConfig
	}
	return json.Marshal(body)
}

func geminiGenerateContentTextEligible(req *IRRequest) bool {
	if req == nil || req.Stream || len(req.Tools) > 0 || requestHasImages(req) || requestHasStructuredOutput(req) || requestRequiresReasoning(req) {
		return false
	}
	for _, message := range req.Messages {
		if message.Role != "user" && message.Role != "assistant" {
			return false
		}
		for _, part := range message.Parts {
			if part.Type != "" && part.Type != "text" {
				return false
			}
		}
	}
	return true
}

func encodeResponsesPassthrough(model string, req *IRRequest, target Target) ([]byte, error) {
	body := providerPassthroughBody(req)
	body["model"] = model
	if tools, ok := body["tools"]; ok {
		body["tools"] = filterResponsesToolsForUpstream(tools)
	}
	// Same-dialect native streaming keeps stream=true so the upstream emits
	// provider SSE. Unary requests stay false; enableNativeUpstreamStream is a
	// belt-and-suspenders rewrite on the native path.
	body["stream"] = req != nil && req.Stream
	if target.ForceStoreFalse {
		body["store"] = false
	}
	if err := applyReasoningToOpenAIResponses(body, req, target); err != nil {
		return nil, err
	}
	return json.Marshal(body)
}

func encodeChatPassthrough(model string, req *IRRequest, target Target) ([]byte, error) {
	body := providerPassthroughBody(req)
	body["model"] = model
	// The router calls upstreams in unary mode and synthesizes downstream SSE.
	// This keeps tool-call responses and usage accounting deterministic.
	body["stream"] = false
	delete(body, "stream_options")
	applyOpenAIChatMaxTokens(body, req, true)
	applyTargetOpenAIChatEncoding(body, target)
	applyDefaultOpenAIChatThinking(body, target)
	if err := applyReasoningToOpenAIChat(body, req, target); err != nil {
		return nil, err
	}
	return json.Marshal(body)
}

func applyDefaultOpenAIChatThinking(body map[string]any, target Target) {
	if len(target.DefaultOpenAIChatThinking) == 0 {
		return
	}
	if _, present := body["thinking"]; present {
		return
	}
	body["thinking"] = target.DefaultOpenAIChatThinking
}

func encodeAnthropicPassthrough(model string, req *IRRequest, target Target) ([]byte, error) {
	// Preserve caller message blocks (tool_use, tool_result, thinking+signature,
	// cache_control) from Raw. Do not rebuild messages from IR text/parts.
	body := providerPassthroughBody(req)
	body["model"] = model
	body["stream"] = false
	normalizeAnthropicPassthroughImages(body)
	if _, ok := body["max_tokens"]; !ok {
		body["max_tokens"] = effectiveMaxTokens(req, 1024)
	}
	injectedThinking := false
	defaultThinking := target.DefaultThinking
	if len(defaultThinking) > 0 {
		if _, ok := body["thinking"]; !ok {
			body["thinking"] = defaultThinking
			injectedThinking = true
		}
	}
	if injectedThinking {
		if toolChoice, ok := body["tool_choice"].(map[string]any); ok && stringValue(toolChoice["type"]) == "tool" {
			delete(body, "tool_choice")
		}
	}
	if err := applyReasoningToAnthropic(body, req, target); err != nil {
		return nil, err
	}
	return json.Marshal(body)
}

// normalizeAnthropicPassthroughImages rewrites only OpenAI-style image_url blocks
// into Anthropic image/source shape in place. All other content blocks are left alone
// so tool_use, tool_result, thinking, and cache_control survive passthrough.
func normalizeAnthropicPassthroughImages(body map[string]any) {
	messages, ok := body["messages"].([]any)
	if !ok {
		return
	}
	for _, item := range messages {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		content, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for i, part := range content {
			m, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if stringValue(m["type"]) != "image_url" {
				continue
			}
			imagePart := decodeContentPart(m)
			if source := imageSourceForAnthropic(imagePart); source != nil {
				content[i] = map[string]any{"type": "image", "source": source}
			}
		}
	}
}

func decodeStopSequences(raw map[string]any) []string {
	if raw == nil {
		return nil
	}
	for _, key := range []string{"stop", "stop_sequences"} {
		if stops := decodeStringOrStringSlice(raw[key]); len(stops) > 0 {
			return stops
		}
	}
	return nil
}

func decodeStringOrStringSlice(v any) []string {
	switch x := v.(type) {
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		out := make([]string, 0, len(x))
		for _, s := range x {
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func providerPassthroughBody(req *IRRequest) map[string]any {
	body := map[string]any{}
	if req == nil {
		return body
	}
	for key, value := range req.Raw {
		switch strings.ToLower(key) {
		case "store", "metadata":
			continue
		default:
			body[key] = value
		}
	}
	return body
}

func applyReasoningToOpenAIChat(body map[string]any, req *IRRequest, target Target) error {
	if !requestRequiresReasoning(req) {
		return nil
	}
	if !targetCanSatisfyReasoning(target, "openai-chat", req) {
		return fmt.Errorf("target does not support requested reasoning")
	}
	if target.Reasoning.RejectsMaxTokens {
		if value, ok := body["max_tokens"]; ok {
			body["max_completion_tokens"] = value
			delete(body, "max_tokens")
		}
	}
	body["reasoning_effort"] = reasoningEffortForTarget(req.Reasoning, target)
	return nil
}

func applyReasoningToOpenAIResponses(body map[string]any, req *IRRequest, target Target) error {
	if !requestRequiresReasoning(req) {
		return nil
	}
	if !targetCanSatisfyReasoning(target, "openai-responses", req) {
		return fmt.Errorf("target does not support requested reasoning")
	}
	reasoning := map[string]any{"effort": reasoningEffortForTarget(req.Reasoning, target)}
	if req.Reasoning.Summary != "" && target.Reasoning.SupportsSummaries {
		reasoning["summary"] = req.Reasoning.Summary
	}
	body["reasoning"] = reasoning
	return nil
}

func applyReasoningToAnthropic(body map[string]any, req *IRRequest, target Target) error {
	if !requestRequiresReasoning(req) {
		return nil
	}
	if !targetCanSatisfyReasoning(target, "anthropic", req) {
		return fmt.Errorf("target does not support requested reasoning")
	}
	budget := reasoningBudgetForTarget(req.Reasoning, target)
	maxTokens := effectiveMaxTokens(req, 1024)
	if target.Reasoning.BudgetMustBeLessThanMaxTokens && budget >= maxTokens {
		return fmt.Errorf("reasoning budget must be less than max_tokens")
	}
	body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
	return nil
}

func effectiveMaxTokens(req *IRRequest, defaultValue int) int {
	if req != nil && req.MaxTokens > 0 {
		return req.MaxTokens
	}
	return defaultValue
}

func applyOpenAIChatMaxTokens(body map[string]any, req *IRRequest, normalizeExisting bool) {
	if req == nil || req.MaxTokens <= 0 {
		return
	}
	field := "max_tokens"
	if req.MaxTokensField == "max_completion_tokens" {
		field = "max_completion_tokens"
	}
	if normalizeExisting {
		delete(body, "max_tokens")
		delete(body, "max_completion_tokens")
		delete(body, "max_output_tokens")
	}
	body[field] = req.MaxTokens
}

func applyTargetOpenAIChatEncoding(body map[string]any, target Target) {
	if target.ForceStoreFalse {
		body["store"] = false
	}
	switch target.OutputTokenField {
	case "max_completion_tokens":
		if value, ok := body["max_tokens"]; ok {
			body["max_completion_tokens"] = value
			delete(body, "max_tokens")
		}
	case "", "max_tokens":
		if value, ok := body["max_completion_tokens"]; ok {
			body["max_tokens"] = value
			delete(body, "max_completion_tokens")
		}
	}
}

func decodeUpstreamResponse(dialect string, raw []byte, model string) (*IRResponse, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	resp := &IRResponse{Model: model, Raw: m}
	switch dialect {
	case "anthropic":
		if parts, ok := m["content"].([]any); ok {
			resp.Text = textFromContentParts(parts)
		}
		resp.StopReason = stringValue(m["stop_reason"])
		resp.Usage = usageFromMap(m["usage"])
	case "openai-responses":
		resp.Text = stringValue(m["output_text"])
		if resp.Text == "" {
			resp.Text = textFromResponsesOutput(m["output"])
		}
		resp.Usage = usageFromMap(m["usage"])
	case "replicate":
		status := stringValue(m["status"])
		if status != "" && status != "succeeded" {
			return nil, fmt.Errorf("replicate prediction status %s", status)
		}
		resp.Text = replicateOutputText(m["output"])
		resp.StopReason = "stop"
		resp.Usage = Usage{InputTokens: 0, OutputTokens: 0, TotalTokens: 0}
	case "gemini-generate-content":
		candidates := valueAsSlice(m["candidates"])
		if len(candidates) == 0 {
			return nil, fmt.Errorf("gemini generateContent response has no candidates")
		}
		candidate, _ := candidates[0].(map[string]any)
		content, _ := candidate["content"].(map[string]any)
		resp.Text = textFromGeminiParts(valueAsSlice(content["parts"]))
		resp.StopReason = geminiStopReason(stringValue(candidate["finishReason"]))
		resp.Usage = geminiUsageFromMap(m["usageMetadata"])
	case "openai-chat":
		if choices, ok := m["choices"].([]any); ok && len(choices) > 0 {
			if ch, ok := choices[0].(map[string]any); ok {
				if msg, ok := ch["message"].(map[string]any); ok {
					resp.Text = contentToText(msg["content"])
				}
				resp.StopReason = stringValue(ch["finish_reason"])
			}
		}
		resp.Usage = usageFromMap(m["usage"])
	default:
		return nil, fmt.Errorf("unsupported upstream dialect %q", dialect)
	}
	if resp.Usage.TotalTokens == 0 {
		resp.Usage.TotalTokens = resp.Usage.InputTokens + resp.Usage.OutputTokens
	}
	return resp, nil
}

func textFromGeminiParts(parts []any) string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if value, ok := part.(map[string]any); ok {
			if text := stringValue(value["text"]); text != "" {
				out = append(out, text)
			}
		}
	}
	return strings.Join(out, "")
}

func geminiStopReason(reason string) string {
	switch strings.ToUpper(strings.TrimSpace(reason)) {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	default:
		return strings.ToLower(strings.TrimSpace(reason))
	}
}

func geminiUsageFromMap(value any) Usage {
	usage, _ := value.(map[string]any)
	input, _ := numberAsInt(usage["promptTokenCount"])
	output, _ := numberAsInt(usage["candidatesTokenCount"])
	total, _ := numberAsInt(usage["totalTokenCount"])
	if total == 0 {
		total = input + output
	}
	var cached *int
	if n, present := numberAsInt(usage["cachedContentTokenCount"]); present {
		cached = validatedCachedInputTokens(&n, input)
	}
	return Usage{InputTokens: input, OutputTokens: output, TotalTokens: total, CachedInputTokens: cached}
}

func decodeResponsesPassthrough(raw []byte, model string) (*IRResponse, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	resp := &IRResponse{Model: model, Raw: m, RawResponse: true}
	resp.ID = stringValue(m["id"])
	resp.Text = stringValue(m["output_text"])
	if resp.Text == "" {
		resp.Text = textFromResponsesOutput(m["output"])
	}
	resp.Usage = usageFromMap(m["usage"])
	if resp.Usage.TotalTokens == 0 {
		resp.Usage.TotalTokens = resp.Usage.InputTokens + resp.Usage.OutputTokens
	}
	return resp, nil
}

func decodeChatPassthrough(raw []byte, model string) (*IRResponse, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	resp := &IRResponse{Model: model, Raw: m, RawResponse: true}
	resp.ID = stringValue(m["id"])
	if choices, ok := m["choices"].([]any); ok && len(choices) > 0 {
		if ch, ok := choices[0].(map[string]any); ok {
			if msg, ok := ch["message"].(map[string]any); ok {
				resp.Text = contentToText(msg["content"])
			}
			resp.StopReason = stringValue(ch["finish_reason"])
		}
	}
	resp.Usage = usageFromMap(m["usage"])
	if resp.Usage.TotalTokens == 0 {
		resp.Usage.TotalTokens = resp.Usage.InputTokens + resp.Usage.OutputTokens
	}
	return resp, nil
}

func decodeAnthropicPassthrough(raw []byte, model string) (*IRResponse, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	resp := &IRResponse{Model: model, Raw: m, RawResponse: true}
	resp.ID = stringValue(m["id"])
	resp.Text = textFromContentParts(valueAsSlice(m["content"]))
	resp.StopReason = stringValue(m["stop_reason"])
	resp.Usage = usageFromMap(m["usage"])
	if resp.Usage.TotalTokens == 0 {
		resp.Usage.TotalTokens = resp.Usage.InputTokens + resp.Usage.OutputTokens
	}
	return resp, nil
}

func replicateOutputText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		parts := []string{}
		for _, item := range x {
			parts = append(parts, contentToText(item))
		}
		return strings.Join(parts, "")
	case map[string]any:
		if txt := stringValue(x["text"]); txt != "" {
			return txt
		}
		raw, _ := json.Marshal(x)
		return string(raw)
	default:
		return contentToText(v)
	}
}

func encodeAnthropicResponse(resp *IRResponse) map[string]any {
	if resp.RawResponse && resp.Raw != nil {
		return resp.Raw
	}
	return map[string]any{
		"id":            resp.ID,
		"type":          "message",
		"role":          "assistant",
		"model":         resp.Model,
		"content":       []map[string]any{{"type": "text", "text": resp.Text}},
		"stop_reason":   defaultString(resp.StopReason, "end_turn"),
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  resp.Usage.InputTokens,
			"output_tokens": resp.Usage.OutputTokens,
		},
	}
}

func encodeChatResponse(resp *IRResponse) map[string]any {
	if resp.RawResponse && resp.Raw != nil {
		return resp.Raw
	}
	usage := chatUsageMap(resp.Usage)
	return map[string]any{
		"id":      resp.ID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   resp.Model,
		"choices": []map[string]any{{
			"index":         0,
			"finish_reason": defaultString(resp.StopReason, "stop"),
			"message":       map[string]any{"role": "assistant", "content": resp.Text},
		}},
		"usage": usage,
	}
}

func encodeResponsesResponse(resp *IRResponse) map[string]any {
	if resp.RawResponse && resp.Raw != nil {
		return resp.Raw
	}
	usage := responsesUsageMap(resp.Usage)
	return map[string]any{
		"id":          resp.ID,
		"object":      "response",
		"created_at":  time.Now().Unix(),
		"status":      "completed",
		"model":       resp.Model,
		"output_text": resp.Text,
		"output": []map[string]any{{
			"type": "message",
			"role": "assistant",
			"content": []map[string]any{{
				"type": "output_text",
				"text": resp.Text,
			}},
		}},
		"usage": usage,
	}
}

func chatUsageMap(usage Usage) map[string]any {
	out := map[string]any{"prompt_tokens": usage.InputTokens, "completion_tokens": usage.OutputTokens, "total_tokens": usage.TotalTokens}
	details := map[string]any{}
	if usage.ReasoningTokens != nil {
		out["completion_tokens_details"] = map[string]any{"reasoning_tokens": *usage.ReasoningTokens}
	}
	if usage.CachedInputTokens != nil {
		details["cached_tokens"] = *usage.CachedInputTokens
	}
	if len(details) > 0 {
		out["prompt_tokens_details"] = details
	}
	return out
}

func responsesUsageMap(usage Usage) map[string]any {
	out := map[string]any{"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens, "total_tokens": usage.TotalTokens}
	if usage.ReasoningTokens != nil {
		out["output_tokens_details"] = map[string]any{"reasoning_tokens": *usage.ReasoningTokens}
	}
	if usage.CachedInputTokens != nil {
		out["input_tokens_details"] = map[string]any{"cached_tokens": *usage.CachedInputTokens}
	}
	return out
}

func decodeMessages(v any) []IRMessage {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := []IRMessage{}
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, IRMessage{
			Role:    defaultString(stringValue(m["role"]), "user"),
			Content: contentToText(m["content"]),
			Parts:   decodeContentParts(m["content"]),
		})
	}
	return out
}

func decodeResponsesMessages(v any) []IRMessage {
	switch x := v.(type) {
	case string:
		if x == "" {
			return nil
		}
		return []IRMessage{{Role: "user", Content: x, Parts: []IRContentPart{{Type: "text", Text: x}}}}
	case map[string]any:
		if _, ok := x["content"]; ok {
			return []IRMessage{decodeResponseMessage(x)}
		}
		if _, ok := x["input"]; ok {
			return decodeResponsesMessages(x["input"])
		}
	case []any:
		out := []IRMessage{}
		for _, item := range x {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if _, ok := m["content"]; ok {
				out = append(out, decodeResponseMessage(m))
			}
		}
		if len(out) > 0 {
			return out
		}
		parts := decodeContentParts(x)
		if len(parts) > 0 {
			return []IRMessage{{Role: "user", Content: contentToText(x), Parts: parts}}
		}
	}
	return nil
}

func decodeResponseMessage(m map[string]any) IRMessage {
	content := m["content"]
	return IRMessage{
		Role:    defaultString(stringValue(m["role"]), "user"),
		Content: contentToText(content),
		Parts:   decodeContentParts(content),
	}
}

func decodeContentParts(v any) []IRContentPart {
	switch x := v.(type) {
	case string:
		if x == "" {
			return nil
		}
		return []IRContentPart{{Type: "text", Text: x}}
	case []any:
		out := []IRContentPart{}
		for _, item := range x {
			if m, ok := item.(map[string]any); ok {
				if _, hasContent := m["content"]; hasContent && stringValue(m["type"]) == "" {
					out = append(out, decodeContentParts(m["content"])...)
					continue
				}
			}
			if part := decodeContentPart(item); part.Type != "" {
				out = append(out, part)
			}
		}
		return out
	case map[string]any:
		if content, ok := x["content"]; ok {
			return decodeContentParts(content)
		}
		if input, ok := x["input"]; ok {
			return decodeContentParts(input)
		}
		if part := decodeContentPart(x); part.Type != "" {
			return []IRContentPart{part}
		}
	}
	return nil
}

func decodeContentPart(v any) IRContentPart {
	m, ok := v.(map[string]any)
	if !ok {
		txt := contentToText(v)
		if txt == "" {
			return IRContentPart{}
		}
		return IRContentPart{Type: "text", Text: txt}
	}
	typ := stringValue(m["type"])
	switch typ {
	case "text", "input_text", "output_text":
		return IRContentPart{Type: "text", Text: stringValue(m["text"])}
	case "image_url", "input_image":
		part := IRContentPart{Type: "image", Detail: stringValue(m["detail"])}
		switch img := m["image_url"].(type) {
		case string:
			part.ImageURL = img
		case map[string]any:
			part.ImageURL = stringValue(img["url"])
			if part.Detail == "" {
				part.Detail = stringValue(img["detail"])
			}
		}
		return part
	case "image":
		part := IRContentPart{Type: "image"}
		if source, ok := m["source"].(map[string]any); ok {
			switch stringValue(source["type"]) {
			case "url":
				part.ImageURL = stringValue(source["url"])
			case "base64":
				part.MediaType = stringValue(source["media_type"])
				part.Data = stringValue(source["data"])
			case "file":
				part.FileID = stringValue(source["file_id"])
			}
		}
		return part
	default:
		txt := contentToText(m)
		if txt != "" {
			return IRContentPart{Type: "text", Text: txt}
		}
		return IRContentPart{}
	}
}

func encodeOpenAIChatContent(m IRMessage) any {
	if !hasImageParts(m.Parts) {
		return m.Content
	}
	parts := []map[string]any{}
	for _, p := range ensureTextPart(m.Content, m.Parts) {
		switch p.Type {
		case "text":
			if p.Text != "" {
				parts = append(parts, map[string]any{"type": "text", "text": p.Text})
			}
		case "image":
			if u := imageURLForOpenAI(p); u != "" {
				img := map[string]any{"url": u}
				if p.Detail != "" {
					img["detail"] = p.Detail
				}
				parts = append(parts, map[string]any{"type": "image_url", "image_url": img})
			}
		}
	}
	return parts
}

func encodeResponsesInput(req *IRRequest) any {
	if !requestHasImages(req) {
		return requestText(req)
	}
	msgs := []map[string]any{}
	for _, m := range req.Messages {
		if m.Role == "system" || m.Role == "developer" {
			continue
		}
		content := []map[string]any{}
		for _, p := range ensureTextPart(m.Content, m.Parts) {
			switch p.Type {
			case "text":
				if p.Text != "" {
					content = append(content, map[string]any{"type": "input_text", "text": p.Text})
				}
			case "image":
				if u := imageURLForOpenAI(p); u != "" {
					part := map[string]any{"type": "input_image", "image_url": u}
					if p.Detail != "" {
						part["detail"] = p.Detail
					}
					content = append(content, part)
				}
			}
		}
		if len(content) > 0 {
			msgs = append(msgs, map[string]any{"role": defaultString(m.Role, "user"), "content": content})
		}
	}
	if len(msgs) == 0 && len(req.InputParts) > 0 {
		content := []map[string]any{}
		for _, p := range ensureTextPart(req.Input, req.InputParts) {
			if p.Type == "text" && p.Text != "" {
				content = append(content, map[string]any{"type": "input_text", "text": p.Text})
			}
			if p.Type == "image" {
				if u := imageURLForOpenAI(p); u != "" {
					content = append(content, map[string]any{"type": "input_image", "image_url": u})
				}
			}
		}
		msgs = append(msgs, map[string]any{"role": "user", "content": content})
	}
	return msgs
}

func encodeAnthropicContent(m IRMessage) any {
	if !hasImageParts(m.Parts) {
		return m.Content
	}
	parts := []map[string]any{}
	for _, p := range ensureTextPart(m.Content, m.Parts) {
		switch p.Type {
		case "text":
			if p.Text != "" {
				parts = append(parts, map[string]any{"type": "text", "text": p.Text})
			}
		case "image":
			if source := imageSourceForAnthropic(p); source != nil {
				parts = append(parts, map[string]any{"type": "image", "source": source})
			}
		}
	}
	return parts
}

func ensureTextPart(content string, parts []IRContentPart) []IRContentPart {
	if len(parts) == 0 && content != "" {
		return []IRContentPart{{Type: "text", Text: content}}
	}
	return parts
}

func hasImageParts(parts []IRContentPart) bool {
	for _, p := range parts {
		if p.Type == "image" {
			return true
		}
	}
	return false
}

func requestHasImages(req *IRRequest) bool {
	if req == nil {
		return false
	}
	if hasImageParts(req.InputParts) {
		return true
	}
	for _, m := range req.Messages {
		if hasImageParts(m.Parts) {
			return true
		}
	}
	return false
}

func requestImageCount(req *IRRequest) int {
	if req == nil {
		return 0
	}
	count := countImageParts(req.InputParts)
	for _, m := range req.Messages {
		count += countImageParts(m.Parts)
	}
	return count
}

func countImageParts(parts []IRContentPart) int {
	count := 0
	for _, p := range parts {
		if p.Type == "image" {
			count++
		}
	}
	return count
}

func requestInputModalities(req *IRRequest) []string {
	if requestHasImages(req) {
		return []string{"text", "image"}
	}
	return []string{"text"}
}

func imageURLForOpenAI(p IRContentPart) string {
	if p.ImageURL != "" {
		return p.ImageURL
	}
	if p.Data != "" && p.MediaType != "" {
		return "data:" + p.MediaType + ";base64," + p.Data
	}
	return ""
}

func imageSourceForAnthropic(p IRContentPart) map[string]any {
	if p.ImageURL != "" {
		if mediaType, data, ok := parseDataURL(p.ImageURL); ok {
			return map[string]any{"type": "base64", "media_type": mediaType, "data": data}
		}
		return map[string]any{"type": "url", "url": p.ImageURL}
	}
	if p.Data != "" && p.MediaType != "" {
		return map[string]any{"type": "base64", "media_type": p.MediaType, "data": p.Data}
	}
	if p.FileID != "" {
		return map[string]any{"type": "file", "file_id": p.FileID}
	}
	return nil
}

func parseDataURL(v string) (string, string, bool) {
	if !strings.HasPrefix(v, "data:") {
		return "", "", false
	}
	head, data, ok := strings.Cut(strings.TrimPrefix(v, "data:"), ",")
	if !ok || data == "" {
		return "", "", false
	}
	parts := strings.Split(head, ";")
	if len(parts) < 2 || parts[0] == "" || parts[len(parts)-1] != "base64" {
		return "", "", false
	}
	if _, err := base64.StdEncoding.DecodeString(data); err != nil {
		return "", "", false
	}
	return parts[0], data, true
}

func contentToText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		parts := []string{}
		for _, p := range x {
			if pm, ok := p.(map[string]any); ok {
				if txt := stringValue(pm["text"]); txt != "" {
					parts = append(parts, txt)
				}
				if txt := contentToText(pm["content"]); txt != "" {
					parts = append(parts, txt)
				}
				if txt := contentToText(pm["input"]); txt != "" {
					parts = append(parts, txt)
				}
				continue
			}
			if txt := contentToText(p); txt != "" {
				parts = append(parts, txt)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if txt := stringValue(x["text"]); txt != "" {
			return txt
		}
		if txt := contentToText(x["content"]); txt != "" {
			return txt
		}
		if txt := contentToText(x["input"]); txt != "" {
			return txt
		}
		if txt := stringValue(x["output_text"]); txt != "" {
			return txt
		}
		if args := stringValue(x["arguments"]); args != "" {
			return args
		}
		if typ := stringValue(x["type"]); typ == "input_text" || typ == "output_text" {
			if txt := stringValue(x["text"]); txt != "" {
				return txt
			}
		}
		return ""
	default:
		return ""
	}
}

func requestText(req *IRRequest) string {
	if req.Input != "" {
		return req.Input
	}
	parts := []string{}
	for _, m := range req.Messages {
		parts = append(parts, m.Role+": "+m.Content)
	}
	return strings.Join(parts, "\n")
}

func textFromContentParts(parts []any) string {
	out := []string{}
	for _, part := range parts {
		if m, ok := part.(map[string]any); ok {
			if stringValue(m["type"]) == "text" || m["text"] != nil {
				out = append(out, stringValue(m["text"]))
			}
		}
	}
	return strings.Join(out, "")
}

func textFromResponsesOutput(v any) string {
	arr, ok := v.([]any)
	if !ok {
		return ""
	}
	out := []string{}
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if content, ok := m["content"].([]any); ok {
			out = append(out, textFromContentParts(content))
		}
	}
	return strings.Join(out, "")
}

func valueAsSlice(v any) []any {
	if arr, ok := v.([]any); ok {
		return arr
	}
	if maps, ok := v.([]map[string]any); ok {
		out := make([]any, 0, len(maps))
		for _, item := range maps {
			out = append(out, item)
		}
		return out
	}
	return nil
}

func usageFromMap(v any) Usage {
	m, ok := v.(map[string]any)
	if !ok {
		return Usage{}
	}
	in, _ := numberAsInt(m["input_tokens"])
	if in == 0 {
		in, _ = numberAsInt(m["prompt_tokens"])
	}
	out, _ := numberAsInt(m["output_tokens"])
	if out == 0 {
		out, _ = numberAsInt(m["completion_tokens"])
	}
	total, _ := numberAsInt(m["total_tokens"])
	imageTokens := imageTokensFromUsage(m)
	var reasoningTokens *int
	// OpenAI-compatible usage schema. Provider-specific aliases require direct evidence.
	for _, key := range []string{"completion_tokens_details", "output_tokens_details"} {
		if details, ok := m[key].(map[string]any); ok {
			if n, present := numberAsInt(details["reasoning_tokens"]); present {
				reasoningTokens = &n
				break
			}
		}
	}
	cachedTokens := cachedInputTokensFromUsage(m)
	// Anthropic reports cache read/write as top-level fields outside input_tokens.
	if n, present := numberAsInt(m["cache_read_input_tokens"]); present {
		cachedTokens = &n
		in += n
	}
	if n, present := numberAsInt(m["cache_creation_input_tokens"]); present {
		in += n
	}
	if total == 0 {
		total = in + out
	}
	cachedTokens = validatedCachedInputTokens(cachedTokens, in)
	upstreamTotalCost, _ := numberAsFloat(m["cost"])
	costDetails, _ := m["cost_details"].(map[string]any)
	upstreamInputCost, _ := numberAsFloat(costDetails["upstream_inference_prompt_cost"])
	upstreamOutputCost, _ := numberAsFloat(costDetails["upstream_inference_completions_cost"])
	if upstreamTotalCost == 0 {
		upstreamTotalCost, _ = numberAsFloat(costDetails["upstream_inference_cost"])
	}
	return Usage{
		InputTokens:                   in,
		OutputTokens:                  out,
		TotalTokens:                   total,
		ReasoningTokens:               reasoningTokens,
		CachedInputTokens:             cachedTokens,
		InputImageTokens:              imageTokens,
		UpstreamReportedInputCostUSD:  upstreamInputCost,
		UpstreamReportedOutputCostUSD: upstreamOutputCost,
		UpstreamReportedTotalCostUSD:  upstreamTotalCost,
	}
}

func cachedInputTokensFromUsage(m map[string]any) *int {
	for _, key := range []string{"input_tokens_details", "prompt_tokens_details"} {
		details, ok := m[key].(map[string]any)
		if !ok {
			continue
		}
		if n, present := numberAsInt(details["cached_tokens"]); present {
			return &n
		}
	}
	return nil
}

func validatedCachedInputTokens(cached *int, inputTokens int) *int {
	if cached == nil {
		return nil
	}
	if *cached < 0 || *cached > inputTokens {
		return nil
	}
	return cached
}

func imageTokensFromUsage(m map[string]any) int {
	for _, key := range []string{"input_tokens_details", "prompt_tokens_details"} {
		details, ok := m[key].(map[string]any)
		if !ok {
			continue
		}
		if n, ok := numberAsInt(details["image_tokens"]); ok {
			return n
		}
	}
	return 0
}

func anySliceToMaps(in []any) []map[string]any {
	out := []map[string]any{}
	for _, v := range in {
		if m, ok := v.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func stringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func numberAsInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	default:
		return 0, false
	}
}

func numberAsFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	default:
		return 0, false
	}
}

func defaultString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

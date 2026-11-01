// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
)

// Anthropic client-tool evidence is separate from the upstream API's evidence.
// Both declarations and the validated inbound dialect are required.
func anthropicToolsBridgeEnabled(target Target, out string) bool {
	return (out == "openai-chat" || out == "openai-responses") &&
		anthropicInboundDialectFilterReason(target, "anthropic", out) == "" &&
		targetSupportsClientTools(target, "anthropic", false) && targetSupportsTools(target, out)
}

func encodeAnthropicToolsBridge(model string, req *IRRequest, target Target, out string) ([]byte, error) {
	chat := *req
	chat.Raw = map[string]any{}
	// These caller controls are needed for eligibility checks and translated
	// Responses encoding; the original thinking object is not forwarded.
	for _, key := range []string{"thinking", "temperature", "top_p"} {
		if value, ok := req.Raw[key]; ok {
			chat.Raw[key] = value
		}
	}
	var messages []any
	for _, item := range valueAsSlice(req.Raw["messages"]) {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := stringValue(m["role"])
		if text, ok := m["content"].(string); ok {
			messages = append(messages, map[string]any{"role": role, "content": text})
			continue
		}
		var content []any
		var calls []any
		flush := func() {
			if len(content) > 0 || len(calls) > 0 {
				msg := map[string]any{"role": role, "content": content}
				if len(calls) > 0 {
					msg["tool_calls"] = calls
				}
				messages = append(messages, msg)
				content = nil
				calls = nil
			}
		}
		for _, b := range valueAsSlice(m["content"]) {
			block, ok := b.(map[string]any)
			if !ok {
				continue
			}
			switch stringValue(block["type"]) {
			case "text":
				content = append(content, map[string]any{"type": "text", "text": block["text"]})
			case "image":
				part := decodeContentPart(block)
				if part.FileID != "" && out == "openai-responses" {
					content = append(content, map[string]any{"type": "input_image", "file_id": part.FileID})
					continue
				}
				url := imageURLForOpenAI(part)
				if url == "" {
					return nil, fmt.Errorf("unsupported Anthropic image source")
				}
				content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
			case "document":
				content = append(content, mediaPart(decodeContentPart(block), "openai-chat"))
			case "tool_use":
				args, err := json.Marshal(block["input"])
				if err != nil {
					return nil, err
				}
				calls = append(calls, map[string]any{"id": block["id"], "type": "function", "function": map[string]any{"name": block["name"], "arguments": string(args)}})
			case "tool_result":
				flush()
				messages = append(messages, map[string]any{"role": "tool", "tool_call_id": block["tool_use_id"], "content": encodeOpenAIChatContent(IRMessage{Content: contentToText(block["content"]), Parts: decodeContentParts(block["content"])})})
			case "thinking", "redacted_thinking": // Provider-private thinking is not portable.
			default:
				return nil, fmt.Errorf("unsupported Anthropic tool bridge content type")
			}
		}
		flush()
	}
	chat.Raw["messages"] = messages
	applyAnthropicFormat(chat.Raw, req, "openai-chat")
	chat.Tools = nil
	for _, tool := range req.Tools {
		chat.Tools = append(chat.Tools, map[string]any{"type": "function", "function": map[string]any{"name": tool["name"], "description": tool["description"], "parameters": tool["input_schema"]}})
	}
	if choice, ok := req.Raw["tool_choice"].(map[string]any); ok {
		switch stringValue(choice["type"]) {
		case "auto", "none":
			chat.Raw["tool_choice"] = choice["type"]
		case "any":
			chat.Raw["tool_choice"] = "required"
		case "tool":
			chat.Raw["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": choice["name"]}}
		default:
			return nil, fmt.Errorf("unsupported Anthropic tool choice")
		}
		if disabled, ok := choice["disable_parallel_tool_use"].(bool); ok {
			chat.Raw["parallel_tool_calls"] = !disabled
		}
	}
	if out == "openai-responses" {
		bridgeTarget := target
		bridgeTarget.Bridges.ChatToResponses.Enabled = true
		bridgeTarget.Bridges.ChatToResponses.Tools = true
		bridgeTarget.Bridges.ChatToResponses.ToolChoice = true
		bridgeTarget.Bridges.ChatToResponses.ParallelToolCalls = true
		bridgeTarget.Bridges.ChatToResponses.StructuredOutputs = target.StructuredOutput
		bridgeTarget.Bridges.ChatToResponses.Images = targetSupportsInputModalities(target, []string{"image"})
		bridgeTarget.Bridges.ChatToResponses.Reasoning = requestRequiresReasoning(req)
		return encodeChatToResponsesBridge(model, &chat, bridgeTarget, "")
	}
	body := map[string]any{"model": model, "messages": messages, "stream": req.Stream, "tools": chat.Tools}
	applyAnthropicFormat(body, req, out)
	if req.System != "" {
		body["messages"] = append([]any{map[string]any{"role": "system", "content": req.System}}, messages...)
	}
	for _, key := range []string{"tool_choice", "parallel_tool_calls"} {
		if v, ok := chat.Raw[key]; ok {
			body[key] = v
		}
	}
	applyOpenAIChatMaxTokens(body, req, false)
	applyTargetOpenAIChatEncoding(body, target)
	applyDefaultOpenAIChatThinking(body, target)
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if topP, ok := numberAsFloat(req.Raw["top_p"]); ok {
		body["top_p"] = topP
	}
	if len(req.Stop) > 0 {
		body["stop"] = req.Stop
	}
	if err := applyReasoningToOpenAIChat(body, req, target); err != nil {
		return nil, err
	}
	return json.Marshal(body)
}

func decodeAnthropicToolsBridge(out string, raw []byte, model string, ids IdentifierTransform) (*IRResponse, error) {
	var resp *IRResponse
	var err error
	if out == "openai-responses" {
		resp, err = decodeChatToResponsesBridgeResponse(raw, model)
	} else {
		resp, err = decodeChatPassthrough(raw, model)
	}
	if err != nil {
		return nil, err
	}
	var content []any
	if resp.Text != "" {
		content = append(content, map[string]any{"type": "text", "text": resp.Text})
	}
	choices := valueAsSlice(resp.Raw["choices"])
	if len(choices) > 0 {
		ch, _ := choices[0].(map[string]any)
		msg, _ := ch["message"].(map[string]any)
		for _, v := range valueAsSlice(msg["tool_calls"]) {
			call, _ := v.(map[string]any)
			fn, _ := call["function"].(map[string]any)
			var input map[string]any
			if err := json.Unmarshal([]byte(stringValue(fn["arguments"])), &input); err != nil {
				return nil, err
			}
			id := stringValue(call["id"])
			if id == "" || stringValue(fn["name"]) == "" || input == nil {
				return nil, fmt.Errorf("invalid upstream tool call")
			}
			if ids != nil {
				id = ids.Encode(id)
			}
			content = append(content, map[string]any{"type": "tool_use", "id": id, "name": fn["name"], "input": input})
		}
	}
	stop := "end_turn"
	if resp.StopReason == "tool_calls" {
		stop = "tool_use"
	} else if resp.StopReason == "length" {
		stop = "max_tokens"
	}
	if ids != nil {
		resp.ID = ids.Encode(resp.ID)
	}
	resp.Raw = map[string]any{"id": resp.ID, "type": "message", "role": "assistant", "model": model, "content": content, "stop_reason": stop, "stop_sequence": nil, "usage": anthropicUsageMap(resp.Usage)}
	return resp, nil
}

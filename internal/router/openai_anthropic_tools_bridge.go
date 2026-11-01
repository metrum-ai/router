// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func openAIAnthropicToolsBridgeEnabled(target Target, caller, out string) bool {
	return (caller == "openai-chat" || caller == "openai-responses") && out == "anthropic" &&
		stringSliceContainsNormalizedDialect(target.RequestShapeSupport.SupportedInboundDialects, caller) &&
		strings.EqualFold(strings.TrimSpace(target.RequestShapeSupport.ValidationStatus), "passed") &&
		targetSupportsClientTools(target, caller, false) && targetSupportsClientTools(target, "anthropic", false)
}

func openAIHasToolHistory(req *IRRequest, caller string) bool {
	if req == nil {
		return false
	}
	if caller == "openai-responses" {
		return responsesHasFunctionHistory(req)
	}
	if caller == "openai-chat" {
		for _, raw := range valueAsSlice(req.Raw["messages"]) {
			message, _ := raw.(map[string]any)
			if stringValue(message["role"]) == "tool" || rawKeyPresent(message, "tool_calls") || message["function_call"] != nil || stringValue(message["refusal"]) != "" {
				return true
			}
		}
	}
	return false
}

// encodeOpenAIToolsToAnthropic translates function definitions and complete
// tool-call/result history. It fails closed on shapes it cannot faithfully map.
func encodeOpenAIToolsToAnthropic(model, caller string, req *IRRequest, target Target) ([]byte, error) {
	var messages []map[string]any
	var system []string
	seenResponsesConversation := false
	if req.System != "" {
		system = append(system, req.System)
	}
	appendBlocks := func(role string, blocks []any) {
		if len(blocks) == 0 {
			return
		}
		if len(messages) != 0 && messages[len(messages)-1]["role"] == role {
			messages[len(messages)-1]["content"] = append(messages[len(messages)-1]["content"].([]any), blocks...)
		} else {
			messages = append(messages, map[string]any{"role": role, "content": blocks})
		}
	}
	contentBlocks := func(content any) ([]any, error) {
		if content == nil {
			return nil, nil
		}
		switch value := content.(type) {
		case string:
		case []any:
			for _, item := range value {
				part, ok := item.(map[string]any)
				if !ok || !chatBridgeContentSupported("user", []any{part}) {
					return nil, fmt.Errorf("unsupported or unrepresentable content part")
				}
				switch stringValue(part["type"]) {
				case "text", "input_text", "output_text":
				case "image_url", "input_image":
					if decodeContentPart(part).Detail != "" {
						return nil, fmt.Errorf("image detail cannot be translated")
					}
				default:
					return nil, fmt.Errorf("unsupported content part")
				}
			}
		default:
			return nil, fmt.Errorf("unsupported content")
		}
		msg := IRMessage{Content: contentToText(content), Parts: decodeContentParts(content)}
		encoded := encodeAnthropicContent(msg)
		switch v := encoded.(type) {
		case string:
			if v == "" {
				return nil, nil
			}
			return []any{map[string]any{"type": "text", "text": v}}, nil
		case []map[string]any:
			out := make([]any, len(v))
			for i := range v {
				out[i] = v[i]
			}
			return out, nil
		default:
			return nil, fmt.Errorf("unsupported content")
		}
	}
	functionCall := func(id, name, arguments string) (map[string]any, error) {
		var input map[string]any
		if id == "" || name == "" || json.Unmarshal([]byte(arguments), &input) != nil || input == nil {
			return nil, fmt.Errorf("invalid function call")
		}
		return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}, nil
	}
	toolResult := func(id string, content any) (map[string]any, error) {
		if id == "" || content == nil {
			return nil, fmt.Errorf("tool result missing id or content")
		}
		blocks, err := contentBlocks(content)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "tool_result", "tool_use_id": id, "content": blocks}, nil
	}
	if caller == "openai-chat" {
		for _, value := range valueAsSlice(req.Raw["messages"]) {
			m, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid message")
			}
			role := stringValue(m["role"])
			if role == "system" || role == "developer" {
				continue
			}
			if role == "tool" {
				result, err := toolResult(stringValue(m["tool_call_id"]), m["content"])
				if err != nil {
					return nil, err
				}
				appendBlocks("user", []any{result})
				continue
			}
			if role != "assistant" && role != "user" {
				return nil, fmt.Errorf("unsupported role")
			}
			if m["function_call"] != nil || stringValue(m["refusal"]) != "" {
				return nil, fmt.Errorf("unsupported assistant message")
			}
			blocks, err := contentBlocks(m["content"])
			if err != nil {
				return nil, err
			}
			if rawCalls := m["tool_calls"]; rawCalls != nil {
				calls, ok := rawCalls.([]any)
				if !ok {
					return nil, fmt.Errorf("invalid tool calls")
				}
				if role != "assistant" {
					return nil, fmt.Errorf("tool calls require assistant role")
				}
				for _, value := range calls {
					call, ok := value.(map[string]any)
					if !ok || stringValue(call["type"]) != "function" {
						return nil, fmt.Errorf("unsupported tool call")
					}
					fn, _ := call["function"].(map[string]any)
					block, err := functionCall(stringValue(call["id"]), stringValue(fn["name"]), stringValue(fn["arguments"]))
					if err != nil {
						return nil, err
					}
					blocks = append(blocks, block)
				}
			}
			appendBlocks(role, blocks)
		}
	} else if caller == "openai-responses" {
		if input, ok := req.Raw["input"].(string); ok {
			blocks, err := contentBlocks(input)
			if err != nil {
				return nil, err
			}
			appendBlocks("user", blocks)
		} else {
			for _, value := range valueAsSlice(req.Raw["input"]) {
				m, ok := value.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("invalid input item")
				}
				switch stringValue(m["type"]) {
				case "function_call":
					seenResponsesConversation = true
					block, err := functionCall(stringValue(m["call_id"]), stringValue(m["name"]), stringValue(m["arguments"]))
					if err != nil {
						return nil, err
					}
					appendBlocks("assistant", []any{block})
				case "function_call_output":
					seenResponsesConversation = true
					output, ok := responsesToolOutputText(m["output"])
					if !ok {
						return nil, fmt.Errorf("function_call_output.output must be text or input_text parts")
					}
					block, err := toolResult(stringValue(m["call_id"]), output)
					if err != nil {
						return nil, err
					}
					appendBlocks("user", []any{block})
				case "", "message":
					role := stringValue(m["role"])
					if role == "" {
						role = "user"
					}
					if role == "system" {
						if seenResponsesConversation {
							return nil, fmt.Errorf("system message after conversation cannot be translated")
						}
						blocks, err := contentBlocks(m["content"])
						if err != nil {
							return nil, err
						}
						for _, value := range blocks {
							block, _ := value.(map[string]any)
							if stringValue(block["type"]) != "text" {
								return nil, fmt.Errorf("non-text system content cannot be translated")
							}
							system = append(system, stringValue(block["text"]))
						}
						continue
					}
					if role != "assistant" && role != "user" {
						return nil, fmt.Errorf("unsupported response role")
					}
					seenResponsesConversation = true
					blocks, err := contentBlocks(m["content"])
					if err != nil {
						return nil, err
					}
					appendBlocks(role, blocks)
				default:
					return nil, fmt.Errorf("unsupported response item")
				}
			}
		}
	} else {
		return nil, fmt.Errorf("unsupported caller dialect")
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("missing messages")
	}
	body := map[string]any{"model": model, "messages": messages, "max_tokens": effectiveMaxTokens(req, 1024), "stream": false}
	if len(system) > 0 {
		body["system"] = strings.Join(system, "\n\n")
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if len(req.Stop) != 0 {
		body["stop_sequences"] = req.Stop
	}
	if schema, ok := openAIJSONSchemaForAnthropic(req); ok {
		body["output_config"] = map[string]any{"format": map[string]any{"type": "json_schema", "schema": schema}}
	}
	var tools []any
	for _, tool := range req.Tools {
		if stringValue(tool["type"]) != "function" {
			return nil, fmt.Errorf("unsupported tool type")
		}
		fn := tool
		if caller == "openai-chat" {
			var ok bool
			fn, ok = tool["function"].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid function tool")
			}
		}
		// Anthropic's tool schema alone is not evidence of OpenAI strict-mode
		// guarantees. Fail closed instead of silently weakening a request.
		if strict, ok := fn["strict"].(bool); ok && strict {
			return nil, fmt.Errorf("strict tool schemas unsupported")
		}
		name := stringValue(fn["name"])
		schema, ok := fn["parameters"].(map[string]any)
		if name == "" || !ok {
			return nil, fmt.Errorf("invalid function schema")
		}
		t := map[string]any{"name": name, "input_schema": schema}
		if desc, ok := fn["description"].(string); ok && desc != "" {
			t["description"] = desc
		}
		tools = append(tools, t)
	}
	if len(tools) != 0 {
		body["tools"] = tools
	}
	if choice, exists := req.Raw["tool_choice"]; exists {
		var mapped map[string]any
		switch value := choice.(type) {
		case string:
			switch value {
			case "auto", "none":
				mapped = map[string]any{"type": value}
			case "required":
				mapped = map[string]any{"type": "any"}
			default:
				return nil, fmt.Errorf("unsupported tool choice")
			}
		case map[string]any:
			if stringValue(value["type"]) != "function" {
				return nil, fmt.Errorf("unsupported tool choice")
			}
			name := stringValue(value["name"])
			if caller == "openai-chat" {
				fn, _ := value["function"].(map[string]any)
				name = stringValue(fn["name"])
			}
			if name == "" {
				return nil, fmt.Errorf("missing tool choice name")
			}
			mapped = map[string]any{"type": "tool", "name": name}
		default:
			return nil, fmt.Errorf("invalid tool choice")
		}
		if parallel, ok := req.Raw["parallel_tool_calls"].(bool); ok && !parallel && mapped["type"] != "none" {
			mapped["disable_parallel_tool_use"] = true
		}
		body["tool_choice"] = mapped
	} else if parallel, ok := req.Raw["parallel_tool_calls"].(bool); ok && !parallel && len(tools) != 0 {
		body["tool_choice"] = map[string]any{"type": "auto", "disable_parallel_tool_use": true}
	}
	if err := applyReasoningToAnthropic(body, req, target); err != nil {
		return nil, err
	}
	return json.Marshal(body)
}

func decodeAnthropicToolsToOpenAI(raw []byte, caller, model string, ids IdentifierTransform) (*IRResponse, error) {
	resp, err := decodeAnthropicPassthrough(raw, model)
	if err != nil {
		return nil, err
	}
	id := resp.ID
	if id == "" {
		return nil, fmt.Errorf("upstream message missing id")
	}
	if ids != nil {
		id = ids.Encode(id)
	}
	var text strings.Builder
	var calls []any
	var outputs []any
	for _, value := range valueAsSlice(resp.Raw["content"]) {
		block, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid upstream content")
		}
		switch stringValue(block["type"]) {
		case "text":
			piece := stringValue(block["text"])
			text.WriteString(piece)
			if caller == "openai-responses" && piece != "" {
				messageID := "msg_" + requestID()
				if ids != nil {
					messageID = ids.Encode(messageID)
				}
				outputs = append(outputs, map[string]any{"id": messageID, "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": piece, "annotations": []any{}, "logprobs": []any{}}}})
			}
		case "thinking", "redacted_thinking": // Provider-private reasoning is not portable.
		case "tool_use":
			callID, name := stringValue(block["id"]), stringValue(block["name"])
			input, ok := block["input"].(map[string]any)
			if callID == "" || name == "" || !ok {
				return nil, fmt.Errorf("invalid upstream tool use")
			}
			args, err := json.Marshal(input)
			if err != nil {
				return nil, err
			}
			if ids != nil {
				callID = ids.Encode(callID)
			}
			calls = append(calls, map[string]any{"id": callID, "type": "function", "function": map[string]any{"name": name, "arguments": string(args)}})
			outputs = append(outputs, map[string]any{"type": "function_call", "id": callID, "call_id": callID, "name": name, "arguments": string(args), "status": "completed"})
		default:
			return nil, fmt.Errorf("unsupported upstream content")
		}
	}
	finish := "stop"
	switch resp.StopReason {
	case "end_turn", "stop_sequence":
	case "tool_use":
		finish = "tool_calls"
	case "max_tokens":
		finish = "length"
	default:
		return nil, fmt.Errorf("unsupported upstream stop reason")
	}
	if finish == "tool_calls" && len(calls) == 0 {
		return nil, fmt.Errorf("tool stop without calls")
	}
	resp.ID, resp.Text, resp.StopReason = id, text.String(), finish
	switch caller {
	case "openai-chat":
		message := map[string]any{"role": "assistant", "content": any(nil)}
		if resp.Text != "" {
			message["content"] = resp.Text
		}
		if len(calls) > 0 {
			message["tool_calls"] = calls
		}
		resp.Raw = map[string]any{"id": id, "object": "chat.completion", "created": time.Now().Unix(), "model": model, "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}, "usage": chatUsageMap(resp.Usage)}
	case "openai-responses":
		status := "completed"
		if finish == "length" {
			status = "incomplete"
		}
		resp.Raw = map[string]any{"id": id, "object": "response", "created_at": time.Now().Unix(), "status": status, "model": model, "output": outputs, "usage": responsesUsageMap(resp.Usage)}
		if status == "incomplete" {
			resp.Raw["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
		}
	default:
		return nil, fmt.Errorf("unsupported caller dialect")
	}
	resp.RawResponse = true
	return resp, nil
}

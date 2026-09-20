// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const bridgeChatToResponses = "chat-to-responses"
const chatToResponsesBridgeDirection = "chat_to_responses"

func isChatToResponsesBridge(callerDialect, outDialect string, target Target) bool {
	return normalizeDialect(callerDialect) == "openai-chat" &&
		normalizeDialect(outDialect) == "openai-responses" &&
		target.Bridges.ChatToResponses.Enabled
}

func chatToResponsesBridgeFilterReason(target Target, req *IRRequest, callerDialect, outDialect string) string {
	if normalizeDialect(callerDialect) != "openai-chat" || normalizeDialect(outDialect) != "openai-responses" {
		return ""
	}
	bridge := target.Bridges.ChatToResponses
	if !bridge.Enabled {
		return "chat-to-responses-bridge-disabled"
	}
	if bridge.Text != nil && !*bridge.Text {
		return "chat-to-responses-text-unsupported"
	}
	if req == nil {
		return ""
	}
	if requestHasImages(req) && !bridge.Images {
		return "chat-to-responses-image-unsupported"
	}
	if len(req.Tools) > 0 {
		if !bridge.Tools {
			return "chat-to-responses-tools-unsupported"
		}
		if !targetSupportsTools(target, "openai-responses") {
			return "tool-support"
		}
	}
	if rawKeyPresent(req.Raw, "tool_choice") && !bridge.ToolChoice {
		return "chat-to-responses-tool-choice-unsupported"
	}
	// A bridge has no lossless mapping for an explicit JSON null. Do not silently
	// turn it into an omitted choice after it has been admitted as an explicit
	// request shape.
	if rawKeyPresent(req.Raw, "tool_choice") && req.Raw["tool_choice"] == nil {
		return "chat-to-responses-tool-choice-null-unsupported"
	}
	if rawValuePresent(req.Raw, "parallel_tool_calls") && !bridge.ParallelToolCalls {
		return "chat-to-responses-parallel-tools-unsupported"
	}
	if requestHasStructuredOutput(req) && !bridge.StructuredOutputs {
		return "chat-to-responses-structured-output-unsupported"
	}
	if requestRequiresReasoning(req) && !bridge.Reasoning {
		return "chat-to-responses-reasoning-unsupported"
	}
	for _, key := range []string{"n", "logit_bias", "logprobs", "top_logprobs"} {
		if rawValuePresent(req.Raw, key) {
			return "chat-to-responses-unsupported-field"
		}
	}
	return ""
}

func encodeChatToResponsesBridge(model string, req *IRRequest, target Target, previousResponseID string) ([]byte, error) {
	if reason := chatToResponsesBridgeFilterReason(target, req, "openai-chat", "openai-responses"); reason != "" {
		return nil, errors.New(reason)
	}
	body := map[string]any{
		"model":  model,
		"input":  chatMessagesToResponsesInput(req),
		"stream": req != nil && req.Stream,
	}
	if req != nil {
		if instructions := chatInstructions(req); instructions != "" {
			body["instructions"] = instructions
		}
		if req.MaxTokens > 0 {
			body["max_output_tokens"] = req.MaxTokens
		}
		if req.Temperature != nil {
			body["temperature"] = *req.Temperature
		}
		if topP, ok := numberAsFloat(req.Raw["top_p"]); ok {
			body["top_p"] = topP
		}
		if stop, ok := req.Raw["stop"]; ok {
			body["stop"] = stop
		}
		if len(req.Tools) > 0 {
			body["tools"] = chatToolsToResponsesTools(req.Tools)
		}
		if toolChoice, ok := chatToolChoiceToResponses(req.Raw["tool_choice"]); ok {
			body["tool_choice"] = toolChoice
		}
		if v, ok := req.Raw["parallel_tool_calls"].(bool); ok {
			body["parallel_tool_calls"] = v
		}
		if format := chatResponseFormatToResponsesText(req.Raw["response_format"]); format != nil {
			body["text"] = map[string]any{"format": format}
		}
	}
	if target.ForceStoreFalse {
		body["store"] = false
	}
	if previousResponseID != "" {
		body["previous_response_id"] = previousResponseID
	}
	if err := applyReasoningToOpenAIResponses(body, req, target); err != nil {
		return nil, err
	}
	return json.Marshal(body)
}

func chatInstructions(req *IRRequest) string {
	if req == nil {
		return ""
	}
	if req.System != "" {
		return req.System
	}
	var parts []string
	for _, raw := range valueAsSlice(req.Raw["messages"]) {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(stringValue(msg["role"])))
		if role == "system" || role == "developer" {
			if text := contentToText(msg["content"]); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func chatMessagesToResponsesInput(req *IRRequest) []map[string]any {
	if req == nil {
		return nil
	}
	var out []map[string]any
	for _, raw := range valueAsSlice(req.Raw["messages"]) {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(defaultString(stringValue(msg["role"]), "user")))
		switch role {
		case "system", "developer":
			continue
		case "tool":
			item := map[string]any{
				"type":    "function_call_output",
				"call_id": defaultString(stringValue(msg["tool_call_id"]), stringValue(msg["id"])),
				"output":  contentToText(msg["content"]),
			}
			out = append(out, item)
		case "assistant":
			if calls := valueAsSlice(msg["tool_calls"]); len(calls) > 0 {
				for _, rawCall := range calls {
					if item := chatToolCallToResponsesInput(rawCall); item != nil {
						out = append(out, item)
					}
				}
			}
			if content := chatContentToResponsesContent(msg["content"], false); len(content) > 0 {
				out = append(out, map[string]any{"role": "assistant", "content": content})
			}
		default:
			if content := chatContentToResponsesContent(msg["content"], true); len(content) > 0 {
				out = append(out, map[string]any{"role": role, "content": content})
			}
		}
	}
	return out
}

func chatContentToResponsesContent(v any, input bool) []map[string]any {
	partType := "output_text"
	if input {
		partType = "input_text"
	}
	if text, ok := v.(string); ok {
		if text == "" {
			return nil
		}
		return []map[string]any{{"type": partType, "text": text}}
	}
	var out []map[string]any
	for _, raw := range valueAsSlice(v) {
		part, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(stringValue(part["type"]))) {
		case "text", "input_text", "output_text":
			if text := stringValue(part["text"]); text != "" {
				out = append(out, map[string]any{"type": partType, "text": text})
			}
		case "image_url", "input_image":
			image := map[string]any{"type": "input_image"}
			switch url := part["image_url"].(type) {
			case string:
				image["image_url"] = url
			case map[string]any:
				image["image_url"] = stringValue(url["url"])
				if detail := stringValue(url["detail"]); detail != "" {
					image["detail"] = detail
				}
			}
			if detail := stringValue(part["detail"]); detail != "" {
				image["detail"] = detail
			}
			if image["image_url"] != "" {
				out = append(out, image)
			}
		}
	}
	return out
}

func chatToolCallToResponsesInput(raw any) map[string]any {
	call, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	fn, _ := call["function"].(map[string]any)
	if fn == nil {
		return nil
	}
	return map[string]any{
		"type":      "function_call",
		"call_id":   defaultString(stringValue(call["id"]), stringValue(call["call_id"])),
		"name":      stringValue(fn["name"]),
		"arguments": defaultString(stringValue(fn["arguments"]), "{}"),
	}
}

func chatToolsToResponsesTools(tools []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		if strings.ToLower(strings.TrimSpace(stringValue(tool["type"]))) != "function" {
			continue
		}
		fn, _ := tool["function"].(map[string]any)
		if fn == nil {
			continue
		}
		mapped := map[string]any{
			"type":       "function",
			"name":       stringValue(fn["name"]),
			"parameters": defaultValue(fn["parameters"], map[string]any{"type": "object"}),
		}
		if desc := stringValue(fn["description"]); desc != "" {
			mapped["description"] = desc
		}
		if strict, ok := fn["strict"].(bool); ok {
			mapped["strict"] = strict
		}
		out = append(out, mapped)
	}
	return out
}

func defaultValue(v, fallback any) any {
	if v == nil {
		return fallback
	}
	return v
}

func chatToolChoiceToResponses(v any) (any, bool) {
	switch x := v.(type) {
	case nil:
		return nil, false
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "auto", "none", "required":
			return x, true
		default:
			return nil, false
		}
	case map[string]any:
		if strings.ToLower(strings.TrimSpace(stringValue(x["type"]))) != "function" {
			return nil, false
		}
		fn, _ := x["function"].(map[string]any)
		name := stringValue(fn["name"])
		if name == "" {
			return nil, false
		}
		return map[string]any{"type": "function", "name": name}, true
	default:
		return nil, false
	}
}

func chatResponseFormatToResponsesText(v any) any {
	format, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	switch stringValue(format["type"]) {
	case "json_schema":
		if schema, ok := format["json_schema"].(map[string]any); ok {
			out := map[string]any{"type": "json_schema"}
			for _, key := range []string{"name", "description", "schema", "strict"} {
				if value, ok := schema[key]; ok {
					out[key] = value
				}
			}
			return out
		}
	case "json_object":
		return map[string]any{"type": "json_object"}
	case "text":
		return map[string]any{"type": "text"}
	}
	return nil
}

func decodeChatToResponsesBridgeResponse(raw []byte, model string) (*IRResponse, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	resp := &IRResponse{Model: model, RawResponse: true}
	resp.ID = stringValue(m["id"])
	resp.Text = stringValue(m["output_text"])
	if resp.Text == "" {
		resp.Text = textFromResponsesOutput(m["output"])
	}
	resp.StopReason = finishReasonFromResponses(m)
	resp.Usage = usageFromMap(m["usage"])
	if resp.Usage.TotalTokens == 0 {
		resp.Usage.TotalTokens = resp.Usage.InputTokens + resp.Usage.OutputTokens
	}
	chat := responsesToChatCompletion(m, model, resp)
	resp.Raw = chat
	return resp, nil
}

func responsesToChatCompletion(raw map[string]any, model string, resp *IRResponse) map[string]any {
	message := map[string]any{"role": "assistant", "content": resp.Text}
	if calls := responsesOutputFunctionCalls(raw["output"]); len(calls) > 0 {
		message["tool_calls"] = calls
		if resp.Text == "" {
			message["content"] = nil
		}
	}
	created := time.Now().Unix()
	if n, ok := numberAsInt(raw["created"]); ok {
		created = int64(n)
	}
	if n, ok := numberAsInt(raw["created_at"]); ok {
		created = int64(n)
	}
	return map[string]any{
		"id":      defaultString(resp.ID, requestID()),
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": defaultString(resp.StopReason, "stop"),
		}},
		"usage": chatUsageMap(resp.Usage),
	}
}

func responsesOutputFunctionCalls(v any) []map[string]any {
	var out []map[string]any
	for _, raw := range valueAsSlice(v) {
		item, ok := raw.(map[string]any)
		if !ok || strings.ToLower(strings.TrimSpace(stringValue(item["type"]))) != "function_call" {
			continue
		}
		id := defaultString(stringValue(item["call_id"]), stringValue(item["id"]))
		out = append(out, map[string]any{
			"id":   id,
			"type": "function",
			"function": map[string]any{
				"name":      stringValue(item["name"]),
				"arguments": defaultString(stringValue(item["arguments"]), "{}"),
			},
		})
	}
	return out
}

func finishReasonFromResponses(m map[string]any) string {
	if len(responsesOutputFunctionCalls(m["output"])) > 0 {
		return "tool_calls"
	}
	switch strings.ToLower(strings.TrimSpace(stringValue(m["status"]))) {
	case "incomplete":
		return "length"
	default:
		return "stop"
	}
}

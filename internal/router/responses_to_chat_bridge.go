// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const responsesToChatBridgeDirection = "responses_to_chat"

func isResponsesToChatBridge(callerDialect, outDialect string, target Target) bool {
	return normalizeDialect(callerDialect) == "openai-responses" &&
		normalizeDialect(outDialect) == "openai-chat" &&
		target.ResponsesToChat.Enabled
}

func responsesToChatBridgeFilterReason(target Target, req *IRRequest, callerDialect, outDialect string) string {
	if normalizeDialect(callerDialect) != "openai-responses" || normalizeDialect(outDialect) != "openai-chat" {
		return ""
	}
	bridge := target.ResponsesToChat
	if !bridge.Enabled {
		return "responses-to-chat-bridge-disabled"
	}
	if req == nil {
		if bridge.Text {
			return ""
		}
		return "responses-to-chat-text-unsupported"
	}
	switch {
	case rawValuePresent(req.Raw, "previous_response_id"):
		return "responses-to-chat-previous-response-id"
	case req.Stream && !bridge.Streaming:
		return "responses-to-chat-streaming"
	case requestHasImages(req) && !bridge.Images:
		return "responses-to-chat-image"
	case requestRequiresReasoning(req) && !bridge.Reasoning:
		return "responses-to-chat-reasoning"
	case requestHasStructuredOutput(req):
		return "responses-to-chat-structured-output"
	case requestFeaturePresent(req, "function_call_output"):
		return "responses-to-chat-function-call-output"
	case requestFeaturePresent(req, "tool_result"):
		return "responses-to-chat-tool-result"
	case rawValuePresent(req.Raw, "include"):
		return "responses-to-chat-include"
	case rawValuePresent(req.Raw, "truncation"):
		return "responses-to-chat-truncation"
	}
	if len(req.Tools) > 0 {
		if !bridge.FunctionTools {
			return "responses-to-chat-tools-unsupported"
		}
		for _, tool := range req.Tools {
			typ := strings.ToLower(strings.TrimSpace(stringValue(tool["type"])))
			if forbiddenProviderHostedResponsesToolType(typ) || genericHostedResponsesToolType(typ) {
				return "responses-to-chat-hosted-tools"
			}
			if typ != "function" {
				return "responses-to-chat-tool-type"
			}
		}
		if rawKeyPresent(req.Raw, "tool_choice") && !bridge.ToolChoice {
			return "responses-to-chat-tool-choice"
		}
		// Preserve the omitted-vs-null evidence boundary: translating null as an
		// omitted key would make tools-omitted evidence cover a different request.
		if rawKeyPresent(req.Raw, "tool_choice") && req.Raw["tool_choice"] == nil {
			return "responses-to-chat-tool-choice-null-unsupported"
		}
		return ""
	}
	// An explicit null is still an explicit choice. Reject it before the
	// no-tools request can fall through to the text-only bridge.
	if rawKeyPresent(req.Raw, "tool_choice") && req.Raw["tool_choice"] == nil {
		return "responses-to-chat-tool-choice-null-unsupported"
	}
	if !bridge.Text {
		return "responses-to-chat-text-unsupported"
	}
	return ""
}

func encodeResponsesToChatBridge(model string, req *IRRequest, target Target) ([]byte, error) {
	if reason := responsesToChatBridgeFilterReason(target, req, "openai-responses", "openai-chat"); reason != "" {
		return nil, errors.New(reason)
	}
	body := map[string]any{"model": model, "stream": req.Stream}
	if req.Stream {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	msgs := make([]map[string]any, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": req.System})
	}
	for _, m := range req.Messages {
		if m.Role == "system" || m.Role == "developer" {
			continue
		}
		msgs = append(msgs, map[string]any{"role": defaultString(m.Role, "user"), "content": encodeOpenAIChatContent(m)})
	}
	if len(msgs) == 0 && req.Input != "" {
		msgs = append(msgs, map[string]any{"role": "user", "content": req.Input})
	}
	body["messages"] = msgs
	applyOpenAIChatMaxTokens(body, req, false)
	applyTargetOpenAIChatEncoding(body, target)
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if topP, ok := numberAsFloat(req.Raw["top_p"]); ok {
		body["top_p"] = topP
	}
	if len(req.Tools) > 0 {
		body["tools"] = responsesFunctionToolsToChatTools(req.Tools)
		if rawChoice, ok := req.Raw["tool_choice"]; ok {
			choice, err := responsesToolChoiceToChat(rawChoice)
			if err != nil {
				return nil, err
			}
			body["tool_choice"] = choice
		}
		if value, ok := req.Raw["parallel_tool_calls"]; ok {
			body["parallel_tool_calls"] = value
		}
	}
	if err := applyReasoningToOpenAIChat(body, req, target); err != nil {
		return nil, err
	}
	return json.Marshal(body)
}

func responsesFunctionToolsToChatTools(tools []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		fn := map[string]any{}
		for _, key := range []string{"name", "description", "parameters", "strict"} {
			if value, ok := tool[key]; ok {
				fn[key] = value
			}
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out
}

func responsesToolChoiceToChat(choice any) (any, error) {
	switch v := choice.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "", "auto", "none", "required":
			return v, nil
		default:
			return nil, fmt.Errorf("unsupported responses tool_choice")
		}
	case map[string]any:
		typ := strings.ToLower(strings.TrimSpace(stringValue(v["type"])))
		switch typ {
		case "auto", "none", "required":
			return map[string]any{"type": typ}, nil
		case "function":
			name := strings.TrimSpace(stringValue(v["name"]))
			if name == "" {
				return nil, fmt.Errorf("unsupported responses tool_choice")
			}
			return map[string]any{"type": "function", "function": map[string]any{"name": name}}, nil
		default:
			return nil, fmt.Errorf("unsupported responses tool_choice")
		}
	default:
		return nil, fmt.Errorf("unsupported responses tool_choice")
	}
}

func decodeResponsesToChatBridge(raw []byte, model string) (*IRResponse, error) {
	var chat map[string]any
	if err := json.Unmarshal(raw, &chat); err != nil {
		return nil, err
	}
	usage := usageFromMap(chat["usage"])
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	text, output, stopReason := responsesOutputFromChat(chat)
	respID := stringValue(chat["id"])
	if respID == "" {
		respID = "resp_" + requestID()
	}
	responses := map[string]any{
		"id":          respID,
		"object":      "response",
		"created_at":  time.Now().Unix(),
		"status":      "completed",
		"model":       model,
		"output_text": text,
		"output":      output,
		"usage":       responsesUsageMap(usage),
	}
	if stopReason != "" {
		responses["finish_reason"] = stopReason
	}
	return &IRResponse{
		ID:          respID,
		Model:       model,
		Text:        text,
		StopReason:  stopReason,
		Usage:       usage,
		Raw:         responses,
		RawResponse: true,
	}, nil
}

func responsesOutputFromChat(chat map[string]any) (string, []map[string]any, string) {
	var text string
	var output []map[string]any
	var stopReason string
	choices := valueAsSlice(chat["choices"])
	if len(choices) == 0 {
		return "", output, ""
	}
	choice, _ := choices[0].(map[string]any)
	stopReason = stringValue(choice["finish_reason"])
	msg, _ := choice["message"].(map[string]any)
	text = contentToText(msg["content"])
	if text != "" {
		output = append(output, map[string]any{
			"type": "message",
			"role": "assistant",
			"content": []map[string]any{{
				"type": "output_text",
				"text": text,
			}},
		})
	}
	for _, rawCall := range valueAsSlice(msg["tool_calls"]) {
		call, _ := rawCall.(map[string]any)
		fn, _ := call["function"].(map[string]any)
		name := stringValue(fn["name"])
		if name == "" {
			continue
		}
		callID := stringValue(call["id"])
		if callID == "" {
			callID = "call_" + requestID()
		}
		output = append(output, map[string]any{
			"type":      "function_call",
			"id":        callID,
			"call_id":   callID,
			"name":      name,
			"arguments": defaultString(stringValue(fn["arguments"]), "{}"),
			"status":    "completed",
		})
	}
	return text, output, stopReason
}

func genericHostedResponsesToolType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "web_search", "web_search_preview", "image_generation":
		return true
	default:
		return false
	}
}

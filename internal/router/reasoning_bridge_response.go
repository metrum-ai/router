// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"strings"
)

// bridgeReasoningText extracts only visible provider-supplied reasoning text.
// Signatures, redacted thinking and encrypted payloads never cross the bridge.
func bridgeReasoningText(raw []byte, upstream string) string {
	var response map[string]any
	if json.Unmarshal(raw, &response) != nil {
		return ""
	}
	var pieces []string
	switch normalizeDialect(upstream) {
	case "anthropic":
		for _, value := range valueAsSlice(response["content"]) {
			block, _ := value.(map[string]any)
			if stringValue(block["type"]) == "thinking" {
				pieces = append(pieces, stringValue(block["thinking"]))
			}
		}
	case "openai-responses":
		for _, value := range valueAsSlice(response["output"]) {
			item, _ := value.(map[string]any)
			if stringValue(item["type"]) != "reasoning" {
				continue
			}
			for _, rawSummary := range valueAsSlice(item["summary"]) {
				summary, _ := rawSummary.(map[string]any)
				if stringValue(summary["type"]) == "summary_text" {
					pieces = append(pieces, stringValue(summary["text"]))
				}
			}
		}
	case "openai-chat":
		choices := valueAsSlice(response["choices"])
		if len(choices) > 0 {
			choice, _ := choices[0].(map[string]any)
			message, _ := choice["message"].(map[string]any)
			pieces = append(pieces, stringValue(message["reasoning_content"]))
		}
	}
	return strings.Join(pieces, "")
}

// attachBridgeReasoning is used only for an explicit caller opt-in. It wraps
// already translated visible output without copying opaque upstream items.
func attachBridgeReasoning(resp *IRResponse, raw []byte, upstream, caller string) {
	text := bridgeReasoningText(raw, upstream)
	if text == "" || resp == nil {
		return
	}
	var result map[string]any
	switch normalizeDialect(caller) {
	case "anthropic":
		result = encodeAnthropicResponse(resp, "")
		content := valueAsSlice(result["content"])
		result["content"] = append([]any{map[string]any{"type": "thinking", "thinking": text, "signature": ""}}, content...)
	case "openai-chat":
		result = encodeChatResponse(resp, "")
		choices := valueAsSlice(result["choices"])
		if len(choices) == 0 {
			return
		}
		choice, _ := choices[0].(map[string]any)
		message, _ := choice["message"].(map[string]any)
		if message == nil {
			return
		}
		message["reasoning_content"] = text
	case "openai-responses":
		result = encodeResponsesResponse(resp, "")
		output := valueAsSlice(result["output"])
		reasoning := map[string]any{"id": "rs_" + requestID(), "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": text}}}
		result["output"] = append([]any{reasoning}, output...)
	default:
		return
	}
	resp.Raw, resp.RawResponse = result, true
}

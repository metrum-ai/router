// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "strings"

// responsesToolOutputText maps only lossless text outputs; provider media and
// unknown parts must not be silently reduced to text on a translated bridge.
func responsesToolOutputText(output any) (string, bool) {
	if text, ok := output.(string); ok {
		return text, true
	}
	parts, ok := output.([]any)
	if !ok {
		return "", false
	}
	var text strings.Builder
	for _, part := range parts {
		value, ok := part.(map[string]any)
		if !ok || stringValue(value["type"]) != "input_text" {
			return "", false
		}
		piece, ok := value["text"].(string)
		if !ok {
			return "", false
		}
		text.WriteString(piece)
	}
	return text.String(), true
}

func invalidResponsesFunctionOutput(req *IRRequest) bool {
	if req == nil {
		return false
	}
	for _, value := range valueAsSlice(req.Raw["input"]) {
		item, _ := value.(map[string]any)
		if stringValue(item["type"]) == "function_call_output" {
			if _, ok := responsesToolOutputText(item["output"]); !ok {
				return true
			}
		}
	}
	return false
}

func chatBridgeContentSupported(role string, content any) bool {
	if content == nil {
		return role == "assistant"
	}
	if _, ok := content.(string); ok {
		return true
	}
	parts, ok := content.([]any)
	if !ok {
		return false
	}
	for _, part := range parts {
		m, ok := part.(map[string]any)
		if !ok {
			return false
		}
		switch stringValue(m["type"]) {
		case "text", "input_text", "output_text":
			if _, ok := m["text"].(string); !ok {
				return false
			}
			for key, value := range m {
				if key == "type" || key == "text" || value == nil {
					continue
				}
				if empty, ok := value.([]any); ok && len(empty) == 0 && (key == "annotations" || key == "logprobs") {
					continue
				}
				return false
			}
		case "input_image", "image_url", "input_file", "file", "input_audio", "audio":
			if role != "user" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func invalidResponsesChatMessageContent(req *IRRequest) bool {
	if req == nil {
		return false
	}
	for _, value := range valueAsSlice(req.Raw["input"]) {
		item, ok := value.(map[string]any)
		if !ok {
			return true
		}
		typ := stringValue(item["type"])
		if typ == "" || typ == "message" {
			role := stringValue(item["role"])
			if role != "user" && role != "assistant" && role != "system" && role != "developer" {
				return true
			}
			if !chatBridgeContentSupported(role, item["content"]) {
				return true
			}
		}
	}
	return false
}

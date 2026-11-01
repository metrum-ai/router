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
		if !ok || stringValue(value["type"]) != "input_text" || !bridgeTextPartSupported(value) {
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

func bridgeTextPartSupported(part map[string]any) bool {
	if _, ok := part["text"].(string); !ok {
		return false
	}
	for key, value := range part {
		if key == "type" || key == "text" || value == nil {
			continue
		}
		if empty, ok := value.([]any); ok && len(empty) == 0 && (key == "annotations" || key == "logprobs") {
			continue
		}
		return false
	}
	return true
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
			if !bridgeTextPartSupported(m) {
				return false
			}
		case "input_image", "image_url":
			if role != "user" {
				return false
			}
			image := decodeContentPart(m)
			if image.FileID == "" && imageURLForOpenAI(image) == "" {
				return false
			}
		case "input_file", "file", "input_audio", "audio":
			if role != "user" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func invalidResponsesAnthropicMessageContent(req *IRRequest) bool {
	if invalidResponsesChatMessageContent(req) {
		return true
	}
	if req == nil {
		return false
	}
	seenConversation := false
	for _, value := range valueAsSlice(req.Raw["input"]) {
		item, _ := value.(map[string]any)
		typ := stringValue(item["type"])
		if typ != "" && typ != "message" {
			seenConversation = true
			continue
		}
		role := stringValue(item["role"])
		if role == "developer" {
			return true
		}
		if role == "system" {
			if seenConversation {
				return true
			}
			if _, ok := item["content"].(string); !ok {
				parts, ok := item["content"].([]any)
				if !ok {
					return true
				}
				for _, value := range parts {
					part, _ := value.(map[string]any)
					typ := stringValue(part["type"])
					if typ != "text" && typ != "input_text" && typ != "output_text" {
						return true
					}
				}
			}
		} else {
			seenConversation = true
		}
		for _, value := range valueAsSlice(item["content"]) {
			part, _ := value.(map[string]any)
			if (stringValue(part["type"]) == "input_image" || stringValue(part["type"]) == "image_url") && decodeContentPart(part).Detail != "" {
				return true
			}
		}
	}
	return false
}

// Call only after the Anthropic eligibility check has validated leading text
// system items. The generic encoder ignores system-role messages themselves.
func responsesAnthropicSystemPrompt(req *IRRequest) string {
	var parts []string
	if req.System != "" {
		parts = append(parts, req.System)
	}
	for _, value := range valueAsSlice(req.Raw["input"]) {
		item, _ := value.(map[string]any)
		typ := stringValue(item["type"])
		if (typ != "" && typ != "message") || stringValue(item["role"]) != "system" {
			continue
		}
		if text, ok := item["content"].(string); ok {
			if text != "" {
				parts = append(parts, text)
			}
			continue
		}
		for _, value := range valueAsSlice(item["content"]) {
			part, _ := value.(map[string]any)
			if text := stringValue(part["text"]); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func invalidResponsesChatMessageContent(req *IRRequest) bool {
	if req == nil {
		return false
	}
	// Responses input objects can be recursively decoded by the generic IR,
	// but these bridges cannot preserve their nested role ordering safely.
	if _, object := req.Raw["input"].(map[string]any); object {
		return true
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

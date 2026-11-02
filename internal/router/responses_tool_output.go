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

// Only the OpenAI detail hint may be discarded on an Anthropic image.
// Reject other image-part metadata rather than silently losing it.
func anthropicImageMetadataUnsupported(req *IRRequest, caller string) bool {
	if req == nil {
		return false
	}
	key := "messages"
	if caller == "openai-responses" {
		key = "input"
	}
	for _, rawMessage := range valueAsSlice(req.Raw[key]) {
		message, _ := rawMessage.(map[string]any)
		for _, rawPart := range valueAsSlice(message["content"]) {
			part, _ := rawPart.(map[string]any)
			if typ := stringValue(part["type"]); typ != "input_image" && typ != "image_url" {
				continue
			}
			if part["file_id"] != nil && part["image_url"] != nil {
				return true
			}
			for k, value := range part {
				if value != nil && k != "type" && k != "image_url" && k != "file_id" && k != "detail" {
					return true
				}
			}
			if image, ok := part["image_url"].(map[string]any); ok {
				for k, value := range image {
					if value != nil && k != "url" && k != "detail" {
						return true
					}
				}
			}
		}
	}
	return false
}

func responsesHasDeveloper(req *IRRequest) bool {
	return responsesHasInstructionRole(req, "developer")
}

func responsesHasInstructionRole(req *IRRequest, role string) bool {
	if req == nil {
		return false
	}
	for _, value := range valueAsSlice(req.Raw["input"]) {
		item, _ := value.(map[string]any)
		typ := stringValue(item["type"])
		if (typ == "" || typ == "message") && stringValue(item["role"]) == role {
			return true
		}
	}
	return false
}

// For newly eligible developer histories, check original order before foreign
// reasoning is removed: stripping must not promote either kind of instruction.
// A developer-only prompt also cannot form an Anthropic conversation.
func responsesDeveloperOrderingInvalid(req *IRRequest) bool {
	if req == nil {
		return false
	}
	input := valueAsSlice(req.Raw["input"])
	if !responsesHasDeveloper(req) {
		return false
	}
	seen, hasConversation := false, false
	for _, value := range input {
		item, ok := value.(map[string]any)
		if !ok {
			seen = true
			continue
		}
		typ := stringValue(item["type"])
		if typ != "" && typ != "message" {
			seen = true
			if typ == "function_call" || typ == "function_call_output" {
				hasConversation = true
			}
			continue
		}
		switch stringValue(item["role"]) {
		case "developer", "system":
			if seen {
				return true
			}
		case "user", "assistant":
			seen, hasConversation = true, true
		default:
			seen = true
		}
	}
	return !hasConversation
}

// Multiple developer parts would be concatenated differently from the IR
// used by PII filtering and cache keys. Keep them ineligible for Anthropic.
func responsesDeveloperMultipart(req *IRRequest) bool {
	if req == nil {
		return false
	}
	for _, value := range valueAsSlice(req.Raw["input"]) {
		item, _ := value.(map[string]any)
		if stringValue(item["role"]) != "developer" {
			continue
		}
		if parts, ok := item["content"].([]any); ok && len(parts) > 1 {
			return true
		}
	}
	return false
}

func invalidResponsesAnthropicMessageContent(req *IRRequest) bool {
	if invalidResponsesChatMessageContent(req) || responsesDeveloperMultipart(req) {
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
		if role == "system" || role == "developer" {
			if seenConversation {
				return true
			}
			if _, ok := item["content"].(string); !ok {
				parts, ok := item["content"].([]any)
				if !ok || (role == "developer" && len(parts) != 1) {
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
	}
	return false
}

// Call only after the Anthropic eligibility check has validated leading text
// system and developer items. The generic encoder ignores these messages themselves.
func responsesAnthropicSystemPrompt(req *IRRequest) string {
	var parts []string
	if req.System != "" {
		parts = append(parts, req.System)
	}
	for _, value := range valueAsSlice(req.Raw["input"]) {
		item, _ := value.(map[string]any)
		typ := stringValue(item["type"])
		role := stringValue(item["role"])
		if (typ != "" && typ != "message") || (role != "system" && role != "developer") {
			continue
		}
		if text, ok := item["content"].(string); ok {
			if text != "" {
				parts = append(parts, text)
			}
			continue
		}
		var text strings.Builder
		for _, value := range valueAsSlice(item["content"]) {
			part, _ := value.(map[string]any)
			text.WriteString(stringValue(part["text"]))
		}
		if text.Len() != 0 {
			parts = append(parts, text.String())
		}
	}
	return strings.Join(parts, "\n\n")
}

func responsesHasMultiTextSystem(req *IRRequest) bool {
	if req == nil {
		return false
	}
	for _, value := range valueAsSlice(req.Raw["input"]) {
		item, _ := value.(map[string]any)
		if stringValue(item["role"]) != "system" {
			continue
		}
		count := 0
		for _, value := range valueAsSlice(item["content"]) {
			part, _ := value.(map[string]any)
			if stringValue(part["text"]) != "" {
				count++
			}
		}
		if count > 1 {
			return true
		}
	}
	return false
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
		if typ != "" && typ != "message" && typ != "function_call" && typ != "function_call_output" {
			return true
		}
		if typ == "" || typ == "message" {
			role := stringValue(item["role"])
			if role != "user" && role != "assistant" && role != "system" && role != "developer" {
				return true
			}
			if !chatBridgeContentSupported(role, item["content"]) {
				return true
			}
			// The generic IR joins separate message text parts with newlines.
			// Until both translated encoders preserve their boundaries, do not
			// silently alter a user, assistant, or developer message.
			if role != "system" {
				textParts := 0
				for _, value := range valueAsSlice(item["content"]) {
					part, _ := value.(map[string]any)
					switch stringValue(part["type"]) {
					case "text", "input_text", "output_text":
						if stringValue(part["text"]) != "" {
							textParts++
						}
					}
				}
				if textParts > 1 {
					return true
				}
			}
		}
	}
	return false
}

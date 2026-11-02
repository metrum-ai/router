// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "strings"

// toolResultText preserves the caller's exact text when a tool result is a
// string or an array of text parts. It deliberately does not flatten mixed
// media or unknown part shapes; callers retain their existing handling of those.
func toolResultText(value any) (string, bool) {
	if text, ok := value.(string); ok {
		return text, true
	}
	parts, ok := value.([]any)
	if !ok {
		return "", false
	}
	var result strings.Builder
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok {
			return "", false
		}
		switch stringValue(part["type"]) {
		case "text", "input_text", "output_text":
		default:
			return "", false
		}
		text, ok := part["text"].(string)
		if !ok || part["content"] != nil || part["input"] != nil {
			return "", false
		}
		result.WriteString(text)
	}
	return result.String(), true
}

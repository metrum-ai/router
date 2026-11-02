// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "strings"

// toolResultText preserves the caller's exact text when a tool result is a
// string or an array of text parts. It deliberately does not flatten mixed
// media or unknown part shapes; callers retain their existing handling of those.
// Part arrays reach here either as decoded JSON ([]any) or as a typed slice built
// by the bridges ([]map[string]any), so both shapes are accepted.
func toolResultText(value any) (string, bool) {
	if text, ok := value.(string); ok {
		return text, true
	}
	if _, ok := value.([]any); !ok {
		if _, ok := value.([]map[string]any); !ok {
			return "", false
		}
	}
	parts := valueAsSlice(value)
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

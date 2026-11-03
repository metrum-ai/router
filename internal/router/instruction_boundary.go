// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"strconv"
	"strings"
)

// Translated instruction surfaces assemble a system prompt from leading
// instruction messages instead of transmitting req.System. PII filtering and
// cache identity must therefore agree with the bridges on one canonical,
// target-bound instruction text (issue #645).
//
// instructionSurfaces names each distinct assembly the router can transmit for
// a caller dialect. "anthropic" is the plain Responses-to-Anthropic surface,
// "anthropic-tools" the function-history Anthropic bridge, and
// "openai-responses" the Chat-to-Responses bridge.
func instructionSurfaces(caller string) []string {
	switch normalizeDialect(caller) {
	case "openai-responses":
		return []string{"anthropic", "anthropic-tools"}
	case "openai-chat":
		return []string{"anthropic", "anthropic-tools", "openai-responses"}
	default:
		return nil
	}
}

// assembleTargetInstruction builds the instruction text a target receives for
// the named surface from the caller's own representation. It never reads
// filtered state, so callers can snapshot it before redaction.
func assembleTargetInstruction(req *IRRequest, caller, surface string) string {
	if req == nil {
		return ""
	}
	switch surface {
	case "anthropic":
		return responsesAnthropicSystemPrompt(req)
	case "anthropic-tools":
		return openAIToolsSystemPrompt(req, caller)
	case "openai-responses":
		return chatInstructions(req)
	default:
		return req.System
	}
}

// targetInstruction returns the instruction text the router actually transmits
// for that surface. A PII filter stores the redacted instruction per surface;
// when present it wins, so the filter, the cache key and the bridge cannot
// disagree about the transmitted text.
func targetInstruction(req *IRRequest, caller, surface string) string {
	if req == nil {
		return ""
	}
	if filtered, ok := req.Instructions[surface]; ok {
		return filtered
	}
	return assembleTargetInstruction(req, caller, surface)
}

// redactTargetInstructions stores the redacted form of every instruction text
// this caller can transmit, so a match spanning distinct instruction messages or
// parts is replaced even though no single raw value contains it.
func redactTargetInstructions(req *IRRequest, caller string, apply normalizedPIIApplyTo, state *piiRedactionState) {
	if req == nil || state == nil {
		return
	}
	// A transmitted instruction mixes the request's system text with leading
	// instruction messages, so it is filtered when either source scope is
	// enabled. Filtering a surface more than once is impossible: each surface is
	// redacted from its pre-filter snapshot.
	if !apply.system && !apply.messages {
		return
	}
	for _, surface := range instructionSurfaces(caller) {
		text := assembleTargetInstruction(req, caller, surface)
		if text == "" {
			continue
		}
		if req.Instructions == nil {
			req.Instructions = map[string]string{}
		}
		req.Instructions[surface] = state.redact(text)
	}
}

// transmittedInstruction returns the instruction text for a surface, falling
// back to a locally assembled value when no filter ran.
func transmittedInstruction(req *IRRequest, surface, assembled string) string {
	if req == nil {
		return assembled
	}
	if filtered, ok := req.Instructions[surface]; ok {
		return filtered
	}
	return assembled
}

// instructionBoundaryKey is the cache-identity projection of every instruction
// text the selected target can transmit. Distinct transmitted instructions get
// distinct keys even when the normalized request representation is identical.
func instructionBoundaryKey(req *IRRequest, caller, outDialect string) string {
	if req == nil {
		return ""
	}
	surfaces := []string{}
	for _, surface := range instructionSurfaces(caller) {
		if surfaceMatchesDialect(surface, outDialect) {
			surfaces = append(surfaces, surface)
		}
	}
	if len(surfaces) == 0 {
		return "system\x00" + strconv.Itoa(len(req.System)) + "\x00" + req.System
	}
	parts := make([]string, 0, len(surfaces))
	for _, surface := range surfaces {
		text := targetInstruction(req, caller, surface)
		// Length-prefix each pair so caller text containing NUL bytes cannot
		// borrow another surface's framing.
		parts = append(parts, surface+"\x00"+strconv.Itoa(len(text))+"\x00"+text)
	}
	return strings.Join(parts, "\x00\x00")
}

func surfaceMatchesDialect(surface, outDialect string) bool {
	switch normalizeDialect(outDialect) {
	case "anthropic":
		return surface == "anthropic" || surface == "anthropic-tools"
	case "openai-responses":
		return surface == "openai-responses"
	default:
		return false
	}
}

// openAIToolsSystemPrompt mirrors the function-history Anthropic bridge. A Chat
// caller's leading system and developer messages are already folded into
// req.System by decoding and the bridge skips them in the raw array, while a
// Responses caller keeps them as leading input items that the bridge appends in
// order, joined with a blank line, with each message's text parts concatenated
// without a separator.
func openAIToolsSystemPrompt(req *IRRequest, caller string) string {
	if req == nil {
		return ""
	}
	var parts []string
	if req.System != "" {
		parts = append(parts, req.System)
	}
	if normalizeDialect(caller) != "openai-responses" {
		return strings.Join(parts, "\n\n")
	}
	items := valueAsSlice(req.Raw["input"])
	for _, value := range items {
		message, _ := value.(map[string]any)
		if typ := stringValue(message["type"]); typ != "" && typ != "message" {
			continue
		}
		role := stringValue(message["role"])
		if role != "system" && role != "developer" {
			continue
		}
		var text strings.Builder
		switch content := message["content"].(type) {
		case string:
			text.WriteString(content)
		case []any:
			for _, value := range content {
				part, _ := value.(map[string]any)
				switch stringValue(part["type"]) {
				case "text", "input_text", "output_text":
					text.WriteString(stringValue(part["text"]))
				}
			}
		}
		if text.Len() > 0 {
			parts = append(parts, text.String())
		}
	}
	return strings.Join(parts, "\n\n")
}

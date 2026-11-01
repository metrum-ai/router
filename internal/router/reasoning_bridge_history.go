// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
)

// reasoningHistoryPresent disables shadow replay, whose destination is not
// guaranteed to be in the same provider's reasoning namespace.
func reasoningHistoryPresent(req *IRRequest) bool {
	if req == nil {
		return false
	}
	for _, item := range valueAsSlice(req.Raw["input"]) {
		m, _ := item.(map[string]any)
		if stringValue(m["type"]) == "reasoning" {
			return true
		}
	}
	for _, item := range valueAsSlice(req.Raw["messages"]) {
		m, _ := item.(map[string]any)
		if rawValuePresent(m, "reasoning_content") || rawValuePresent(m, "reasoning") || rawValuePresent(m, "signature") || rawValuePresent(m, "encrypted_content") {
			return true
		}
		for _, block := range valueAsSlice(m["content"]) {
			b, _ := block.(map[string]any)
			if typ := stringValue(b["type"]); typ == "thinking" || typ == "redacted_thinking" || typ == "reasoning" {
				return true
			}
		}
	}
	return false
}

// foreignReasoningHistory strips opaque provider-owned reasoning history when
// crossing dialects. Native passthrough is unchanged.
func foreignReasoningHistory(req *IRRequest, caller, out string) (*IRRequest, error) {
	if req == nil || normalizeDialect(caller) == normalizeDialect(out) || !reasoningHistoryPresent(req) {
		return req, nil
	}
	copyReq := *req
	raw := make(map[string]any, len(req.Raw))
	for key, value := range req.Raw {
		raw[key] = value
	}
	switch normalizeDialect(caller) {
	case "anthropic", "openai-chat":
		messages := make([]any, 0, len(valueAsSlice(raw["messages"])))
		for _, value := range valueAsSlice(raw["messages"]) {
			message, ok := value.(map[string]any)
			if !ok {
				messages = append(messages, value)
				continue
			}
			clean := make(map[string]any, len(message))
			for k, v := range message {
				if k != "reasoning_content" && k != "reasoning" && k != "signature" && k != "encrypted_content" {
					clean[k] = v
				}
			}
			if blocks, ok := clean["content"].([]any); ok {
				filtered := make([]any, 0, len(blocks))
				for _, block := range blocks {
					m, _ := block.(map[string]any)
					if typ := stringValue(m["type"]); typ == "thinking" || typ == "redacted_thinking" || typ == "reasoning" {
						continue
					}
					filtered = append(filtered, block)
				}
				clean["content"] = filtered
			}
			messages = append(messages, clean)
		}
		raw["messages"] = messages
	case "openai-responses":
		if input, ok := raw["input"].([]any); ok {
			filtered := make([]any, 0, len(input))
			for _, item := range input {
				m, _ := item.(map[string]any)
				if stringValue(m["type"]) == "reasoning" {
					continue
				}
				filtered = append(filtered, item)
			}
			raw["input"] = filtered
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	decoded, err := decodeRequest(caller, encoded, http.Header{})
	if err != nil {
		return nil, err
	}
	copyReq.Raw, copyReq.Messages, copyReq.Input, copyReq.InputParts, copyReq.System = raw, decoded.Messages, decoded.Input, decoded.InputParts, decoded.System
	return &copyReq, nil
}

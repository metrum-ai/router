// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "encoding/base64"

// mediaPart encodes media without reducing it to text. File identifiers remain
// provider-owned: configuring a modality is an assertion that the target accepts it.
func mediaPart(p IRContentPart, dialect string) map[string]any {
	if p.Type == "audio" {
		return p.Raw
	}
	if p.Type != "document" {
		return nil
	}
	if dialect == "anthropic" {
		if stringValue(p.Raw["type"]) == "document" {
			return p.Raw
		}
		source := map[string]any{"type": "file", "file_id": p.FileID}
		if p.Data != "" {
			source = map[string]any{"type": "base64", "media_type": p.MediaType, "data": p.Data}
		}
		if p.ImageURL != "" {
			source = map[string]any{"type": "url", "url": p.ImageURL}
		}
		return map[string]any{"type": "document", "source": source}
	}
	file := map[string]any{}
	if src, ok := p.Raw["file"].(map[string]any); ok {
		for k, v := range src {
			file[k] = v
		}
	}
	if name := stringValue(p.Raw["filename"]); name != "" {
		file["filename"] = name
	}
	if p.FileID != "" {
		file["file_id"] = p.FileID
	}
	if p.Data != "" {
		file["file_data"] = "data:" + p.MediaType + ";base64," + p.Data
	}
	if p.ImageURL != "" {
		file["file_url"] = p.ImageURL
	}
	if src, ok := p.Raw["source"].(map[string]any); ok && stringValue(src["type"]) == "text" {
		file["file_data"] = "data:text/plain;base64," + base64.StdEncoding.EncodeToString([]byte(stringValue(src["data"])))
		file["filename"] = "document.txt"
	}
	if file["file_data"] != nil && file["filename"] == nil {
		file["filename"] = "document.pdf"
	}
	if dialect == "openai-chat" {
		return map[string]any{"type": "file", "file": file}
	}
	file["type"] = "input_file"
	return file
}

func hasMediaParts(parts []IRContentPart) bool {
	for _, p := range parts {
		if p.Type != "text" {
			return true
		}
	}
	return false
}

func requestHasMedia(req *IRRequest) bool {
	if hasMediaParts(req.InputParts) {
		return true
	}
	for _, m := range req.Messages {
		if hasMediaParts(m.Parts) {
			return true
		}
	}
	return false
}

func anthropicFormat(req *IRRequest) map[string]any {
	if req == nil {
		return nil
	}
	cfg, _ := req.Raw["output_config"].(map[string]any)
	format, _ := cfg["format"].(map[string]any)
	return format
}

func applyAnthropicFormat(body map[string]any, req *IRRequest, dialect string) {
	f := anthropicFormat(req)
	if f == nil {
		return
	}
	schema := map[string]any{"name": "response", "schema": f["schema"], "strict": true}
	if dialect == "openai-chat" {
		body["response_format"] = map[string]any{"type": "json_schema", "json_schema": schema}
	}
	if dialect == "openai-responses" {
		schema["type"] = "json_schema"
		body["text"] = map[string]any{"format": schema}
	}
}

// mediaAdmission checks the group before any upstream attempt. Native opaque
// Anthropic blocks are portable only to another native Anthropic endpoint.
func mediaTargetReason(target Target, req *IRRequest, caller, out string) string {
	if req == nil {
		return ""
	}
	if caller == "anthropic" && out != "anthropic" && (unknownAnthropicBlocks(req.Raw["messages"]) || unknownAnthropicBlocks(req.Raw["system"])) {
		return "unsupported_block"
	}
	if !targetSupportsInputModalities(target, requestInputModalities(req)) {
		return "unsupported_modality"
	}
	parts := append([]IRContentPart(nil), req.InputParts...)
	for _, m := range req.Messages {
		parts = append(parts, m.Parts...)
	}
	for _, p := range parts {
		if p.Type == "image" && p.FileID != "" && caller != out && out == "openai-chat" {
			return "unsupported_modality"
		}
	}
	if requestHasMedia(req) && (out == "replicate" || out == "gemini-generate-content") {
		return "unsupported_modality"
	}
	if anthropicFormat(req) != nil && (!target.StructuredOutput || (out != "anthropic" && out != "openai-chat" && out != "openai-responses")) {
		return "unsupported_parameter"
	}
	return ""
}

func (s *Service) mediaAdmission(group ModelGroup, req *IRRequest, caller string) string {
	first := ""
	for _, target := range group.Targets {
		out := targetDialect(s.cfg.Provider[target.Provider], target)
		reason := mediaTargetReason(target, req, caller, out)
		if reason == "" {
			return ""
		}
		if first == "" {
			first = reason
		}
	}
	return first
}

func unknownAnthropicBlocks(v any) bool {
	for _, item := range valueAsSlice(v) {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		typ := stringValue(m["type"])
		switch typ {
		case "", "text", "image", "document", "tool_use", "tool_result":
		default:
			return true
		}
		if unknownAnthropicBlocks(m["content"]) {
			return true
		}
	}
	return false
}

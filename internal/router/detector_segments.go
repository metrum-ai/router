// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "github.com/metrum-ai/router/pkg/detector"

func extractDetectorSegments(req *IRRequest) []detector.Segment {
	if req == nil || req.Raw == nil {
		return nil
	}
	out := make([]detector.Segment, 0, 4)
	add := func(kind, text string) {
		if len(out) >= detector.MaxSegments || len(text) == 0 || len(text) > detector.MaxSegmentBytes {
			return
		}
		total := 0
		for _, current := range out {
			total += len(current.Text)
		}
		if total+len(text) > detector.MaxSegmentBytesTotal {
			return
		}
		out = append(out, detector.Segment{Kind: kind, Index: len(out), Text: text})
	}
	var walk func(any)
	walk = func(value any) {
		for _, item := range valueAsSlice(value) {
			block, _ := item.(map[string]any)
			if block == nil {
				continue
			}
			typ := stringValue(block["type"])
			switch typ {
			case "tool_result":
				add("tool_result", detectorText(block["content"]))
			case "function_call_output":
				add("tool_result", detectorText(block["output"]))
			case "document":
				add("document", detectorDocumentText(block))
			}
			if typ != "tool_result" && typ != "function_call_output" && typ != "document" {
				if stringValue(block["role"]) == "tool" {
					add("tool_result", detectorText(block["content"]))
				}
				walk(block["messages"])
				walk(block["input"])
				walk(block["content"])
				walk(block["output"])
			}
		}
	}
	walk(req.Raw["messages"])
	walk(req.Raw["input"])
	return out
}

func detectorText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		var text string
		for _, partValue := range typed {
			part, _ := partValue.(map[string]any)
			if part == nil {
				continue
			}
			typ := stringValue(part["type"])
			if typ == "text" || typ == "input_text" || typ == "output_text" {
				text += stringValue(part["text"])
			}
		}
		return text
	default:
		return ""
	}
}

func detectorDocumentText(block map[string]any) string {
	if text := stringValue(block["text"]); text != "" {
		return text
	}
	source, _ := block["source"].(map[string]any)
	if source == nil || stringValue(source["type"]) != "text" {
		return ""
	}
	return stringValue(source["text"])
}

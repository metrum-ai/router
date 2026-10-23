// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// captureChatMessages retains only canonical text/chat-tool fields, never raw
// provider extensions. Unsupported multimodal shapes fail closed rather than
// quietly yielding a partial transcript that could be mistaken for replay data.
func captureChatMessages(messages []any) []any {
	out := make([]any, 0, len(messages))
	for _, value := range messages {
		m, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		role, _ := m["role"].(string)
		switch role {
		case "system", "developer", "user", "assistant", "tool":
		default:
			return nil
		}
		row := map[string]any{"role": role}
		if content, exists := m["content"]; exists {
			if content != nil {
				if _, ok := content.(string); !ok {
					return nil
				}
			}
			row["content"] = content
		}
		if role == "tool" {
			id, ok := m["tool_call_id"].(string)
			if !ok || id == "" {
				return nil
			}
			row["tool_call_id"] = id
		}
		if calls, exists := m["tool_calls"]; exists {
			if role != "assistant" {
				return nil
			}
			list, ok := calls.([]any)
			if !ok {
				return nil
			}
			clean := make([]any, 0, len(list))
			for _, value := range list {
				call, ok := value.(map[string]any)
				if !ok {
					return nil
				}
				id, _ := call["id"].(string)
				typ, _ := call["type"].(string)
				fn, ok := call["function"].(map[string]any)
				if !ok {
					return nil
				}
				name, _ := fn["name"].(string)
				args, ok := fn["arguments"].(string)
				if id == "" || typ != "function" || name == "" || !ok {
					return nil
				}
				clean = append(clean, map[string]any{"id": id, "type": typ, "function": map[string]any{"name": name, "arguments": args}})
			}
			row["tool_calls"] = clean
		}
		out = append(out, row)
	}
	return out
}

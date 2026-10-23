// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "encoding/json"

// trainingChatValid rejects incomplete/ambiguous function continuations. The
// harness attests task/schema correctness; the exporter independently checks
// transport integrity and never repairs malformed arguments or call IDs.
func trainingChatValid(messages []any) bool {
	pending := map[string]bool{}
	seen := map[string]bool{}
	for _, value := range messages {
		m, ok := value.(map[string]any)
		if !ok {
			return false
		}
		role, _ := m["role"].(string)
		if role == "tool" {
			id, _ := m["tool_call_id"].(string)
			if !pending[id] {
				return false
			}
			delete(pending, id)
			continue
		}
		if len(pending) > 0 {
			return false
		}
		if calls, exists := m["tool_calls"]; exists {
			list, ok := calls.([]any)
			if !ok || role != "assistant" {
				return false
			}
			for _, v := range list {
				c, ok := v.(map[string]any)
				if !ok {
					return false
				}
				id, _ := c["id"].(string)
				fn, ok := c["function"].(map[string]any)
				if !ok {
					return false
				}
				args, ok := fn["arguments"].(string)
				var parsed map[string]any
				if !ok || json.Unmarshal([]byte(args), &parsed) != nil || parsed == nil || id == "" || seen[id] {
					return false
				}
				seen[id] = true
				pending[id] = true
			}
		}
	}
	return len(pending) == 0
}

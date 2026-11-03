// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestInstructionTransmittedOnce pins the property that the function-history
// Anthropic bridge sends each instruction text exactly once. A Chat caller's
// leading system/developer messages are folded into req.System while decoding,
// and the bridge skips them in the raw array; a Responses caller keeps
// `instructions` plus leading items, which are joined once. A regression here
// would silently duplicate the Anthropic system prompt.
func TestInstructionTransmittedOnce(t *testing.T) {
	cases := []struct{ name, caller, body, want string }{
		{"chat-single-system", "openai-chat",
			`{"model":"m","messages":[{"role":"system","content":"SYS"},{"role":"user","content":"go"},{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"done"}]}`,
			"SYS"},
		{"chat-two-systems", "openai-chat",
			`{"model":"m","messages":[{"role":"system","content":"A"},{"role":"system","content":"B"},{"role":"user","content":"go"},{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"done"}]}`,
			"A\nB"},
		{"chat-multipart-system", "openai-chat",
			`{"model":"m","messages":[{"role":"system","content":[{"type":"text","text":"pa"},{"type":"text","text":"rt"}]},{"role":"user","content":"go"},{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"done"}]}`,
			"pa\nrt"},
		{"chat-developer", "openai-chat",
			`{"model":"m","messages":[{"role":"developer","content":"DEV"},{"role":"user","content":"go"},{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"done"}]}`,
			"DEV"},
		{"responses-instructions", "openai-responses",
			`{"model":"m","instructions":"INST","input":[{"role":"user","content":"go"},{"type":"function_call","call_id":"c1","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"done"}]}`,
			"INST"},
		{"responses-instructions-plus-item", "openai-responses",
			`{"model":"m","instructions":"INST","input":[{"role":"system","content":"SYS"},{"role":"user","content":"go"},{"type":"function_call","call_id":"c1","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"done"}]}`,
			"INST\n\nSYS"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := decodeRequest(c.caller, []byte(c.body), http.Header{})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := encodeOpenAIToolsToAnthropic("m", c.caller, req, anthropicToolTarget())
			if err != nil {
				t.Fatal(err)
			}
			var sent map[string]any
			if err := json.Unmarshal(raw, &sent); err != nil {
				t.Fatal(err)
			}
			system := stringValue(sent["system"])
			if system != c.want {
				t.Fatalf("system=%q want %q", system, c.want)
			}
			// No instruction segment may appear twice, even when it is a substring
			// of another segment.
			for _, segment := range strings.Split(c.want, "\n\n") {
				for _, piece := range strings.Split(segment, "\n") {
					if piece != "" && strings.Count(system, piece) != 1 {
						t.Fatalf("instruction text %q transmitted %d times in %q", piece, strings.Count(system, piece), system)
					}
				}
			}
		})
	}
}

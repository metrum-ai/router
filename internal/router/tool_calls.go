// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"strings"
)

// maxToolCallsPerResponse bounds how many tool calls a single response may
// contribute to routing reliability evidence. A response above the bound is
// not rejected; extraction stops at the bound and the attempt records the
// truncation.
const maxToolCallsPerResponse = 64

// IRToolCall is one tool call an upstream asked the caller to execute,
// normalized across the three caller-visible dialects. Arguments stay as
// upstream text for OpenAI dialects and become canonical JSON for an Anthropic
// input object, so validation reads one shape and the caller-facing bytes are
// never rebuilt from it.
type IRToolCall struct {
	ID        string
	Name      string
	Arguments string
	Index     int
	Dialect   string
}

// IRToolCallTruncated reports whether extraction hit maxToolCallsPerResponse.
type IRToolCallTruncated bool

// toolCallsFromResponse extracts normalized tool calls from a decoded
// non-streaming upstream body. Unknown dialects yield no calls.
func toolCallsFromResponse(dialect string, m map[string]any) ([]IRToolCall, IRToolCallTruncated) {
	if m == nil {
		return nil, false
	}
	switch normalizeDialect(dialect) {
	case "openai-chat":
		return toolCallsFromOpenAIChat(m)
	case "openai-responses":
		return toolCallsFromResponses(m)
	case "anthropic":
		return toolCallsFromAnthropic(m)
	default:
		return nil, false
	}
}

func toolCallsFromOpenAIChat(m map[string]any) ([]IRToolCall, IRToolCallTruncated) {
	choices, _ := m["choices"].([]any)
	if len(choices) == 0 {
		return nil, false
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	rawCalls, _ := message["tool_calls"].([]any)
	calls := make([]IRToolCall, 0, len(rawCalls))
	for i, item := range rawCalls {
		if len(calls) >= maxToolCallsPerResponse {
			return calls, true
		}
		call, ok := item.(map[string]any)
		if !ok {
			continue
		}
		function, _ := call["function"].(map[string]any)
		calls = append(calls, IRToolCall{
			ID:        stringValue(call["id"]),
			Name:      stringValue(function["name"]),
			Arguments: toolCallArgumentsText(function["arguments"]),
			Index:     i,
			Dialect:   "openai-chat",
		})
	}
	return calls, false
}

func toolCallsFromResponses(m map[string]any) ([]IRToolCall, IRToolCallTruncated) {
	output, _ := m["output"].([]any)
	calls := make([]IRToolCall, 0, len(output))
	index := 0
	for _, item := range output {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if stringValue(entry["type"]) != "function_call" {
			continue
		}
		if len(calls) >= maxToolCallsPerResponse {
			return calls, true
		}
		id := stringValue(entry["call_id"])
		if id == "" {
			id = stringValue(entry["id"])
		}
		calls = append(calls, IRToolCall{
			ID:        id,
			Name:      stringValue(entry["name"]),
			Arguments: toolCallArgumentsText(entry["arguments"]),
			Index:     index,
			Dialect:   "openai-responses",
		})
		index++
	}
	return calls, false
}

func toolCallsFromAnthropic(m map[string]any) ([]IRToolCall, IRToolCallTruncated) {
	content, _ := m["content"].([]any)
	calls := make([]IRToolCall, 0, len(content))
	index := 0
	for _, item := range content {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if stringValue(block["type"]) != "tool_use" {
			continue
		}
		if len(calls) >= maxToolCallsPerResponse {
			return calls, true
		}
		calls = append(calls, IRToolCall{
			ID:        stringValue(block["id"]),
			Name:      stringValue(block["name"]),
			Arguments: canonicalToolCallArguments(block["input"]),
			Index:     index,
			Dialect:   "anthropic",
		})
		index++
	}
	return calls, false
}

// toolCallArgumentsText keeps OpenAI argument text exactly as the upstream sent
// it. A provider that sends an object instead of a string is canonicalized so
// validation still sees JSON text.
func toolCallArgumentsText(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case nil:
		return ""
	default:
		return canonicalToolCallArguments(value)
	}
}

func canonicalToolCallArguments(v any) string {
	if v == nil {
		return ""
	}
	if text, ok := v.(string); ok {
		return text
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// toolCallAccumulator assembles streamed tool-call deltas into complete calls.
// It is pure state: callers feed fragments in arrival order and ask for the
// current snapshot; it never holds a response body.
type toolCallAccumulator struct {
	order      []int
	calls      map[int]*IRToolCall
	ids        map[int]string
	names      map[int]string
	args       map[int]*strings.Builder
	dialects   map[int]string
	truncatedC bool
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{
		calls:    map[int]*IRToolCall{},
		ids:      map[int]string{},
		names:    map[int]string{},
		args:     map[int]*strings.Builder{},
		dialects: map[int]string{},
	}
}

// addDelta records one fragment. Later fragments for the same index extend the
// name and argument text; the first non-empty id wins.
func (a *toolCallAccumulator) addDelta(dialect string, index int, id, name, arguments string) {
	if _, ok := a.calls[index]; !ok {
		if len(a.calls) >= maxToolCallsPerResponse {
			a.truncatedC = true
			return
		}
		a.order = append(a.order, index)
		a.calls[index] = &IRToolCall{Index: index, Dialect: dialect}
		a.dialects[index] = dialect
		a.args[index] = &strings.Builder{}
	}
	if id != "" && a.ids[index] == "" {
		a.ids[index] = id
	}
	if name != "" && !strings.Contains(a.names[index], name) {
		a.names[index] += name
	}
	if arguments != "" {
		if a.args[index] == nil {
			a.args[index] = &strings.Builder{}
		}
		if a.args[index].Len() < maxToolCallArgumentBytes {
			_, _ = a.args[index].WriteString(arguments)
		}
	}
}

// snapshot returns the calls assembled so far, in first-seen order.
func (a *toolCallAccumulator) snapshot() []IRToolCall {
	if a == nil {
		return nil
	}
	out := make([]IRToolCall, 0, len(a.order))
	for position, index := range a.order {
		arguments := ""
		if a.args[index] != nil {
			arguments = a.args[index].String()
		}
		out = append(out, IRToolCall{
			ID:        a.ids[index],
			Name:      a.names[index],
			Arguments: arguments,
			Index:     position,
			Dialect:   a.dialects[index],
		})
	}
	return out
}

func (a *toolCallAccumulator) truncated() bool {
	return a != nil && a.truncatedC
}

func truncatedToolCalls(calls []IRToolCall, truncated IRToolCallTruncated) ([]IRToolCall, bool) {
	return calls, bool(truncated)
}

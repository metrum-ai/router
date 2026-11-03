// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustToolMap(t *testing.T, body string) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return decoded
}

func TestToolCallsFromOpenAIChatResponse(t *testing.T) {
	body := `{"choices":[{"message":{"tool_calls":[
		{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.txt\"}"}},
		{"id":"call_2","type":"function","function":{"name":"write_file","arguments":{"path":"b.txt"}}}
	]},"finish_reason":"tool_calls"}]}`
	calls, truncated := toolCallsFromResponse("openai-chat", mustToolMap(t, body))
	if truncated {
		t.Fatal("unexpected truncation")
	}
	if len(calls) != 2 {
		t.Fatalf("calls=%d, want 2", len(calls))
	}
	if calls[0].ID != "call_1" || calls[0].Name != "read_file" || calls[0].Arguments != `{"path":"a.txt"}` {
		t.Fatalf("unexpected first call: %+v", calls[0])
	}
	// An object-shaped argument is canonicalized so validation still sees JSON.
	if calls[1].Arguments != `{"path":"b.txt"}` {
		t.Fatalf("object arguments: %q", calls[1].Arguments)
	}
	if calls[0].Index != 0 || calls[1].Index != 1 {
		t.Fatalf("indexes: %+v", calls)
	}
}

func TestToolCallsFromResponsesResponse(t *testing.T) {
	body := `{"output":[
		{"type":"message","content":[{"type":"output_text","text":"hello"}]},
		{"type":"function_call","call_id":"fc_1","name":"read_file","arguments":"{}"},
		{"type":"function_call","id":"fc_2","name":"list_files","arguments":""}
	]}`
	calls, _ := toolCallsFromResponse("openai-responses", mustToolMap(t, body))
	if len(calls) != 2 {
		t.Fatalf("calls=%d, want 2", len(calls))
	}
	if calls[0].ID != "fc_1" || calls[0].Name != "read_file" {
		t.Fatalf("unexpected first call: %+v", calls[0])
	}
	if calls[1].ID != "fc_2" {
		t.Fatalf("id fallback: %+v", calls[1])
	}
	if calls[0].Index != 0 || calls[1].Index != 1 {
		t.Fatalf("indexes are dense over tool calls only: %+v", calls)
	}
}

func TestToolCallsFromAnthropicResponse(t *testing.T) {
	body := `{"content":[
		{"type":"text","text":"reading"},
		{"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"a.txt"}}
	]}`
	calls, _ := toolCallsFromResponse("anthropic", mustToolMap(t, body))
	if len(calls) != 1 {
		t.Fatalf("calls=%d, want 1", len(calls))
	}
	if calls[0].ID != "toolu_1" || calls[0].Name != "read_file" || calls[0].Dialect != "anthropic" {
		t.Fatalf("unexpected call: %+v", calls[0])
	}
	if calls[0].Arguments != `{"path":"a.txt"}` {
		t.Fatalf("canonical arguments: %q", calls[0].Arguments)
	}
}

func TestToolCallExtractionIsBounded(t *testing.T) {
	var builder strings.Builder
	builder.WriteString(`{"choices":[{"message":{"tool_calls":[`)
	for i := 0; i < maxToolCallsPerResponse+4; i++ {
		if i > 0 {
			builder.WriteString(",")
		}
		builder.WriteString(`{"id":"c","function":{"name":"t","arguments":"{}"}}`)
	}
	builder.WriteString(`]}}]}`)
	calls, truncated := toolCallsFromResponse("openai-chat", mustToolMap(t, builder.String()))
	if !truncated {
		t.Fatal("expected truncation signal")
	}
	if len(calls) != maxToolCallsPerResponse {
		t.Fatalf("calls=%d, want %d", len(calls), maxToolCallsPerResponse)
	}
}

func TestDecodedResponsesCarryToolCallsAndKeepRaw(t *testing.T) {
	body := []byte(`{"id":"msg","choices":[{"message":{"content":"hi","tool_calls":[{"id":"c1","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
	resp, err := decodeChatPassthrough(body, "public-model")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "read_file" {
		t.Fatalf("passthrough tool calls: %+v", resp.ToolCalls)
	}
	encoded, err := json.Marshal(resp.Raw)
	if err != nil {
		t.Fatal(err)
	}
	var original, round any
	if err := json.Unmarshal(body, &original); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &round); err != nil {
		t.Fatal(err)
	}
	if string(mustMarshal(t, original)) != string(mustMarshal(t, round)) {
		t.Fatal("Raw changed while extracting tool calls")
	}

	resp, err = decodeUpstreamResponse("anthropic", []byte(`{"content":[{"type":"tool_use","id":"t1","name":"read_file","input":{}}]}`), "m")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Dialect != "anthropic" {
		t.Fatalf("upstream decode tool calls: %+v", resp.ToolCalls)
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestToolCallAccumulatorAssemblesFragments(t *testing.T) {
	acc := newToolCallAccumulator()
	acc.addDelta("openai-chat", 0, "call_1", "read_file", `{"pa`)
	acc.addDelta("openai-chat", 1, "call_2", "list_files", `{"dir":`)
	acc.addDelta("openai-chat", 0, "", "", `th":"a.txt"}`)
	acc.addDelta("openai-chat", 1, "", "", `"/tmp"}`)
	calls := acc.snapshot()
	if len(calls) != 2 {
		t.Fatalf("calls=%d, want 2", len(calls))
	}
	if calls[0].ID != "call_1" || calls[0].Arguments != `{"path":"a.txt"}` {
		t.Fatalf("first call: %+v", calls[0])
	}
	if calls[1].Arguments != `{"dir":"/tmp"}` {
		t.Fatalf("second call: %+v", calls[1])
	}
	if acc.truncated() {
		t.Fatal("unexpected truncation")
	}
}

func TestToolCallAccumulatorKeepsFirstIDAndDeduplicatesName(t *testing.T) {
	acc := newToolCallAccumulator()
	acc.addDelta("openai-chat", 0, "call_1", "read_file", "")
	acc.addDelta("openai-chat", 0, "call_2", "read_file", "")
	acc.addDelta("openai-chat", 0, "", "", "{}")
	calls := acc.snapshot()
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "read_file" {
		t.Fatalf("unexpected snapshot: %+v", calls)
	}
}

func toolsFixture(t *testing.T) []map[string]any {
	t.Helper()
	var tools []map[string]any
	body := `[
		{"type":"function","function":{"name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}}},
		{"type":"function","function":{"name":"set_mode","parameters":{"type":"object","properties":{"mode":{"type":"string","enum":["fast","safe"]}}}}},
		{"type":"function","function":{"name":"untyped"}}
	]`
	if err := json.Unmarshal([]byte(body), &tools); err != nil {
		t.Fatal(err)
	}
	return tools
}

func anthropicToolsFixture(t *testing.T) []map[string]any {
	t.Helper()
	var tools []map[string]any
	body := `[{"name":"read_file","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}]`
	if err := json.Unmarshal([]byte(body), &tools); err != nil {
		t.Fatal(err)
	}
	return tools
}

func enabledOptions() toolValidationOptions {
	return toolValidationOptions{
		ArgumentsJSON:     true,
		Schema:            toolSchemaModeBasic,
		NameMustBeOffered: true,
	}
}

func TestValidateToolCallsAcceptsAValidCall(t *testing.T) {
	calls := []IRToolCall{{ID: "c1", Name: "read_file", Arguments: `{"path":"a.txt"}`}}
	result := validateToolCalls(calls, toolsFixture(t), "tool_calls", enabledOptions())
	if result.Calls != 1 || result.Valid != 1 || result.Invalid != 0 || result.FirstFailure != "" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestValidateToolCallsDisabledByDefault(t *testing.T) {
	calls := []IRToolCall{{Name: "made_up", Arguments: "not json"}}
	result := validateToolCalls(calls, toolsFixture(t), "tool_calls", toolValidationOptions{})
	if result.Valid != 1 || result.Invalid != 0 || result.FirstFailure != "" {
		t.Fatalf("zero options must not validate: %+v", result)
	}
}

func TestValidateToolCallsClasses(t *testing.T) {
	cases := []struct {
		name       string
		call       IRToolCall
		finish     string
		want       string
		unknownArg bool
	}{
		{name: "invalid json", call: IRToolCall{Name: "read_file", Arguments: `{"path":`}, finish: "tool_calls", want: toolFailureInvalidJSON},
		{name: "truncated arguments", call: IRToolCall{Name: "read_file", Arguments: `{"path":`}, finish: "length", want: toolFailureTruncatedArgs},
		{name: "unknown tool", call: IRToolCall{Name: "rm_rf", Arguments: `{}`}, finish: "tool_calls", want: toolFailureUnknownTool},
		{name: "missing required", call: IRToolCall{Name: "read_file", Arguments: `{}`}, finish: "tool_calls", want: toolFailureMissingRequired},
		{name: "wrong type", call: IRToolCall{Name: "read_file", Arguments: `{"path":3}`}, finish: "tool_calls", want: toolFailureSchemaViolation},
		{name: "extra property", call: IRToolCall{Name: "read_file", Arguments: `{"path":"a","x":1}`}, finish: "tool_calls", want: toolFailureSchemaViolation},
		{name: "enum violation", call: IRToolCall{Name: "set_mode", Arguments: `{"mode":"turbo"}`}, finish: "tool_calls", want: toolFailureSchemaViolation},
		{name: "no schema is not a failure", call: IRToolCall{Name: "untyped", Arguments: `{"anything":true}`}, finish: "tool_calls", want: ""},
		{name: "empty arguments object is valid", call: IRToolCall{Name: "untyped", Arguments: `{}`}, finish: "tool_calls", want: ""},
		{name: "empty argument text is invalid", call: IRToolCall{Name: "untyped", Arguments: ""}, finish: "tool_calls", want: toolFailureInvalidJSON},
		{name: "array arguments are invalid", call: IRToolCall{Name: "untyped", Arguments: `[1,2]`}, finish: "tool_calls", want: toolFailureInvalidJSON},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := validateToolCalls([]IRToolCall{testCase.call}, toolsFixture(t), testCase.finish, enabledOptions())
			if result.FirstFailure != testCase.want {
				t.Fatalf("first failure=%q, want %q (result %+v)", result.FirstFailure, testCase.want, result)
			}
			if testCase.want == "" && result.Invalid != 0 {
				t.Fatalf("unexpected invalid count: %+v", result)
			}
			if testCase.want != "" && result.Invalid != 1 {
				t.Fatalf("invalid count=%d, want 1", result.Invalid)
			}
		})
	}
}

func TestValidateToolCallsUnsupportedKeywordNeverFails(t *testing.T) {
	tools := []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name": "read_file",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{"path": map[string]any{"$ref": "#/definitions/path"}},
				},
			},
		},
	}
	result := validateToolCalls(
		[]IRToolCall{{Name: "read_file", Arguments: `{"path":42}`}},
		tools, "tool_calls", enabledOptions(),
	)
	if result.Invalid != 0 || result.FirstFailure != "" {
		t.Fatalf("unsupported keywords must not fail a call: %+v", result)
	}
	if !result.UnsupportedKeyword {
		t.Fatal("expected the unsupported-keyword signal")
	}
}

func TestValidateToolCallsForcedChoice(t *testing.T) {
	opts := enabledOptions()
	opts.ForcedToolNames = []string{"read_file"}

	noCalls := validateToolCalls(nil, toolsFixture(t), "stop", opts)
	if noCalls.FirstFailure != toolFailureForcedMismatch {
		t.Fatalf("no-call forced mismatch: %+v", noCalls)
	}
	wrong := validateToolCalls([]IRToolCall{{Name: "set_mode", Arguments: `{"mode":"fast"}`}}, toolsFixture(t), "tool_calls", opts)
	if wrong.FirstFailure != toolFailureForcedMismatch {
		t.Fatalf("wrong forced tool: %+v", wrong)
	}
	right := validateToolCalls([]IRToolCall{{Name: "read_file", Arguments: `{"path":"a"}`}}, toolsFixture(t), "tool_calls", opts)
	if right.FirstFailure != "" || right.Valid != 1 {
		t.Fatalf("forced match: %+v", right)
	}
}

func TestValidateToolCallsAnthropicSchemaAndOversizeArguments(t *testing.T) {
	result := validateToolCalls(
		[]IRToolCall{{Name: "read_file", Arguments: `{}`}},
		anthropicToolsFixture(t), "tool_use", enabledOptions(),
	)
	if result.FirstFailure != toolFailureMissingRequired {
		t.Fatalf("anthropic input_schema required: %+v", result)
	}
	huge := `{"path":"` + strings.Repeat("a", maxToolCallArgumentBytes) + `"}`
	result = validateToolCalls([]IRToolCall{{Name: "read_file", Arguments: huge}}, toolsFixture(t), "tool_calls", enabledOptions())
	if result.FirstFailure != toolFailureInvalidJSON {
		t.Fatalf("oversize arguments: %+v", result)
	}
}

func TestValidateToolCallsCountsEveryClass(t *testing.T) {
	calls := []IRToolCall{
		{Name: "read_file", Arguments: `{"path":"ok"}`},
		{Name: "read_file", Arguments: `{`},
		{Name: "other", Arguments: `{}`},
	}
	result := validateToolCalls(calls, toolsFixture(t), "tool_calls", enabledOptions())
	if result.Calls != 3 || result.Valid != 1 || result.Invalid != 2 {
		t.Fatalf("counts: %+v", result)
	}
	counts := result.classCounts()
	if counts[toolFailureInvalidJSON] != 1 || counts[toolFailureUnknownTool] != 1 {
		t.Fatalf("class counts: %+v", counts)
	}
}

func TestValidateToolCallsDeepSchemaIsBoundedNotFailed(t *testing.T) {
	schema := map[string]any{"type": "object"}
	cursor := schema
	for i := 0; i < maxToolSchemaDepth+4; i++ {
		next := map[string]any{"type": "object"}
		cursor["properties"] = map[string]any{"n": next}
		cursor = next
	}
	tools := []map[string]any{{
		"type":     "function",
		"function": map[string]any{"name": "deep", "parameters": schema},
	}}
	value := "{}"
	for i := 0; i < maxToolSchemaDepth+4; i++ {
		value = `{"n":` + value + `}`
	}
	result := validateToolCalls([]IRToolCall{{Name: "deep", Arguments: value}}, tools, "tool_calls", enabledOptions())
	if result.FirstFailure != "" {
		t.Fatalf("depth bound must not fail: %+v", result)
	}
	if !result.UnsupportedKeyword {
		t.Fatal("depth bound should be reported as unsupported, not a failure")
	}
}

func TestOfferedToolSchemasIgnoresUnnamedTools(t *testing.T) {
	tools := []map[string]any{
		{"type": "function", "function": map[string]any{"name": ""}},
		{"name": "messages_tool", "input_schema": map[string]any{"type": "object"}},
	}
	names, schemas := offeredToolSchemas(tools)
	if names["messages_tool"] != true || len(schemas) != 1 {
		t.Fatalf("offered=%v schemas=%v", names, schemas)
	}
}

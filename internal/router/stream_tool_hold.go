// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/metrum-ai/router/internal/stream"
)

type streamToolCallHold struct {
	timeout             time.Duration
	maxBytes            int
	started             time.Time
	usedBytes           int
	budget              stream.LineBufferBudget
	reserved            int64
	emitting            int64
	held                []stream.Event
	calls               *toolCallAccumulator
	inspectors          []toolCallInspector
	indexes             map[int]bool
	chatToolChoices     map[int]bool
	chatFinishedChoices map[int]string
	hostedDeltas        map[int]string
	idle                *streamIdleReader
	baseIdle            time.Duration
}

// clearStreamToolCallHold starts a clean per-target attempt and releases any
// reservation left by a previous attempt before fallback selects a new group.
func clearStreamToolCallHold(rc *requestContext) {
	if rc == nil {
		return
	}
	if rc.streamToolCallHold != nil {
		rc.streamToolCallHold.reset()
		rc.streamToolCallHold = nil
	}
}

func newStreamToolCallHold(cfg ToolRoutingStreamingConfig, tools []map[string]any, options toolValidationOptions, budgets ...stream.LineBufferBudget) *streamToolCallHold {
	h := &streamToolCallHold{
		timeout:    time.Duration(cfg.HoldTimeoutMS) * time.Millisecond,
		maxBytes:   cfg.HoldMaxBytes,
		calls:      newToolCallAccumulator(),
		indexes:    map[int]bool{},
		inspectors: []toolCallInspector{&schemaToolCallInspector{tools: tools, options: options}},
	}
	if len(budgets) > 0 {
		h.budget = budgets[0]
	}
	return h
}

func (h *streamToolCallHold) bindIdleReader(reader *streamIdleReader, base time.Duration) {
	h.idle = reader
	h.baseIdle = base
}

// apply emits ordinary text immediately, while keeping all tool events private
// until the complete call set validates. Held bytes count against both the
// per-stream limit and the process-wide #926 line-buffer budget.
func (h *streamToolCallHold) apply(event stream.Event) ([]stream.Event, error) {
	if h == nil {
		return []stream.Event{event}, nil
	}
	if !h.started.IsZero() && time.Since(h.started) > h.timeout {
		h.reset()
		return nil, upstreamError{Class: "tool_call_hold_timeout", Message: "tool-call inspection hold timed out", Committed: true, Fallbackable: false}
	}
	var payload map[string]any
	if json.Unmarshal(event.Data, &payload) != nil {
		return []stream.Event{event}, nil
	}
	eventType := event.Type
	if eventType == "" {
		eventType = stringValue(payload["type"])
	}
	if err := h.recordHostedArgumentEvent(eventType, payload); err != nil {
		h.reset()
		return nil, err
	}
	toolEvent := false
	terminal := false
	terminalReason := ""
	passThrough := event
	switch eventType {
	case "response.output_item.added":
		item, _ := payload["item"].(map[string]any)
		if item != nil {
			idx := streamToolIndex(payload["output_index"])
			call, tool, recognized := responsesToolItemCall(item, idx)
			if tool && !recognized {
				return h.failClosedUnsupportedTool(stringValue(item["type"]))
			}
			if recognized {
				if isHostedResponsesTool(call) && !h.shouldInspectHostedCall(call) {
					return []stream.Event{event}, nil
				}
				initialArguments := call.Arguments
				if isHostedResponsesTool(call) {
					initialArguments = ""
				}
				h.calls.addDelta("openai-responses", idx, call.ID, call.Name, initialArguments)
				h.calls.setKind(idx, call.Kind)
				h.indexes[idx] = true
				toolEvent = true
				passThrough = stream.Event{}
			}
		}
	case "response.function_call_arguments.delta":
		idx := streamToolIndex(payload["output_index"])
		h.calls.addDelta("openai-responses", idx, "", "", stringValue(payload["delta"]))
		h.indexes[idx] = true
		toolEvent = true
		passThrough = stream.Event{}
	case "response.function_call_arguments.done":
		idx := streamToolIndex(payload["output_index"])
		h.calls.addDelta("openai-responses", idx, "", "", "")
		if err := h.setResponsesSnapshotArguments(idx, stringValue(payload["arguments"])); err != nil {
			h.reset()
			return nil, err
		}
		h.indexes[idx] = true
		toolEvent = true
		passThrough = stream.Event{}
	case "response.custom_tool_call_input.delta":
		idx := streamToolIndex(payload["output_index"])
		h.calls.addDelta("openai-responses", idx, "", "", stringValue(payload["delta"]))
		h.calls.setKind(idx, "custom_tool_call")
		h.indexes[idx] = true
		toolEvent = true
		passThrough = stream.Event{}
	case "response.custom_tool_call_input.done":
		idx := streamToolIndex(payload["output_index"])
		h.calls.addDelta("openai-responses", idx, "", "", "")
		h.calls.setKind(idx, "custom_tool_call")
		if err := h.setResponsesSnapshotArguments(idx, stringValue(payload["input"])); err != nil {
			h.reset()
			return nil, err
		}
		h.indexes[idx] = true
		toolEvent = true
		passThrough = stream.Event{}
	case "response.output_item.done":
		item, _ := payload["item"].(map[string]any)
		idx := streamToolIndex(payload["output_index"])
		if item != nil {
			call, tool, recognized := responsesToolItemCall(item, idx)
			if tool && !recognized {
				return h.failClosedUnsupportedTool(stringValue(item["type"]))
			}
			if recognized {
				if isHostedResponsesTool(call) && !h.shouldInspectHostedCall(call) {
					return []stream.Event{event}, nil
				}
				h.calls.addDelta("openai-responses", idx, call.ID, call.Name, "")
				h.calls.setKind(idx, call.Kind)
				if err := h.setResponsesSnapshotArguments(idx, call.Arguments, true); err != nil {
					h.reset()
					return nil, err
				}
				h.indexes[idx] = true
				toolEvent = true
				passThrough = stream.Event{}
			}
		}
	case "response.completed":
		terminal, terminalReason = true, "tool_calls"
		response, _ := payload["response"].(map[string]any)
		output, _ := response["output"].([]any)
		for idx, rawItem := range output {
			item, _ := rawItem.(map[string]any)
			if item == nil {
				continue
			}
			call, tool, recognized := responsesToolItemCall(item, idx)
			if tool && !recognized {
				return h.failClosedUnsupportedTool(stringValue(item["type"]))
			}
			if recognized {
				if isHostedResponsesTool(call) && !h.shouldInspectHostedCall(call) {
					continue
				}
				h.calls.addDelta("openai-responses", idx, call.ID, call.Name, "")
				h.calls.setKind(idx, call.Kind)
				if err := h.setResponsesSnapshotArguments(idx, call.Arguments, true); err != nil {
					h.reset()
					return nil, err
				}
				h.indexes[idx] = true
				toolEvent = true
			}
		}
		if toolEvent {
			event.Data, _ = json.Marshal(payload)
			passThrough = stream.Event{}
		}
	case "response.failed", "response.incomplete":
		sanitizeResponsesTerminalToolItems(payload)
		if len(h.held) > 0 {
			h.reset()
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, upstreamError{Class: "tool_call_hold_inspection_failed", Message: "unable to sanitize Responses terminal event", Err: err}
		}
		event.Data = encoded
		return []stream.Event{event}, nil
	case "content_block_start":
		block, _ := payload["content_block"].(map[string]any)
		if block != nil && stringValue(block["type"]) != "tool_use" && strings.Contains(stringValue(block["type"]), "tool") {
			return h.failClosedUnsupportedTool(stringValue(block["type"]))
		}
		if block != nil && stringValue(block["type"]) == "tool_use" {
			idx := streamToolIndex(payload["index"])
			initialArgs := ""
			if input, ok := block["input"].(map[string]any); ok && len(input) > 0 {
				initialArgs = canonicalToolCallArguments(input)
			}
			h.calls.addDelta("anthropic", idx, stringValue(block["id"]), stringValue(block["name"]), initialArgs)
			h.indexes[idx] = true
			toolEvent = true
			passThrough = stream.Event{}
		}
	case "content_block_delta":
		delta, _ := payload["delta"].(map[string]any)
		idx := streamToolIndex(payload["index"])
		if delta != nil && stringValue(delta["type"]) == "input_json_delta" {
			h.calls.addDelta("anthropic", idx, "", "", stringValue(delta["partial_json"]))
			h.indexes[idx] = true
			toolEvent = true
			passThrough = stream.Event{}
		}
	case "content_block_stop":
		idx := streamToolIndex(payload["index"])
		toolEvent = h.indexes[idx]
		if toolEvent {
			passThrough = stream.Event{}
		}
	case "message_stop":
		terminal, terminalReason = true, "tool_use"
	case "message_delta":
		delta, _ := payload["delta"].(map[string]any)
		stopReason := stringValue(delta["stop_reason"])
		if stopReason == "tool_use" {
			terminal, terminalReason = true, "tool_use"
		} else if stopReason != "" && len(h.held) > 0 {
			h.reset()
			return nil, upstreamError{Class: "tool_call_hold_incomplete", Message: "Anthropic stream ended with a non-tool stop reason while a tool call was incomplete", Committed: true, Fallbackable: false}
		}
	default:
		if strings.HasPrefix(eventType, "response.") && (strings.Contains(eventType, "call") || strings.Contains(eventType, "tool")) {
			kind := hostedResponsesCallEventType(eventType)
			if kind == "mcp_list_tools" {
				return []stream.Event{event}, nil
			}
			if kind != "" {
				idx := streamToolIndex(payload["output_index"])
				if !h.indexes[idx] {
					return []stream.Event{event}, nil
				}
				if strings.HasPrefix(eventType, "response.mcp_call_arguments.delta") || strings.HasPrefix(eventType, "response.code_interpreter_call_code.delta") {
					h.calls.addDelta("openai-responses", idx, "", "", stringValue(payload["delta"]))
				} else if strings.HasSuffix(eventType, ".done") {
					field := "arguments"
					if kind == "code_interpreter_call" {
						field = "code"
					}
					h.calls.setArguments(idx, stringValue(payload[field]))
				}
				toolEvent = true
				passThrough = stream.Event{}
			} else {
				return h.failClosedUnsupportedTool(eventType)
			}
		}
		if choices, ok := payload["choices"].([]any); ok {
			heldChoices := make([]any, 0, len(choices))
			for _, rawChoice := range choices {
				choice, _ := rawChoice.(map[string]any)
				if choice == nil {
					continue
				}
				choiceIndex := streamToolIndex(choice["index"])
				finishReason := stringValue(choice["finish_reason"])
				delta, _ := choice["delta"].(map[string]any)
				if rawLegacy, exists := delta["function_call"]; exists {
					legacy, ok := rawLegacy.(map[string]any)
					if !ok {
						return h.failClosedUnsupportedTool("function_call")
					}
					toolEvent = true
					heldChoice := map[string]any{"delta": map[string]any{"function_call": legacy}, "finish_reason": nil}
					if index, ok := choice["index"]; ok {
						heldChoice["index"] = index
					}
					heldChoices = append(heldChoices, heldChoice)
					legacyIndex := chatStreamCallIndex(choiceIndex, maxToolCallsPerResponse-1)
					h.markChatToolChoice(choiceIndex)
					h.calls.addDelta("openai-chat", legacyIndex, "", stringValue(legacy["name"]), stringValue(legacy["arguments"]))
					h.indexes[legacyIndex] = true
					delete(delta, "function_call")
				}
				if hasUnknownToolCallDelta(delta) {
					return h.failClosedUnsupportedTool("unknown_chat_tool_delta")
				}
				rawCalls, _ := delta["tool_calls"].([]any)
				if len(rawCalls) == 0 {
					h.markChatChoiceFinished(choiceIndex, finishReason)
					if len(heldChoices) > 0 {
						if len(delta) == 0 {
							choice["delta"] = map[string]any{}
						}
					}
					continue
				}
				toolEvent = true
				heldChoice := map[string]any{"delta": map[string]any{"tool_calls": rawCalls}, "finish_reason": nil}
				if index, ok := choice["index"]; ok {
					heldChoice["index"] = index
				}
				heldChoices = append(heldChoices, heldChoice)
				for _, rawCall := range rawCalls {
					call, _ := rawCall.(map[string]any)
					if call == nil {
						continue
					}
					idx := chatStreamCallIndex(choiceIndex, streamToolIndex(call["index"]))
					h.markChatToolChoice(choiceIndex)
					function, _ := call["function"].(map[string]any)
					if function == nil {
						function = map[string]any{}
					}
					h.calls.addDelta("openai-chat", idx, stringValue(call["id"]), stringValue(function["name"]), stringValue(function["arguments"]))
					h.indexes[idx] = true
				}
				h.markChatChoiceFinished(choiceIndex, finishReason)
				delete(delta, "tool_calls")
				if len(delta) == 0 {
					choice["delta"] = map[string]any{}
				}
			}
			if toolEvent {
				heldPayload := make(map[string]any, len(payload))
				for key, value := range payload {
					if key != "choices" && key != "usage" {
						heldPayload[key] = value
					}
				}
				heldPayload["choices"] = heldChoices
				encoded, err := json.Marshal(heldPayload)
				if err != nil {
					return nil, upstreamError{Class: "stream_translator_error", Message: "unable to split held tool-call frame", Err: err}
				}
				heldEvent := event
				heldEvent.Data = encoded
				event = heldEvent
				if len(choices) > 0 && choicesContainOnlyNonTerminalToolData(payload) && payload["usage"] == nil {
					passThrough = stream.Event{}
				} else {
					passThrough.Data, err = json.Marshal(payload)
					if err != nil {
						return nil, upstreamError{Class: "stream_translator_error", Message: "unable to split held tool-call frame", Err: err}
					}
				}
			}
			if len(h.chatToolChoices) > 0 {
				terminal = true
				for choiceIndex := range h.chatToolChoices {
					if h.chatFinishedChoices[choiceIndex] == "" {
						terminal = false
						break
					}
				}
				if terminal {
					terminalReason = h.chatToolChoicesFinishReason()
				}
			}
		}
	}
	if h.calls.truncated() {
		h.reset()
		return nil, upstreamError{Class: "tool_call_hold_data_truncated", Message: "streamed tool-call data exceeded inspection limits", Committed: true, Fallbackable: false}
	}
	if toolEvent {
		if h.started.IsZero() {
			h.started = time.Now()
			if h.idle != nil {
				h.idle.SetToolHoldDeadline(h.started.Add(h.timeout))
			}
		}
		frameBytes := len(event.Data) + len(event.Name) + 8
		if frameBytes > h.maxBytes-h.usedBytes {
			h.reset()
			return nil, upstreamError{Class: "tool_call_hold_too_large", Message: "tool-call inspection hold exceeded hold_max_bytes", Committed: true, Fallbackable: false}
		}
		if h.budget != nil && !h.budget.TryAcquire(int64(frameBytes)) {
			h.reset()
			return nil, upstreamError{Class: "upstream_stream_buffer_limit", Message: "process-wide streaming buffer budget exhausted while holding a tool call", Committed: true, Fallbackable: false}
		}
		h.usedBytes += frameBytes
		h.reserved += int64(frameBytes)
		h.held = append(h.held, event)
	}
	if eventType == "" && !terminal && len(h.held) > 0 {
		if chatPayloadHasFinishReason(payload) && (passThrough.Data != nil || passThrough.Name != "" || passThrough.Type != "") {
			frameBytes := len(passThrough.Data) + len(passThrough.Name) + 8
			if frameBytes > h.maxBytes-h.usedBytes {
				h.reset()
				return nil, upstreamError{Class: "tool_call_hold_too_large", Message: "tool-call inspection hold exceeded hold_max_bytes", Committed: true, Fallbackable: false}
			}
			if h.budget != nil && !h.budget.TryAcquire(int64(frameBytes)) {
				h.reset()
				return nil, upstreamError{Class: "upstream_stream_buffer_limit", Message: "process-wide streaming buffer budget exhausted while holding a tool call", Committed: true, Fallbackable: false}
			}
			h.usedBytes += frameBytes
			h.reserved += int64(frameBytes)
			h.held = append(h.held, passThrough)
			passThrough = stream.Event{}
		}
	}
	out := make([]stream.Event, 0, len(h.held)+1)
	if terminal && len(h.held) > 0 {
		calls := h.calls.snapshot()
		originalCalls := append([]IRToolCall(nil), calls...)
		replacementText := ""
		for _, inspector := range h.inspectors {
			if schema, ok := inspector.(*schemaToolCallInspector); ok {
				schema.stopReason = terminalReason
			}
			result, err := inspector.inspectStreamToolCalls(calls)
			if err != nil {
				h.reset()
				return nil, upstreamError{Class: "tool_call_hold_inspection_failed", Message: "streamed tool call inspection failed", Committed: true, Fallbackable: false, Err: err}
			}
			if result.Action == toolCallInspectionBlock {
				h.reset()
				return nil, upstreamError{Class: "tool_call_hold_validation_failed", Message: "streamed tool call failed inspection", Committed: true, Fallbackable: false}
			}
			if result.Action == toolCallInspectionResultText {
				replacementText = result.Text
				break
			}
			if result.Action == toolCallInspectionRedact {
				if !validToolCallRedaction(calls, result.Calls) {
					h.reset()
					return nil, upstreamError{Class: "tool_call_hold_inspection_failed", Message: "tool-call inspector returned an invalid redaction", Committed: true, Fallbackable: false}
				}
				calls = result.Calls
			} else if result.Action != "" && result.Action != toolCallInspectionAllow {
				h.reset()
				return nil, upstreamError{Class: "tool_call_hold_inspection_failed", Message: "tool-call inspector returned an unknown action", Committed: true, Fallbackable: false}
			}
			if result.Action != toolCallInspectionRedact && result.Calls != nil && toolCallsChanged(calls, result.Calls) {
				h.reset()
				return nil, upstreamError{Class: "tool_call_hold_inspection_failed", Message: "allowing inspector changed tool calls without redacting", Committed: true, Fallbackable: false}
			}
		}
		if replacementText != "" {
			out = append(out, toolCallResultEvents(calls, event, replacementText)...)
			h.finishEmission()
			passThrough = stream.Event{}
		} else if toolCallsChanged(originalCalls, calls) {
			var err error
			h.held, err = rewriteHeldToolCalls(h.held, calls)
			if err != nil {
				h.reset()
				return nil, upstreamError{Class: "tool_call_hold_inspection_failed", Message: "unable to apply tool-call redaction", Committed: true, Fallbackable: false, Err: err}
			}
		}
		if replacementText == "" {
			out = append(out, h.held...)
			h.finishEmission()
		}
	}
	if passThrough.Data != nil || passThrough.Name != "" || passThrough.Type != "" {
		out = append(out, passThrough)
	}
	return out, nil
}

func (h *streamToolCallHold) recordHostedArgumentEvent(eventType string, payload map[string]any) error {
	kind := hostedResponsesCallEventType(eventType)
	if kind != "mcp_call" && kind != "code_interpreter_call" {
		return nil
	}
	idx := streamToolIndex(payload["output_index"])
	if !h.indexes[idx] {
		return nil
	}
	if strings.HasSuffix(eventType, ".delta") {
		if h.hostedDeltas == nil {
			h.hostedDeltas = make(map[int]string)
		}
		h.hostedDeltas[idx] += stringValue(payload["delta"])
		return nil
	}
	if !strings.HasSuffix(eventType, ".done") {
		return nil
	}
	delta, ok := h.hostedDeltas[idx]
	if !ok {
		return nil
	}
	field := "arguments"
	if kind == "code_interpreter_call" {
		field = "code"
	}
	if delta != stringValue(payload[field]) {
		return upstreamError{Class: "tool_call_snapshot_mismatch", Message: "Responses tool argument snapshot does not match streamed arguments", Committed: true, Fallbackable: false}
	}
	return nil
}

func (h *streamToolCallHold) failClosedUnsupportedTool(kind string) ([]stream.Event, error) {
	h.reset()
	return nil, upstreamError{Class: "tool_call_hold_unsupported_type", Message: fmt.Sprintf("streamed tool item type %q cannot be inspected safely", kind), Committed: true, Fallbackable: false}
}

func hasUnknownToolCallDelta(delta map[string]any) bool {
	for key := range delta {
		if key == "tool_calls" {
			continue
		}
		if strings.Contains(key, "tool_call") || strings.Contains(key, "function_call") || strings.Contains(key, "tool") {
			return true
		}
	}
	return false
}

func (h *streamToolCallHold) pending() bool {
	return h != nil && len(h.held) > 0
}

func (h *streamToolCallHold) shouldInspectHostedCall(call IRToolCall) bool {
	if h == nil {
		return false
	}
	for _, inspector := range h.inspectors {
		switch firewall := inspector.(type) {
		case firewallToolCallInspector:
			return firewall.shouldInspectHostedCall(call)
		case *firewallToolCallInspector:
			return firewall.shouldInspectHostedCall(call)
		}
	}
	return false
}

func (h *streamToolCallHold) setResponsesSnapshotArguments(index int, arguments string, compareHostedPayload ...bool) error {
	if h == nil || h.calls == nil || h.calls.args[index] == nil {
		return upstreamError{Class: "tool_call_snapshot_mismatch", Message: "Responses tool arguments have no matching streamed call", Committed: true, Fallbackable: false}
	}
	accumulated := h.calls.args[index].String()
	if isHostedResponsesTool(IRToolCall{Dialect: "openai-responses", Kind: h.calls.kinds[index]}) && (len(compareHostedPayload) == 0 || !compareHostedPayload[0]) {
		// Hosted snapshots carry lifecycle metadata in the item envelope. Their
		// call payload is normalized separately from status and other metadata.
		// Keep their argument stream authoritative and update from the payload-only
		// snapshot without comparing the complete hosted item representation.
		h.calls.setArguments(index, arguments)
		return nil
	}
	if accumulated == "" {
		h.calls.setArguments(index, arguments)
		return nil
	}
	if accumulated != arguments {
		return upstreamError{Class: "tool_call_snapshot_mismatch", Message: "Responses tool argument snapshot does not match streamed arguments", Committed: true, Fallbackable: false}
	}
	return nil
}

func hostedResponsesCallEventType(eventType string) string {
	for _, kind := range []string{
		"web_search_call", "file_search_call", "code_interpreter_call", "image_generation_call",
		"browser_search_call", "vector_store_search_call", "mcp_call", "mcp_list_tools",
	} {
		if strings.HasPrefix(eventType, "response."+kind+".") {
			return kind
		}
	}
	switch eventType {
	case "response.mcp_call_arguments.delta", "response.mcp_call_arguments.done":
		return "mcp_call"
	case "response.code_interpreter_call_code.delta", "response.code_interpreter_call_code.done":
		return "code_interpreter_call"
	}
	return ""
}

func responsesToolItemCall(item map[string]any, index int) (IRToolCall, bool, bool) {
	kind := stringValue(item["type"])
	if strings.EqualFold(kind, "mcp_list_tools") {
		return IRToolCall{Index: index, Dialect: "openai-responses", Kind: kind}, false, false
	}
	toolBearing := strings.Contains(kind, "call") || strings.Contains(kind, "tool")
	call := IRToolCall{Index: index, Dialect: "openai-responses", Kind: kind, Name: stringValue(item["name"])}
	if call.Name == "" && isHostedResponsesTool(call) {
		call.Name = kind
	}
	call.ID = stringValue(item["call_id"])
	if call.ID == "" {
		call.ID = stringValue(item["id"])
	}
	switch kind {
	case "function_call":
		call.Arguments = toolCallArgumentsText(item["arguments"])
	case "custom_tool_call":
		call.Arguments = toolCallArgumentsText(item["input"])
	case "local_shell_call":
		call.Arguments = canonicalToolCallArguments(item["action"])
	case "web_search_call", "file_search_call", "code_interpreter_call", "image_generation_call", "browser_search_call", "vector_store_search_call", "mcp_call":
		call.Arguments = canonicalToolCallArguments(responsesHostedToolPayload(kind, item))
	default:
		return call, toolBearing, false
	}
	return call, true, true
}

func responsesHostedToolPayload(kind string, item map[string]any) any {
	payload := make(map[string]any)
	for _, field := range responsesHostedToolPayloadFields(kind) {
		if value, ok := item[field]; ok {
			payload[field] = value
		}
	}
	if len(payload) == 1 {
		for _, value := range payload {
			return value
		}
	}
	return payload
}

func responsesHostedToolPayloadFields(kind string) []string {
	switch kind {
	case "web_search_call":
		return []string{"action", "query", "search_query"}
	case "file_search_call":
		return []string{"queries", "query"}
	case "code_interpreter_call":
		return []string{"code"}
	case "image_generation_call":
		return []string{"prompt", "input"}
	case "browser_search_call":
		return []string{"action", "query", "url"}
	case "vector_store_search_call":
		return []string{"query", "queries"}
	case "mcp_call":
		return []string{"arguments"}
	default:
		return nil
	}
}

func sanitizeResponsesTerminalToolItems(payload map[string]any) {
	response, _ := payload["response"].(map[string]any)
	output, _ := response["output"].([]any)
	for index, rawItem := range output {
		item, _ := rawItem.(map[string]any)
		if item == nil || !shouldSanitizeResponsesOutputItem(stringValue(item["type"])) {
			continue
		}
		safe := make(map[string]any, 4)
		for _, field := range []string{"id", "type", "status", "error"} {
			if value, ok := item[field]; ok {
				safe[field] = value
			}
		}
		output[index] = safe
	}
}

func shouldSanitizeResponsesOutputItem(kind string) bool {
	kind = strings.ToLower(strings.TrimSpace(kind))
	return kind != "message" && kind != "reasoning"
}

func (h *streamToolCallHold) reset() {
	if h != nil && h.budget != nil {
		h.budget.Release(h.reserved + h.emitting)
	}
	if h == nil {
		return
	}
	h.reserved = 0
	h.emitting = 0
	h.clear()
}

// finishEmission transfers held-byte accounting to the returned events. The
// stream handler calls releaseEmitted only after every returned frame is
// written, while reset also covers downstream errors and panics.
func (h *streamToolCallHold) finishEmission() {
	if h == nil {
		return
	}
	h.emitting += h.reserved
	h.reserved = 0
	h.clear()
}

func (h *streamToolCallHold) releaseEmitted() {
	if h == nil || h.emitting <= 0 {
		return
	}
	if h.budget != nil {
		h.budget.Release(h.emitting)
	}
	h.emitting = 0
}

func (h *streamToolCallHold) clear() {
	if h != nil && !h.started.IsZero() && h.idle != nil {
		h.idle.SetIdle(h.baseIdle, false)
	}
	h.started = time.Time{}
	h.usedBytes = 0
	h.held = nil
	h.calls = newToolCallAccumulator()
	h.indexes = map[int]bool{}
	h.chatToolChoices = nil
	h.chatFinishedChoices = nil
	h.hostedDeltas = nil
}

func (h *streamToolCallHold) markChatToolChoice(index int) {
	if h.chatToolChoices == nil {
		h.chatToolChoices = make(map[int]bool)
	}
	h.chatToolChoices[index] = true
}

func (h *streamToolCallHold) markChatChoiceFinished(index int, finishReason string) {
	if finishReason == "" || !h.chatToolChoices[index] {
		return
	}
	if h.chatFinishedChoices == nil {
		h.chatFinishedChoices = make(map[int]string)
	}
	h.chatFinishedChoices[index] = finishReason
}

func (h *streamToolCallHold) chatToolChoicesFinishReason() string {
	var fallback string
	choices := make([]int, 0, len(h.chatToolChoices))
	for index := range h.chatToolChoices {
		choices = append(choices, index)
	}
	sort.Ints(choices)
	for _, index := range choices {
		reason := h.chatFinishedChoices[index]
		if reason == "length" || reason == "max_tokens" {
			return reason
		}
		if fallback == "" && reason != "" {
			fallback = reason
		}
	}
	if fallback != "" {
		return fallback
	}
	return "tool_calls"
}

func chatStreamCallIndex(choiceIndex, callIndex int) int {
	if choiceIndex < 0 {
		choiceIndex = 0
	}
	if callIndex < 0 {
		callIndex = 0
	}
	return choiceIndex*maxToolCallsPerResponse + callIndex
}

func streamToolIndex(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	default:
		return 0
	}
}

func choicesContainOnlyNonTerminalToolData(payload map[string]any) bool {
	choices, _ := payload["choices"].([]any)
	if len(choices) == 0 {
		return false
	}
	for _, rawChoice := range choices {
		choice, _ := rawChoice.(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if len(delta) != 0 || stringValue(choice["finish_reason"]) != "" {
			return false
		}
	}
	return true
}

func chatPayloadHasFinishReason(payload map[string]any) bool {
	choices, _ := payload["choices"].([]any)
	for _, rawChoice := range choices {
		choice, _ := rawChoice.(map[string]any)
		if stringValue(choice["finish_reason"]) != "" {
			return true
		}
	}
	return false
}

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/metrum-ai/router/internal/stream"
)

type streamToolCallHold struct {
	timeout    time.Duration
	maxBytes   int
	started    time.Time
	usedBytes  int
	budget     stream.LineBufferBudget
	reserved   int64
	emitting   int64
	held       []stream.Event
	calls      *toolCallAccumulator
	inspectors []toolCallInspector
	indexes    map[int]bool
	idle       *streamIdleReader
	baseIdle   time.Duration
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
				h.calls.addDelta("openai-responses", idx, call.ID, call.Name, call.Arguments)
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
		h.calls.setArguments(idx, stringValue(payload["arguments"]))
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
		h.calls.setArguments(idx, stringValue(payload["input"]))
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
				h.calls.addDelta("openai-responses", idx, call.ID, call.Name, "")
				h.calls.setKind(idx, call.Kind)
				h.calls.setArguments(idx, call.Arguments)
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
				h.calls.addDelta("openai-responses", idx, call.ID, call.Name, "")
				h.calls.setKind(idx, call.Kind)
				h.calls.setArguments(idx, call.Arguments)
				h.indexes[idx] = true
				toolEvent = true
			}
		}
		if toolEvent {
			event.Data, _ = json.Marshal(payload)
			passThrough = stream.Event{}
		}
	case "response.failed", "response.incomplete":
		if err := stripResponsesTerminalToolArguments(payload); err != nil {
			h.reset()
			return nil, err
		}
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
			return h.failClosedUnsupportedTool(eventType)
		}
		if choices, ok := payload["choices"].([]any); ok {
			heldChoices := make([]any, 0, len(choices))
			for _, rawChoice := range choices {
				choice, _ := rawChoice.(map[string]any)
				if choice == nil {
					continue
				}
				if finish := stringValue(choice["finish_reason"]); finish != "" {
					terminal, terminalReason = true, finish
				}
				delta, _ := choice["delta"].(map[string]any)
				rawCalls, _ := delta["tool_calls"].([]any)
				if len(rawCalls) == 0 {
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
					idx := streamToolIndex(call["index"])
					function, _ := call["function"].(map[string]any)
					if function == nil {
						function = map[string]any{}
					}
					h.calls.addDelta("openai-chat", idx, stringValue(call["id"]), stringValue(function["name"]), stringValue(function["arguments"]))
					h.indexes[idx] = true
				}
				delete(delta, "tool_calls")
				if len(delta) == 0 {
					choice["delta"] = map[string]any{}
				}
			}
			if toolEvent {
				heldPayload := make(map[string]any, len(payload))
				for key, value := range payload {
					if key != "choices" {
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
				if len(choices) > 0 && choicesContainOnlyNonTerminalToolData(payload) {
					passThrough = stream.Event{}
				} else {
					passThrough.Data, err = json.Marshal(payload)
					if err != nil {
						return nil, upstreamError{Class: "stream_translator_error", Message: "unable to split held tool-call frame", Err: err}
					}
				}
			}
		}
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
	out := make([]stream.Event, 0, len(h.held)+1)
	if terminal && len(h.held) > 0 {
		calls := h.calls.snapshot()
		originalCalls := append([]IRToolCall(nil), calls...)
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
		if toolCallsChanged(originalCalls, calls) {
			var err error
			h.held, err = rewriteHeldToolCalls(h.held, calls)
			if err != nil {
				h.reset()
				return nil, upstreamError{Class: "tool_call_hold_inspection_failed", Message: "unable to apply tool-call redaction", Committed: true, Fallbackable: false, Err: err}
			}
		}
		out = append(out, h.held...)
		h.finishEmission()
	}
	if passThrough.Data != nil || passThrough.Name != "" || passThrough.Type != "" {
		out = append(out, passThrough)
	}
	return out, nil
}

func (h *streamToolCallHold) failClosedUnsupportedTool(kind string) ([]stream.Event, error) {
	h.reset()
	return nil, upstreamError{Class: "tool_call_hold_unsupported_type", Message: fmt.Sprintf("streamed tool item type %q cannot be inspected safely", kind), Committed: true, Fallbackable: false}
}

func (h *streamToolCallHold) pending() bool {
	return h != nil && len(h.held) > 0
}

func responsesToolItemCall(item map[string]any, index int) (IRToolCall, bool, bool) {
	kind := stringValue(item["type"])
	toolBearing := strings.Contains(kind, "call") || strings.Contains(kind, "tool")
	call := IRToolCall{Index: index, Dialect: "openai-responses", Kind: kind, Name: stringValue(item["name"])}
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
	default:
		return call, toolBearing, false
	}
	return call, true, true
}

func stripResponsesTerminalToolArguments(payload map[string]any) error {
	response, _ := payload["response"].(map[string]any)
	output, _ := response["output"].([]any)
	for _, rawItem := range output {
		item, _ := rawItem.(map[string]any)
		if item == nil {
			continue
		}
		_, tool, recognized := responsesToolItemCall(item, 0)
		if tool && !recognized {
			return upstreamError{Class: "tool_call_hold_unsupported_type", Message: fmt.Sprintf("streamed tool item type %q cannot be inspected safely", stringValue(item["type"])), Committed: true, Fallbackable: false}
		}
		if recognized {
			delete(item, "arguments")
			delete(item, "input")
			delete(item, "action")
		}
	}
	return nil
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

// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/metrum-ai/router/internal/stream"
)

type toolCallInspectionAction string

const (
	toolCallInspectionAllow      toolCallInspectionAction = "allow"
	toolCallInspectionBlock      toolCallInspectionAction = "block"
	toolCallInspectionRedact     toolCallInspectionAction = "redact"
	toolCallInspectionResultText toolCallInspectionAction = "result_text"
)

// toolCallInspectionResult is one inspector's decision for the complete call
// set. Redactors return the same calls with only Arguments changed. Inspectors
// run in order, and each receives the preceding inspector's result.
type toolCallInspectionResult struct {
	Action toolCallInspectionAction
	Calls  []IRToolCall
	Text   string
}

// firewallToolCallInspector runs the same firewall and approval state machine
// used for completed responses, at the bounded stream hold boundary.
type firewallToolCallInspector struct {
	service  *Service
	rc       *requestContext
	callerID string
	request  *IRRequest
}

func (i firewallToolCallInspector) inspectStreamToolCalls(calls []IRToolCall, truncated bool) (toolCallInspectionResult, error) {
	if i.service == nil || i.rc == nil || len(calls) == 0 {
		return toolCallInspectionResult{Action: toolCallInspectionAllow}, nil
	}
	fingerprint, ok := toolCallApprovalFingerprint(i.callerID, calls)
	approvalID, approvalState := "", ""
	if ok && i.service.toolApprovals != nil {
		approvalID, approvalState = i.service.toolApprovals.lookup(fingerprint)
	}
	outcome := i.service.inspectToolCallsForFirewall(i.callerID, i.request, calls, truncated, approvalState == "release")
	disposition := i.service.resolveToolFirewallDecision(i.rc, i.callerID, fingerprint, approvalID, approvalState, outcome)
	if disposition.pending {
		text, _ := json.Marshal(map[string]string{"status": "approval_pending", "approval_id": disposition.approvalID, "message": "Retry this request after approval."})
		return toolCallInspectionResult{Action: toolCallInspectionResultText, Text: string(text)}, nil
	}
	if disposition.blocked {
		return toolCallInspectionResult{Action: toolCallInspectionResultText, Text: "Tool call blocked by router policy."}, nil
	}
	return toolCallInspectionResult{Action: toolCallInspectionAllow}, nil
}

// toolCallInspector inspects a complete streamed tool-call set before any
// held frame is released. Implementations may allow, block, or redact calls.
// Request-specific state is captured by the inspector when it is registered.
type toolCallInspector interface {
	inspectStreamToolCalls(calls []IRToolCall, truncated bool) (toolCallInspectionResult, error)
}

// schemaToolCallInspector is the first inspector in the chain. The firewall
// (#891) and outbound secret DLP (#890) register their inspectors at the same
// held-call boundary after their dependencies merge.
type schemaToolCallInspector struct {
	tools      []map[string]any
	options    toolValidationOptions
	stopReason string
}

func (i schemaToolCallInspector) inspectStreamToolCalls(calls []IRToolCall, _ bool) (toolCallInspectionResult, error) {
	validation := validateToolCalls(calls, i.tools, i.stopReason, i.options)
	if validation.Invalid > 0 {
		return toolCallInspectionResult{Action: toolCallInspectionBlock}, nil
	}
	return toolCallInspectionResult{Action: toolCallInspectionAllow}, nil
}

func validToolCallRedaction(original, redacted []IRToolCall) bool {
	if len(original) == 0 || len(original) != len(redacted) {
		return false
	}
	byIndex := make(map[int]IRToolCall, len(original))
	for _, call := range original {
		byIndex[call.Index] = call
	}
	for _, call := range redacted {
		prior, ok := byIndex[call.Index]
		if !ok || prior.ID != call.ID || prior.Name != call.Name || prior.Dialect != call.Dialect {
			return false
		}
		delete(byIndex, call.Index)
	}
	return len(byIndex) == 0
}

func toolCallsChanged(original, current []IRToolCall) bool {
	if len(original) != len(current) {
		return true
	}
	for i := range original {
		if original[i] != current[i] {
			return true
		}
	}
	return false
}

// rewriteHeldToolCalls preserves each protocol's event sequence while moving
// redacted arguments into the provider's argument-bearing fields.
func rewriteHeldToolCalls(events []stream.Event, calls []IRToolCall) ([]stream.Event, error) {
	arguments := make(map[int]string, len(calls))
	for _, call := range calls {
		arguments[call.Index] = call.Arguments
	}
	usesAnthropicDelta := map[int]bool{}
	for _, event := range events {
		var payload map[string]any
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return nil, fmt.Errorf("decode held event for redaction: %w", err)
		}
		if stringValue(payload["type"]) == "content_block_delta" {
			if delta, ok := payload["delta"].(map[string]any); ok && stringValue(delta["type"]) == "input_json_delta" {
				usesAnthropicDelta[streamToolIndex(payload["index"])] = true
			}
		}
	}
	first := map[int]bool{}
	out := make([]stream.Event, len(events))
	copy(out, events)
	for i, event := range events {
		var payload map[string]any
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return nil, fmt.Errorf("decode held event for redaction: %w", err)
		}
		eventType := stringValue(payload["type"])
		changed := false
		switch {
		case eventType == "response.output_item.added" || eventType == "response.output_item.done":
			idx := streamToolIndex(payload["output_index"])
			item, _ := payload["item"].(map[string]any)
			if item != nil && stringValue(item["type"]) == "function_call" {
				if replacement, ok := arguments[idx]; ok && (eventType != "response.output_item.added" || stringValue(item["arguments"]) != "") {
					item["arguments"] = replacement
					if eventType == "response.output_item.added" {
						first[idx] = true
					}
					changed = true
				}
			}
		case eventType == "response.function_call_arguments.delta" || eventType == "response.function_call_arguments.done":
			idx := streamToolIndex(payload["output_index"])
			if replacement, ok := arguments[idx]; ok {
				if eventType == "response.function_call_arguments.done" || !first[idx] {
					payload["delta"] = replacement
					if eventType == "response.function_call_arguments.done" {
						payload["arguments"] = replacement
						delete(payload, "delta")
					}
					first[idx] = true
				} else {
					payload["delta"] = ""
				}
				changed = true
			}
		case eventType == "content_block_start":
			idx := streamToolIndex(payload["index"])
			block, _ := payload["content_block"].(map[string]any)
			if block != nil && stringValue(block["type"]) == "tool_use" {
				if replacement, ok := arguments[idx]; ok {
					if usesAnthropicDelta[idx] {
						block["input"] = map[string]any{}
					} else {
						var input any
						if err := json.Unmarshal([]byte(replacement), &input); err != nil {
							return nil, fmt.Errorf("decode redacted Anthropic tool arguments: %w", err)
						}
						block["input"] = input
					}
					changed = true
				}
			}
		case eventType == "content_block_delta":
			idx := streamToolIndex(payload["index"])
			delta, _ := payload["delta"].(map[string]any)
			if delta != nil && stringValue(delta["type"]) == "input_json_delta" {
				if replacement, ok := arguments[idx]; ok {
					if !first[idx] {
						delta["partial_json"] = replacement
						first[idx] = true
					} else {
						delta["partial_json"] = ""
					}
					changed = true
				}
			}
		default:
			if choices, ok := payload["choices"].([]any); ok {
				for _, raw := range choices {
					choice, _ := raw.(map[string]any)
					delta, _ := choice["delta"].(map[string]any)
					calls, _ := delta["tool_calls"].([]any)
					for _, rawCall := range calls {
						call, _ := rawCall.(map[string]any)
						idx := streamToolIndex(call["index"])
						function, _ := call["function"].(map[string]any)
						if function == nil {
							continue
						}
						if replacement, ok := arguments[idx]; ok {
							if !first[idx] {
								function["arguments"] = replacement
								first[idx] = true
							} else {
								function["arguments"] = ""
							}
							changed = true
						}
					}
				}
			}
		}
		if changed {
			encoded, err := json.Marshal(payload)
			if err != nil {
				return nil, fmt.Errorf("encode redacted held event: %w", err)
			}
			out[i].Data = encoded
		}
	}
	return out, nil
}

func toolCallResultEvents(calls []IRToolCall, terminal stream.Event, text string) []stream.Event {
	var payload map[string]any
	_ = json.Unmarshal(terminal.Data, &payload)
	makeEvent := func(name string, value any) stream.Event {
		data, _ := json.Marshal(value)
		return stream.Event{Name: name, Type: name, Data: data}
	}
	dialect := ""
	if len(calls) > 0 {
		dialect = calls[0].Dialect
	}
	switch dialect {
	case "openai-chat":
		payload["choices"] = []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}}
		data, _ := json.Marshal(payload)
		return []stream.Event{{Data: data}}
	case "anthropic":
		index := 0
		for _, call := range calls {
			if call.Index >= index {
				index = call.Index + 1
			}
		}
		return []stream.Event{
			makeEvent("content_block_start", map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]any{"type": "text", "text": ""}}),
			makeEvent("content_block_delta", map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "text_delta", "text": text}}),
			makeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": index}),
			makeEvent("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]any{}}),
		}
	case "openai-responses":
		id := "msg_" + strings.TrimPrefix(requestID(), "req_")
		part := map[string]any{"type": "output_text", "text": text}
		item := map[string]any{"id": id, "type": "message", "status": "completed", "role": "assistant", "content": []any{part}}
		response, _ := payload["response"].(map[string]any)
		if response == nil {
			response = map[string]any{"id": "resp_" + id, "object": "response", "status": "completed"}
		}
		response["output"] = []any{item}
		response["status"] = "completed"
		return []stream.Event{
			makeEvent("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"id": id, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}}),
			makeEvent("response.content_part.added", map[string]any{"type": "response.content_part.added", "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": ""}}),
			makeEvent("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "output_index": 0, "content_index": 0, "delta": text}),
			makeEvent("response.output_text.done", map[string]any{"type": "response.output_text.done", "output_index": 0, "content_index": 0, "text": text}),
			makeEvent("response.content_part.done", map[string]any{"type": "response.content_part.done", "output_index": 0, "content_index": 0, "part": part}),
			makeEvent("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item}),
			makeEvent("response.completed", map[string]any{"type": "response.completed", "response": response}),
		}
	default:
		return nil
	}
}

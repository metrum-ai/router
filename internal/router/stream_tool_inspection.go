// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"errors"
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

// toolCallInspector inspects a complete streamed tool-call set before any
// held frame is released. Implementations may allow, block, or redact calls.
// Request-specific state is captured by the inspector when it is registered.
type toolCallInspector interface {
	inspectStreamToolCalls(calls []IRToolCall) (toolCallInspectionResult, error)
}

// firewallToolCallInspector runs firewall decisions at the bounded stream hold boundary.
type firewallToolCallInspector struct {
	service  *Service
	rc       *requestContext
	callerID string
	request  *IRRequest
}

func (i firewallToolCallInspector) inspectStreamToolCalls(calls []IRToolCall) (toolCallInspectionResult, error) {
	if i.service == nil || i.rc == nil {
		return toolCallInspectionResult{Action: toolCallInspectionAllow}, nil
	}
	fingerprint, ok := toolCallApprovalFingerprint(i.callerID, calls)
	approvalID, approvalState := "", ""
	if ok && i.service.toolApprovals != nil {
		approvalID, approvalState = i.service.toolApprovals.lookup(fingerprint)
	}
	outcome := i.service.inspectToolCallsForFirewall(i.callerID, i.request, calls, false, approvalState == "release")
	disposition := i.service.resolveToolFirewallDecision(i.rc, i.callerID, fingerprint, approvalID, approvalState, outcome, i.service.cfg.Server.ToolFirewall)
	if disposition.pending {
		text, _ := json.Marshal(map[string]string{"status": "approval_pending", "approval_id": disposition.approvalID, "message": "Retry this request after approval."})
		return toolCallInspectionResult{Action: toolCallInspectionResultText, Text: string(text)}, nil
	}
	if disposition.blocked {
		return toolCallInspectionResult{Action: toolCallInspectionResultText, Text: "Tool call blocked by router policy."}, nil
	}
	return toolCallInspectionResult{Action: toolCallInspectionAllow}, nil
}

func (i firewallToolCallInspector) shouldInspectHostedCall(call IRToolCall) bool {
	if i.service == nil || i.service.toolFirewall == nil || i.service.cfg == nil {
		return false
	}
	return i.service.toolFirewall.hostedToolNamedByPolicy(i.service.cfg.Server.ToolFirewall, call.Name)
}

// secretDLPToolCallInspector applies the request's configured outbound secret
// policy to complete, bounded tool arguments before their frames are released.
type secretDLPToolCallInspector struct {
	dlp *secretStreamDLP
}

func (i secretDLPToolCallInspector) inspectStreamToolCalls(calls []IRToolCall) (toolCallInspectionResult, error) {
	if i.dlp == nil || i.dlp.state == nil || i.dlp.state.result == nil {
		return toolCallInspectionResult{Action: toolCallInspectionAllow}, nil
	}
	updated := append([]IRToolCall(nil), calls...)
	changed := false
	for idx := range updated {
		if hasSecretStreamOversizeCandidate(updated[idx].Arguments) {
			return toolCallInspectionResult{}, errSecretStreamWindowExceeded
		}
		before := i.dlp.state.result.Replacements
		redacted, _, err := redactToolArgumentJSON(updated[idx].Arguments, i.dlp.state)
		if err != nil {
			var blocked piiFilterBlockedError
			if errors.As(err, &blocked) {
				return toolCallInspectionResult{Action: toolCallInspectionBlock}, nil
			}
			return toolCallInspectionResult{}, err
		}
		if err := i.dlp.failIfMatched(before); err != nil {
			return toolCallInspectionResult{Action: toolCallInspectionBlock}, nil
		}
		if redacted != updated[idx].Arguments {
			updated[idx].Arguments = redacted
			changed = true
		}
	}
	if changed {
		return toolCallInspectionResult{Action: toolCallInspectionRedact, Calls: updated}, nil
	}
	return toolCallInspectionResult{Action: toolCallInspectionAllow}, nil
}

// schemaToolCallInspector is the first inspector in the chain. The firewall
// (#891) and outbound secret DLP (#890) register their inspectors at the same
// held-call boundary after their dependencies merge.
type schemaToolCallInspector struct {
	tools      []map[string]any
	options    toolValidationOptions
	stopReason string
}

func (i schemaToolCallInspector) inspectStreamToolCalls(calls []IRToolCall) (toolCallInspectionResult, error) {
	validated := make([]IRToolCall, 0, len(calls))
	for _, call := range calls {
		if call.Kind == "" || call.Kind == "function_call" || call.Kind == "tool_use" {
			validated = append(validated, call)
		}
	}
	validation := validateToolCalls(validated, i.tools, i.stopReason, i.options)
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
		if !ok || prior.ID != call.ID || prior.Name != call.Name || prior.Dialect != call.Dialect || prior.Kind != call.Kind {
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
			if item != nil {
				if replacement, ok := arguments[idx]; ok && (eventType != "response.output_item.added" || responsesItemHasArguments(item) || isHostedResponsesTool(IRToolCall{Dialect: "openai-responses", Kind: stringValue(item["type"])})) {
					if isHostedResponsesTool(IRToolCall{Dialect: "openai-responses", Kind: stringValue(item["type"])}) {
						redactedItem, err := decodeResponsesHostedItem(replacement)
						if err != nil {
							return nil, err
						}
						payload["item"] = redactedItem
					} else if err := setResponsesItemArguments(item, replacement); err != nil {
						return nil, err
					}
					if eventType == "response.output_item.added" {
						first[idx] = true
					}
					changed = true
				}
			}
		case strings.HasPrefix(eventType, "response.mcp_call_arguments.") || strings.HasPrefix(eventType, "response.code_interpreter_call_code."):
			idx := streamToolIndex(payload["output_index"])
			if replacement, ok := arguments[idx]; ok {
				kind := hostedResponsesCallEventType(eventType)
				field, err := responsesHostedArgumentField(replacement, kind)
				if err != nil {
					return nil, err
				}
				if strings.HasSuffix(eventType, ".delta") {
					if first[idx] {
						payload["delta"] = ""
					} else {
						payload["delta"] = field
						first[idx] = true
					}
				} else if strings.HasSuffix(eventType, ".done") {
					if kind == "code_interpreter_call" {
						payload["code"] = field
					} else {
						payload["arguments"] = field
					}
					delete(payload, "delta")
				}
				changed = true
			}
		case eventType == "response.function_call_arguments.delta" || eventType == "response.function_call_arguments.done" || eventType == "response.custom_tool_call_input.delta" || eventType == "response.custom_tool_call_input.done":
			idx := streamToolIndex(payload["output_index"])
			if replacement, ok := arguments[idx]; ok {
				if eventType == "response.function_call_arguments.done" || eventType == "response.custom_tool_call_input.done" || !first[idx] {
					payload["delta"] = replacement
					if eventType == "response.function_call_arguments.done" {
						payload["arguments"] = replacement
						delete(payload, "delta")
					} else if eventType == "response.custom_tool_call_input.done" {
						payload["input"] = replacement
						delete(payload, "delta")
					}
					first[idx] = true
				} else {
					payload["delta"] = ""
				}
				changed = true
			}
		case eventType == "response.completed":
			response, _ := payload["response"].(map[string]any)
			output, _ := response["output"].([]any)
			for outputIndex, rawItem := range output {
				item, _ := rawItem.(map[string]any)
				if item == nil {
					continue
				}
				if replacement, ok := arguments[outputIndex]; ok {
					if isHostedResponsesTool(IRToolCall{Dialect: "openai-responses", Kind: stringValue(item["type"])}) {
						redactedItem, err := decodeResponsesHostedItem(replacement)
						if err != nil {
							return nil, err
						}
						output[outputIndex] = redactedItem
					} else if err := setResponsesItemArguments(item, replacement); err != nil {
						return nil, err
					}
					changed = true
				}
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
					choiceIndex := streamToolIndex(choice["index"])
					delta, _ := choice["delta"].(map[string]any)
					if function, ok := delta["function_call"].(map[string]any); ok {
						idx := chatStreamCallIndex(choiceIndex, maxToolCallsPerResponse-1)
						if replacement, exists := arguments[idx]; exists {
							if !first[idx] {
								function["arguments"] = replacement
								first[idx] = true
							} else {
								function["arguments"] = ""
							}
							changed = true
						}
					}
					calls, _ := delta["tool_calls"].([]any)
					for _, rawCall := range calls {
						call, _ := rawCall.(map[string]any)
						idx := chatStreamCallIndex(choiceIndex, streamToolIndex(call["index"]))
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

func decodeResponsesHostedItem(arguments string) (map[string]any, error) {
	var item map[string]any
	if err := json.Unmarshal([]byte(arguments), &item); err != nil || item == nil {
		return nil, fmt.Errorf("decode redacted hosted Responses item")
	}
	return item, nil
}

func responsesHostedArgumentField(arguments, kind string) (string, error) {
	item, err := decodeResponsesHostedItem(arguments)
	if err != nil {
		return "", err
	}
	var value any
	switch kind {
	case "mcp_call":
		value = item["arguments"]
	case "code_interpreter_call":
		value = item["code"]
	default:
		return "", fmt.Errorf("unsupported hosted Responses argument event")
	}
	if value == nil {
		return "", fmt.Errorf("redacted hosted Responses item lacks arguments")
	}
	if text, ok := value.(string); ok {
		return text, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode redacted hosted Responses arguments: %w", err)
	}
	return string(encoded), nil
}

func responsesItemHasArguments(item map[string]any) bool {
	switch stringValue(item["type"]) {
	case "function_call":
		return stringValue(item["arguments"]) != ""
	case "custom_tool_call":
		return stringValue(item["input"]) != ""
	case "local_shell_call":
		return item["action"] != nil
	default:
		return false
	}
}

func setResponsesItemArguments(item map[string]any, arguments string) error {
	switch stringValue(item["type"]) {
	case "function_call":
		item["arguments"] = arguments
	case "custom_tool_call":
		item["input"] = arguments
	case "local_shell_call":
		var action any
		if err := json.Unmarshal([]byte(arguments), &action); err != nil {
			return fmt.Errorf("decode redacted local shell action: %w", err)
		}
		item["action"] = action
	}
	return nil
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
		if len(calls) > 0 {
			index = calls[0].Index
		}
		usage := payload["usage"]
		if usage == nil {
			usage = map[string]any{}
		}
		return []stream.Event{
			makeEvent("content_block_start", map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]any{"type": "text", "text": ""}}),
			makeEvent("content_block_delta", map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "text_delta", "text": text}}),
			makeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": index}),
			makeEvent("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": usage}),
		}
	case "openai-responses":
		outputIndex := 0
		heldIndexes := make(map[int]bool, len(calls))
		heldIDs := make(map[string]bool, len(calls))
		if len(calls) > 0 {
			outputIndex = calls[0].Index
			for _, call := range calls {
				heldIndexes[call.Index] = true
				if call.ID != "" {
					heldIDs[call.ID] = true
				}
			}
		}
		id := "msg_" + strings.TrimPrefix(requestID(), "req_")
		part := map[string]any{"type": "output_text", "text": text}
		item := map[string]any{"id": id, "type": "message", "status": "completed", "role": "assistant", "content": []any{part}}
		response, _ := payload["response"].(map[string]any)
		if response == nil {
			response = map[string]any{"id": "resp_" + id, "object": "response", "status": "completed"}
		}
		originalOutput := toolCallAnySlice(response["output"])
		output := make([]any, 0)
		inserted := false
		for index, rawItem := range originalOutput {
			entry, _ := rawItem.(map[string]any)
			callID := stringValue(entry["call_id"])
			if callID == "" {
				callID = stringValue(entry["id"])
			}
			if heldIndexes[index] || (callID != "" && heldIDs[callID]) {
				if !inserted && index == outputIndex {
					output = append(output, item)
					inserted = true
				}
				continue
			}
			output = append(output, rawItem)
		}
		if !inserted {
			insertAt := outputIndex
			if insertAt < 0 || insertAt > len(output) {
				insertAt = len(output)
			}
			output = append(output, nil)
			copy(output[insertAt+1:], output[insertAt:])
			output[insertAt] = item
		}
		response["output"] = output
		response["status"] = "completed"
		return []stream.Event{
			makeEvent("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": outputIndex, "item": map[string]any{"id": id, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}}),
			makeEvent("response.content_part.added", map[string]any{"type": "response.content_part.added", "output_index": outputIndex, "content_index": 0, "part": map[string]any{"type": "output_text", "text": ""}}),
			makeEvent("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "output_index": outputIndex, "content_index": 0, "delta": text}),
			makeEvent("response.output_text.done", map[string]any{"type": "response.output_text.done", "output_index": outputIndex, "content_index": 0, "text": text}),
			makeEvent("response.content_part.done", map[string]any{"type": "response.content_part.done", "output_index": outputIndex, "content_index": 0, "part": part}),
			makeEvent("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": outputIndex, "item": item}),
			makeEvent("response.completed", map[string]any{"type": "response.completed", "response": response}),
		}
	default:
		return nil
	}
}

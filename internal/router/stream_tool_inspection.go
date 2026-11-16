// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/metrum-ai/router/internal/stream"
)

type toolCallInspectionAction string

const (
	toolCallInspectionAllow  toolCallInspectionAction = "allow"
	toolCallInspectionBlock  toolCallInspectionAction = "block"
	toolCallInspectionRedact toolCallInspectionAction = "redact"
)

// toolCallInspectionResult is one inspector's decision for the complete call
// set. Redactors return the same calls with only Arguments changed. Inspectors
// run in order, and each receives the preceding inspector's result.
type toolCallInspectionResult struct {
	Action toolCallInspectionAction
	Calls  []IRToolCall
}

// toolCallInspector inspects a complete streamed tool-call set before any
// held frame is released. Implementations may allow, block, or redact calls.
// Request-specific state is captured by the inspector when it is registered.
type toolCallInspector interface {
	inspectStreamToolCalls(calls []IRToolCall) (toolCallInspectionResult, error)
}

// secretDLPToolCallInspector applies the request's configured outbound secret
// policy to complete, bounded tool arguments before their frames are released.
type secretDLPToolCallInspector struct {
	dlp *secretStreamDLP
}

func (i secretDLPToolCallInspector) inspectStreamToolCalls(calls []IRToolCall) (toolCallInspectionResult, error) {
	if i.dlp == nil {
		return toolCallInspectionResult{Action: toolCallInspectionAllow}, nil
	}
	updated := append([]IRToolCall(nil), calls...)
	changed := false
	for idx := range updated {
		redacted, err := i.dlp.redactCandidate(updated[idx].Arguments)
		if err != nil {
			var blocked piiFilterBlockedError
			if errors.As(err, &blocked) {
				return toolCallInspectionResult{Action: toolCallInspectionBlock}, nil
			}
			return toolCallInspectionResult{}, err
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
				if replacement, ok := arguments[idx]; ok && (eventType != "response.output_item.added" || responsesItemHasArguments(item)) {
					if err := setResponsesItemArguments(item, replacement); err != nil {
						return nil, err
					}
					if eventType == "response.output_item.added" {
						first[idx] = true
					}
					changed = true
				}
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
					if err := setResponsesItemArguments(item, replacement); err != nil {
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

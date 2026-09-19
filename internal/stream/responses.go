// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package stream

import (
	"encoding/json"
	"errors"
)

// IdentifierEncoder is satisfied by router.IdentifierTransform.
type IdentifierEncoder interface{ Encode(string) string }

// Responses is a same-dialect translator. It retains no content or item history.
type Responses struct {
	IDs                IdentifierEncoder
	UnknownEvents      int
	terminal, finished bool
}

var _ StreamTranslator = (*Responses)(nil)

func (t *Responses) Begin(TokenEstimate) ([]Event, error) { return nil, nil }
func (t *Responses) Next(up Event) ([]Event, error) {
	if t.terminal || t.finished {
		return nil, ErrTerminal
	}
	var p map[string]json.RawMessage
	if err := json.Unmarshal(up.Data, &p); err != nil {
		return nil, errors.New("stream: malformed Responses event")
	}
	var typ string
	_ = json.Unmarshal(p["type"], &typ)
	if !responsesEvent(typ) {
		t.UnknownEvents++
		return nil, nil
	}
	if up.Name != "" && up.Name != typ {
		return nil, errors.New("stream: mismatched Responses event type")
	}
	rewrite := func(obj map[string]json.RawMessage, key string) {
		var id string
		if t.IDs != nil && json.Unmarshal(obj[key], &id) == nil && id != "" {
			obj[key], _ = json.Marshal(t.IDs.Encode(id))
		}
	}
	rewrite(p, "item_id")
	rewrite(p, "response_id")
	rewriteItem := func(raw json.RawMessage) json.RawMessage {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil || item == nil {
			return raw
		}
		rewrite(item, "id")
		rewrite(item, "call_id")
		out, _ := json.Marshal(item)
		return out
	}
	if raw, ok := p["item"]; ok {
		p["item"] = rewriteItem(raw)
	}
	if raw, ok := p["response"]; ok {
		var response map[string]json.RawMessage
		if json.Unmarshal(raw, &response) != nil || response == nil {
			return nil, errors.New("stream: invalid response object")
		}
		rewrite(response, "id")
		var output []json.RawMessage
		if json.Unmarshal(response["output"], &output) == nil && output != nil {
			for i := range output {
				output[i] = rewriteItem(output[i])
			}
			response["output"], _ = json.Marshal(output)
		}
		p["response"], _ = json.Marshal(response)
	}
	up.Type = typ
	up.Data, _ = json.Marshal(p)
	switch typ {
	case "response.completed", "response.failed", "response.incomplete", "error":
		t.terminal = true
		return []Event{up}, ErrTerminal
	}
	return []Event{up}, nil
}
func (t *Responses) Finish(reason StopReason, usage Usage) ([]Event, error) {
	if t.finished {
		return nil, nil
	}
	t.finished = true
	// Never fabricate success after truncation, cancellation, or an upstream error.
	return nil, nil
}
func responsesEvent(typ string) bool {
	switch typ {
	case "response.created", "response.in_progress", "response.completed", "response.failed", "response.incomplete", "error",
		"response.output_item.added", "response.output_item.done", "response.content_part.added", "response.content_part.done",
		"response.output_text.delta", "response.output_text.done", "response.output_text.annotation.added",
		"response.refusal.delta", "response.refusal.done", "response.function_call_arguments.delta", "response.function_call_arguments.done",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done", "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done",
		"response.reasoning_text.delta", "response.reasoning_text.done",
		"response.file_search_call.in_progress", "response.file_search_call.searching", "response.file_search_call.completed",
		"response.web_search_call.in_progress", "response.web_search_call.searching", "response.web_search_call.completed",
		"response.code_interpreter_call.in_progress", "response.code_interpreter_call.interpreting", "response.code_interpreter_call.completed",
		"response.code_interpreter_call_code.delta", "response.code_interpreter_call_code.done":
		return true
	}
	return false
}

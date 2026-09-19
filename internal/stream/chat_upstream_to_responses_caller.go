// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package stream

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ChatUpstreamToResponsesCaller implements the responses_to_chat request bridge.
// Output is retained for Responses done snapshots, bounded by the runner's
// upstream byte limit. Deltas are emitted immediately, without waiting for EOF.
type ChatUpstreamToResponsesCaller struct {
	IDs                       IdentifierEncoder
	ResponseID, Model         string
	CreatedAt                 int64
	sequence                  int
	begun, finished, terminal bool
	finishReason              string
	items                     []*chatResponseItem
	tools                     map[int]*chatResponseItem
	message                   *chatResponseItem
	usage                     map[string]any
}
type chatResponseItem struct {
	id, callID, name string
	text             strings.Builder
	index            int
	tool, closed     bool
}

var _ StreamTranslator = (*ChatUpstreamToResponsesCaller)(nil)

func (t *ChatUpstreamToResponsesCaller) id(s string) string {
	if t.IDs != nil {
		return t.IDs.Encode(s)
	}
	return s
}
func (t *ChatUpstreamToResponsesCaller) event(typ string, p map[string]any) Event {
	p["type"], p["sequence_number"] = typ, t.sequence
	t.sequence++
	raw, _ := json.Marshal(p)
	return Event{Name: typ, Type: typ, Data: raw}
}
func (t *ChatUpstreamToResponsesCaller) response(status string) map[string]any {
	output := make([]any, 0, len(t.items))
	for _, i := range t.items {
		output = append(output, t.item(i))
	}
	p := map[string]any{"id": t.ResponseID, "object": "response", "created_at": t.CreatedAt, "model": t.Model, "status": status, "output": output}
	if t.usage != nil {
		p["usage"] = t.usage
	}
	if status == "incomplete" {
		reason := "max_output_tokens"
		if t.finishReason == "content_filter" {
			reason = "content_filter"
		}
		p["incomplete_details"] = map[string]any{"reason": reason}
	}
	return p
}
func (t *ChatUpstreamToResponsesCaller) Begin(TokenEstimate) ([]Event, error) {
	if t.begun {
		return nil, nil
	}
	t.begun = true
	t.ResponseID = t.id(t.ResponseID)
	t.tools = make(map[int]*chatResponseItem)
	return []Event{t.event("response.created", map[string]any{"response": t.response("in_progress")})}, nil
}
func (t *ChatUpstreamToResponsesCaller) part(i *chatResponseItem) map[string]any {
	return map[string]any{"type": "output_text", "text": i.text.String(), "annotations": []any{}}
}
func (t *ChatUpstreamToResponsesCaller) item(i *chatResponseItem) map[string]any {
	status := "in_progress"
	if i.closed {
		status = "completed"
		if t.finishReason == "length" || t.finishReason == "content_filter" {
			status = "incomplete"
		}
	}
	p := map[string]any{"id": i.id, "status": status}
	if i.tool {
		p["type"], p["call_id"], p["name"], p["arguments"] = "function_call", i.callID, i.name, i.text.String()
	} else {
		p["type"], p["role"], p["content"] = "message", "assistant", []any{t.part(i)}
	}
	return p
}
func (t *ChatUpstreamToResponsesCaller) fields(i *chatResponseItem) map[string]any {
	p := map[string]any{"item_id": i.id, "output_index": i.index}
	if !i.tool {
		p["content_index"] = 0
	}
	return p
}
func (t *ChatUpstreamToResponsesCaller) closeItems() []Event {
	var events []Event
	for _, i := range t.items {
		if i.closed {
			continue
		}
		i.closed = true
		p := t.fields(i)
		if i.tool {
			p["arguments"] = i.text.String()
			events = append(events, t.event("response.function_call_arguments.done", p))
		} else {
			p["text"] = i.text.String()
			events = append(events, t.event("response.output_text.done", p))
			p = t.fields(i)
			p["part"] = t.part(i)
			events = append(events, t.event("response.content_part.done", p))
		}
		events = append(events, t.event("response.output_item.done", map[string]any{"output_index": i.index, "item": t.item(i)}))
	}
	return events
}
func (t *ChatUpstreamToResponsesCaller) Next(up Event) ([]Event, error) {
	if t.finished || t.terminal {
		return nil, ErrTerminal
	}
	if bytes.Equal(bytes.TrimSpace(up.Data), []byte("[DONE]")) {
		if t.finishReason == "" {
			return nil, errors.New("stream: Chat ended without finish_reason")
		}
		t.terminal = true
		return nil, ErrTerminal
	}
	var p struct {
		Error json.RawMessage `json:"error"`
		Usage *struct {
			Input         int            `json:"prompt_tokens"`
			Output        int            `json:"completion_tokens"`
			InputDetails  map[string]any `json:"prompt_tokens_details"`
			OutputDetails map[string]any `json:"completion_tokens_details"`
		} `json:"usage"`
		Choices []struct {
			Index  int    `json:"index"`
			Finish string `json:"finish_reason"`
			Delta  struct {
				Content string `json:"content"`
				Tools   []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(up.Data, &p); err != nil {
		return nil, err
	}
	if len(p.Error) > 0 && string(p.Error) != "null" {
		return nil, errors.New("stream: Chat upstream error")
	}
	if p.Choices == nil && p.Usage == nil {
		return nil, errors.New("stream: missing Chat choices")
	}
	if p.Usage != nil {
		u := p.Usage
		t.usage = map[string]any{"input_tokens": u.Input, "output_tokens": u.Output, "total_tokens": u.Input + u.Output}
		if u.InputDetails != nil {
			t.usage["input_tokens_details"] = u.InputDetails
		}
		if u.OutputDetails != nil {
			t.usage["output_tokens_details"] = u.OutputDetails
		}
	}
	var events []Event
	for _, c := range p.Choices {
		if c.Index != 0 {
			continue
		}
		if t.finishReason != "" {
			return nil, errors.New("stream: Chat choice after finish_reason")
		}
		if c.Delta.Content != "" {
			if t.message == nil {
				i := &chatResponseItem{index: len(t.items)}
				i.id = t.id(fmt.Sprintf("%s_msg_%d", t.ResponseID, i.index))
				t.message = i
				t.items = append(t.items, i)
				item := t.item(i)
				item["content"] = []any{}
				events = append(events, t.event("response.output_item.added", map[string]any{"output_index": i.index, "item": item}))
				fields := t.fields(i)
				fields["part"] = t.part(i)
				events = append(events, t.event("response.content_part.added", fields))
			}
			i := t.message
			i.text.WriteString(c.Delta.Content)
			fields := t.fields(i)
			fields["delta"] = c.Delta.Content
			events = append(events, t.event("response.output_text.delta", fields))
		}
		for _, call := range c.Delta.Tools {
			i := t.tools[call.Index]
			if i == nil {
				if call.Function.Name == "" {
					return nil, errors.New("stream: first tool fragment missing name")
				}
				i = &chatResponseItem{index: len(t.items), tool: true, name: call.Function.Name}
				i.id = t.id(fmt.Sprintf("%s_fc_%d", t.ResponseID, i.index))
				callID := call.ID
				if callID == "" {
					callID = fmt.Sprintf("%s_call_%d", t.ResponseID, i.index)
				}
				i.callID = t.id(callID)
				t.tools[call.Index] = i
				t.items = append(t.items, i)
				events = append(events, t.event("response.output_item.added", map[string]any{"output_index": i.index, "item": t.item(i)}))
			}
			if call.Function.Arguments != "" {
				i.text.WriteString(call.Function.Arguments)
				fields := t.fields(i)
				fields["delta"] = call.Function.Arguments
				events = append(events, t.event("response.function_call_arguments.delta", fields))
			}
		}
		if c.Finish != "" {
			t.finishReason = c.Finish
			events = append(events, t.closeItems()...)
		}
	}
	return events, nil
}
func (t *ChatUpstreamToResponsesCaller) Finish(reason StopReason, _ Usage) ([]Event, error) {
	if t.finished {
		return nil, nil
	}
	t.finished = true
	if reason != StopComplete || !t.terminal {
		return nil, nil
	}
	status := "completed"
	if t.finishReason == "length" || t.finishReason == "content_filter" {
		status = "incomplete"
	}
	return []Event{t.event("response."+status, map[string]any{"response": t.response(status)})}, nil
}

// UsageSnapshot preserves received accounting even if the stream is interrupted
// after the usage chunk but before [DONE]. It contains no generated content.
func (t *ChatUpstreamToResponsesCaller) UsageSnapshot() Usage {
	var u Usage
	if t.usage == nil {
		return u
	}
	u.InputTokens, _ = t.usage["input_tokens"].(int)
	u.OutputTokens, _ = t.usage["output_tokens"].(int)
	u.TotalTokens = u.InputTokens + u.OutputTokens
	if d, ok := t.usage["input_tokens_details"].(map[string]any); ok {
		if n, ok := d["cached_tokens"].(float64); ok {
			v := int(n)
			u.CachedInputTokens = &v
		}
	}
	if d, ok := t.usage["output_tokens_details"].(map[string]any); ok {
		if n, ok := d["reasoning_tokens"].(float64); ok {
			v := int(n)
			u.ReasoningTokens = &v
		}
	}
	return u
}

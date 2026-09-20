// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package stream

import (
	"encoding/json"
	"errors"
)

// ResponsesUpstreamToChatCaller implements the chat_to_responses request bridge.
// Only metadata and tool indexes are retained; argument deltas remain raw strings.
type ResponsesUpstreamToChatCaller struct {
	IDs                      IdentifierEncoder
	ResponseID, Model        string
	CreatedAt                int64
	role, terminal, finished bool
	tools                    map[int]responsesChatTool
	usage                    map[string]any
	accounting               Usage
}
type responsesChatTool struct {
	id    string
	index int
}

var _ StreamTranslator = (*ResponsesUpstreamToChatCaller)(nil)

func (t *ResponsesUpstreamToChatCaller) Begin(TokenEstimate) ([]Event, error) { return nil, nil }
func (t *ResponsesUpstreamToChatCaller) chunk(delta map[string]any, finish any) Event {
	if !t.role && len(delta) > 0 {
		delta["role"] = "assistant"
		t.role = true
	}
	id := t.ResponseID
	if t.IDs != nil {
		id = t.IDs.Encode(id)
	}
	p := map[string]any{"id": id, "object": "chat.completion.chunk", "created": t.CreatedAt, "model": t.Model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
	if finish != nil && t.usage != nil {
		p["usage"] = t.usage
	}
	raw, _ := json.Marshal(p)
	return Event{Data: raw}
}
func (t *ResponsesUpstreamToChatCaller) Next(up Event) ([]Event, error) {
	if t.terminal || t.finished {
		return nil, ErrTerminal
	}
	var p struct {
		Type        string `json:"type"`
		OutputIndex int    `json:"output_index"`
		ItemID      string `json:"item_id"`
		Delta       string `json:"delta"`
		Item        struct {
			ID, Type, Name, Arguments string
			CallID                    string `json:"call_id"`
		} `json:"item"`
		Response *struct {
			ID                string `json:"id"`
			CreatedAt         int64  `json:"created_at"`
			IncompleteDetails struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
			Usage *struct {
				Input         int            `json:"input_tokens"`
				Output        int            `json:"output_tokens"`
				Total         int            `json:"total_tokens"`
				InputDetails  map[string]any `json:"input_tokens_details"`
				OutputDetails map[string]any `json:"output_tokens_details"`
			} `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal(up.Data, &p); err != nil {
		return nil, err
	}
	if p.Type == "" || (up.Name != "" && up.Name != p.Type) {
		return nil, errors.New("stream: invalid Responses event type")
	}
	if p.Response != nil && p.Response.Usage != nil {
		u := p.Response.Usage
		total := u.Total
		if total == 0 {
			total = u.Input + u.Output
		}
		t.usage = map[string]any{"prompt_tokens": u.Input, "completion_tokens": u.Output, "total_tokens": total}
		t.accounting = Usage{InputTokens: u.Input, OutputTokens: u.Output, TotalTokens: total}
		if u.InputDetails != nil {
			t.usage["prompt_tokens_details"] = u.InputDetails
			if n, ok := u.InputDetails["cached_tokens"].(float64); ok {
				v := int(n)
				t.accounting.CachedInputTokens = &v
			}
		}
		if u.OutputDetails != nil {
			t.usage["completion_tokens_details"] = u.OutputDetails
			if n, ok := u.OutputDetails["reasoning_tokens"].(float64); ok {
				v := int(n)
				t.accounting.ReasoningTokens = &v
			}
		}
	}
	switch p.Type {
	case "response.created":
		if p.Response == nil || p.Response.ID == "" || t.ResponseID != "" {
			return nil, errors.New("stream: invalid response.created")
		}
		t.ResponseID = p.Response.ID
		t.CreatedAt = p.Response.CreatedAt
		return nil, nil
	case "response.failed", "error":
		return nil, errors.New("stream: Responses upstream error")
	}
	if t.ResponseID == "" {
		return nil, errors.New("stream: event before response.created")
	}
	switch p.Type {
	case "response.output_item.added":
		if p.Item.Type != "function_call" {
			return nil, nil
		}
		if p.Item.CallID == "" || p.Item.Name == "" {
			return nil, errors.New("stream: function call missing identity")
		}
		if t.tools == nil {
			t.tools = map[int]responsesChatTool{}
		}
		if _, ok := t.tools[p.OutputIndex]; ok {
			return nil, errors.New("stream: duplicate function call")
		}
		i := len(t.tools)
		t.tools[p.OutputIndex] = responsesChatTool{id: p.Item.ID, index: i}
		id := p.Item.CallID
		if t.IDs != nil {
			id = t.IDs.Encode(id)
		}
		return []Event{t.chunk(map[string]any{"tool_calls": []any{map[string]any{"index": i, "id": id, "type": "function", "function": map[string]any{"name": p.Item.Name, "arguments": p.Item.Arguments}}}}, nil)}, nil
	case "response.function_call_arguments.delta":
		tool, ok := t.tools[p.OutputIndex]
		if !ok || (p.ItemID != "" && tool.id != p.ItemID) {
			return nil, errors.New("stream: arguments for unknown function call")
		}
		return []Event{t.chunk(map[string]any{"tool_calls": []any{map[string]any{"index": tool.index, "function": map[string]any{"arguments": p.Delta}}}}, nil)}, nil
	case "response.output_text.delta", "response.refusal.delta":
		key := "content"
		if p.Type == "response.refusal.delta" {
			key = "refusal"
		}
		return []Event{t.chunk(map[string]any{key: p.Delta}, nil)}, nil
	case "response.completed", "response.incomplete":
		if p.Response == nil {
			return nil, errors.New("stream: missing terminal response")
		}
		reason := "stop"
		if len(t.tools) > 0 {
			reason = "tool_calls"
		}
		if p.Type == "response.incomplete" {
			reason = "length"
			if p.Response.IncompleteDetails.Reason == "content_filter" {
				reason = "content_filter"
			}
		}
		t.terminal = true
		return []Event{t.chunk(map[string]any{}, reason)}, ErrTerminal
	}
	// Lifecycle snapshots and sequence numbers do not produce duplicate Chat deltas.
	return nil, nil
}
func (t *ResponsesUpstreamToChatCaller) Finish(reason StopReason, _ Usage) ([]Event, error) {
	if t.finished {
		return nil, nil
	}
	t.finished = true
	if reason != StopComplete || !t.terminal {
		return nil, nil
	}
	return []Event{{Data: []byte("[DONE]")}}, nil
}
func (t *ResponsesUpstreamToChatCaller) UsageSnapshot() Usage { return t.accounting }
